// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gitlabds

import (
	"context"
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

// gitlab is a server that speaks the tags API: keyset headers, a forced page
// size of two, and the same 404 for "no such project" and "no permission".
// The bodies are data; the protocol is code, so a client that stopped
// paginating or sent the wrong path would fail here rather than in
// production.
type gitlab struct {
	mu       sync.Mutex
	projects map[string][]string // encoded path -> tag names
	requests []string
	token    string
}

func (g *gitlab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests = append(g.requests, r.URL.RequestURI())
	g.mu.Unlock()

	if g.token != "" && r.Header.Get("Authorization") != "Bearer "+g.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// EscapedPath, not Path: Go decodes %2F to "/" in Path, and the whole
	// point of the encoding is that GitLab sees "group%2Fproject" as ONE
	// segment. A server reading the decoded path would accept a client that
	// forgot to encode - which is exactly the client bug this fake must catch.
	const prefix = "/api/v4/projects/"
	path := r.URL.EscapedPath()
	if !strings.HasPrefix(path, prefix) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(path, prefix)
	id, _, _ := strings.Cut(rest, "/repository/tags")
	tags, ok := g.projects[id]
	if !ok || !strings.HasSuffix(path, "/repository/tags") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	const pageSize = 2
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	end := min(start+pageSize, len(tags))
	if start > len(tags) {
		start = len(tags)
	}
	if end < len(tags) {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	} else {
		w.Header().Set("X-Next-Page", "")
	}

	var body []map[string]any
	for i, name := range tags[start:end] {
		body = append(body, map[string]any{
			"name": name,
			"commit": map[string]any{
				"id":             fmt.Sprintf("%040d", start+i),
				"committed_date": time.Date(2026, 1, 1+start+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
			},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func newFixture(t *testing.T, token string) (*Datasource, *gitlab, *harness.Recorder) {
	t.Helper()
	srv := &gitlab{
		projects: map[string][]string{
			"devops%2Fci-cd-components%2Flint-tools": {"1.33.59", "1.33.60", "1.33.61", "1.33.62", "1.33.64"},
			"devops%2Fpinup%2Fempty":                 {},
		},
		token: token,
	}
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("git.example", srv)

	var rules []httpx.HostRule
	if token != "" {
		rules = append(rules, httpx.HostRule{MatchHost: "git.example", Token: token})
	}
	client := httpx.New(httpx.Options{
		Transport: rt,
		HostRules: rules,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	return New(Tags, client, "https://git.example"), srv, rec
}

func TestTagsPaginateAndCarryTimestamps(t *testing.T) {
	ds, srv, rec := newFixture(t, "")
	rs, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "gitlab-tags", PackageName: "devops/ci-cd-components/lint-tools",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}

	if len(rs.Releases) != 5 {
		t.Errorf("got %d releases, want 5 - pagination stopped early", len(rs.Releases))
	}
	// Five tags at two per page is three requests. A client that ignored
	// X-Next-Page would make one; one that never stopped would loop.
	if n := len(srv.requests); n != 3 {
		t.Errorf("made %d requests, want 3: %v", n, srv.requests)
	}
	// The project path must reach the server as ONE encoded segment.
	for _, r := range srv.requests {
		if !strings.Contains(r, "devops%2Fci-cd-components%2Flint-tools") {
			t.Errorf("project path was not URL-encoded as a single segment: %s", r)
		}
	}
	for _, rel := range rs.Releases {
		if rel.Timestamp.IsZero() {
			t.Errorf("%s carries no timestamp; minimumReleaseAge would have to fall back to firstseen", rel.Version)
		}
		if rel.Digest == "" {
			t.Errorf("%s carries no commit id", rel.Version)
		}
	}
	// Measured: the source URL is the project page under the registry,
	// which is what the matchSourceUrls rules see after lookup.
	if want := "https://git.example/devops/ci-cd-components/lint-tools"; rs.SourceURL != want {
		t.Errorf("sourceUrl = %q, want %q", rs.SourceURL, want)
	}
}

func TestTokenIsSentWhenConfigured(t *testing.T) {
	ds, _, rec := newFixture(t, "glpat-secret")
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "gitlab-tags", PackageName: "devops/ci-cd-components/lint-tools",
	})
	if err != nil {
		t.Fatalf("with the token configured the lookup should succeed: %v", err)
	}
	if rec.Failed() {
		t.Fatal(rec.String())
	}
}

// GitLab answers 404 for a project that does not exist AND for one the token
// may not read. The error must say both, so nobody reads "not found" as
// "does not exist" - the same trap the yasrt session hit on protected
// branches.
func TestNotFoundNamesBothPossibilities(t *testing.T) {
	ds, _, _ := newFixture(t, "")
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "gitlab-tags", PackageName: "devops/nosuch",
	})
	if err == nil {
		t.Fatal("a missing project resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "cannot read") {
		t.Errorf("the 404 error must name both readings: %q", msg)
	}
}

func TestAProjectWithNoTagsIsEmptyNotAnError(t *testing.T) {
	ds, _, _ := newFixture(t, "")
	rs, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "gitlab-tags", PackageName: "devops/pinup/empty",
	})
	if err != nil {
		t.Fatalf("an empty tag list is a real answer, not a failure: %v", err)
	}
	if len(rs.Releases) != 0 {
		t.Errorf("got %d releases from a project with none", len(rs.Releases))
	}
}

func TestAnUnregisteredHostIsRefused(t *testing.T) {
	ds, _, rec := newFixture(t, "")
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "gitlab-tags", PackageName: "x", RegistryURLs: []string{"https://other.example"},
	})
	if err == nil {
		t.Error("a lookup against an unregistered host succeeded")
	}
	if !rec.Failed() || !rec.Mentions("other.example") {
		t.Errorf("the refusing transport did not name the escaping host: %s", rec.String())
	}
}
