// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"maps"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/wire"
)

// A rule maps a pin without an advisory ecosystem to an OSV package -
// trivy, released on GitHub, is the Go module github.com/aquasecurity/trivy
// (checked 2026-10-06: trivy 0.50.0 has 8 advisories there). The mapping
// is explicit and names its rule; a pin no rule maps stays unasked, and
// pinup never guesses one from the repository name.
func TestARuleMapsAPinToAnOSVPackage(t *testing.T) {
	engine, err := rules.Compile([]any{
		map[string]any{"matchDepNames": []any{"aquasecurity/trivy"}, "osvPackage": map[string]any{"ecosystem": "Go", "name": "github.com/aquasecurity/trivy"}},
	}, wire.Versionings())
	if err != nil {
		t.Fatal(err)
	}
	trivy := applyDepRules(engine, map[string]any{}, model.Dependency{DepName: "aquasecurity/trivy", Datasource: "github-releases", CurrentValue: "v0.50.0", Versioning: "semver", File: ".gitlab-ci.yml"})
	other := applyDepRules(engine, map[string]any{}, model.Dependency{DepName: "aquasecurity/tracee", Datasource: "github-releases", CurrentValue: "v0.20.0", Versioning: "semver", File: ".gitlab-ci.yml"})
	if trivy.OSVPackage == nil || *trivy.OSVPackage != (model.OSVPackage{Ecosystem: "Go", Name: "github.com/aquasecurity/trivy"}) || trivy.OSVPackageBy != "packageRules[0]" {
		t.Fatalf("trivy mapping %+v by %q", trivy.OSVPackage, trivy.OSVPackageBy)
	}
	if other.OSVPackage != nil {
		t.Fatalf("an unmapped pin got a mapping: %+v", other.OSVPackage)
	}
	deps := []model.Dependency{trivy, other}
	rec := &wolfiAdvisories{}
	_, c := checkAdvisories(t.Context(), rec, map[string]any{"osvVulnerabilityAlerts": true}, deps, func(string) string { return "semver" }, func(model.Dependency) *model.ReleaseSet { return nil })
	if len(rec.asked) != 1 || rec.asked[0].PackageName != "github.com/aquasecurity/trivy" || rec.asked[0].Ecosystem != "Go" || !rec.asked[0].Explicit {
		t.Errorf("asked %+v, want trivy as the Go module", rec.asked)
	}
	if deps[0].AdvisoryCoverage.State != model.CoveredByAdvisories || deps[1].AdvisoryCoverage.Reason != model.ReasonNoEcosystem {
		t.Errorf("coverage: trivy %+v, tracee %+v", deps[0].AdvisoryCoverage, deps[1].AdvisoryCoverage)
	}
	if !maps.Equal(c.NotCovered, map[string]int{"github-releases": 1}) {
		t.Errorf("not covered %v", c.NotCovered)
	}
	// The watch asks the same, from the index.
	idx := report.NewIndex()
	idx.Record("group/app", &model.Plan{Deps: deps}, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	w := &wolfiAdvisories{}
	if _, err := watchAdvisories(t.Context(), w, nil, idx, nil, "", &advisoriesState{Seen: map[string]time.Time{}}, time.Date(2026, 10, 6, 12, 15, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	asked := false
	for _, q := range w.asked {
		if q.PackageName == "github.com/aquasecurity/trivy" && q.Ecosystem == "Go" && q.Explicit {
			asked = true
		}
	}
	if !asked {
		t.Errorf("the watch did not ask the mapped package: %+v", w.asked)
	}
}

// An explicit mapping is asked in its ecosystem even where the datasource
// has one of its own; without Explicit the datasource's wins.
func TestExplicitEcosystemWins(t *testing.T) {
	if !osv.Asks(osv.Query{Datasource: "github-releases", Ecosystem: "Go", Explicit: true, Versioning: "semver"}) {
		t.Error("an explicit Go mapping on github-releases is not asked")
	}
	if osv.Asks(osv.Query{Datasource: "github-releases", Versioning: "semver"}) {
		t.Error("github-releases without a mapping is asked")
	}
}
