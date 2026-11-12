// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/wire"
)

func TestMovesToFix(t *testing.T) {
	schemes := wire.Versionings()
	for _, c := range []struct {
		scheme, rng, installed, fixed string
		want                          bool
	}{
		// vitest 4.1.12 pins @vitest/mocker exactly; the fix is 4.1.12.
		{"npm", "4.1.12", "4.1.11", "4.1.12", true},
		// A caret floor above the fix.
		{"npm", "^3.1.10", "3.1.8", "3.1.9", true},
		// A range that admits the fix, not the vulnerable version.
		{"npm", ">=3.1.9 <4", "3.1.8", "3.1.9", true},
		// The installed version still satisfies: the lock stays.
		{"npm", "^3.0.1", "3.1.8", "3.1.9", false},
		// Moved, but not far enough.
		{"npm", "4.1.11-1", "4.1.10", "4.1.12", false},
		{"npm", "~4.0.5", "4.0.3", "4.1.0", false},
		// Nothing a range can say.
		{"npm", "npm:other@1.0.0", "1.0.0", "1.0.1", false},
		{"npm", "", "1.0.0", "1.0.1", false},
		{"npm", "^2.0.0", "1.0.0", "", false},
		// composer's require.
		{"composer", "^2.6.3", "2.6.0", "2.6.3", true},
		{"composer", "^2.0", "2.6.0", "2.6.3", false},
	} {
		v, err := schemes.Get(c.scheme)
		if err != nil {
			t.Fatal(err)
		}
		if got := movesToFix(v, c.rng, c.installed, c.fixed); got != c.want {
			t.Errorf("%s %q from %s, fix %s: %t, want %t", c.scheme, c.rng, c.installed, c.fixed, got, c.want)
		}
	}
}

var mockerAdvisory = model.Advisory{ID: "GHSA-qxg6-89mg-rqv8", Package: "@vitest/mocker", Installed: "4.1.11", Fixed: "4.1.12"}

func vitestBump(newVersion string) model.Update {
	return model.Update{
		DepKey: "package.json|npm|vitest", NewValue: newVersion, NewVersion: newVersion, Type: model.UpdatePatch,
		Dep: model.Dependency{Manager: "npm", Datasource: "npm", File: "package.json", DepName: "vitest", CurrentValue: "4.1.11", CustomManager: model.NoCustomManager},
	}
}

func TestMarkTransitiveFixes(t *testing.T) {
	locks := map[string]string{"npm|.": "package-lock.json"}
	transitive := map[string][]model.Advisory{"npm|package-lock.json": {mockerAdvisory}}
	requires := func(d model.Dependency, version string) (map[string]string, error) {
		switch d.DepName + "@" + version {
		case "vitest@4.1.12":
			return map[string]string{"@vitest/mocker": "4.1.12", "@vitest/spy": "4.1.12"}, nil
		case "vitest@4.1.11-1":
			return map[string]string{"@vitest/mocker": "4.1.11"}, nil
		}
		return nil, errors.New("registry down")
	}
	direct := vitestBump("4.1.12")
	direct.SecurityFix = true
	maintenance := model.Update{Type: model.UpdateLockFileMaintenance, Dep: model.Dependency{Manager: "npm", Datasource: "npm", File: "package-lock.json"}, NewVersion: "x"}
	node := vitestBump("4.1.12")
	node.Dep.Datasource = "node-version"
	for _, c := range []struct {
		name     string
		update   model.Update
		security bool
		warning  string
	}{
		{"the bump that moves @vitest/mocker", vitestBump("4.1.12"), true, ""},
		{"a bump that leaves it", vitestBump("4.1.11-1"), false, ""},
		{"requirements unreadable", vitestBump("4.2.0"), false, "vitest 4.2.0: requirements not read (registry down)"},
		{"a direct security fix stays one", direct, true, ""},
		{"a lock refresh is not a bump", maintenance, false, ""},
		{"another datasource", node, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			ups := []model.Update{c.update}
			warns := markTransitiveFixes(ups, locks, transitive, requires, wire.Versionings(), lockVersioning)
			if ups[0].SecurityFix != c.security {
				t.Errorf("securityFix %t, want %t", ups[0].SecurityFix, c.security)
			}
			wantFixes := c.security && c.update.Type != model.UpdateLockFileMaintenance && !c.update.SecurityFix
			if got := len(ups[0].TransitiveFixes) > 0; got != wantFixes {
				t.Errorf("transitive fixes %+v", ups[0].TransitiveFixes)
			}
			if c.warning == "" && len(warns) > 0 || c.warning != "" && (len(warns) != 1 || !strings.Contains(warns[0].Msg, c.warning)) {
				t.Errorf("warnings %+v, want %q", warns, c.warning)
			}
		})
	}
	// A lock without a finding asks nothing.
	asked := false
	markTransitiveFixes([]model.Update{vitestBump("4.1.12")}, locks, map[string][]model.Advisory{"npm|other/package-lock.json": {mockerAdvisory}},
		func(model.Dependency, string) (map[string]string, error) { asked = true; return nil, nil }, wire.Versionings(), lockVersioning)
	if asked {
		t.Error("requirements asked for a bump whose lock carries no advisory")
	}
}

func TestTransitiveSecurityOverlayKeepsTheBranchAndTheDecisions(t *testing.T) {
	cfg := map[string]any{
		"schedule": []any{"* 0,4,8,12,16,20 * * *"}, "groupName": "npm packages", "enabled": false, "dependencyDashboardApproval": true,
		"vulnerabilityAlerts": map[string]any{
			"schedule": []any{"at any time"}, "labels": []any{"security"}, "minimumReleaseAge": nil, "automerge": true, "commitMessageSuffix": "[SECURITY]",
			"enabled": true, "dependencyDashboardApproval": false, "groupName": nil, "branchTopic": "{{{depNameSanitized}}}-vulnerability",
		},
	}
	got := transitiveSecurityOverlay(cfg)
	if s, _ := got["schedule"].([]any); !slices.Equal(s, []any{"at any time"}) {
		t.Errorf("schedule %v", got["schedule"])
	}
	if got["commitMessageSuffix"] != "[SECURITY]" || got["automerge"] != true {
		t.Errorf("suffix %v, automerge %v", got["commitMessageSuffix"], got["automerge"])
	}
	if got["groupName"] != "npm packages" {
		t.Errorf("groupName %v: the bump left its group", got["groupName"])
	}
	if _, ok := got["branchTopic"]; ok {
		t.Error("branchTopic taken over from vulnerabilityAlerts")
	}
	if got["enabled"] != false || got["dependencyDashboardApproval"] != true {
		t.Errorf("enabled %v, approval %v: a rule's decision was overruled", got["enabled"], got["dependencyDashboardApproval"])
	}
}

// requiringDS is datedDS that also says what each version requires.
type requiringDS struct {
	datedDS
	requires map[string]map[string]string
}

func (d requiringDS) Requirements(_ context.Context, ref lookup.Ref, version string) (map[string]string, error) {
	r, ok := d.requires[ref.PackageName+"@"+version]
	if !ok {
		return nil, fmt.Errorf("no requirements for %s@%s", ref.PackageName, version)
	}
	return r, nil
}

// End to end, the case behind the change: collab-rte's npm group outside
// its four-hourly window, vitest 4.1.11 -> 4.1.12 in it, and
// @vitest/mocker 4.1.11 in the lock with an advisory fixed in 4.1.12. The
// bump opens now, titled and labelled as a security fix, on the group's
// branch; its sibling waits for the window; the refresh carries the lift
// the runner reads back. Without the advisory the window holds the group.
func TestABumpThatMovesATransitiveFixIsASecurityFix(t *testing.T) {
	for _, c := range []struct {
		name     string
		advisory bool
	}{
		{"no advisory: the window holds the group", false},
		{"@vitest/mocker advised: the bump opens now", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/package.json", []byte(`{"name":"collab-rte","devDependencies":{"typescript-eslint":"8.70.0","vitest":"4.1.11"}}`), 0o644)
			os.WriteFile(root+"/package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"name":"collab-rte"},
				"node_modules/typescript-eslint":{"version":"8.70.0"},"node_modules/vitest":{"version":"4.1.11"},"node_modules/@vitest/mocker":{"version":"4.1.11"}}}`), 0o644)
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true, "osvTransitiveAlerts": true,
				"lockFileMaintenance": {"enabled": false},
				"packageRules": [{"matchManagers": ["npm"], "groupName": "npm packages", "schedule": ["after 1am and before 6am"], "labels": ["dependencies"]}],
				"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null}}`), 0o644)
			opts := treeOptions(t, root, "development/moselwal/collab-rte", time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC))
			opts.Datasources["npm"] = requiringDS{
				datedDS: datedDS{releases: map[string]map[string]time.Time{
					"vitest":            {"4.1.11": day("2026-10-01"), "4.1.12": day("2026-10-22")},
					"typescript-eslint": {"8.70.0": day("2026-10-01"), "8.72.1": day("2026-10-22")},
				}},
				requires: map[string]map[string]string{
					"vitest@4.1.12":            {"@vitest/mocker": "4.1.12"},
					"typescript-eslint@8.72.1": {"@typescript-eslint/parser": "8.72.1"},
				},
			}
			opts.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
			client := &askedAdvisories{answers: map[string][]osv.Advisory{}}
			if c.advisory {
				client.answers["@vitest/mocker"] = []osv.Advisory{{ID: "GHSA-qxg6-89mg-rqv8", Fixed: "4.1.12"}}
			}
			opts.Advisories = client
			plan, err := whatif(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			// The fixture's runner configuration names the npm group's
			// branch renovate/npm; the security refresh of the lock has
			// a branch of its own and is not looked at here.
			var b *model.Branch
			for i := range plan.Branches {
				if plan.Branches[i].Name == "renovate/npm" {
					b = &plan.Branches[i]
				}
			}
			if b == nil {
				t.Fatalf("no group branch; branches: %+v", plan.Branches)
			}
			if !c.advisory {
				if b.SuppressedBy != model.BlockSchedule || slices.Contains(b.Labels, "security") {
					t.Errorf("suppressedBy %q, labels %v: want the window's hold and no security label", b.SuppressedBy, b.Labels)
				}
				return
			}
			if b.SuppressedBy != "" {
				t.Fatalf("held by %q: the transitive fix waited for the window", b.SuppressedBy)
			}
			if len(b.UpdateKeys) != 1 || !strings.Contains(b.UpdateKeys[0], "vitest") {
				t.Errorf("members %v, want vitest alone until the sibling's window", b.UpdateKeys)
			}
			if !strings.HasSuffix(b.Title, "[security]") || !slices.Contains(b.Labels, "security") || !b.SecurityByLifts {
				t.Errorf("title %q, labels %v, securityByLifts %t", b.Title, b.Labels, b.SecurityByLifts)
			}
			var lifts []model.Lift
			for _, task := range b.Tasks {
				lifts = append(lifts, task.Lifts...)
			}
			if !slices.Equal(lifts, []model.Lift{{Package: "@vitest/mocker", Installed: "4.1.11", Fixed: "4.1.12"}}) {
				t.Errorf("lifts %+v, want @vitest/mocker read back after the refresh", lifts)
			}
			for _, u := range plan.Updates {
				if u.Dep.DepName == "typescript-eslint" && u.SecurityFix {
					t.Error("typescript-eslint, which moves no advised package, became a security fix")
				}
			}
		})
	}
}
