// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/kev"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
)

// A private advisory takes OSV's path end to end: OSV knows nothing about
// zzz/vuln, the installation's own feed does (a recorded fixture, as a
// CSAF-generated feed would publish it), and the fix opens as a security
// branch - labelled, first, without the soak - and CISA's catalog matches
// it through the CVE alias. Without the feed the same update waits for its
// release age.
func TestAPrivateAdvisoryTakesTheSecurityPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		feed bool
	}{{"with the private feed", true}, {"without it", false}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/composer.json", []byte(`{"name":"acme/site","require":{"aaa/routine":"1.0.0","zzz/vuln":"2.0.0"}}`), 0o644)
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true,
				"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null},
				"minimumReleaseAge": "3 days"}`), 0o644)
			feedDir := t.TempDir()
			os.WriteFile(filepath.Join(feedDir, "KOH-2026-0009.json"), []byte(`{"id":"KOH-2026-0009","aliases":["CVE-2026-0009"],"summary":"own package, own advisory",
				"affected":[{"package":{"purl":"pkg:composer/zzz/vuln"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"2.0.1"}]}]}]}`), 0o644)
			at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			opts := treeOptions(t, root, "development/moselwal/site", at)
			opts.Datasources["packagist"] = cannedDS{name: "packagist", scheme: "composer", releases: map[string][]string{
				"aaa/routine": {"1.0.0", "1.0.1"}, "zzz/vuln": {"2.0.0", "2.0.1"},
			}}
			src := advisorySources{osv: &askedAdvisories{}}
			if tc.feed {
				src.private = &osv.PrivateFeed{Sources: []string{"file://" + feedDir}}
			}
			opts.Advisories = src
			opts.Exploited = &cannedCatalog{Cat: kev.Catalog{"CVE-2026-0009": {CVE: "CVE-2026-0009"}}}
			plan, err := whatif(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(plan.Branches, func(b model.Branch) bool { return strings.Contains(b.Name, "zzz") })
			if i < 0 {
				t.Fatalf("no branch for zzz/vuln: %+v", plan.Branches)
			}
			fix := plan.Branches[i]
			if tc.feed {
				if fix.SuppressedBy != "" || i != 0 || !slices.Contains(fix.Labels, "security") || !slices.Contains(fix.Labels, kevLabel) {
					t.Errorf("fix at %d held by %q labels %v; want first, open, security + %s", i, fix.SuppressedBy, fix.Labels, kevLabel)
				}
				d := plan.Deps[slices.IndexFunc(plan.Deps, func(d model.Dependency) bool { return d.DepName == "zzz/vuln" })]
				if d.AdvisoryCoverage == nil || !slices.Contains(d.AdvisoryCoverage.Sources, "private") {
					t.Errorf("coverage %+v names no private source", d.AdvisoryCoverage)
				}
				return
			}
			if fix.SuppressedBy != model.BlockMinimumReleaseAge {
				t.Errorf("without the feed the soak holds it; held by %q", fix.SuppressedBy)
			}
		})
	}
}

// A gitlab-* dependency has no OSV ecosystem; with a private feed it is
// asked and counted as covered, and without one it stays no-ecosystem.
func TestAPrivateFeedCoversFirstPartyArtefacts(t *testing.T) {
	feedDir := t.TempDir()
	os.WriteFile(filepath.Join(feedDir, "a.json"), []byte(`{"id":"KOH-2026-0001","affected":[{"package":{"purl":"pkg:gitlab/devops/ci-cd-components/lint-tools"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.30.0"},{"fixed":"1.36.27"}]}]}]}`), 0o644)
	for _, tc := range []struct {
		name  string
		feed  *osv.PrivateFeed
		state string
		bound string
	}{
		{"with the feed", &osv.PrivateFeed{Sources: []string{"file://" + feedDir}}, model.CoveredByAdvisories, "1.36.27"},
		{"without", nil, model.NotCovered, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := []model.Dependency{{DepName: "devops/ci-cd-components/lint-tools", PackageName: "devops/ci-cd-components/lint-tools", Datasource: "gitlab-tags", Versioning: "semver", CurrentValue: "1.36.26", File: ".gitlab-ci.yml"}}
			_, c := checkAdvisories(t.Context(), advisorySources{osv: &askedAdvisories{}, private: tc.feed}, map[string]any{"osvVulnerabilityAlerts": true}, deps, func(string) string { return "semver" }, func(model.Dependency) *model.ReleaseSet { return nil })
			got := deps[0].AdvisoryCoverage
			if got == nil || got.State != tc.state || deps[0].VulnerabilityBound != tc.bound {
				t.Errorf("coverage %+v bound %q, want %s %q", got, deps[0].VulnerabilityBound, tc.state, tc.bound)
			}
			if tc.feed != nil && (c.Asked != 1 || !slices.Equal(got.Sources, []string{"private"})) {
				t.Errorf("asked %d sources %v", c.Asked, got.Sources)
			}
		})
	}
}

// An advisory OSV already carries is not listed again from the private
// feed, matched by any of its ids or aliases.
func TestAPrivateAdvisoryOSVCarriesIsListedOnce(t *testing.T) {
	feedDir := t.TempDir()
	os.WriteFile(filepath.Join(feedDir, "a.json"), []byte(`{"id":"KOH-2026-0010","aliases":["CVE-2026-0010"],"affected":[{"package":{"purl":"pkg:npm/left-pad"},"versions":["1.3.0"]}]}`), 0o644)
	deps := []model.Dependency{{DepName: "left-pad", Datasource: "npm", Versioning: "semver", CurrentValue: "1.3.0", File: "package.json"}}
	pub := &askedAdvisories{answers: map[string][]osv.Advisory{"left-pad": {{ID: "GHSA-aaaa-bbbb-cccc", Aliases: []string{"CVE-2026-0010"}, Fixed: "1.3.1"}}}}
	checkAdvisories(t.Context(), advisorySources{osv: pub, private: &osv.PrivateFeed{Sources: []string{"file://" + feedDir}}}, map[string]any{"osvVulnerabilityAlerts": true}, deps, func(string) string { return "semver" }, func(model.Dependency) *model.ReleaseSet { return nil })
	if len(deps[0].Advisories) != 1 || deps[0].Advisories[0].ID != "GHSA-aaaa-bbbb-cccc" || deps[0].VulnerabilityBound != "1.3.1" {
		t.Errorf("advisories %+v bound %q, want the OSV one alone", deps[0].Advisories, deps[0].VulnerabilityBound)
	}
	if !slices.Equal(deps[0].AdvisoryCoverage.Sources, []string{"osv", "private"}) {
		t.Errorf("sources %v", deps[0].AdvisoryCoverage.Sources)
	}
}
