// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package wire is the only place that knows every implementation.
//
// Every stage below it declares an interface and receives a registry; none of
// them names a concrete manager, datasource or platform. That is what keeps
// the stages testable against a fake and what stops a manager reaching
// sideways into another one. The cost is this package, which by design imports
// everything - and a convention test that stops anything but cmd/ importing
// it.
package wire

import (
	"fmt"
	"net/http"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/config"
	"git.ole-hartwig.eu/pinup/pinup/datasource/apkds"
	"git.ole-hartwig.eu/pinup/pinup/datasource/customds"
	"git.ole-hartwig.eu/pinup/pinup/datasource/dockerds"
	"git.ole-hartwig.eu/pinup/pinup/datasource/githubds"
	"git.ole-hartwig.eu/pinup/pinup/datasource/gitlabds"
	"git.ole-hartwig.eu/pinup/pinup/datasource/npmds"
	"git.ole-hartwig.eu/pinup/pinup/datasource/packagist"
	"git.ole-hartwig.eu/pinup/pinup/datasource/terraformds"
	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/manager/composerman"
	"git.ole-hartwig.eu/pinup/pinup/manager/dockerfile"
	"git.ole-hartwig.eu/pinup/pinup/manager/gitlabci"
	"git.ole-hartwig.eu/pinup/pinup/manager/npmman"
	"git.ole-hartwig.eu/pinup/pinup/manager/regexm"
	"git.ole-hartwig.eu/pinup/pinup/manager/terraform"
	"git.ole-hartwig.eu/pinup/pinup/manager/tfversion"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/platform/gitlab"
	"git.ole-hartwig.eu/pinup/pinup/publish"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
	"git.ole-hartwig.eu/pinup/pinup/versioning/apk"
	"git.ole-hartwig.eu/pinup/pinup/versioning/coerced"
	"git.ole-hartwig.eu/pinup/pinup/versioning/composer"
	vdocker "git.ole-hartwig.eu/pinup/pinup/versioning/docker"
	"git.ole-hartwig.eu/pinup/pinup/versioning/golang"
	"git.ole-hartwig.eu/pinup/pinup/versioning/hashicorp"
	"git.ole-hartwig.eu/pinup/pinup/versioning/loose"
	"git.ole-hartwig.eu/pinup/pinup/versioning/npm"
	"git.ole-hartwig.eu/pinup/pinup/versioning/partial"
	"git.ole-hartwig.eu/pinup/pinup/versioning/regexver"
	"git.ole-hartwig.eu/pinup/pinup/versioning/semver"
)

// Versionings returns every versioning scheme, keyed by the name the
// configuration uses.
func Versionings() versioning.Registry {
	return versioning.Registry{
		"semver":         semver.New(),
		"semver-partial": partial.New(),
		"semver-coerced": coerced.New(),
		"loose":          loose.New(),
		"docker":         vdocker.New(),
		"apk":            apk.New(),
		"composer":       composer.New(),
		"npm":            npm.New(),
		"go":             golang.New(),
		"hashicorp":      hashicorp.New(),
		"regex":          regexver.New(),
	}
}

// Managers returns the built-in managers, keyed by the name the configuration
// uses.
func Managers() extract.Registry {
	return extract.Registry{
		"dockerfile": dockerfile.New(),
		"gitlabci":   gitlabci.New(),
		"composer":   composerman.New(),
		"npm":        npmman.New(),
		"terraform":  terraform.New(),
		// The registry key is the manager's Renovate name, which the
		// enabledManagers list and the rules' matchManagers use.
		"terraform-version": tfversion.New(),
	}
}

// DatasourceOptions is what the datasources need beyond the shared client.
type DatasourceOptions struct {
	// GitLabURL is the fallback instance for the three gitlab-* sources when
	// a dependency names no registryUrls.
	GitLabURL string
	// Transport is what the docker datasource dials registries and token
	// realms through; it manages bearer tokens itself, which httpx has no
	// hook for. nil means http.DefaultTransport.
	Transport http.RoundTripper
	// RegistryCredentials answers basic-auth credentials for a token realm
	// host, e.g. git.ole-hartwig.eu for registry.ole-hartwig.eu. nil means
	// every realm is asked anonymously.
	RegistryCredentials dockerds.Credentials
	// CustomDatasources are the configuration's customDatasources, served
	// generically - except the names ApkViews serves natively.
	CustomDatasources map[string]model.CustomDatasource
}

// ApkViews are the apk datasources served natively rather than through the
// Renovate runner's sidecar. custom.wolfi is what at least one of the public
// Wolfi repository and the estate's mirror carries, on both architectures;
// custom.koh-apk is the estate's own packages only - the sidecar's two
// views, measured in datasource/apkds.
var ApkViews = map[string]apkds.View{
	"custom.wolfi": {
		Mirrors: []string{"https://packages.wolfi.dev/os", "https://pub-e45431adaf86477786d9d2ef6ec04768.r2.dev"},
		Arches:  []string{"x86_64", "aarch64"},
	},
	"custom.koh-apk": {
		Mirrors: []string{"https://pub-e45431adaf86477786d9d2ef6ec04768.r2.dev"},
		Arches:  []string{"x86_64", "aarch64"},
	},
}

// Datasources returns every datasource, keyed by the name the configuration
// uses.
func Datasources(client *httpx.Client, o DatasourceOptions) lookup.Registry {
	r := lookup.Registry{
		"gitlab-tags":     gitlabds.New(gitlabds.Tags, client, o.GitLabURL),
		"gitlab-releases": gitlabds.New(gitlabds.Releases, client, o.GitLabURL),
		"gitlab-packages": gitlabds.New(gitlabds.Packages, client, o.GitLabURL),
		"github-releases": githubds.New(githubds.Releases, client, ""),
		"github-tags":     githubds.New(githubds.Tags, client, ""),
		"docker":          dockerds.New(o.Transport, o.RegistryCredentials),
		"packagist":       packagist.New(client),
		"npm":             npmds.New(client),
		// Renovate's default registry; the estate's terraform manager
		// pins every provider to registry.opentofu.org instead.
		"terraform-provider": terraformds.New(terraformds.Provider, client, "https://registry.terraform.io"),
		"terraform-module":   terraformds.New(terraformds.Module, client, "https://registry.terraform.io"),
	}
	for name, view := range ApkViews {
		r[name] = apkds.New(name, client, view)
	}
	for name, ds := range CustomDatasources(client, o.CustomDatasources) {
		r[name] = ds
	}
	return r
}

// CustomDatasources returns the generic datasources for a configuration's
// customDatasources, skipping the names served natively. The
// configuration is known only once it is resolved, so this is called from
// the run as well, with what the repository's configuration declares.
func CustomDatasources(client *httpx.Client, defs map[string]model.CustomDatasource) lookup.Registry {
	r := lookup.Registry{}
	for _, def := range defs {
		name := "custom." + def.Name
		if _, native := ApkViews[name]; native {
			continue
		}
		r[name] = customds.New(def, client)
	}
	return r
}

// DefaultVersioning names the scheme a datasource implies when neither the
// dependency nor a rule names one. Unknown datasources answer "", and the
// planner then says so rather than guessing.
func DefaultVersioning(ds lookup.Registry) func(string) string {
	return func(name string) string {
		if d, ok := ds[name]; ok {
			return d.DefaultVersioning()
		}
		return ""
	}
}

// LockedVersions reads the versions a manager's lock file pins, keyed by
// package name. A manager receives one file, the manifest; the lock is a
// sibling the run reads for it. Managers without a lock answer nil.
func LockedVersions(manager string, lock []byte) (map[string]string, error) {
	switch manager {
	case "composer":
		return composerman.LockedVersions(lock)
	case "npm":
		return npmman.LockedVersions(lock)
	case "terraform":
		return terraform.LockedVersions(lock)
	}
	return nil, nil
}

// Platform returns the GitLab platform for an instance. header is the
// header the token travels in: PRIVATE-TOKEN or JOB-TOKEN.
func Platform(baseURL, token, header string) publish.Platform {
	return gitlab.New(baseURL, nil, gitlab.Token{Value: token, Header: header})
}

// Plan is a resolved manager assignment: which implementation handles a
// discovered file, and with what configuration.
type Plan struct {
	Manager extract.Manager
	Config  extract.ManagerConfig
}

// Resolve maps a discovery key to the manager that handles it.
//
// A custom definition is discovered under its own key - "custom.regex#13" -
// rather than under a single shared one, so the right definition reaches the
// manager. Running all thirty-seven over every file any of them matched would
// produce dependencies from definitions that never selected that file.
func Resolve(key string, decoded config.Decoded, managers extract.Registry) (Plan, error) {
	if idx, ok := customIndex(key); ok {
		if idx >= len(decoded.CustomManagers) {
			return Plan{}, fmt.Errorf("wire: %s names definition %d, but only %d are configured",
				key, idx, len(decoded.CustomManagers))
		}
		def := decoded.CustomManagers[idx]
		return Plan{
			Manager: regexm.New(),
			Config:  extract.ManagerConfig{Custom: &def, FilePatterns: def.FilePatterns},
		}, nil
	}
	m, err := managers.Get(key)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		Manager: m,
		Config:  extract.ManagerConfig{FilePatterns: decoded.FilePatterns[key]},
	}, nil
}

// customIndex reads the definition number out of a discovery key.
func customIndex(key string) (int, bool) {
	const prefix = "custom.regex#"
	if !strings.HasPrefix(key, prefix) {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(key[len(prefix):], "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// DiscoveryPatterns builds the pattern map discovery needs: one entry per
// custom definition, plus the built-in managers that are enabled.
//
// A manager the configuration enables but pinup does not implement is left
// out deliberately rather than defaulted - discover reports it as a warning,
// and a warning naming the manager is more use than a silent absence.
func DiscoveryPatterns(decoded config.Decoded, managers extract.Registry) map[string][]string {
	out := map[string][]string{}
	for name, patterns := range decoded.FilePatterns {
		if _, isCustom := customIndex(name); isCustom {
			out[name] = patterns
			continue
		}
		if _, ok := managers[name]; ok {
			out[name] = patterns
		}
	}
	for name, m := range managers {
		if _, already := out[name]; already {
			continue
		}
		if p := m.FilePatterns(); len(p) > 0 {
			out[name] = p
		}
	}
	return out
}

// EnabledKeys expands the configured manager list into discovery keys: the
// built-in names stay as they are, and "custom.regex" becomes one key per
// configured definition.
func EnabledKeys(decoded config.Decoded) []string {
	var out []string
	for _, name := range decoded.EnabledManagers {
		if name != "custom.regex" {
			out = append(out, name)
			continue
		}
		for i := range decoded.CustomManagers {
			out = append(out, config.CustomManagerName(i))
		}
	}
	return out
}

// ManagerNameOf reports the manager name to record on a dependency, collapsing
// the per-definition discovery keys back to the one the configuration uses.
func ManagerNameOf(key string) string {
	if _, ok := customIndex(key); ok {
		return "custom.regex"
	}
	return key
}
