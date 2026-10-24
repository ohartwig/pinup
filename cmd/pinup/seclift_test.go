// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
)

// A fix for a CVE waits for no release age - the project's included, where
// the lock's tool can be told so. The same eslint 10.11.0 that
// TestAProjectsReleaseAgeIsAFloorUnderPinupsOwn holds as a routine update,
// 5.6 days old under a seven-day .npmrc, is offered at once as a security
// fix in npm's or pnpm's lock, and its lock refresh carries the age's
// override in that tool's spelling. A yarn lock, and a manifest with no
// lock pinup refreshes, keep the floor: their install would refuse the
// version.
func TestASecurityFixTakesAVersionYoungerThanTheProjectsAge(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	released := time.Date(2026, 9, 18, 20, 15, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, lockFile, lock string
		refresh              string // the lock refresh expected, "" when held
	}{
		{"package-lock.json", "package-lock.json", `{"lockfileVersion":3,"packages":{"":{"name":"site"},"node_modules/eslint":{"version":"10.10.0"}}}`,
			"npm install --package-lock-only --no-audit --ignore-scripts --allow-remote=all eslint --min-release-age=0"},
		{"pnpm-lock.yaml", "pnpm-lock.yaml", "lockfileVersion: '9.0'\n\nimporters:\n\n  .:\n    devDependencies:\n      eslint:\n        specifier: 10.10.0\n        version: 10.10.0\n",
			"pnpm install --lockfile-only --ignore-scripts --pm-on-fail=warn --config.minimum-release-age=0"},
		{"yarn.lock", "yarn.lock", "# yarn lockfile v1\n\n\neslint@10.10.0:\n  version \"10.10.0\"\n", ""},
		{"no lock", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/package.json", []byte(`{"name":"site","devDependencies":{"eslint":"10.10.0"}}`), 0o644)
			os.WriteFile(root+"/.npmrc", []byte("min-release-age=7\n"), 0o644)
			if tc.lockFile != "" {
				os.WriteFile(root+"/"+tc.lockFile, []byte(tc.lock), 0o644)
			}
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true,
				"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null}}`), 0o644)
			opts := treeOptions(t, root, "development/moselwal/site", now)
			opts.Datasources["npm"] = datedDS{releases: map[string]map[string]time.Time{
				"eslint": {"10.10.0": day("2026-09-04"), "10.11.0": released},
			}}
			opts.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
			opts.Advisories = &askedAdvisories{answers: map[string][]osv.Advisory{
				"eslint": {{ID: "GHSA-test-0000-0001", Fixed: "10.11.0"}},
			}}
			plan, err := whatif(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			var fix *model.Update
			for i, u := range plan.Updates {
				if u.Dep.DepName == "eslint" && u.SecurityFix {
					fix = &plan.Updates[i]
				}
			}
			if fix == nil {
				t.Fatalf("no security fix for eslint: %+v", plan.Updates)
			}
			lifted := tc.refresh != ""
			if held := fix.Blocked(); held == lifted {
				t.Fatalf("blocked %t (%+v), want lifted %t", held, fix.Blocks, lifted)
			}
			if !lifted {
				if fix.Blocks[0].Reason != model.BlockMinimumReleaseAge || fix.Blocks[0].Org.Source != "file:.npmrc" {
					t.Errorf("hold %+v, want the .npmrc's age", fix.Blocks[0])
				}
				return
			}
			var refresh []string
			for _, b := range plan.Branches {
				for _, task := range b.Tasks {
					if task.Kind == model.TaskLockRefresh {
						refresh = task.Command
					}
				}
			}
			if got := strings.Join(refresh, " "); got != tc.refresh {
				t.Errorf("refresh %q, want %q", got, tc.refresh)
			}
		})
	}
}

// The case behind the change: fast-uri 3.1.8 locked under ajv's ^3.0.1,
// 3.1.9 four days old, a seven-day .npmrc. The maintenance branch carries
// the routine refresh, which keeps the age, and then the targeted lift of
// fast-uri past it, which the runner checks; the dashboard's line says the
// refresh lifts it instead of blaming the constraints.
func TestATransitiveAdvisoryInNpmsLockIsLiftedPastTheProjectsAge(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(root+"/package.json", []byte(`{"name":"site","devDependencies":{"ajv":"8.17.1"}}`), 0o644)
	os.WriteFile(root+"/.npmrc", []byte("min-release-age=7\n"), 0o644)
	os.WriteFile(root+"/package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"name":"site"},"node_modules/ajv":{"version":"8.17.1"},"node_modules/fast-uri":{"version":"3.1.8"}}}`), 0o644)
	os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true, "osvTransitiveAlerts": true,
		"lockFileMaintenance": {"enabled": false},
		"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null}}`), 0o644)
	opts := treeOptions(t, root, "development/moselwal/sitepackage-base", time.Date(2026, 10, 23, 21, 0, 0, 0, time.UTC))
	opts.Datasources["npm"] = datedDS{releases: map[string]map[string]time.Time{"ajv": {"8.17.1": day("2026-07-01")}}}
	opts.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
	opts.Advisories = &askedAdvisories{answers: map[string][]osv.Advisory{
		"fast-uri": {{ID: "GHSA-333x-w4r6-6crp", Fixed: "3.1.9"}, {ID: "GHSA-wjr9-j795-jxcr", Fixed: "3.1.9"}},
	}}
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var b *model.Branch
	for i := range plan.Branches {
		if plan.Branches[i].Name == "renovate/lock-file-maintenance" {
			b = &plan.Branches[i]
		}
	}
	if b == nil || b.SuppressedBy != "" {
		t.Fatalf("maintenance branch %+v", b)
	}
	if len(b.Tasks) != 2 {
		t.Fatalf("tasks %+v, want the refresh and the lift", b.Tasks)
	}
	if got := strings.Join(b.Tasks[0].Command, " "); got != "npm update --package-lock-only --no-audit --ignore-scripts --allow-remote=all" {
		t.Errorf("routine refresh %q", got)
	}
	lift := b.Tasks[1]
	if got := strings.Join(lift.Command, " "); got != "npm update fast-uri --package-lock-only --no-audit --ignore-scripts --allow-remote=all --min-release-age=0" {
		t.Errorf("lift %q", got)
	}
	if !slices.Equal(lift.Lifts, []model.Lift{{Package: "fast-uri", Installed: "3.1.8", Fixed: "3.1.9"}}) {
		t.Errorf("lifts %+v", lift.Lifts)
	}
	var line string
	for _, w := range plan.Warnings {
		if strings.Contains(w.Msg, "transitive fast-uri 3.1.8") {
			line = w.Msg
		}
	}
	if !strings.HasSuffix(line, "(fixed in 3.1.9); the security lock refresh lifts it past any release age, as far as the constraints that require it allow") {
		t.Errorf("dashboard line %q", line)
	}
}

func TestLiftsOfTakesTheFixThatClearsEveryAdvisory(t *testing.T) {
	got := liftsOf([]model.Advisory{
		{ID: "A", Package: "tar", Installed: "7.4.3", Fixed: "7.5.3"},
		{ID: "B", Package: "nanoid", Installed: "3.3.11", Fixed: "3.3.18"},
		{ID: "C", Package: "tar", Installed: "7.4.3", Fixed: "7.5.21"},
		{ID: "D", Package: "tar", Installed: "7.4.3", Fixed: "7.5.10"},
		{ID: "E", Package: "request", Installed: "2.88.2"},
	})
	want := []model.Lift{{Package: "tar", Installed: "7.4.3", Fixed: "7.5.21"}, {Package: "nanoid", Installed: "3.3.11", Fixed: "3.3.18"}, {Package: "request", Installed: "2.88.2"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
