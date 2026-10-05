// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/fake/packagistfake"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/packagistadv"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/wire"
)

// sa022 is TYPO3-CORE-SA-2026-022 (CVE-2026-77132) as packagist.org served
// it on 2026-10-05, four weeks after it was published and still absent from
// OSV.
var sa022 = packagistfake.Advisory{
	AdvisoryID: "PKSA-745m-816f-bzfy", PackageName: "typo3/cms-backend", Title: "TYPO3-CORE-SA-2026-022",
	CVE: "CVE-2026-77132", AffectedVersions: ">=10.0.0,<10.4.60|>=11.0.0,<11.5.54|>=12.0.0,<12.4.49|>=13.0.0,<13.4.35|>=14.0.0,<14.3.7",
	ReportedAt: "2026-09-08 09:00:00",
	Sources:    []packagistfake.Source{{Name: "FriendsOfPHP/security-advisories", RemoteID: "typo3/cms-backend/CVE-2026-77132.yaml"}},
}

func packagistClient(t *testing.T, srv *packagistfake.Server) *packagistadv.Client {
	t.Helper()
	rt := harness.NewRefusingTransport(t)
	rt.Handle("packagist.org", srv)
	return &packagistadv.Client{Transport: rt}
}

func TestPackagistAdvisoriesJoinOSVFindings(t *testing.T) {
	backend := osv.Query{Datasource: "packagist", PackageName: "typo3/cms-backend", Version: "14.3.6", Versioning: "composer"}
	for _, tc := range []struct {
		name     string
		query    osv.Query
		fromOSV  []osv.Advisory
		ids      []string
		bound    string
		asked    bool
		releases []string
	}{
		{"OSV knows nothing", backend, nil, []string{"PKSA-745m-816f-bzfy"}, "14.3.7", true, nil},
		{"OSV has it under the CVE", backend, []osv.Advisory{{ID: "GHSA-xxxx", Aliases: []string{"CVE-2026-77132"}, Fixed: "14.3.7"}}, []string{"GHSA-xxxx"}, "14.3.7", true, nil},
		{"fixed version", osv.Query{Datasource: "packagist", PackageName: "typo3/cms-backend", Version: "14.3.7", Versioning: "composer"}, nil, nil, "", true, nil},
		{"the fix from the release set", backend, nil, []string{"PKSA-745m-816f-bzfy"}, "14.3.8", true, []string{"14.3.6", "14.3.8"}},
		{"a range is not asked", osv.Query{Datasource: "packagist", PackageName: "typo3/cms-backend", Version: "^14.3", Versioning: "composer"}, nil, nil, "", false, nil},
		{"npm is not asked", osv.Query{Datasource: "npm", PackageName: "typo3/cms-backend", Version: "14.3.6", Versioning: "npm"}, nil, nil, "", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &packagistfake.Server{ByPackage: map[string][]packagistfake.Advisory{"typo3/cms-backend": {sa022}}}
			var src advisoryChecker = advisorySources{
				osv:       &askedAdvisories{answers: map[string][]osv.Advisory{"typo3/cms-backend": tc.fromOSV}},
				packagist: packagistClient(t, srv),
			}
			if tc.releases != nil {
				src = src.(releaseAware).withReleases(func(string) []string { return tc.releases })
			}
			fs, err := src.Check(t.Context(), wire.Versionings(), []osv.Query{tc.query})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, a := range fs[0].Advisories {
				ids = append(ids, a.ID)
			}
			if !slices.Equal(ids, tc.ids) || fs[0].Bound != tc.bound {
				t.Errorf("advisories %v bound %q, want %v %q", ids, fs[0].Bound, tc.ids, tc.bound)
			}
			if asked := len(srv.Asked) > 0; asked != tc.asked {
				t.Errorf("packagist asked %t", asked)
			}
			for _, a := range fs[0].Advisories {
				if a.ID == "PKSA-745m-816f-bzfy" && !slices.Contains(a.Aliases, "CVE-2026-77132") {
					t.Errorf("the CVE is no alias - KEV could not match it: %v", a.Aliases)
				}
			}
		})
	}
}

// Packagist that cannot be reached is a warning; OSV's answer stands and
// the run goes on.
func TestPackagistUnreachableIsAWarning(t *testing.T) {
	src := advisorySources{
		osv:       &askedAdvisories{answers: map[string][]osv.Advisory{"typo3/cms-backend": {{ID: "GHSA-old", Fixed: "14.3.6"}}}},
		packagist: packagistClient(t, &packagistfake.Server{Status: http.StatusServiceUnavailable}),
	}
	fs, err := src.Check(t.Context(), wire.Versionings(), []osv.Query{{Datasource: "packagist", PackageName: "typo3/cms-backend", Version: "14.3.5", Versioning: "composer"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs[0].Advisories) != 1 || fs[0].Bound != "14.3.6" {
		t.Errorf("OSV's answer changed: %+v", fs[0])
	}
	if len(fs[0].Warnings) != 1 || !strings.Contains(fs[0].Warnings[0], "OSV only") {
		t.Errorf("warnings %v", fs[0].Warnings)
	}
	// OSV that fails is still the run's error to report, as before.
	broken := advisorySources{osv: &askedAdvisories{err: errors.New("osv down")}, packagist: packagistClient(t, &packagistfake.Server{})}
	if _, err := broken.Check(t.Context(), wire.Versionings(), []osv.Query{{Datasource: "packagist", PackageName: "a/b", Version: "1.0.0", Versioning: "composer"}}); err == nil {
		t.Error("an OSV failure was swallowed")
	}
}

// SA-2026-022 end to end: typo3/cms-backend 14.3.6, OSV silent, Packagist
// listing the advisory. With Packagist the update is a security fix: its own
// branch, the vulnerabilityAlerts label, no soak. With OSV alone it is the
// routine update of the typo3 group, and nothing says it closes a CVE -
// the 2026-08-11 stall had exactly that, for 21 days.
func TestPackagistAdvisoryOpensASecurityFixOSVDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		packagist bool
		security  bool
	}{
		{"with packagist", true, true},
		{"osv alone", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(root+"/composer.json", []byte(`{"name":"acme/site","require":{"typo3/cms-backend":"14.3.6"}}`), 0o644)
			os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true,
				"minimumReleaseAge": "3 days", "vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null}}`), 0o644)
			at := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
			opts := treeOptions(t, root, "development/moselwal/site", at)
			opts.Datasources["packagist"] = cannedDS{name: "packagist", scheme: "composer", releases: map[string][]string{
				"typo3/cms-backend": {"14.3.6", "14.3.7"},
			}}
			src := advisorySources{osv: &askedAdvisories{}}
			if tc.packagist {
				src.packagist = packagistClient(t, &packagistfake.Server{ByPackage: map[string][]packagistfake.Advisory{"typo3/cms-backend": {sa022}}})
			}
			opts.Advisories = src
			plan, err := whatif(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			var fix *model.Branch
			for i := range plan.Branches {
				if strings.Contains(plan.Branches[i].Name, "cms-backend") || strings.Contains(plan.Branches[i].Title, "cms-backend") {
					fix = &plan.Branches[i]
				}
			}
			if fix == nil {
				t.Fatalf("no branch for cms-backend: %+v", plan.Branches)
			}
			if fix.SuppressedBy != "" {
				t.Errorf("branch %s held by %q", fix.Name, fix.SuppressedBy)
			}
			if security := slices.Contains(fix.Labels, "security"); security != tc.security {
				t.Errorf("branch %s labels %v; security fix %t, want %t", fix.Name, fix.Labels, security, tc.security)
			}
		})
	}
}

// The watch reads Packagist per package once, then only what changed since,
// and reports a new advisory once.
func TestAdvisoriesWatchReadsPackagist(t *testing.T) {
	calls := 0
	rt := harness.NewRefusingTransport(t)
	rt.Handle("api.osv.dev", osvHandler(t, &calls))
	srv := &packagistfake.Server{ByPackage: map[string][]packagistfake.Advisory{"typo3/cms-backend": {sa022}}}
	rt.Handle("packagist.org", srv)
	idx := report.NewIndex()
	idx.Dependencies["development/site"] = []report.Dependency{
		{Datasource: "packagist", PackageName: "typo3/cms-backend", Version: "14.3.6", Versioning: "composer", File: "composer.lock"},
	}
	state := &advisoriesState{Seen: map[string]time.Time{}}
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	rep, err := watchAdvisories(t.Context(), &osv.Client{Transport: rt}, &packagistadv.Client{Transport: rt}, idx, nil, "", state, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.New) != 1 || rep.New[0].Advisory != "PKSA-745m-816f-bzfy" || rep.New[0].Fixed != "14.3.7" || !slices.Equal(rep.New[0].Repositories, []string{"development/site"}) {
		t.Fatalf("new %+v", rep.New)
	}
	if !state.PackagistSince.Equal(now) || len(srv.Asked) != 1 {
		t.Errorf("first read: since %v, asked %v", state.PackagistSince, srv.Asked)
	}
	// Fifteen minutes later: only the changes since the last read, with an
	// hour's overlap, and nothing reported twice.
	srv.Updated = map[string][]packagistfake.Advisory{"typo3/cms-backend": {sa022}}
	again, err := watchAdvisories(t.Context(), &osv.Client{Transport: rt}, &packagistadv.Client{Transport: rt}, idx, nil, "", state, now.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.New) != 0 || len(srv.Asked) != 1 || len(srv.Since) != 1 || srv.Since[0] != now.Add(-time.Hour).Unix() {
		t.Errorf("second read: new %v, asked %v, since %v", again.New, srv.Asked, srv.Since)
	}
	// Unreachable: a warning, OSV's watch goes on, and the next run asks
	// the same window again.
	srv.Status = http.StatusBadGateway
	before := state.PackagistSince
	down, err := watchAdvisories(t.Context(), &osv.Client{Transport: rt}, &packagistadv.Client{Transport: rt}, idx, nil, "", state, now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !state.PackagistSince.Equal(before) || !slices.ContainsFunc(down.Warnings, func(w string) bool { return strings.Contains(w, "OSV only") }) {
		t.Errorf("unreachable: since %v, warnings %v", state.PackagistSince, down.Warnings)
	}
}
