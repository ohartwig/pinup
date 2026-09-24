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
// after the .npmrc, free the moment npm will install it - instead of pushed
// into a lock refresh that never returns; the exempt first-party package at
// four days is offered, and a fortnight-old release is. Without the .npmrc
// eslint is offered as before.
func TestAProjectsNpmrcReleaseAgeIsAFloorUnderPinupsOwn(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	ds := datedDS{releases: map[string]map[string]time.Time{
		"eslint":                  {"10.10.0": day("2026-09-04"), "10.11.0": time.Date(2026, 9, 18, 20, 15, 0, 0, time.UTC)},
		"@moselwal/eslint-config": {"1.1.0": day("2026-09-01"), "1.1.1": day("2026-09-20")},
		"left-pad":                {"1.0.0": day("2026-01-01"), "1.3.0": day("2026-09-10")},
	}}
	for _, tc := range []struct {
		name  string
		npmrc string
		want  map[string]string // dependency -> the version offered, "" for none
	}{
		{"with the repository's .npmrc", "min-release-age=7\nmin-release-age-exclude=@moselwal/*\n",
			map[string]string{"eslint": "", "@moselwal/eslint-config": "1.1.1", "left-pad": "1.3.0"}},
		{"without it", "",
			map[string]string{"eslint": "10.11.0", "@moselwal/eslint-config": "1.1.1", "left-pad": "1.3.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/package.json", []byte(`{"name":"sales-agent","devDependencies":{"eslint":"10.10.0","@moselwal/eslint-config":"1.1.0","left-pad":"1.0.0"}}`), 0o644)
			if tc.npmrc != "" {
				os.WriteFile(root+"/.npmrc", []byte(tc.npmrc), 0o644)
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
			if tc.npmrc != "" {
				if eslintHold == nil || eslintHold.Reason != model.BlockMinimumReleaseAge || eslintHold.Org.Source != "file:.npmrc" ||
					!eslintHold.Until.Equal(time.Date(2026, 9, 25, 20, 15, 0, 0, time.UTC)) {
					t.Errorf("eslint 10.11.0 hold = %+v, want minimumReleaseAge from file:.npmrc until 2026-09-25T20:15Z", eslintHold)
				}
			}
		})
	}
}

// How the floor meets pinup's own age: the longer applies, the .npmrc never
// shortens one, and when it lengthens one the hold names the file and the
// moment the version becomes installable.
func TestTheLongerAgeAppliesAndTheHoldNamesTheNpmrc(t *testing.T) {
	floor := npmrcFloor{origin: model.Origin{Source: "file:.npmrc", Pointer: "/min-release-age", Rule: model.NoRule}}
	floor.age.Days = 7
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
