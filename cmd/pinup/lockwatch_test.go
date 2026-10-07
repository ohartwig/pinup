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

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/report"
)

// shopTree is the shape of ai-ready-platform/platform/commerce on
// 2026-10-07: a composer.lock and a package-lock.json side by side, and
// source-map-js 1.2.1 pinned only by the npm lock (postcss requires it).
func shopTree(t *testing.T, vulnerable bool, pkg string) *model.Plan {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(root+"/composer.json", []byte(`{"name":"acme/shop","require":{"monolog/monolog":"^3.0"}}`), 0o644)
	os.WriteFile(root+"/composer.lock", []byte(`{"packages":[{"name":"monolog/monolog","version":"3.8.0"},{"name":"psr/log","version":"3.0.0"}],"packages-dev":[]}`), 0o644)
	os.WriteFile(root+"/package.json", []byte(`{"name":"shop","version":"1.0.0","devDependencies":{"postcss":"^8.5.0"}}`), 0o644)
	os.WriteFile(root+"/package-lock.json", []byte(`{"name":"shop","lockfileVersion":3,"requires":true,"packages":{"":{"name":"shop","devDependencies":{"postcss":"^8.5.0"}},"node_modules/postcss":{"version":"8.5.6","dev":true,"dependencies":{"source-map-js":"^1.2.1"}},"node_modules/source-map-js":{"version":"1.2.1","dev":true}}}`), 0o644)
	os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true, "osvTransitiveAlerts": true, "timezone": "Europe/Berlin",
		"lockFileMaintenance": {"enabled": true, "schedule": ["after 1am and before 6am"], "automerge": true},
		"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null, "automerge": true}}`), 0o644)
	// 09:00 UTC is 11:00 in Berlin: outside the lock refresh window.
	opts := treeOptions(t, root, "ai-ready-platform/platform/commerce", time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	opts.Datasources["packagist"] = cannedDS{name: "packagist", scheme: "composer", releases: map[string][]string{"monolog/monolog": {"3.8.0"}}}
	opts.Datasources["npm"] = cannedDS{name: "npm", scheme: "npm", releases: map[string][]string{"postcss": {"8.5.6"}}}
	opts.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
	client := &askedAdvisories{answers: map[string][]osv.Advisory{}}
	if vulnerable {
		client.answers["source-map-js"] = []osv.Advisory{{ID: "GHSA-68fv-2mgg-jv7q", Aliases: []string{"CVE-2026-93749"}, Fixed: "1.2.2"}}
	}
	opts.Advisories = client
	opts.Package = pkg
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func lockBranch(t *testing.T, plan *model.Plan) *model.Branch {
	t.Helper()
	for i := range plan.Branches {
		if plan.Branches[i].Name == "renovate/lock-file-maintenance" {
			return &plan.Branches[i]
		}
	}
	t.Fatalf("no lock-file-maintenance branch: %+v", plan.Branches)
	return nil
}

// The commerce stall of 2026-10-07: the package-lock.json refresh was a
// security fix and released from its window, but it shares the branch
// with the composer.lock refresh, whose window held the whole branch until
// 23:00 UTC. The security refresh opens now; the composer refresh stays
// off the branch until its own window. Nothing found: the window holds
// both, as before.
func TestASecurityLockRefreshIsNotHeldByASiblingsWindow(t *testing.T) {
	for _, c := range []struct {
		name       string
		vulnerable bool
		held       bool
	}{
		{"source-map-js vulnerable: refreshed now", true, false},
		{"nothing found: the window holds the branch", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := shopTree(t, c.vulnerable, "")
			b := lockBranch(t, plan)
			if held := b.SuppressedBy != ""; held != c.held {
				t.Fatalf("branch suppressed by %q, want held %t", b.SuppressedBy, c.held)
			}
			if c.held {
				return
			}
			if !slices.Contains(b.Labels, "security") {
				t.Errorf("labels %v", b.Labels)
			}
			for _, k := range b.UpdateKeys {
				if strings.Contains(k, "composer.lock") {
					t.Errorf("the composer refresh rides along outside its window: %v", b.UpdateKeys)
				}
			}
		})
	}
}

// The full run hands the lock's transitive packages to the index, marked
// with their lock and without a consumer entry; the watch then asks about
// them like any dependency.
func TestTheIndexCarriesLockOnlyPackages(t *testing.T) {
	plan := shopTree(t, false, "")
	idx := report.NewIndex()
	idx.Record("ai-ready-platform/platform/commerce", plan, time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	var got *report.Dependency
	for i, d := range idx.Dependencies["ai-ready-platform/platform/commerce"] {
		if d.PackageName == "source-map-js" {
			got = &idx.Dependencies["ai-ready-platform/platform/commerce"][i]
		}
	}
	if got == nil || got.Version != "1.2.1" || got.Lock != "package-lock.json" || got.Datasource != "npm" {
		t.Fatalf("index entry for source-map-js = %+v", got)
	}
	if _, listed := idx.Consumers["npm|source-map-js"]; listed {
		t.Errorf("a transitive package is a consumer entry: %v", idx.Consumers["npm|source-map-js"])
	}
	if slices.ContainsFunc(idx.Dependencies["ai-ready-platform/platform/commerce"], func(d report.Dependency) bool { return d.PackageName == "psr/log" && d.Lock == "composer.lock" }) == false {
		t.Errorf("psr/log from composer.lock missing: %+v", idx.Dependencies["ai-ready-platform/platform/commerce"])
	}
}

// The watch asks about a lock-only entry and reports a new advisory with
// the repository, which is what starts the targeted run.
func TestTheWatchAsksAboutLockOnlyPackages(t *testing.T) {
	calls := 0
	rt := harness.NewRefusingTransport(t)
	rt.Handle("api.osv.dev", osvHandler(t, &calls))
	idx := advisoriesIndex()
	idx.Dependencies["ai-ready-platform/platform/commerce"] = []report.Dependency{
		{Datasource: "npm", PackageName: "lodash", Version: "4.17.20", Versioning: "npm", File: "package-lock.json", Lock: "package-lock.json"},
	}
	only := glob.NewSet([]string{"ai-ready-platform/**"})
	rep, err := watchAdvisories(context.Background(), &osv.Client{Transport: rt}, nil, idx, only, "pinup/shadow-fixture", &advisoriesState{Seen: map[string]time.Time{}}, time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.New) != 1 || !slices.Contains(rep.New[0].Repositories, "ai-ready-platform/platform/commerce") {
		t.Fatalf("new = %+v", rep.New)
	}
}

// The watch's targeted run names the transitive package; the run plans the
// lock's security refresh and opens it now.
func TestATargetedRunForATransitivePackageRefreshesTheLock(t *testing.T) {
	plan := shopTree(t, true, "npm|source-map-js")
	b := lockBranch(t, plan)
	if b.SuppressedBy != "" || !slices.Contains(b.Labels, "security") {
		t.Fatalf("branch suppressed by %q, labels %v", b.SuppressedBy, b.Labels)
	}
}
