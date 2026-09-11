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
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/config"
	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/manager/dockerfile"
	"git.ole-hartwig.eu/pinup/pinup/manager/regexm"
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
	}
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
