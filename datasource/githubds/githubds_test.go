// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package githubds

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// ghRelease is one fixture item. published is the zero time for the (never
// returned) draft in the fixture, matching the real API's null published_at.
type ghRelease struct {
	tag        string
	published  time.Time
	draft      bool
	prerelease bool
}

// github is a server that speaks the releases and tags endpoints: RFC 5988
// Link-header pagination at a forced page size of two, and the same 404 for
// "no such repository" and "no permission" the real API gives. The bodies
// are data; the protocol is code, so a client that stopped paginating or
// read the wrong field would fail here rather than in production.
type github struct {
	mu       sync.Mutex
	releases map[string][]ghRelease // "owner/repo" -> releases, in API order
	tags     map[string][]string    // "owner/repo" -> tag names, in API order
	status   map[string]int         // "owner/repo" -> status code to force
	headers  map[string]http.Header // "owner/repo" -> headers to send with a forced status
	requests []string
}

func (g *github) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests = append(g.requests, r.URL.RequestURI())
	g.mu.Unlock()

	const prefix = "/repos/"
	path := r.URL.Path
	if !strings.HasPrefix(path, prefix) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(path, prefix)

	var kind, repoPath string
	switch {
	case strings.HasSuffix(rest, "/releases"):
		kind, repoPath = "releases", strings.TrimSuffix(rest, "/releases")
	case strings.HasSuffix(rest, "/tags"):
		kind, repoPath = "tags", strings.TrimSuffix(rest, "/tags")
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if code, forced := g.status[repoPath]; forced {
		for k, vs := range g.headers[repoPath] {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(code)
		return
	}

	const pageSize = 2
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize

	switch kind {
	case "releases":
		items, ok := g.releases[repoPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if start > len(items) {
			start = len(items)
		}
		end := min(start+pageSize, len(items))
		if end < len(items) {
			g.setNextLink(w, r, path, page+1)
		}
		var body []map[string]any
		for _, it := range items[start:end] {
			m := map[string]any{"tag_name": it.tag, "draft": it.draft, "prerelease": it.prerelease}
			if !it.published.IsZero() {
				m["published_at"] = it.published.Format(time.RFC3339)
			}
			body = append(body, m)
		}
		writeJSON(w, body)

	case "tags":
		names, ok := g.tags[repoPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if start > len(names) {
			start = len(names)
		}
		end := min(start+pageSize, len(names))
		if end < len(names) {
			g.setNextLink(w, r, path, page+1)
		}
		var body []map[string]any
		for _, n := range names[start:end] {
			body = append(body, map[string]any{"name": n})
		}
		writeJSON(w, body)
	}
}

// setNextLink writes a Link header with an absolute rel="next" URL, the way
// the real API does. A client that parsed only the query string, or missed
// that the URL is already absolute, would fail the pagination tests.
func (g *github) setNextLink(w http.ResponseWriter, r *http.Request, path string, nextPage int) {
	w.Header().Set("Link", fmt.Sprintf(`<%s://%s%s?per_page=100&page=%d>; rel="next", <%s://%s%s?per_page=100&page=99>; rel="last"`,
		r.URL.Scheme, r.URL.Host, path, nextPage, r.URL.Scheme, r.URL.Host, path))
}

func writeJSON(w http.ResponseWriter, body []map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func newFixture(t *testing.T) (*httpx.Client, *github, *harness.Recorder, *harness.RefusingTransport) {
	t.Helper()
	srv := &github{
		releases: map[string][]ghRelease{},
		tags:     map[string][]string{},
		status:   map[string]int{},
		headers:  map[string]http.Header{},
	}
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("api.github.com", srv)
	rt.Forbid("developer.mend.io")

	client := httpx.New(httpx.Options{
		Transport: rt,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	return client, srv, rec, rt
}

func TestReleasesPaginateDropDraftsKeepPrereleases(t *testing.T) {
	client, srv, rec, rt := newFixture(t)
	published := func(daysAfterEpoch int) time.Time {
		return time.Date(2024, 1, 1+daysAfterEpoch, 0, 0, 0, 0, time.UTC)
	}
	srv.releases["hadolint/hadolint"] = []ghRelease{
		{tag: "v1.0.0", published: published(0)},
		{tag: "v1.1.0", published: published(10)},
		{tag: "v1.2.0-draft", draft: true}, // no published_at, like the real API
		{tag: "v2.0.0-rc1", published: published(20), prerelease: true},
		{tag: "v2.0.0", published: published(30)},
	}

	ds := New(Releases, client, "")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "github-releases", PackageName: "hadolint/hadolint",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	want := []string{"v1.0.0", "v1.1.0", "v2.0.0-rc1", "v2.0.0"}
	if len(rs.Releases) != len(want) {
		t.Fatalf("got %d releases, want %d (draft must be dropped, prerelease kept): %v",
			len(rs.Releases), len(want), rs.Releases)
	}
	for i, w := range want {
		if rs.Releases[i].Version != w {
			t.Errorf("release %d = %q, want %q - order must match the API's", i, rs.Releases[i].Version, w)
		}
		if rs.Releases[i].Timestamp.IsZero() {
			t.Errorf("%s carries no timestamp", w)
		}
	}

	// Five items at two per page is three requests. A client that ignored
	// the Link header would make one; one that never stopped would loop.
	if n := len(srv.requests); n != 3 {
		t.Errorf("made %d requests, want 3: %v", n, srv.requests)
	}
	if rs.SourceURL != "https://github.com/hadolint/hadolint" {
		t.Errorf("SourceURL = %q, want https://github.com/hadolint/hadolint", rs.SourceURL)
	}
}

func TestTagsHaveNoTimestamps(t *testing.T) {
	client, srv, rec, rt := newFixture(t)
	srv.tags["openvex/vexctl"] = []string{"v0.1.0", "v0.2.0", "v0.3.0"}

	ds := New(Tags, client, "")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "github-tags", PackageName: "openvex/vexctl",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	want := []string{"v0.1.0", "v0.2.0", "v0.3.0"}
	if len(rs.Releases) != len(want) {
		t.Fatalf("got %d releases, want %d: %v", len(rs.Releases), len(want), rs.Releases)
	}
	for i, w := range want {
		if rs.Releases[i].Version != w {
			t.Errorf("release %d = %q, want %q", i, rs.Releases[i].Version, w)
		}
		if !rs.Releases[i].Timestamp.IsZero() {
			t.Errorf("%s carries a timestamp; the tags endpoint has none", w)
		}
	}

	// Three tags at two per page is two requests.
	if n := len(srv.requests); n != 2 {
		t.Errorf("made %d requests, want 2: %v", n, srv.requests)
	}
}

// GitHub answers 404 for a repository that does not exist AND for one the
// token may not read. The error must say both, so nobody reads "not found"
// as "does not exist".
func TestNotFoundNamesBothPossibilities(t *testing.T) {
	client, _, _, _ := newFixture(t)
	ds := New(Releases, client, "")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "github-releases", PackageName: "nosuch/repo",
	})
	if err == nil {
		t.Fatal("a missing repository resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "cannot read") {
		t.Errorf("the 404 error must name both readings: %q", msg)
	}
}

// GitHub's primary limit is a 403 with X-RateLimit-Remaining: 0, its
// secondary limit a 429; both name when the limit clears. The error must say
// "rate limit" in words a rule author can grep for, and name the reset time.
func TestRateLimitExhaustionIsNamed(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		client, srv, _, _ := newFixture(t)
		srv.status["hadolint/hadolint"] = code
		srv.headers["hadolint/hadolint"] = http.Header{
			"X-Ratelimit-Remaining": {"0"},
			"X-Ratelimit-Reset":     {"1789200000"},
		}

		ds := New(Releases, client, "")
		_, err := ds.Releases(t.Context(), lookup.Ref{
			Datasource: "github-releases", PackageName: "hadolint/hadolint",
		})
		if err == nil {
			t.Fatalf("a %d response resolved", code)
		}
		if !strings.Contains(err.Error(), "rate limit") {
			t.Errorf("a %d must be named as the rate limit, got: %q", code, err.Error())
		}
		if !strings.Contains(err.Error(), "resets at 2026-09-12T08:00:00Z") {
			t.Errorf("the reset time must be named: %q", err.Error())
		}
	}
}

// A 403 that is not the rate limit must not be misreported as one - the two
// need different remedies (wait, versus fix the token's scope).
func TestPlainForbiddenIsNotReportedAsRateLimit(t *testing.T) {
	client, srv, _, _ := newFixture(t)
	srv.status["hadolint/hadolint"] = http.StatusForbidden

	ds := New(Releases, client, "")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "github-releases", PackageName: "hadolint/hadolint",
	})
	if err == nil {
		t.Fatal("a 403 response resolved")
	}
	if strings.Contains(err.Error(), "rate limit") {
		t.Errorf("a plain 403 must not be reported as the rate limit: %q", err.Error())
	}
}

func TestOwnerRepoParsing(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{name: "bare", in: "hadolint/hadolint", wantOwner: "hadolint", wantRepo: "hadolint"},
		{name: "url", in: "https://github.com/openvex/vexctl", wantOwner: "openvex", wantRepo: "vexctl"},
		{name: "url with trailing slash", in: "https://github.com/openvex/vexctl/", wantOwner: "openvex", wantRepo: "vexctl"},
		{name: "url with .git suffix", in: "https://github.com/openvex/vexctl.git", wantOwner: "openvex", wantRepo: "vexctl"},
		{name: "no slash", in: "hadolint", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := splitOwnerRepo(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitOwnerRepo(%q) resolved to %q/%q, want an error", tc.in, owner, repo)
				}
				if !strings.Contains(err.Error(), "owner/repo") {
					t.Errorf("error must name the expected form %q, got: %q", "owner/repo", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("splitOwnerRepo(%q) = error %v, want %q/%q", tc.in, err, tc.wantOwner, tc.wantRepo)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Errorf("splitOwnerRepo(%q) = %q/%q, want %q/%q", tc.in, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

func TestAnUnregisteredHostIsRefused(t *testing.T) {
	client, _, rec, _ := newFixture(t)
	ds := New(Releases, client, "")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "github-releases", PackageName: "hadolint/hadolint",
		RegistryURLs: []string{"https://other.example"},
	})
	if err == nil {
		t.Error("a lookup against an unregistered host succeeded")
	}
	if !rec.Failed() || !rec.Mentions("other.example") {
		t.Errorf("the refusing transport did not name the escaping host: %s", rec.String())
	}
}
