// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/versioning"
)

// askedAdvisories answers from a table by package name and records every
// query, so a test can say what was asked as well as what came back.
type askedAdvisories struct {
	answers map[string][]osv.Advisory
	err     error
	asked   []osv.Query
}

func (a *askedAdvisories) Check(_ context.Context, _ versioning.Registry, queries []osv.Query) ([]osv.Finding, error) {
	a.asked = append(a.asked, queries...)
	if a.err != nil {
		return nil, a.err
	}
	out := make([]osv.Finding, len(queries))
	for i, q := range queries {
		out[i] = osv.Finding{Query: q, Advisories: a.answers[q.PackageName]}
		for _, adv := range out[i].Advisories {
			out[i].Bound = adv.Fixed
		}
	}
	return out, nil
}

func lockVersioning(ds string) string {
	return map[string]string{"packagist": "composer", "npm": "npm"}[ds]
}

// The lock a site carries: one package its composer.json names, one only
// the lock knows (guzzle under a CMS), and a branch install no advisory
// range can contain.
var siteLocks = map[string]lockSet{
	"composer|composer.lock": {manager: "composer", path: "composer.lock", versions: map[string]string{
		"typo3/cms-core": "14.3.5", "guzzlehttp/psr7": "2.6.0", "koh/dev": "dev-main",
	}},
}

var siteDeps = []model.Dependency{{Manager: "composer", DepName: "typo3/cms-core", LockedVersion: "14.3.5"}}

func TestTransitiveAdvisoriesAskOnlyWhatNoManifestNames(t *testing.T) {
	client := &askedAdvisories{answers: map[string][]osv.Advisory{
		"guzzlehttp/psr7": {{ID: "GHSA-wxmh-65f7-jcvw", Fixed: "2.6.3"}},
	}}
	cfg := map[string]any{"osvVulnerabilityAlerts": true, "osvTransitiveAlerts": true}
	found, warns := checkLockedAdvisories(t.Context(), client, cfg, siteLocks, siteDeps, lockVersioning)

	if len(client.asked) != 1 || client.asked[0].PackageName != "guzzlehttp/psr7" || client.asked[0].Datasource != "packagist" || client.asked[0].Version != "2.6.0" {
		t.Fatalf("asked %+v, want guzzlehttp/psr7 2.6.0 at packagist alone", client.asked)
	}
	got := found["composer|composer.lock"]
	if len(got) != 1 || got[0].Package != "guzzlehttp/psr7" || got[0].Installed != "2.6.0" || got[0].Fixed != "2.6.3" {
		t.Fatalf("found %+v", found)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Msg, "transitive guzzlehttp/psr7 2.6.0: GHSA-wxmh-65f7-jcvw (fixed in 2.6.3)") || warns[0].File != "composer.lock" {
		t.Errorf("warnings %+v", warns)
	}
}

func TestTransitiveAdvisoriesStayOffUnlessAsked(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  map[string]any
	}{
		{"not opted in", map[string]any{"osvVulnerabilityAlerts": true}},
		{"advisories off", map[string]any{"osvTransitiveAlerts": true}},
		{"vulnerabilityAlerts disabled", map[string]any{"osvVulnerabilityAlerts": true, "osvTransitiveAlerts": true, "vulnerabilityAlerts": map[string]any{"enabled": false}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := &askedAdvisories{}
			found, warns := checkLockedAdvisories(t.Context(), client, c.cfg, siteLocks, siteDeps, lockVersioning)
			if found != nil || warns != nil || len(client.asked) != 0 {
				t.Errorf("found %v, warnings %v, asked %v", found, warns, client.asked)
			}
		})
	}
}

func TestTransitiveAdvisoriesUnreachableIsAWarning(t *testing.T) {
	client := &askedAdvisories{err: errors.New("connection refused")}
	cfg := map[string]any{"osvVulnerabilityAlerts": true, "osvTransitiveAlerts": true}
	found, warns := checkLockedAdvisories(t.Context(), client, cfg, siteLocks, siteDeps, lockVersioning)
	if found != nil || len(warns) != 1 || !strings.Contains(warns[0].Msg, "transitive packages are not checked") {
		t.Errorf("found %v, warnings %v", found, warns)
	}
}

func TestSecurityLockRefresh(t *testing.T) {
	deps := []model.Dependency{{Manager: "composer", File: "composer.json", DepName: "typo3/cms-core", LockedVersion: "14.3.5", LockFiles: []string{"composer.lock"}}}
	locks := map[string]string{"composer|.": "composer.lock"}
	finding := map[string][]model.Advisory{"composer|composer.lock": {{ID: "GHSA-wxmh-65f7-jcvw", Package: "guzzlehttp/psr7", Installed: "2.6.0", Fixed: "2.6.3"}}}
	on := map[string]any{"lockFileMaintenance": map[string]any{"enabled": true}}
	off := map[string]any{"lockFileMaintenance": map[string]any{"enabled": false}}
	for _, c := range []struct {
		name       string
		cfg        map[string]any
		transitive map[string][]model.Advisory
		want       int
		security   bool
	}{
		{"maintenance on, nothing found", on, nil, 1, false},
		{"maintenance on, a finding", on, finding, 1, true},
		{"maintenance off, nothing found", off, nil, 0, false},
		{"maintenance off, a finding still refreshes", off, finding, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ups := lockMaintenance(c.cfg, deps, locks, c.transitive)
			if len(ups) != c.want {
				t.Fatalf("%d updates, want %d", len(ups), c.want)
			}
			if c.want == 0 {
				return
			}
			if ups[0].SecurityFix != c.security || (len(ups[0].Dep.Advisories) > 0) != c.security {
				t.Errorf("update %+v", ups[0])
			}
		})
	}
}

func TestSecurityLockOverlayKeepsTheMaintenanceBranch(t *testing.T) {
	cfg := map[string]any{
		"schedule":    []any{"after 1am and before 6am"},
		"branchTopic": "lock-file-maintenance",
		"vulnerabilityAlerts": map[string]any{
			"schedule": []any{"at any time"}, "labels": []any{"security"}, "minimumReleaseAge": nil,
			"branchTopic": "{{{datasource}}}-{{{depNameSanitized}}}-vulnerability", "groupName": nil,
		},
	}
	got := securityLockOverlay(cfg)
	if s, _ := got["schedule"].([]any); !slices.Equal(s, []any{"at any time"}) {
		t.Errorf("schedule %v", got["schedule"])
	}
	if l, _ := got["labels"].([]any); !slices.Equal(l, []any{"security"}) {
		t.Errorf("labels %v", got["labels"])
	}
	if v, ok := got["minimumReleaseAge"]; !ok || v != nil {
		t.Errorf("minimumReleaseAge %v (present %t)", v, ok)
	}
	if got["branchTopic"] != "lock-file-maintenance" {
		t.Errorf("branchTopic %v: the refresh left its maintenance branch", got["branchTopic"])
	}
	if _, ok := got["groupName"]; ok {
		t.Error("groupName taken over from vulnerabilityAlerts")
	}
	if s, _ := cfg["schedule"].([]any); !slices.Equal(s, []any{"after 1am and before 6am"}) {
		t.Error("the overlay changed its input")
	}
}

// End to end: the nightly refresh waits for its window, the same refresh
// for a vulnerable transitive package does not, stays on the maintenance
// branch and carries the security label.
func TestATransitiveAdvisoryRefreshesTheLockNow(t *testing.T) {
	for _, c := range []struct {
		name     string
		advisory bool
		held     bool
	}{
		{"nothing found: the window holds it", false, true},
		{"an advisory on psr/log: refreshed now", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/composer.json", []byte(`{"name":"acme/site","require":{"monolog/monolog":"^3.0"}}`), 0o644)
			os.WriteFile(root+"/composer.lock", []byte(`{"packages":[{"name":"monolog/monolog","version":"3.8.0"},{"name":"psr/log","version":"3.0.0"}],"packages-dev":[]}`), 0o644)
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true, "osvTransitiveAlerts": true,
				"lockFileMaintenance": {"enabled": true, "schedule": ["after 1am and before 6am"]},
				"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null}}`), 0o644)
			at := time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC)
			opts := treeOptions(t, root, "development/moselwal/site", at)
			opts.Datasources["packagist"] = cannedDS{name: "packagist", scheme: "composer", releases: map[string][]string{"monolog/monolog": {"3.8.0"}}}
			opts.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
			client := &askedAdvisories{answers: map[string][]osv.Advisory{}}
			if c.advisory {
				client.answers["psr/log"] = []osv.Advisory{{ID: "GHSA-test-0000-0000", Fixed: "3.0.1"}}
			}
			opts.Advisories = client
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
			if b == nil {
				t.Fatalf("no maintenance branch; branches: %+v", plan.Branches)
			}
			if held := b.SuppressedBy != ""; held != c.held {
				t.Errorf("suppressedBy %q, want held %t", b.SuppressedBy, c.held)
			}
			if security := slices.Contains(b.Labels, "security"); security != c.advisory {
				t.Errorf("labels %v", b.Labels)
			}
		})
	}
}
