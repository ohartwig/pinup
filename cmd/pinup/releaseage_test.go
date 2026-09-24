// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/planner"
)

// datedDS answers releases with the day each was published, which is what
// an age is measured from.
type datedDS struct {
	releases map[string]map[string]time.Time
}

func (d datedDS) Name() string              { return "npm" }
func (d datedDS) DefaultVersioning() string { return "npm" }
func (d datedDS) Releases(_ context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	vs, ok := d.releases[ref.PackageName]
	if !ok {
		return nil, fmt.Errorf("npm: no dated releases for %s", ref.PackageName)
	}
	rs := &model.ReleaseSet{}
	for v, at := range vs {
		rs.Releases = append(rs.Releases, model.Release{Version: v, Timestamp: at})
	}
	return rs, nil
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// development/sales-agent-ai on 2026-09-24: the estate's rules offer an npm
// release at three days, the repository's .npmrc refuses one under seven
// except its own scope. So eslint 10.11.0, 5.6 days old, is held - named
// after the file that set the age, free the moment the package manager
// will install it - instead of pushed into a lock refresh that never
// returns (npm) or fails (pnpm, yarn); the exempt first-party package at
// four days is offered, and a fortnight-old release is. pnpm's and yarn's
// settings hold the same way, the longest of several wins, an exemption
// narrowed to a version covers that version, and a setting pinup cannot
// read is a warning, not a floor.
func TestAProjectsReleaseAgeIsAFloorUnderPinupsOwn(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	released := time.Date(2026, 9, 18, 20, 15, 0, 0, time.UTC)
	ds := datedDS{releases: map[string]map[string]time.Time{
		"eslint":                  {"10.10.0": day("2026-09-04"), "10.11.0": released},
		"@moselwal/eslint-config": {"1.1.0": day("2026-09-01"), "1.1.1": day("2026-09-20")},
		"left-pad":                {"1.0.0": day("2026-01-01"), "1.3.0": day("2026-09-10")},
	}}
	all := map[string]string{"eslint": "10.11.0", "@moselwal/eslint-config": "1.1.1", "left-pad": "1.3.0"}
	held := map[string]string{"eslint": "", "@moselwal/eslint-config": "1.1.1", "left-pad": "1.3.0"}
	for _, tc := range []struct {
		name     string
		files    map[string]string
		want     map[string]string // dependency -> the version offered, "" for none
		holdFrom string            // the origin of eslint's hold, "file:<path>#<pointer>"
		until    time.Time
		warning  string
	}{
		{"with the repository's .npmrc", map[string]string{".npmrc": "min-release-age=7\nmin-release-age-exclude=@moselwal/*\n"},
			held, "file:.npmrc#/min-release-age", released.Add(7 * 24 * time.Hour), ""},
		{"without it", nil, all, "", time.Time{}, ""},
		{"pnpm's workspace", map[string]string{"pnpm-workspace.yaml": "minimumReleaseAge: 10080\nminimumReleaseAgeExclude:\n  - \"@moselwal/*\"\n"},
			held, "file:pnpm-workspace.yaml#/minimumReleaseAge", released.Add(7 * 24 * time.Hour), ""},
		{"pnpm 10's .npmrc key", map[string]string{".npmrc": "minimum-release-age=10080\nminimum-release-age-exclude[]=@moselwal/*\n"},
			held, "file:.npmrc#/minimum-release-age", released.Add(7 * 24 * time.Hour), ""},
		{"yarn's gate", map[string]string{".yarnrc.yml": "npmMinimalAgeGate: 7d\nnpmPreapprovedPackages:\n  - \"@moselwal/*\"\n"},
			held, "file:.yarnrc.yml#/npmMinimalAgeGate", released.Add(7 * 24 * time.Hour), ""},
		{"the longest wins", map[string]string{
			".npmrc":              "min-release-age=6\nmin-release-age-exclude=@moselwal/*\n",
			"pnpm-workspace.yaml": "minimumReleaseAge: 11520\nminimumReleaseAgeExclude:\n  - \"@moselwal/*\"\n"},
			held, "file:pnpm-workspace.yaml#/minimumReleaseAge", released.Add(8 * 24 * time.Hour), ""},
		{"an exemption for this very version", map[string]string{"pnpm-workspace.yaml": "minimumReleaseAge: 10080\nminimumReleaseAgeExclude:\n  - \"@moselwal/*\"\n  - \"eslint@10.11.0\"\n"},
			all, "", time.Time{}, ""},
		{"an exemption for other versions", map[string]string{".yarnrc.yml": "npmMinimalAgeGate: 7d\nnpmPreapprovedPackages:\n  - \"@moselwal/*\"\n  - \"eslint@^9\"\n"},
			held, "file:.yarnrc.yml#/npmMinimalAgeGate", released.Add(7 * 24 * time.Hour), ""},
		{"a setting pinup cannot read", map[string]string{"pnpm-workspace.yaml": "minimumReleaseAge: a week\n"},
			all, "", time.Time{}, "release age not read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/package.json", []byte(`{"name":"sales-agent","devDependencies":{"eslint":"10.10.0","@moselwal/eslint-config":"1.1.0","left-pad":"1.0.0"}}`), 0o644)
			for name, body := range tc.files {
				os.WriteFile(root+"/"+name, []byte(body), 0o644)
			}
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"]}`), 0o644)
			opts := treeOptions(t, root, "development/sales-agent-ai", now)
			opts.Datasources["npm"] = ds
			plan, err := whatif(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			offered := map[string]string{}
			var eslintHold *model.Block
			for _, u := range plan.Updates {
				if u.Dep.Manager != "npm" || u.Type == model.UpdateLockFileMaintenance {
					continue
				}
				if !u.Blocked() {
					offered[u.Dep.DepName] = u.NewVersion
				} else if u.Dep.DepName == "eslint" {
					eslintHold = &u.Blocks[0]
				}
			}
			for dep, want := range tc.want {
				if offered[dep] != want {
					t.Errorf("%s: offered %q, want %q (all: %v)", dep, offered[dep], want, offered)
				}
			}
			if tc.holdFrom != "" {
				if eslintHold == nil || eslintHold.Reason != model.BlockMinimumReleaseAge ||
					eslintHold.Org.Source+"#"+eslintHold.Org.Pointer != tc.holdFrom || !eslintHold.Until.Equal(tc.until) {
					t.Errorf("eslint 10.11.0 hold = %+v, want minimumReleaseAge from %s until %s", eslintHold, tc.holdFrom, tc.until)
				}
			}
			warned := false
			for _, w := range plan.Warnings {
				warned = warned || (tc.warning != "" && strings.Contains(w.Msg, tc.warning))
			}
			if warned != (tc.warning != "") {
				t.Errorf("warning %q present = %t (all: %+v)", tc.warning, warned, plan.Warnings)
			}
		})
	}
}

// How the floor meets pinup's own age: the longer applies, the project's
// age never shortens one, and when it lengthens one the hold names the file
// and the moment the version becomes installable.
func TestTheLongerAgeAppliesAndTheHoldNamesTheFile(t *testing.T) {
	floor := ageRule{origin: model.Origin{Source: "file:.npmrc", Pointer: "/min-release-age", Rule: model.NoRule}}
	floor.age.Age = 7 * 24 * time.Hour
	rule := model.Origin{Source: "packageRules", Rule: 1}
	for _, tc := range []struct {
		pinup, want string
		fromNpmrc   bool
	}{
		{"3 days", "7 days", true},
		{"7 days", "7 days", false},
		{"14 days", "14 days", false},
		{"", "7 days", true},
	} {
		p := planner.Policy{Enabled: true, MinimumReleaseAge: tc.pinup, Origins: map[string]model.Origin{"minimumReleaseAge": rule}}
		floor.raise(&p)
		if p.MinimumReleaseAge != tc.want || (p.Origins["minimumReleaseAge"] == floor.origin) != tc.fromNpmrc {
			t.Errorf("pinup %q: age %q from %+v, want %q (from .npmrc: %t)", tc.pinup, p.MinimumReleaseAge, p.Origins["minimumReleaseAge"], tc.want, tc.fromNpmrc)
		}
	}

	released := time.Date(2026, 9, 18, 20, 15, 0, 0, time.UTC)
	p := planner.Policy{Enabled: true, MinimumReleaseAge: "3 days", Origins: map[string]model.Origin{"minimumReleaseAge": rule}}
	floor.raise(&p)
	u, err := planner.Decide(model.Update{Type: model.UpdateMinor, NewVersion: "10.11.0", ReleaseTime: released, TimeSource: model.TimeFromDatasource}, p, time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Blocks) != 1 || u.Blocks[0].Reason != model.BlockMinimumReleaseAge || u.Blocks[0].Org.Source != "file:.npmrc" ||
		!u.Blocks[0].Until.Equal(released.Add(7*24*time.Hour)) || !strings.Contains(u.Blocks[0].Note, "needs 7 days") {
		t.Errorf("hold = %+v", u.Blocks)
	}
}

// pnpm and yarn write ages in minutes, so an age need not be whole days.
// It is written back in a unit the configuration reads, and reads back as
// the same duration.
func TestAnAgeIsWrittenInTheConfigurationsNotation(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{7 * 24 * time.Hour, "7 days"},
		{36 * time.Hour, "1.5 days"},
		{140 * time.Hour, "5.833333333333333 days"},
		{12 * time.Hour, "12 hours"},
		{90 * time.Minute, "90 minutes"},
	} {
		var a ageRule
		a.age.Age = tc.age
		got := a.String()
		back, err := planner.ParseAge(got)
		if got != tc.want || err != nil || back.Round(time.Second) != tc.age {
			t.Errorf("%v: %q (reads back as %v, %v), want %q", tc.age, got, back, err, tc.want)
		}
	}
}
