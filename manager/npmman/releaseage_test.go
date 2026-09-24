// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package npmman

import (
	"slices"
	"strings"
	"testing"
	"time"
)

const day = 24 * time.Hour

// The .npmrc of development/sales-agent-ai, and the shapes npm and pnpm 10
// accept besides it: a repeated array key, a comma list, quotes, comments.
// npm splits its exclude at commas; pnpm does not, and a comma list there
// is one pattern that matches nothing (measured in pnpm 10.34).
func TestAnNpmrcNamesItsReleaseAges(t *testing.T) {
	for _, tc := range []struct {
		name, npmrc string
		want        []ReleaseAge
	}{
		{"the estate's shape", "@moselwal:registry=https://git.example/npm/\n# wait a week\nmin-release-age=7\n\nmin-release-age-exclude=@moselwal/*\nallow-remote=root\n",
			[]ReleaseAge{{7 * day, "/min-release-age", []Exemption{{Pattern: "@moselwal/*"}}}}},
		{"array keys, repeated", "min-release-age = 3\nmin-release-age-exclude[]=@a/*\nmin-release-age-exclude[]=left-pad\n",
			[]ReleaseAge{{3 * day, "/min-release-age", []Exemption{{Pattern: "@a/*"}, {Pattern: "left-pad"}}}}},
		{"a comma list, quoted", "min-release-age=\"1.5\"\nmin-release-age-exclude=\"@a/*, @b/*\"\n",
			[]ReleaseAge{{36 * time.Hour, "/min-release-age", []Exemption{{Pattern: "@a/*"}, {Pattern: "@b/*"}}}}},
		{"pnpm 10's key, in minutes", "minimum-release-age=10080\nminimum-release-age-exclude[]=@types/*\nminimum-release-age-exclude=left-pad@1.3.0\n",
			[]ReleaseAge{{7 * day, "/minimum-release-age", []Exemption{{Pattern: "@types/*"}, {Pattern: "left-pad", Range: "1.3.0"}}}}},
		{"pnpm's comma list stays one pattern", "minimum-release-age=60\nminimum-release-age-exclude=a,b\n",
			[]ReleaseAge{{time.Hour, "/minimum-release-age", []Exemption{{Pattern: "a,b"}}}}},
		{"both", "min-release-age=2\nminimum-release-age=4320\n",
			[]ReleaseAge{{2 * day, "/min-release-age", nil}, {3 * day, "/minimum-release-age", nil}}},
		{"commented out", "; min-release-age=7\n# minimum-release-age=7\n", nil},
		{"not a number", "min-release-age=soon\n", nil},
		{"absent", "save-exact=true\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadNpmrc([]byte(tc.npmrc))
			if !slices.EqualFunc(got, tc.want, equalAge) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func equalAge(a, b ReleaseAge) bool {
	return a.Age == b.Age && a.Setting == b.Setting && slices.Equal(a.Exempt, b.Exempt)
}

// pnpm-workspace.yaml: minutes, a list of exclusions that may carry exact
// versions. A version range there is something pnpm 11.27 itself rejects
// (ERR_PNPM_INVALID_MINIMUM_RELEASE_AGE_EXCLUDE), so reading it as a range
// changes nothing that could run.
func TestAPnpmWorkspaceNamesItsReleaseAge(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       ReleaseAge
		err        string
	}{
		{"a week, two exclusions", "packages:\n  - apps/*\nminimumReleaseAge: 10080\nminimumReleaseAgeExclude:\n  - \"@moselwal/*\"\n  - \"webpack@5.1.0 || 5.1.1\"\n",
			ReleaseAge{7 * day, "/minimumReleaseAge", []Exemption{{Pattern: "@moselwal/*"}, {Pattern: "webpack", Range: "5.1.0 || 5.1.1"}}}, ""},
		{"a day, quoted", "minimumReleaseAge: \"1440\"\n", ReleaseAge{day, "/minimumReleaseAge", nil}, ""},
		{"none", "packages:\n  - apps/*\n", ReleaseAge{Setting: "/minimumReleaseAge"}, ""},
		{"not a number", "minimumReleaseAge: a week\n", ReleaseAge{Setting: "/minimumReleaseAge"}, "minimumReleaseAge"},
		{"exclusions not a list", "minimumReleaseAge: 60\nminimumReleaseAgeExclude: left-pad\n", ReleaseAge{time.Hour, "/minimumReleaseAge", nil}, "minimumReleaseAgeExclude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadPnpmWorkspace([]byte(tc.yaml))
			if (err == nil) != (tc.err == "") || (err != nil && !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("err = %v, want one naming %q", err, tc.err)
			}
			if !equalAge(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// .yarnrc.yml: a bare number is minutes, a unit says what it means. Each
// of these was checked against yarn 4.14.1 with a version 5.76 days old:
// 5d and 130h let it through, 6d, 140h, 1w, 8400 and "7d" held it.
func TestAYarnrcNamesItsReleaseAge(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       time.Duration
		exempt     []Exemption
		err        bool
	}{
		{"days", "npmMinimalAgeGate: 7d\n", 7 * day, nil, false},
		{"quoted days", "npmMinimalAgeGate: \"7d\"\n", 7 * day, nil, false},
		{"hours", "npmMinimalAgeGate: 140h\n", 140 * time.Hour, nil, false},
		{"weeks", "npmMinimalAgeGate: 1w\n", 7 * day, nil, false},
		{"bare minutes", "npmMinimalAgeGate: 8400\n", 8400 * time.Minute, nil, false},
		{"preapproved", "npmMinimalAgeGate: 3d\nnpmPreapprovedPackages:\n  - \"@types/*\"\n  - \"left-pad@^1\"\n",
			3 * day, []Exemption{{Pattern: "@types/*"}, {Pattern: "left-pad", Range: "^1"}}, false},
		{"none", "nodeLinker: node-modules\n", 0, nil, false},
		{"an unknown unit", "npmMinimalAgeGate: 7x\n", 0, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadYarnrc([]byte(tc.yaml))
			if (err != nil) != tc.err {
				t.Fatalf("err = %v, want error %t", err, tc.err)
			}
			if got.Age != tc.want || got.Setting != "/npmMinimalAgeGate" || !slices.Equal(got.Exempt, tc.exempt) {
				t.Errorf("got %+v, want %v exempting %v", got, tc.want, tc.exempt)
			}
		})
	}
}

// An exemption names a package exactly or by a scope pattern; a pattern
// for one scope does not reach into another, nor into a name that only
// starts the same way. A narrowed one covers the versions in its range and
// no others - and none at all when the version is not known.
func TestAnExemptionMatchesByNameScopeAndRange(t *testing.T) {
	ra := ReleaseAge{Age: 7 * day, Exempt: []Exemption{
		{Pattern: "@moselwal/*"}, {Pattern: "left-pad"}, {Pattern: "webpack", Range: "5.1.0 || 5.1.1"},
	}}
	// A stand-in for the npm range check: the exemptions here only need
	// exact versions joined by ||.
	satisfies := func(v, rng string) bool {
		for alt := range strings.SplitSeq(rng, "||") {
			if strings.TrimSpace(alt) == v {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		pkg, version string
		want         bool
	}{
		{"@moselwal/eslint-config", "1.0.0", true},
		{"left-pad", "", true},
		{"eslint", "10.11.0", false},
		{"@moselwalx/tool", "1.0.0", false},
		{"left-pad-extra", "1.0.0", false},
		{"webpack", "5.1.1", true},
		{"webpack", "5.2.0", false},
		{"webpack", "", false},
	} {
		if got := ra.Exempts(tc.pkg, tc.version, satisfies); got != tc.want {
			t.Errorf("Exempts(%q, %q) = %t, want %t", tc.pkg, tc.version, got, tc.want)
		}
	}
}
