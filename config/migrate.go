// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"maps"
	"slices"
)

// Migrate applies the normalisations Renovate performs on a configuration
// before it is used, each one measured on the estate file against the
// container's --print-config output (testdata/renovate/.../presets/README.md):
//
//   - minimumReleaseAge: "0" becomes null. Both mean no hold; the resolved
//     form is null, and a rule's null is what the rules engine sees.
//   - a description written as a string becomes a one-element list.
//   - executionTimeout is a global option, not repository configuration,
//     and does not appear in the resolved repository config.
//
// It returns the migrated document and a note per change, so print-config
// can say what it rewrote.
func Migrate(doc map[string]any) (map[string]any, []string) {
	var notes []string
	out := migrateObject(doc, "", &notes)
	for _, k := range slices.Sorted(maps.Keys(GlobalOnly)) {
		if _, ok := out[k]; ok {
			delete(out, k)
			notes = append(notes, k+" is a global option and is dropped from the repository configuration")
		}
	}
	return out, notes
}

// GlobalOnly names keys that configure the RUNNER rather than a
// repository. They are read from the runner's file before migration and
// dropped from anything a repository brings, because a repository setting
// one of them decides something about the bot rather than about itself.
//
// allowedCommands is the one that matters: it is the allowlist a
// postUpgradeTasks command must match, so a repository that could set it
// would be authorising its own commands. Until 2026-09-22 nothing dropped
// it - this map existed but only executionTimeout was ever deleted, by
// name, in the loop above - and the fallback in cmd/pinup read the
// allowlist out of the RESOLVED document, which is the repository's layer
// whenever the repository carries a configuration file.
var GlobalOnly = map[string]bool{"executionTimeout": true, "allowedCommands": true}

func migrateObject(obj map[string]any, path string, notes *[]string) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		p := path + "/" + escapePointer(k)
		switch k {
		case "minimumReleaseAge":
			if s, ok := v.(string); ok && s == "0" {
				out[k] = nil
				*notes = append(*notes, p+`: "0" migrated to null`)
				continue
			}
		case "description":
			if s, ok := v.(string); ok {
				out[k] = []any{s}
				continue
			}
		}
		switch x := v.(type) {
		case map[string]any:
			out[k] = migrateObject(x, p, notes)
		case []any:
			list := make([]any, len(x))
			for i, e := range x {
				if m, ok := e.(map[string]any); ok {
					list[i] = migrateObject(m, fmt.Sprintf("%s/%d", p, i), notes)
				} else {
					list[i] = e
				}
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	return out
}
