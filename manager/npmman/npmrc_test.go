// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package npmman

import (
	"slices"
	"testing"
)

// The .npmrc of development/sales-agent-ai, and the shapes npm accepts
// besides it: a repeated array key, a comma list, quotes, comments.
func TestAnNpmrcNamesItsReleaseAge(t *testing.T) {
	for _, tc := range []struct {
		name, npmrc string
		days        float64
		exclude     []string
	}{
		{"the estate's shape", "@moselwal:registry=https://git.example/npm/\n# wait a week\nmin-release-age=7\n\nmin-release-age-exclude=@moselwal/*\nallow-remote=root\n",
			7, []string{"@moselwal/*"}},
		{"array keys, repeated", "min-release-age = 3\nmin-release-age-exclude[]=@a/*\nmin-release-age-exclude[]=left-pad\n",
			3, []string{"@a/*", "left-pad"}},
		{"a comma list, quoted", "min-release-age=\"1.5\"\nmin-release-age-exclude=\"@a/*, @b/*\"\n",
			1.5, []string{"@a/*", "@b/*"}},
		{"commented out", "; min-release-age=7\n# min-release-age=7\n", 0, nil},
		{"not a number", "min-release-age=soon\n", 0, nil},
		{"absent", "save-exact=true\n", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadReleaseAge([]byte(tc.npmrc))
			if got.Days != tc.days || !slices.Equal(got.Exclude, tc.exclude) {
				t.Errorf("got %+v, want %v days excluding %v", got, tc.days, tc.exclude)
			}
		})
	}
}

// An exclusion names a package exactly or by a scope pattern; a pattern for
// one scope does not reach into another, nor into a name that only starts
// the same way.
func TestAnNpmrcExclusionMatchesByNameOrScope(t *testing.T) {
	ra := ReleaseAge{Days: 7, Exclude: []string{"@moselwal/*", "left-pad"}}
	for pkg, want := range map[string]bool{
		"@moselwal/eslint-config": true,
		"left-pad":                true,
		"eslint":                  false,
		"@moselwalx/tool":         false,
		"left-pad-extra":          false,
	} {
		if got := ra.Excludes(pkg); got != want {
			t.Errorf("Excludes(%q) = %t, want %t", pkg, got, want)
		}
	}
}
