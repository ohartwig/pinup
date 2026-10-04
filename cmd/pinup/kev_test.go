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

	"github.com/ohartwig/pinup/kev"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
)

// cannedCatalog answers Load from a table; Err makes it fail.
type cannedCatalog struct {
	Cat   kev.Catalog
	Err   error
	Loads int
}

func (c *cannedCatalog) Load(context.Context, time.Time) (kev.Catalog, []string, error) {
	c.Loads++
	return c.Cat, nil, c.Err
}

var log4shell = kev.Catalog{"CVE-2021-44228": {CVE: "CVE-2021-44228", DateAdded: "2021-12-10", DueDate: "2021-12-24", Ransomware: "Known"}}

func TestMarkExploited(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		deps      []model.Dependency
		lock      []model.Advisory
		err       error
		loads     int
		marked    []string // advisory ids carrying a mark, deps then lock
		warnsWith string
	}{
		{"no advisory: the feed is not read", []model.Dependency{{DepName: "a"}}, nil, nil, 0, nil, ""},
		{"an alias matches", []model.Dependency{{DepName: "log4j", Advisories: []model.Advisory{{ID: "GHSA-jfh8-c2jp-5v3q", Aliases: []string{"CVE-2021-44228"}}}}}, nil, nil, 1, []string{"GHSA-jfh8-c2jp-5v3q"}, ""},
		{"not listed: no mark", []model.Dependency{{DepName: "b", Advisories: []model.Advisory{{ID: "GHSA-x", Aliases: []string{"CVE-2026-93748"}}}}}, nil, nil, 1, nil, ""},
		{"a withdrawal is no advisory to mark", []model.Dependency{{DepName: "c", Advisories: []model.Advisory{{ID: "CVE-2021-44228", Withdrawn: true}}}}, nil, nil, 1, nil, ""},
		{"a transitive package is marked too", []model.Dependency{{DepName: "d"}}, []model.Advisory{{ID: "CVE-2021-44228", Package: "log4j"}}, nil, 1, []string{"CVE-2021-44228"}, ""},
		{"an unreadable catalog is a warning", []model.Dependency{{DepName: "e", Advisories: []model.Advisory{{ID: "CVE-2021-44228"}}}}, nil, errors.New("503"), 1, nil, "without the KEV priority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := &cannedCatalog{Cat: log4shell, Err: tc.err}
			transitive := map[string][]model.Advisory{}
			if tc.lock != nil {
				transitive["maven|pom.lock"] = tc.lock
			}
			warns := markExploited(t.Context(), cat, now, tc.deps, transitive)
			if cat.Loads != tc.loads {
				t.Errorf("catalog read %d times, want %d", cat.Loads, tc.loads)
			}
			var marked []string
			for _, d := range tc.deps {
				for _, a := range d.Advisories {
					if a.Exploited != nil {
						marked = append(marked, a.ID)
					}
				}
			}
			for _, a := range transitive["maven|pom.lock"] {
				if a.Exploited != nil {
					marked = append(marked, a.ID)
				}
			}
			if !slices.Equal(marked, tc.marked) {
				t.Errorf("marked %v, want %v", marked, tc.marked)
			}
			if got := len(warns) > 0 && strings.Contains(warns[0].Msg, tc.warnsWith); (tc.warnsWith != "") != got {
				t.Errorf("warnings %+v", warns)
			}
		})
	}
	if warns := markExploited(t.Context(), nil, now, []model.Dependency{{Advisories: []model.Advisory{{ID: "CVE-2021-44228"}}}}, nil); warns != nil {
		t.Errorf("no catalog configured: %v", warns)
	}
}

// End to end: the same vulnerable dependency, its fix a major one day old,
// under a configuration whose vulnerabilityAlerts keep a three-day soak.
// Listed in KEV, the fix opens now, first, labelled security:kev; not
// listed, the soak holds it and the routine update leads.
func TestAnExploitedFixOpensFirstWithoutTheSoak(t *testing.T) {
	for _, tc := range []struct {
		name    string
		catalog kev.Catalog
		kev     bool
	}{
		{"listed in KEV", kev.Catalog{"CVE-2026-0001": {CVE: "CVE-2026-0001", DateAdded: "2026-08-01", DueDate: "2026-08-22"}}, true},
		{"not listed", kev.Catalog{"CVE-1999-0001": {CVE: "CVE-1999-0001"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/composer.json", []byte(`{"name":"acme/site","require":{"aaa/routine":"1.0.0","zzz/vuln":"2.0.0"}}`), 0o644)
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true,
				"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": "3 days"}}`), 0o644)
			at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			opts := treeOptions(t, root, "development/moselwal/site", at)
			opts.Datasources["packagist"] = cannedDS{name: "packagist", scheme: "composer", releases: map[string][]string{
				"aaa/routine": {"1.0.0", "1.0.1"}, "zzz/vuln": {"2.0.0", "3.0.0"},
			}}
			opts.Advisories = &askedAdvisories{answers: map[string][]osv.Advisory{
				"zzz/vuln": {{ID: "GHSA-test-kev0-0001", Aliases: []string{"CVE-2026-0001"}, Fixed: "3.0.0"}},
			}}
			opts.Exploited = &cannedCatalog{Cat: tc.catalog}
			plan, err := whatif(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			var fix *model.Branch
			at0 := -1
			for i := range plan.Branches {
				if strings.Contains(plan.Branches[i].Name, "zzz") {
					fix, at0 = &plan.Branches[i], i
				}
			}
			if fix == nil {
				t.Fatalf("no fix branch: %+v", plan.Branches)
			}
			if held := fix.SuppressedBy != ""; held == tc.kev {
				t.Errorf("fix held by %q; kev %t", fix.SuppressedBy, tc.kev)
			}
			if !tc.kev && fix.SuppressedBy != model.BlockMinimumReleaseAge {
				t.Errorf("without KEV the soak should hold it, held by %q", fix.SuppressedBy)
			}
			if got := slices.Contains(fix.Labels, kevLabel); got != tc.kev {
				t.Errorf("labels %v", fix.Labels)
			}
			if first := at0 == 0; first != tc.kev {
				t.Errorf("fix at position %d of %d; kev %t", at0, len(plan.Branches), tc.kev)
			}
		})
	}
}

// A major security fix is never merged on its own, even when
// vulnerabilityAlerts.automerge is true and the CVE is in KEV; a minor one
// keeps the configured automerge. Decided 2026-10-04 (I-055, I-237).
func TestAMajorSecurityFixIsNeverAutomerged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fixed     string
		catalog   kev.Catalog
		automerge bool
	}{
		{"major, in KEV", "3.0.0", kev.Catalog{"CVE-2026-0001": {CVE: "CVE-2026-0001"}}, false},
		{"major, not in KEV", "3.0.0", kev.Catalog{}, false},
		{"minor, in KEV", "2.1.0", kev.Catalog{"CVE-2026-0001": {CVE: "CVE-2026-0001"}}, true},
		{"minor, not in KEV", "2.1.0", kev.Catalog{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/composer.json", []byte(`{"name":"acme/site","require":{"zzz/vuln":"2.0.0"}}`), 0o644)
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true,
				"vulnerabilityAlerts": {"enabled": true, "automerge": true, "schedule": ["at any time"], "minimumReleaseAge": null}}`), 0o644)
			at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			opts := treeOptions(t, root, "development/moselwal/site", at)
			opts.Datasources["packagist"] = cannedDS{name: "packagist", scheme: "composer", releases: map[string][]string{
				"zzz/vuln": {"2.0.0", tc.fixed},
			}}
			opts.Advisories = &askedAdvisories{answers: map[string][]osv.Advisory{
				"zzz/vuln": {{ID: "GHSA-test-kev0-0001", Aliases: []string{"CVE-2026-0001"}, Fixed: tc.fixed}},
			}}
			opts.Exploited = &cannedCatalog{Cat: tc.catalog}
			plan, err := whatif(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			var fix *model.Branch
			for i := range plan.Branches {
				if strings.Contains(plan.Branches[i].Name, "zzz") {
					fix = &plan.Branches[i]
				}
			}
			if fix == nil {
				t.Fatalf("no fix branch: %+v", plan.Branches)
			}
			if fix.SuppressedBy != "" {
				t.Fatalf("fix held by %q", fix.SuppressedBy)
			}
			if fix.Automerge != tc.automerge {
				t.Errorf("automerge %t, want %t", fix.Automerge, tc.automerge)
			}
		})
	}
}
