// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package kev

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
)

// feedDoc is a catalog in CISA's shape, trimmed to the fields that matter
// plus one the package ignores.
const feedDoc = `{"title":"CISA Catalog of Known Exploited Vulnerabilities","catalogVersion":"2026.10.02","count":2,
"vulnerabilities":[
 {"cveID":"CVE-2026-102490","vendorProject":"Zammad GmbH","vulnerabilityName":"Zammad Improper Privilege Management","dateAdded":"2026-10-02","dueDate":"2026-10-05","knownRansomwareCampaignUse":"Unknown","cwes":["CWE-269"]},
 {"cveID":"cve-2021-44228","vulnerabilityName":"Apache Log4j2 Remote Code Execution","dateAdded":"2021-12-10","dueDate":"2021-12-24","knownRansomwareCampaignUse":"Known"}
]}`

// feedServer speaks the feed: a GET on the path answers the document, any
// other method or path is refused. Fail switches it to a 503.
type feedServer struct {
	body  string
	Fail  bool
	gets  int
	paths []string
}

func (s *feedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.paths = append(s.paths, r.URL.Path)
	if r.Method != http.MethodGet || r.URL.Path != "/feeds/kev.json" {
		http.Error(w, "not here", http.StatusNotFound)
		return
	}
	s.gets++
	if s.Fail {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(s.body))
}

// memStore keeps payloads with the time they were put.
type memStore struct {
	payload []byte
	at      time.Time
}

func (m *memStore) Get(_ string, ttl time.Duration, now time.Time) ([]byte, bool, bool) {
	if m.payload == nil {
		return nil, false, false
	}
	return m.payload, now.Sub(m.at) < ttl, true
}

func (m *memStore) Put(_ string, payload []byte, now time.Time) { m.payload, m.at = payload, now }

func newClient(t *testing.T, srv *feedServer, store Store) (*Client, *harness.RefusingTransport) {
	t.Helper()
	rt := harness.NewRefusingTransport(t)
	rt.Handle("cisa.example", srv)
	rt.Forbid("www.cisa.gov")
	http := httpx.New(httpx.Options{Transport: rt, Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}})
	return &Client{HTTP: http, URL: "https://cisa.example/feeds/kev.json", Store: store}, rt
}

func TestMatchReadsCVEAliasesOnly(t *testing.T) {
	cat, err := Parse([]byte(feedDoc))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ids  []string
		want string
	}{
		{[]string{"GHSA-jfh8-c2jp-5v3q", "CVE-2021-44228"}, "CVE-2021-44228"},
		{[]string{"cve-2026-102490"}, "CVE-2026-102490"},
		{[]string{"GO-2026-4442", "GHSA-x9p2-77v6-6vhf"}, ""},
		{[]string{"CVE-2026-93748"}, ""},
		{nil, ""},
	} {
		e, ok := cat.Match(tc.ids...)
		if ok != (tc.want != "") || e.CVE != tc.want {
			t.Errorf("Match(%v) = %q, %v; want %q", tc.ids, e.CVE, ok, tc.want)
		}
	}
	if e, _ := cat.Match("CVE-2021-44228"); e.Ransomware != "Known" || e.DateAdded != "2021-12-10" || e.DueDate != "2021-12-24" {
		t.Errorf("entry fields: %+v", e)
	}
}

func TestParseRefusesACatalogWithoutCVEs(t *testing.T) {
	for _, doc := range []string{`{"catalogVersion":"x","vulnerabilities":[]}`, `{"vulnerabilities":[{"cveID":"GHSA-1"}]}`, `not json`} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("Parse(%q) accepted", doc)
		}
	}
}

// One fetch a day: a fresh cache answers without the network, an old one
// is refreshed, and a failed refresh falls back to the old copy with a
// warning rather than dropping every exploited mark.
func TestLoadCachesForADayAndFallsBackToStale(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)
	srv := &feedServer{body: feedDoc}
	store := &memStore{}
	c, rt := newClient(t, srv, store)

	if cat, warns, err := c.Load(ctx, t0); err != nil || len(warns) != 0 || len(cat) != 2 {
		t.Fatalf("first load: %d entries, %v, %v", len(cat), warns, err)
	}
	if _, _, err := c.Load(ctx, t0.Add(23*time.Hour)); err != nil || srv.gets != 1 {
		t.Fatalf("fresh cache: gets=%d err=%v", srv.gets, err)
	}
	if _, _, err := c.Load(ctx, t0.Add(25*time.Hour)); err != nil || srv.gets != 2 {
		t.Fatalf("stale cache refreshed: gets=%d err=%v", srv.gets, err)
	}

	srv.Fail = true
	cat, warns, err := c.Load(ctx, t0.Add(50*time.Hour))
	if err != nil || len(cat) != 2 || len(warns) != 1 || !strings.Contains(warns[0], "cached catalog") {
		t.Fatalf("stale fallback: %d entries, %v, %v", len(cat), warns, err)
	}

	empty, _ := newClient(t, srv, &memStore{})
	if _, _, err := empty.Load(ctx, t0); err == nil {
		t.Error("a failed fetch with nothing cached must be an error")
	}
	rt.MustNotHaveBeenCalled("www.cisa.gov")
}
