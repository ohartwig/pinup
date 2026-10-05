// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/packagistadv"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/versioning"
)

// wolfiAdvisories answers through the real osv.Asks rule, records what
// it was asked, and reports one advisory for openssl.
type wolfiAdvisories struct{ asked []osv.Query }

func (r *wolfiAdvisories) Check(_ context.Context, _ versioning.Registry, qs []osv.Query) ([]osv.Finding, error) {
	out := make([]osv.Finding, len(qs))
	for i, q := range qs {
		r.asked = append(r.asked, q)
		out[i] = osv.Finding{Query: q, Ecosystem: wolfiCmpOr(osv.Ecosystem(q.Datasource), q.Ecosystem)}
		if q.PackageName == "openssl" {
			out[i].Advisories = []osv.Advisory{{ID: "CGA-test-0001", Fixed: "3.0.8-r0"}}
			out[i].Bound = "3.0.8-r0"
		}
	}
	return out, nil
}

func wolfiCmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// customDatasources.wolfi.osvEcosystem asks OSV's Wolfi ecosystem about the
// apk pins custom.wolfi resolves - and only those: the php platform entry
// resolved through the same datasource is semver and stays unasked, as does
// custom.koh-apk, which names no ecosystem. Everything unasked is counted
// by datasource, so the dashboard can say what was never looked up.
func TestAWolfiPinIsAskedUnderTheConfiguredEcosystem(t *testing.T) {
	deps := func() []model.Dependency {
		return []model.Dependency{
			{DepName: "openssl", Datasource: "custom.wolfi", CurrentValue: "3.0.0-r0", Versioning: "apk", File: "Containerfile"},
			{DepName: "php", Datasource: "custom.wolfi", CurrentValue: "8.5.11", Versioning: "semver", File: "composer.json"},
			{DepName: "frankenphp-8.5", Datasource: "custom.koh-apk", CurrentValue: "1.13.0-r0", Versioning: "apk", File: "Containerfile"},
			{DepName: "registry.example/base", Datasource: "docker", CurrentValue: "1.2.3", File: "Containerfile"},
			{DepName: "left-pad", Datasource: "npm", CurrentValue: "1.3.0", File: "package.json"},
		}
	}
	for _, tc := range []struct {
		name       string
		custom     map[string]any
		asked      []string
		notCovered map[string]int
		openssl    string // the bound written on openssl
	}{
		{"wolfi names its ecosystem", map[string]any{"wolfi": map[string]any{"osvEcosystem": "Wolfi"}},
			[]string{"custom.wolfi/openssl/Wolfi", "npm/left-pad/"}, map[string]int{"custom.wolfi": 1, "custom.koh-apk": 1, "docker": 1}, "3.0.8-r0"},
		{"no ecosystem configured", map[string]any{"wolfi": map[string]any{"format": "json"}},
			[]string{"npm/left-pad/"}, map[string]int{"custom.wolfi": 2, "custom.koh-apk": 1, "docker": 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{"osvVulnerabilityAlerts": true, "customDatasources": tc.custom}
			ds := deps()
			rec := &wolfiAdvisories{}
			_, coverage := checkAdvisories(context.Background(), rec, cfg, ds, func(string) string { return "semver" }, func(model.Dependency) *model.ReleaseSet { return nil })
			var asked []string
			for _, q := range rec.asked {
				asked = append(asked, q.Datasource+"/"+q.PackageName+"/"+q.Ecosystem)
			}
			if strings.Join(asked, " ") != strings.Join(tc.asked, " ") {
				t.Errorf("asked %v, want %v", asked, tc.asked)
			}
			if coverage == nil || !maps.Equal(coverage.NotCovered, tc.notCovered) || coverage.Asked != len(tc.asked) {
				t.Errorf("coverage %+v, want asked %d, not covered %v", coverage, len(tc.asked), tc.notCovered)
			}
			if ds[0].VulnerabilityBound != tc.openssl {
				t.Errorf("openssl bound %q, want %q", ds[0].VulnerabilityBound, tc.openssl)
			}
			wantEco := ""
			if tc.openssl != "" {
				wantEco = "Wolfi"
			}
			if ds[0].OSVEcosystem != wantEco || ds[4].OSVEcosystem != "" {
				t.Errorf("osvEcosystem on the deps: openssl %q, left-pad %q", ds[0].OSVEcosystem, ds[4].OSVEcosystem)
			}
		})
	}
}

// The consumer index carries the ecosystem, so the advisory watch - which
// reads no configuration - asks Wolfi too; what it cannot ask, it counts.
func TestTheWatchAsksTheIndexedEcosystemAndCountsTheRest(t *testing.T) {
	plan := &model.Plan{Deps: []model.Dependency{
		{DepName: "openssl", Datasource: "custom.wolfi", CurrentValue: "3.0.0-r0", Versioning: "apk", OSVEcosystem: "Wolfi", File: "Containerfile"},
		{DepName: "frankenphp-8.5", Datasource: "custom.koh-apk", CurrentValue: "1.13.0-r0", Versioning: "apk", File: "Containerfile"},
		{DepName: "registry.example/base", Datasource: "docker", CurrentValue: "1.2.3", File: "Containerfile"},
	}}
	idx := report.NewIndex()
	idx.Record("group/app", plan, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if got := idx.Dependencies["group/app"][1].OSVEcosystem; got != "Wolfi" { // sorted by name: frankenphp, openssl, registry
		t.Fatalf("index lost the ecosystem: %+v", idx.Dependencies["group/app"])
	}
	srv := &wolfiAdvisories{}
	rep, err := watchAdvisories(context.Background(), srv, nil, idx, nil, "", &advisoriesState{Seen: map[string]time.Time{}}, time.Date(2026, 10, 5, 12, 15, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.asked) != 3 || srv.asked[1].Ecosystem != "Wolfi" {
		t.Errorf("asked %+v", srv.asked)
	}
	if rep.Queried != 1 || !maps.Equal(rep.NotCovered, map[string]int{"custom.koh-apk": 1, "docker": 1}) {
		t.Errorf("queried %d, not covered %v", rep.Queried, rep.NotCovered)
	}
	if len(rep.New) != 1 || rep.New[0].Advisory != "CGA-test-0001" {
		t.Errorf("new %+v", rep.New)
	}
}

// The gap reads the same on the dashboard and in the watch's output.
func TestNotCoveredLine(t *testing.T) {
	for _, tc := range []struct {
		in   map[string]int
		want string
	}{
		{nil, ""},
		{map[string]int{"docker": 0}, ""},
		{map[string]int{"custom.koh-apk": 12, "docker": 30, "gitlab-tags": 12}, "not covered by advisories: 54 (docker 30, custom.koh-apk 12, gitlab-tags 12)"},
	} {
		if got := report.NotCoveredLine(tc.in); got != tc.want {
			t.Errorf("NotCoveredLine(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	plan := &model.Plan{AdvisoryCoverage: &model.AdvisoryCoverage{Asked: 7, NotCovered: map[string]int{"docker": 3}}}
	body := report.Dashboard(plan, nil, nil, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(body, "Advisories were asked for 7 of them; not covered by advisories: 3 (docker 3).") {
		t.Errorf("dashboard does not state the gap:\n%s", body)
	}
}

// Through the run's real wrapper: advisorySources hands the Wolfi query to
// OSV with its ecosystem, and Packagist - behind a transport that refuses
// every host - is never asked about it.
func TestAWolfiPinFlowsThroughAdvisorySources(t *testing.T) {
	rec := &wolfiAdvisories{}
	src := advisorySources{osv: rec, packagist: &packagistadv.Client{Transport: harness.NewRefusingTransport(t)}}
	cfg := map[string]any{"osvVulnerabilityAlerts": true, "customDatasources": map[string]any{"wolfi": map[string]any{"osvEcosystem": "Wolfi"}}}
	deps := []model.Dependency{{DepName: "openssl", Datasource: "custom.wolfi", CurrentValue: "3.0.0-r0", Versioning: "apk", File: "Containerfile"}}
	warns, coverage := checkAdvisories(t.Context(), src, cfg, deps, func(string) string { return "semver" }, func(model.Dependency) *model.ReleaseSet { return nil })
	if len(warns) != 0 {
		t.Errorf("warnings %v", warns)
	}
	if len(rec.asked) != 1 || rec.asked[0].Ecosystem != "Wolfi" || deps[0].VulnerabilityBound != "3.0.8-r0" {
		t.Errorf("asked %+v, bound %q", rec.asked, deps[0].VulnerabilityBound)
	}
	if coverage.Asked != 1 || coverage.Uncovered() != 0 {
		t.Errorf("coverage %+v", coverage)
	}
}
