// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package packagistadv

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/fake/packagistfake"
	"github.com/ohartwig/pinup/semverx"
	"github.com/ohartwig/pinup/versioning"
)

// testScheme is strict semver with composer's AND-list ranges
// (">=1.0.0,<1.2.0"). packagistadv is L2 and may not import
// versioning/composer; what is under test is this package's splitting of
// the "|" alternatives and its fix derivation, not composer's parser.
type testScheme struct{}

func (testScheme) Name() string               { return "test" }
func (testScheme) IsValid(v string) bool      { _, ok := semverx.Parse(v); return ok }
func (testScheme) IsVersion(v string) bool    { _, ok := semverx.Parse(v); return ok }
func (testScheme) IsStable(v string) bool     { p, ok := semverx.Parse(v); return ok && len(p.Pre) == 0 }
func (testScheme) Major(v string) (int, bool) { p, ok := semverx.Parse(v); return p.Major, ok }
func (testScheme) Minor(v string) (int, bool) { p, ok := semverx.Parse(v); return p.Minor, ok }
func (testScheme) Patch(v string) (int, bool) { p, ok := semverx.Parse(v); return p.Patch, ok }
func (testScheme) Compare(a, b string) int {
	pa, oka := semverx.Parse(a)
	pb, okb := semverx.Parse(b)
	if !oka || !okb {
		return 0
	}
	return semverx.CompareVersions(pa, pb)
}
func (t testScheme) Equal(a, b string) bool { return t.Compare(a, b) == 0 }
func (t testScheme) Satisfies(v, rng string) bool {
	if strings.Contains(rng, "|") {
		return false // a single "|" is this package's job to split
	}
	for _, c := range strings.Split(rng, ",") {
		c = strings.TrimSpace(c)
		var op string
		for _, o := range []string{">=", "<=", ">", "<"} {
			if strings.HasPrefix(c, o) {
				op = o
				break
			}
		}
		cmp := t.Compare(v, strings.TrimPrefix(c, op))
		ok := map[string]bool{">=": cmp >= 0, "<=": cmp <= 0, ">": cmp > 0, "<": cmp < 0, "": cmp == 0}[op]
		if !ok {
			return false
		}
	}
	return true
}
func (testScheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	return target, nil
}

var scheme versioning.Versioning = testScheme{}

// sa022 is TYPO3-CORE-SA-2026-022 as Packagist served it on 2026-10-05.
var sa022 = packagistfake.Advisory{
	AdvisoryID: "PKSA-745m-816f-bzfy", PackageName: "typo3/cms-backend", Title: "TYPO3-CORE-SA-2026-022",
	CVE: "CVE-2026-77132", AffectedVersions: ">=10.0.0,<10.4.60|>=11.0.0,<11.5.54|>=12.0.0,<12.4.49|>=13.0.0,<13.4.35|>=14.0.0,<14.3.7",
	ReportedAt: "2026-09-08 09:00:00",
	Sources:    []packagistfake.Source{{Name: "FriendsOfPHP/security-advisories", RemoteID: "typo3/cms-backend/CVE-2026-77132.yaml"}},
}

func newFixture(t *testing.T, srv *packagistfake.Server) (*Client, *harness.Recorder) {
	t.Helper()
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("packagist.example", srv)
	rt.Forbid("evil.example")
	return &Client{Transport: rt, Base: "https://packagist.example"}, rec
}

func TestForPackagesDecodesAndBatches(t *testing.T) {
	srv := &packagistfake.Server{ByPackage: map[string][]packagistfake.Advisory{"typo3/cms-backend": {sa022}}}
	c, rec := newFixture(t, srv)
	names := []string{"typo3/cms-backend", "typo3/cms-backend"}
	for i := range 45 {
		names = append(names, "vendor/p"+strconv.Itoa(i))
	}
	got, err := c.ForPackages(t.Context(), names)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatal(rec)
	}
	if len(srv.Asked) != 2 || len(srv.Asked[0]) != 40 || len(srv.Asked[1]) != 6 {
		t.Errorf("batches %v, want 40 + 6 distinct names", srv.Asked)
	}
	adv := got["typo3/cms-backend"]
	if len(adv) != 1 {
		t.Fatalf("advisories %+v", got)
	}
	a := adv[0]
	if a.ID != "PKSA-745m-816f-bzfy" || a.CVE != "CVE-2026-77132" || len(a.GHSA) != 0 || !a.Reported.Equal(time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("decoded %+v", a)
	}
	if !slices.Equal(a.Aliases(), []string{"CVE-2026-77132"}) {
		t.Errorf("aliases %v", a.Aliases())
	}
}

func TestEmptyAnswerIsAnArray(t *testing.T) {
	srv := &packagistfake.Server{}
	c, _ := newFixture(t, srv)
	got, err := c.ForPackages(t.Context(), []string{"vendor/clean"})
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
	got, err = c.UpdatedSince(t.Context(), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(got) != 0 {
		t.Errorf("since: got %v, %v", got, err)
	}
	if len(srv.Since) != 1 || srv.Since[0] != time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("since asked %v", srv.Since)
	}
}

func TestGHSAFromSources(t *testing.T) {
	adv := packagistfake.Advisory{AdvisoryID: "PKSA-x", PackageName: "a/b", CVE: "CVE-1", AffectedVersions: "<1.0.0", ReportedAt: "2026-01-01 00:00:00",
		Sources: []packagistfake.Source{{Name: "GitHub", RemoteID: "GHSA-aaaa-bbbb-cccc"}, {Name: "FriendsOfPHP/security-advisories", RemoteID: "a/b/CVE-1.yaml"}}}
	c, _ := newFixture(t, &packagistfake.Server{Updated: map[string][]packagistfake.Advisory{"a/b": {adv}}})
	got, err := c.UpdatedSince(t.Context(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if a := got["a/b"][0]; !slices.Equal(a.Aliases(), []string{"CVE-1", "GHSA-aaaa-bbbb-cccc"}) {
		t.Errorf("aliases %v", a.Aliases())
	}
}

func TestUnreachableIsAnError(t *testing.T) {
	c, _ := newFixture(t, &packagistfake.Server{Status: http.StatusBadGateway})
	if _, err := c.ForPackages(t.Context(), []string{"a/b"}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err %v", err)
	}
}

func TestAffectsAndFixed(t *testing.T) {
	a := Advisory{Affected: sa022.AffectedVersions}
	for _, tc := range []struct {
		version  string
		releases []string
		affected bool
		fixed    string
	}{
		{"14.3.6", nil, true, "14.3.7"},
		{"14.3.6", []string{"14.3.5", "14.3.6", "14.3.8", "14.3.7", "15.0.0-beta1"}, true, "14.3.7"},
		// The bound itself is not released: the next release that is not
		// affected is the fix.
		{"14.3.6", []string{"14.3.6", "14.3.9"}, true, "14.3.9"},
		{"13.4.34", nil, true, "13.4.35"},
		{"14.3.7", nil, false, ""},
		{"12.4.49", nil, false, ""},
		{"9.5.0", nil, false, ""},
	} {
		if got := Affects(a, tc.version, scheme); got != tc.affected {
			t.Errorf("Affects(%s) = %t", tc.version, got)
		}
		if got := Fixed(a, tc.version, scheme, tc.releases); got != tc.fixed {
			t.Errorf("Fixed(%s, %v) = %q, want %q", tc.version, tc.releases, got, tc.fixed)
		}
	}
	// An upper bound the advisory also covers names no fix.
	b := Advisory{Affected: "<1.2.0|>=1.2.0,<1.3.0"}
	if got := Fixed(b, "1.1.0", scheme, nil); got != "" {
		t.Errorf("covered bound named as fix: %q", got)
	}
	if got := Fixed(b, "1.1.0", scheme, []string{"1.2.0", "1.3.0"}); got != "1.3.0" {
		t.Errorf("with releases: %q", got)
	}
}
