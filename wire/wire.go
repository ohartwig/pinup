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
	"slices"
	"strings"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/datasource/apkds"
	"github.com/ohartwig/pinup/datasource/customds"
	"github.com/ohartwig/pinup/datasource/dockerds"
	"github.com/ohartwig/pinup/datasource/githubds"
	"github.com/ohartwig/pinup/datasource/gitlabds"
	"github.com/ohartwig/pinup/datasource/gittagsds"
	"github.com/ohartwig/pinup/datasource/gods"
	"github.com/ohartwig/pinup/datasource/helmds"
	"github.com/ohartwig/pinup/datasource/npmds"
	"github.com/ohartwig/pinup/datasource/packagist"
	"github.com/ohartwig/pinup/datasource/terraformds"
	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/manager/composerman"
	"github.com/ohartwig/pinup/manager/dockerfile"
	"github.com/ohartwig/pinup/manager/gitlabci"
	"github.com/ohartwig/pinup/manager/gomod"
	"github.com/ohartwig/pinup/manager/kustomize"
	"github.com/ohartwig/pinup/manager/npmman"
	"github.com/ohartwig/pinup/manager/regexm"
	"github.com/ohartwig/pinup/manager/terraform"
	"github.com/ohartwig/pinup/manager/tfversion"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/platform/gitlab"
	"github.com/ohartwig/pinup/publish"
	"github.com/ohartwig/pinup/versioning"
	"github.com/ohartwig/pinup/versioning/apk"
	"github.com/ohartwig/pinup/versioning/coerced"
	"github.com/ohartwig/pinup/versioning/composer"
	vdocker "github.com/ohartwig/pinup/versioning/docker"
	"github.com/ohartwig/pinup/versioning/godirective"
	"github.com/ohartwig/pinup/versioning/golang"
	"github.com/ohartwig/pinup/versioning/hashicorp"
	"github.com/ohartwig/pinup/versioning/loose"
	"github.com/ohartwig/pinup/versioning/npm"
	"github.com/ohartwig/pinup/versioning/partial"
	"github.com/ohartwig/pinup/versioning/regexver"
	"github.com/ohartwig/pinup/versioning/semver"
)

// Versionings returns every versioning scheme, keyed by the name the
// configuration uses.
func Versionings() versioning.Registry {
	return versioning.Registry{
		"semver":           semver.New(),
		"semver-partial":   partial.New(),
		"semver-coerced":   coerced.New(),
		"loose":            loose.New(),
		"docker":           vdocker.New(),
		"apk":              apk.New(),
		"composer":         composer.New(),
		"npm":              npm.New(),
		"node":             npm.NewNode(),
		"go":               golang.New(),
		"go-mod-directive": godirective.New(),
		"hashicorp":        hashicorp.New(),
		"regex":            regexver.New(),
	}
}

// Managers returns the built-in managers, keyed by the name the configuration
// uses.
func Managers() extract.Registry {
	return extract.Registry{
		"dockerfile": dockerfile.New(),
		"gitlabci":   gitlabci.New(),
		"kustomize":  kustomize.New(),
		"gomod":      gomod.New(),
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
		"go":                 gods.New(gods.Module, client, "https://proxy.golang.org"),
		"golang-version":     gods.New(gods.Toolchain, client, "https://go.dev"),
		"helm":               helmds.New(client),
		"git-tags":           gittagsds.New(),
		"git-refs":           gittagsds.NewKind(gittagsds.Refs),
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
	case "gomod":
		return gomod.LockedVersions(lock)
	}
	return nil, nil
}

// LockedRegistries reads the registries a manager's lock file resolves its
// packages from, keyed like LockedVersions, where the lock records that at
// all: terraform's does, for a provider from a registry other than the
// default. Every other manager answers nil.
func LockedRegistries(manager string, lock []byte) map[string]string {
	if manager == "terraform" {
		return terraform.LockedRegistries(lock)
	}
	return nil
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
		if covered, ok := coveredBy[name]; ok {
			// Renovate splits what one file holds across two managers;
			// pinup's one manager reads both shapes, so enabling the
			// second is satisfied by the first and warns about nothing.
			if !slices.Contains(decoded.EnabledManagers, covered) && !slices.Contains(out, covered) {
				out = append(out, covered)
			}
			continue
		}
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

// coveredBy maps a Renovate manager name onto the pinup manager that reads
// the same lines: `include:` entries of .gitlab-ci.yml belong to Renovate's
// gitlabci-include and to pinup's gitlabci.
var coveredBy = map[string]string{
	"gitlabci-include": "gitlabci",
}

// Covers reports whether a configured manager name is read by a pinup
// manager - implemented under that name, or covered by the one that reads
// the same lines.
func Covers(name string) bool {
	if _, ok := Managers()[name]; ok {
		return true
	}
	_, ok := coveredBy[name]
	return ok
}

// ManagerNameOf reports the manager name to record on a dependency, collapsing
// the per-definition discovery keys back to the one the configuration uses.
func ManagerNameOf(key string) string {
	if _, ok := customIndex(key); ok {
		return "custom.regex"
	}
	return key
}
