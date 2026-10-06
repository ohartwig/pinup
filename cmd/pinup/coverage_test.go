// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/report"
)

// Every dependency the run checks lands in one of three states: an advisory
// source was asked (with which), only its publisher's withdrawal list
// speaks for it, or nothing does (with why). The plan carries the state per
// dependency; the coverage counts them, and notCovered keeps its meaning:
// covered by nothing at all.
func TestEveryDependencyHasACoverageState(t *testing.T) {
	const own = "https://apk.example.org/withdrawn.json"
	const images = "https://lists.example.org/withdrawn-images.json"
	deps := []model.Dependency{
		{DepName: "left-pad", Datasource: "npm", CurrentValue: "1.3.0", File: "package.json"},
		{DepName: "frankenphp-8.5", Datasource: "custom.koh-apk", CurrentValue: "1.13.0-r0", Versioning: "apk", File: "Containerfile"},
		{DepName: "docker.io/library/nginx", Datasource: "docker", CurrentValue: "1.27", File: "Containerfile"},
		{DepName: "registry.example.org/devops/images/x", Datasource: "docker", CurrentValue: "2.0.0", File: "Containerfile"},
		{DepName: "php", Datasource: "custom.wolfi", CurrentValue: "8.5.11", Versioning: "semver", File: "composer.json"},
		{DepName: "lodash", Datasource: "npm", CurrentValue: "^9.0.0", File: "package.json"},
		{DepName: "openssl", Datasource: "custom.wolfi", CurrentValue: "3.0.0-r0", Versioning: "apk", File: "Containerfile"},
		{DepName: "skipped", Datasource: "npm", CurrentValue: "1.0.0", SkipReason: "ignored", File: "package.json"},
	}
	lists := map[string]string{"frankenphp-8.5": own, "registry.example.org/devops/images/x": images, "openssl": own}
	releasesOf := func(d model.Dependency) *model.ReleaseSet {
		if l, ok := lists[d.DepName]; ok {
			return &model.ReleaseSet{WithdrawalList: l}
		}
		return &model.ReleaseSet{}
	}
	cfg := map[string]any{"osvVulnerabilityAlerts": true, "customDatasources": map[string]any{"wolfi": map[string]any{"osvEcosystem": "Wolfi"}}}
	_, c := checkAdvisories(t.Context(), &wolfiAdvisories{}, cfg, deps, func(ds string) string {
		if ds == "npm" {
			return "npm"
		}
		return "docker"
	}, releasesOf)
	coverWithdrawals(c, deps, releasesOf)

	for _, tc := range []struct {
		dep                       string
		state, reason, withdrawal string
		sources                   []string
	}{
		{"left-pad", model.CoveredByAdvisories, "", "", []string{"osv"}},
		{"frankenphp-8.5", model.CoveredByWithdrawal, model.ReasonNoEcosystem, own, nil},
		{"docker.io/library/nginx", model.NotCovered, model.ReasonNoEcosystem, "", nil},
		{"registry.example.org/devops/images/x", model.CoveredByWithdrawal, model.ReasonNoEcosystem, images, nil},
		{"php", model.NotCovered, model.ReasonOtherVersioning, "", nil},
		{"lodash", model.NotCovered, model.ReasonNoVersion, "", nil},
		{"openssl", model.CoveredByAdvisories, "", own, []string{"osv"}},
	} {
		i := slices.IndexFunc(deps, func(d model.Dependency) bool { return d.DepName == tc.dep })
		got := deps[i].AdvisoryCoverage
		if got == nil || got.State != tc.state || got.Reason != tc.reason || got.Withdrawal != tc.withdrawal || !slices.Equal(got.Sources, tc.sources) {
			t.Errorf("%s: coverage %+v, want state %s reason %q withdrawal %q sources %v", tc.dep, got, tc.state, tc.reason, tc.withdrawal, tc.sources)
		}
	}
	if deps[7].AdvisoryCoverage != nil {
		t.Errorf("a skipped dependency is not checked and carries no state: %+v", deps[7].AdvisoryCoverage)
	}
	if c.Asked != 2 {
		t.Errorf("asked %d, want 2", c.Asked)
	}
	if want := map[string]int{"docker": 1, "custom.wolfi": 1, "npm": 1}; !maps.Equal(c.NotCovered, want) {
		t.Errorf("not covered %v, want %v", c.NotCovered, want)
	}
	if want := map[string]int{"custom.koh-apk": 1, "docker": 1}; !maps.Equal(c.WithdrawalOnly, want) {
		t.Errorf("withdrawal only %v, want %v", c.WithdrawalOnly, want)
	}
	if want := map[string]int{model.ReasonNoEcosystem: 3, model.ReasonOtherVersioning: 1, model.ReasonNoVersion: 1}; !maps.Equal(c.Reasons, want) {
		t.Errorf("reasons %v, want %v", c.Reasons, want)
	}
}

// Packagist is named beside OSV for composer packages, and only when the
// run reads Packagist.
func TestAdvisorySourceNames(t *testing.T) {
	with := advisorySources{packagist: nil}
	if got := advisorySourceNames(with, "packagist"); !slices.Equal(got, []string{"osv"}) {
		t.Errorf("without Packagist: %v", got)
	}
	if got := advisorySourceNames(cannedAdvisories{}, "packagist"); !slices.Equal(got, []string{"osv", "packagist"}) {
		t.Errorf("with Packagist: %v", got)
	}
	if got := advisorySourceNames(cannedAdvisories{}, "npm"); !slices.Equal(got, []string{"osv"}) {
		t.Errorf("npm: %v", got)
	}
}

// The dashboard states both gaps: what only a withdrawal list covers, and
// what nothing does.
func TestDashboardStatesWithdrawalOnlyCoverage(t *testing.T) {
	plan := &model.Plan{AdvisoryCoverage: &model.AdvisoryCoverage{Asked: 7, NotCovered: map[string]int{"docker": 3}, WithdrawalOnly: map[string]int{"custom.koh-apk": 68}}}
	body := report.Dashboard(plan, nil, nil, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	want := "Advisories were asked for 7 of them; covered by a withdrawal list only: 68 (custom.koh-apk 68); not covered by advisories: 3 (docker 3)."
	if !strings.Contains(body, want) {
		t.Errorf("dashboard lacks %q:\n%s", want, body)
	}
}

// The watch reads the index, not the plan: the index carries the withdrawal
// list, and a dependency it covers is counted as withdrawalOnly, not as
// notCovered.
func TestTheWatchSplitsWithdrawalOnlyFromNotCovered(t *testing.T) {
	plan := &model.Plan{Deps: []model.Dependency{
		{DepName: "frankenphp-8.5", Datasource: "custom.koh-apk", CurrentValue: "1.13.0-r0", Versioning: "apk", File: "Containerfile",
			AdvisoryCoverage: &model.DependencyCoverage{State: model.CoveredByWithdrawal, Reason: model.ReasonNoEcosystem, Withdrawal: "https://apk.example.org/withdrawn.json"}},
		{DepName: "docker.io/library/nginx", Datasource: "docker", CurrentValue: "1.27", File: "Containerfile",
			AdvisoryCoverage: &model.DependencyCoverage{State: model.NotCovered, Reason: model.ReasonNoEcosystem}},
	}}
	idx := report.NewIndex()
	idx.Record("group/app", plan, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	rep, err := watchAdvisories(t.Context(), &wolfiAdvisories{}, nil, idx, nil, "", &advisoriesState{Seen: map[string]time.Time{}}, time.Date(2026, 10, 6, 12, 15, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(rep.WithdrawalOnly, map[string]int{"custom.koh-apk": 1}) || !maps.Equal(rep.NotCovered, map[string]int{"docker": 1}) {
		t.Errorf("withdrawal only %v, not covered %v", rep.WithdrawalOnly, rep.NotCovered)
	}
}
