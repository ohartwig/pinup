// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/cvelist"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/versioning"
)

// The case behind the change, devops/rpm-packages on 2026-11-18/19:
// specs/clamav/clamav.spec follows Cisco-Talos/clamav's tags through
// `extractVersion=^clamav-(?<version>1\.5\..*)$`. ClamAV 1.5.5 (ten CVEs,
// released 16:09 UTC) was seen and held by the release age, as a routine
// update: github-tags has no advisory ecosystem, so nothing said 1.5.4
// was vulnerable.
var (
	clamavRepo        = "https://github.com/Cisco-Talos/clamav"
	clamav154Released = time.Date(2026, 6, 17, 14, 0, 0, 0, time.UTC)
	clamav155Seen     = time.Date(2026, 11, 18, 16, 20, 0, 0, time.UTC)
	upstreamNow       = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
)

func clamavReleases() *model.ReleaseSet {
	return &model.ReleaseSet{PackageName: "Cisco-Talos/clamav", Datasource: "github-tags", SourceURL: clamavRepo, Releases: []model.Release{
		{Version: "clamav-1.4.7", FirstSeen: clamav155Seen},
		{Version: "clamav-1.5.3", FirstSeen: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{Version: "clamav-1.5.4", FirstSeen: clamav154Released},
		{Version: "clamav-1.5.5", FirstSeen: clamav155Seen},
		// Outside the pattern's series: never a fix for 1.5.4.
		{Version: "clamav-1.6.0", FirstSeen: clamav155Seen},
	}}
}

func clamavDep() model.Dependency {
	return model.Dependency{
		DepName: "Cisco-Talos/clamav", Datasource: "github-tags", CurrentValue: "1.5.4", File: "specs/clamav/clamav.spec",
		ExtractVersion:   `^clamav-(?<version>1\.5\..*)$`,
		AdvisoryCoverage: &model.DependencyCoverage{State: model.NotCovered, Reason: model.ReasonNoEcosystem},
	}
}

// gitOSV is OSV's API as far as the GIT lookup uses it: querybatch
// answers per (repository, tag), vulns/{id} the record. status, when set,
// answers every request with it.
type gitOSV struct {
	tags    map[string][]map[string]string // "repo|tag" -> [{id, modified}]
	records map[string]map[string]any
	status  int
	asked   []string
}

func (s *gitOSV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.status != 0 {
		w.WriteHeader(s.status)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
		var req struct {
			Queries []struct {
				Package struct{ Name, Ecosystem string }
				Version string
			}
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		results := make([]map[string]any, len(req.Queries))
		for i, q := range req.Queries {
			s.asked = append(s.asked, q.Package.Ecosystem+"|"+q.Package.Name+"|"+q.Version)
			results[i] = map[string]any{}
			if q.Package.Ecosystem == "GIT" {
				if v := s.tags[q.Package.Name+"|"+q.Version]; v != nil {
					results[i]["vulns"] = v
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
		rec, ok := s.records[strings.TrimPrefix(r.URL.Path, "/v1/vulns/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(rec)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// clamavOSV answers as OSV would once it carries a CVE record for 1.5.4
// with a GIT range: listed under clamav-1.5.4, not under clamav-1.5.5, and
// analysed after 1.5.5 was tagged. OSV-2022-94 lists 1.5.4 too, but OSV
// last analysed it a month before 1.5.5 existed (the real record's
// modification time): 1.5.5's absence from it says nothing.
func clamavOSV() *gitOSV {
	return &gitOSV{
		tags: map[string][]map[string]string{
			clamavRepo + "|clamav-1.5.4": {
				{"id": "CVE-2026-76411", "modified": "2026-11-19T08:00:00Z"},
				{"id": "OSV-2022-94", "modified": "2026-10-19T14:51:34.908103Z"},
			},
			clamavRepo + "|clamav-1.5.3": {{"id": "CVE-2026-76411", "modified": "2026-11-19T08:00:00Z"}},
		},
		records: map[string]map[string]any{
			"CVE-2026-76411": {"id": "CVE-2026-76411", "summary": "ClamAV phishcheck stack buffer overflow", "published": "2026-11-18T16:30:52Z", "modified": "2026-11-19T08:00:00Z",
				"severity": []map[string]string{{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H"}}},
			"OSV-2022-94": {"id": "OSV-2022-94", "summary": "Heap-buffer-overflow in cli_strlcat", "published": "2022-01-20T00:00:52Z", "modified": "2026-10-19T14:51:34.908103Z"},
		},
	}
}

func osvClientFor(t *testing.T, h http.Handler) *osv.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &osv.Client{Base: srv.URL}
}

func semverDefault(string) string { return "semver" }

// OSV's GIT ecosystem knows the CVE under the tag in use and not under the
// next one: 1.5.5 is the fix, the dependency's bound, and the planner's
// fast path takes it. The record OSV has not analysed since 1.5.5 was
// tagged is listed without a fix, and 1.6.0 - outside the pattern - is not
// even asked about.
func TestOSVGitMakesClamAV155ASecurityFix(t *testing.T) {
	srv := clamavOSV()
	src := &upstreamSources{osv: osvClientFor(t, srv)}
	deps := []model.Dependency{clamavDep()}
	coverage := &model.AdvisoryCoverage{NotCovered: map[string]int{"github-tags": 1}, Reasons: map[string]int{model.ReasonNoEcosystem: 1}}
	warns := checkUpstreamAdvisories(t.Context(), src, map[string]any{"osvVulnerabilityAlerts": true}, deps, coverage, semverDefault,
		func(model.Dependency) *model.ReleaseSet { return clamavReleases() })
	if len(warns) != 0 {
		t.Fatalf("warnings %+v", warns)
	}
	d := deps[0]
	if d.VulnerabilityBound != "1.5.5" {
		t.Fatalf("bound %q, want 1.5.5; advisories %+v", d.VulnerabilityBound, d.Advisories)
	}
	fixed := map[string]string{}
	for _, a := range d.Advisories {
		fixed[a.ID] = a.Fixed
	}
	if fixed["CVE-2026-76411"] != "1.5.5" || fixed["OSV-2022-94"] != "" || len(fixed) != 2 {
		t.Errorf("fixes %v, want CVE-2026-76411 fixed in 1.5.5 and OSV-2022-94 without a fix", fixed)
	}
	if d.Advisories[0].Summary == "" || d.Advisories[0].Severity == "" {
		t.Errorf("the record's details are missing: %+v", d.Advisories[0])
	}
	want := []string{"GIT|" + clamavRepo + "|clamav-1.5.4", "GIT|" + clamavRepo + "|clamav-1.5.5"}
	if !slices.Equal(srv.asked, want) {
		t.Errorf("asked %v, want %v", srv.asked, want)
	}
	if d.AdvisoryCoverage.State != model.CoveredByAdvisories || !slices.Equal(d.AdvisoryCoverage.Sources, []string{sourceOSVGit}) {
		t.Errorf("coverage %+v", d.AdvisoryCoverage)
	}
	if len(coverage.NotCovered) != 0 || len(coverage.Reasons) != 0 || coverage.Asked != 1 {
		t.Errorf("run coverage %+v, want the dependency moved to asked", coverage)
	}
}

// upstreamNotes serves release notes by tag.
type upstreamNotes struct {
	notes []model.ReleaseNote
	err   error
	calls int
}

func (f *upstreamNotes) Releases(context.Context, string, versioning.Versioning, func(string) (string, bool), string) ([]model.ReleaseNote, error) {
	f.calls++
	return f.notes, f.err
}

// upstreamCVEs is the CVE List: a record per id, or an error.
type upstreamCVEs map[string]string

func (f upstreamCVEs) Get(_ context.Context, id string) (cvelist.Record, error) {
	raw, ok := f[id]
	if !ok {
		return cvelist.Record{}, errors.New("unexpected status 404")
	}
	return cvelist.Parse([]byte(raw))
}

func cveRecord(id, published, description string) string {
	raw, _ := json.Marshal(map[string]any{
		"cveMetadata": map[string]string{"cveId": id, "state": "PUBLISHED", "datePublished": published},
		"containers":  map[string]any{"cna": map[string]any{"descriptions": []map[string]string{{"lang": "en", "value": description}}}},
	})
	return string(raw)
}

// The day it happened OSV had nothing - no CVE record for 1.5.5's ten
// CVEs a day later - but 1.5.5's notes named them, and the CVE List had
// Cisco's records twenty minutes after the release. A CVE the notes name
// counts when its record is published, names ClamAV, and is younger than
// 1.5.4. An old id, a record about another product, one already named at
// or below 1.5.4, and one the CVE List does not know are not taken - the
// note is the release's own word, and a forged release would carry one.
func TestReleaseNotesCVEsConfirmedByTheCVEListMakeASecurityFix(t *testing.T) {
	notes := &upstreamNotes{notes: []model.ReleaseNote{
		{Version: "clamav-1.5.5", Published: time.Date(2026, 11, 18, 16, 9, 58, 0, time.UTC), Body: "ClamAV 1.5.5 is a patch release with the following fixes:\n\n" +
			"- [CVE-2026-76411](https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2026-76411): URL canonicalization.\n" +
			"- CVE-2026-76422: Rust-to-C logging.\n" +
			"- CVE-2020-3123 (old), CVE-2026-11111 (not ClamAV), CVE-2026-22222 (unknown), CVE-2026-33333 (already in 1.5.4)."},
		{Version: "clamav-1.5.4", Published: clamav154Released, Body: "Fixes CVE-2026-33333."},
	}}
	cves := upstreamCVEs{
		"CVE-2026-76411": cveRecord("CVE-2026-76411", "2026-11-18T16:30:52.793Z", "A vulnerability in the phishcheck functionality of ClamAV could allow a DoS."),
		"CVE-2026-76422": cveRecord("CVE-2026-76422", "2026-11-18T16:31:10.000Z", "A vulnerability in ClamAV logging."),
		"CVE-2020-3123":  cveRecord("CVE-2020-3123", "2020-02-05T18:15:00.000Z", "A vulnerability in the Data-Loss-Prevention module of ClamAV."),
		"CVE-2026-11111": cveRecord("CVE-2026-11111", "2026-11-18T10:00:00.000Z", "A vulnerability in Caddy."),
		"CVE-2026-33333": cveRecord("CVE-2026-33333", "2026-07-01T00:00:00.000Z", "A vulnerability in ClamAV."),
	}
	src := &upstreamSources{notes: notes, cves: cves}
	deps := []model.Dependency{clamavDep()}
	releases := func(model.Dependency) *model.ReleaseSet { return clamavReleases() }

	// Off unless asked for: the notes are not read.
	checkUpstreamAdvisories(t.Context(), src, map[string]any{"osvVulnerabilityAlerts": true}, deps, nil, semverDefault, releases)
	if notes.calls != 0 || len(deps[0].Advisories) != 0 {
		t.Fatalf("read %d times without releaseNotesAdvisories: %+v", notes.calls, deps[0].Advisories)
	}

	warns := checkUpstreamAdvisories(t.Context(), src, map[string]any{"releaseNotesAdvisories": true}, deps, nil, semverDefault, releases)
	d := deps[0]
	var ids []string
	for _, a := range d.Advisories {
		ids = append(ids, a.ID)
		if a.Fixed != "1.5.5" {
			t.Errorf("%s fixed %q, want 1.5.5", a.ID, a.Fixed)
		}
	}
	if !slices.Equal(ids, []string{"CVE-2026-76411", "CVE-2026-76422"}) {
		t.Errorf("advisories %v, want the two confirmed CVEs", ids)
	}
	if d.VulnerabilityBound != "1.5.5" {
		t.Errorf("bound %q, want 1.5.5", d.VulnerabilityBound)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Msg, "CVE-2026-22222") {
		t.Errorf("warnings %+v, want the one unconfirmed id", warns)
	}
	if !slices.Equal(d.AdvisoryCoverage.Sources, []string{sourceReleaseNotes}) {
		t.Errorf("coverage %+v", d.AdvisoryCoverage)
	}
}

// Fail safe: OSV that does not answer, release notes that cannot be read
// and a CVE List that is down plan nothing extra and hold nothing - a
// warning each, no advisory, no bound, so the update stays the ordinary
// one under its release age.
func TestUpstreamLookupFailuresAreWarningsNotFastPaths(t *testing.T) {
	src := &upstreamSources{
		osv:   osvClientFor(t, &gitOSV{status: http.StatusServiceUnavailable}),
		notes: &upstreamNotes{err: errors.New("changelog: github Cisco-Talos/clamav: unexpected status 403")},
		cves:  upstreamCVEs{},
	}
	deps := []model.Dependency{clamavDep()}
	warns := checkUpstreamAdvisories(t.Context(), src, map[string]any{"osvVulnerabilityAlerts": true, "releaseNotesAdvisories": true}, deps, nil, semverDefault,
		func(model.Dependency) *model.ReleaseSet { return clamavReleases() })
	if len(deps[0].Advisories) != 0 || deps[0].VulnerabilityBound != "" {
		t.Fatalf("advisories %+v bound %q, want none", deps[0].Advisories, deps[0].VulnerabilityBound)
	}
	if len(warns) != 2 || !strings.Contains(warns[0].Msg, "advisory database (GIT") || !strings.Contains(warns[1].Msg, "release notes") {
		t.Errorf("warnings %+v, want one for OSV and one for the notes", warns)
	}
	if deps[0].AdvisoryCoverage.State != model.NotCovered {
		t.Errorf("coverage %+v, want still not covered: nothing answered", deps[0].AdvisoryCoverage)
	}

	// The CVE List down: the note's ids stay unconfirmed.
	src = &upstreamSources{notes: &upstreamNotes{notes: []model.ReleaseNote{{Version: "clamav-1.5.5", Body: "CVE-2026-76411", Published: clamav155Seen}, {Version: "clamav-1.5.4", Published: clamav154Released}}}, cves: upstreamCVEs{}}
	deps = []model.Dependency{clamavDep()}
	warns = checkUpstreamAdvisories(t.Context(), src, map[string]any{"releaseNotesAdvisories": true}, deps, nil, semverDefault,
		func(model.Dependency) *model.ReleaseSet { return clamavReleases() })
	if len(deps[0].Advisories) != 0 || deps[0].VulnerabilityBound != "" || len(warns) != 1 {
		t.Errorf("advisories %+v bound %q warnings %+v", deps[0].Advisories, deps[0].VulnerabilityBound, warns)
	}

	// vulnerabilityAlerts off turns the lookup off like the database's.
	deps = []model.Dependency{clamavDep()}
	srv := clamavOSV()
	checkUpstreamAdvisories(t.Context(), &upstreamSources{osv: osvClientFor(t, srv)}, map[string]any{"osvVulnerabilityAlerts": true, "vulnerabilityAlerts": map[string]any{"enabled": false}}, deps, nil, semverDefault,
		func(model.Dependency) *model.ReleaseSet { return clamavReleases() })
	if len(srv.asked) != 0 || deps[0].VulnerabilityBound != "" {
		t.Errorf("asked %v with vulnerabilityAlerts off", srv.asked)
	}
}

// tagsDS is github-tags with the tags the clamav case carries, and their
// first-seen times: the tags endpoint publishes no dates.
type tagsDS struct{}

func (tagsDS) Name() string              { return "github-tags" }
func (tagsDS) DefaultVersioning() string { return "semver" }
func (tagsDS) Releases(context.Context, lookup.Ref) (*model.ReleaseSet, error) {
	return clamavReleases(), nil
}

// End to end, as devops/rpm-packages plans it: the spec's annotation,
// a 24-hour release age, the estate's vulnerabilityAlerts. With OSV's GIT
// answer, 1.5.5 - twenty hours old - is a security fix, not held, labelled
// security. With OSV down it is the routine update it was on 2026-11-19,
// held by the release age: the failure costs the fast path and nothing
// else.
func TestClamAVSpecGetsItsSecurityFixPastTheReleaseAge(t *testing.T) {
	for _, tc := range []struct {
		name     string
		srv      *gitOSV
		security bool
	}{
		{"osv knows", clamavOSV(), true},
		{"osv down", &gitOSV{status: http.StatusBadGateway}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.MkdirAll(root+"/specs/clamav", 0o755)
			os.WriteFile(root+"/specs/clamav/clamav.spec", []byte("Name:           clamav\n"+
				"# renovate: datasource=github-tags depName=Cisco-Talos/clamav extractVersion=^clamav-(?<version>1\\.5\\..*)$\n"+
				"Version:        1.5.4\nRelease:        2\n"), 0o644)
			os.WriteFile(root+"/renovate.json", []byte(`{
				"minimumReleaseAge": "24 hours",
				"osvVulnerabilityAlerts": true,
				"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null, "automerge": true},
				"customManagers": [{"customType": "regex", "managerFilePatterns": ["/(^|/)specs/[^/]+/[^/]+\\.spec$/"],
					"matchStrings": ["# renovate: datasource=(?<datasource>[a-z-]+) depName=(?<depName>\\S+)(?: versioning=(?<versioning>\\S+))?(?: extractVersion=(?<extractVersion>\\S+))?\\nVersion:\\s+(?<currentValue>\\S+)"]}]
			}`), 0o644)
			opts := treeOptions(t, root, "devops/rpm-packages", upstreamNow)
			opts.Datasources["github-tags"] = tagsDS{}
			opts.Advisories = &askedAdvisories{}
			opts.Upstream = &upstreamSources{osv: osvClientFor(t, tc.srv)}
			plan, err := whatif(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			var u *model.Update
			for i := range plan.Updates {
				if plan.Updates[i].Dep.DepName == "Cisco-Talos/clamav" {
					u = &plan.Updates[i]
				}
			}
			if u == nil {
				t.Fatalf("no update for clamav: %+v", plan.Updates)
			}
			if u.NewValue != "1.5.5" || u.SecurityFix != tc.security {
				t.Fatalf("update to %s, security %t; want 1.5.5, security %t", u.NewValue, u.SecurityFix, tc.security)
			}
			if tc.security {
				if u.Blocked() {
					t.Errorf("the security fix is held: %+v", u.Blocks)
				}
				labelled := false
				for _, b := range plan.Branches {
					for _, e := range b.Edits {
						if e.File == "specs/clamav/clamav.spec" && slices.Contains(b.Labels, "security") {
							labelled = true
						}
					}
				}
				if !labelled {
					t.Errorf("the branch is not labelled security: %+v", plan.Branches)
				}
				return
			}
			if !u.Blocked() || u.Blocks[0].Reason != model.BlockMinimumReleaseAge {
				t.Errorf("blocks %+v, want the release age", u.Blocks)
			}
			warned := false
			for _, w := range plan.Warnings {
				warned = warned || strings.Contains(w.Msg, "advisory database (GIT")
			}
			if !warned {
				t.Errorf("no warning for the unreachable database: %+v", plan.Warnings)
			}
		})
	}
}
