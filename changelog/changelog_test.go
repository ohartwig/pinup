// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/fake/harness"
	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/semverx"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// scheme is strict three-part semver, enough to order releases.
type scheme struct{}

func (scheme) Name() string { return "semver" }
func (scheme) IsValid(s string) bool {
	_, ok := semverx.Parse(s)
	return ok
}
func (s scheme) IsVersion(v string) bool { return s.IsValid(v) }
func (s scheme) IsStable(v string) bool  { return s.IsValid(v) }
func (scheme) Major(v string) (int, bool) {
	p, ok := semverx.Parse(v)
	return p.Major, ok
}
func (scheme) Minor(v string) (int, bool) {
	p, ok := semverx.Parse(v)
	return p.Minor, ok
}
func (scheme) Patch(v string) (int, bool) {
	p, ok := semverx.Parse(v)
	return p.Patch, ok
}
func (scheme) Compare(a, b string) int {
	va, _ := semverx.Parse(a)
	vb, _ := semverx.Parse(b)
	return semverx.CompareVersions(va, vb)
}
func (s scheme) Equal(a, b string) bool       { return s.Compare(a, b) == 0 }
func (s scheme) Satisfies(v, rng string) bool { return s.Equal(v, rng) }
func (scheme) NewValue(_, target string, _ versioning.RangeStrategy) (string, error) {
	return target, nil
}
func (scheme) IsCompatible(_, _ string) bool { return true }

// github speaks the releases endpoint: a JSON list, drafts included, in
// the order the API returns them (newest first).
type github struct {
	mu       sync.Mutex
	releases map[string][]map[string]any
	calls    int
}

func (g *github) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		http.Error(w, "accept", http.StatusNotAcceptable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	path = strings.TrimSuffix(path, "/releases")
	list, ok := g.releases[path]
	if !ok {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page(list, r.URL.Query()))
}

// gitlab speaks the projects releases endpoint with the path URL-encoded.
type gitlab struct {
	releases map[string][]map[string]any
	seenAuth string
	calls    int
}

func (g *gitlab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.seenAuth = r.Header.Get("PRIVATE-TOKEN")
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/")
	enc := strings.TrimSuffix(rest, "/releases")
	list, ok := g.releases[enc]
	if !ok {
		http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
		return
	}
	g.calls++
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page(list, r.URL.Query()))
}

// page slices a list the way both APIs do: per_page and page, 1-based.
func page(list []map[string]any, q url.Values) []map[string]any {
	per, _ := strconv.Atoi(q.Get("per_page"))
	n, _ := strconv.Atoi(q.Get("page"))
	if per <= 0 {
		per = 30
	}
	if n <= 0 {
		n = 1
	}
	from := (n - 1) * per
	if from >= len(list) {
		return []map[string]any{}
	}
	to := from + per
	if to > len(list) {
		to = len(list)
	}
	return list[from:to]
}

type memCache struct {
	m map[string][]byte
}

func (c *memCache) GetReleases(key string, _ time.Duration, _ time.Time) ([]byte, bool, error) {
	p, ok := c.m[key]
	return p, ok, nil
}
func (c *memCache) PutReleases(key string, payload []byte, _ time.Time) error {
	c.m[key] = payload
	return nil
}

func fixture(t *testing.T) (*Fetcher, *github, *gitlab, *harness.RefusingTransport, *harness.Recorder) {
	t.Helper()
	gh := &github{releases: map[string][]map[string]any{}}
	gl := &gitlab{releases: map[string][]map[string]any{}}
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("api.github.com", gh)
	rt.Handle("git.example.test", gl)
	rt.Forbid("developer.mend.io")
	client := httpx.New(httpx.Options{
		Transport: rt,
		HostRules: []httpx.HostRule{{MatchHost: "git.example.test", Token: "glpat-x", HeaderName: "PRIVATE-TOKEN"}},
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	f := &Fetcher{Client: client, GitLabURL: "https://git.example.test", MaxBody: 40}
	return f, gh, gl, rt, rec
}

func TestGitHubNotesSpanCurrentToTarget(t *testing.T) {
	f, gh, _, rt, rec := fixture(t)
	gh.releases["hadolint/hadolint"] = []map[string]any{
		{"tag_name": "v2.13.0", "name": "v2.13.0", "body": "future", "html_url": "https://github.com/hadolint/hadolint/releases/tag/v2.13.0", "published_at": "2026-05-01T00:00:00Z"},
		{"tag_name": "v2.12.1", "name": "Bugfix 2.12.1", "body": "fixes a thing that was broken for a long time indeed", "html_url": "https://github.com/hadolint/hadolint/releases/tag/v2.12.1", "published_at": "2026-04-01T00:00:00Z"},
		{"tag_name": "v2.12.1-draft", "name": "draft", "body": "no", "draft": true},
		{"tag_name": "v2.12.0", "name": "", "body": "new", "html_url": "https://github.com/hadolint/hadolint/releases/tag/v2.12.0", "published_at": "2026-03-01T00:00:00Z"},
		{"tag_name": "v2.11.0", "name": "old", "body": "past", "html_url": "u", "published_at": "2026-02-01T00:00:00Z"},
		{"tag_name": "nightly", "name": "rolling", "body": "not a version"},
	}
	notes, compare, err := f.Notes(context.Background(), "https://github.com/hadolint/hadolint", scheme{}, "2.11.0", "2.12.1")
	if err != nil {
		t.Fatal(err)
	}
	// The manifest says 2.11.0; the forge's tag is v2.11.0.
	if compare != "https://github.com/hadolint/hadolint/compare/v2.11.0...v2.12.1" {
		t.Errorf("compare = %q", compare)
	}
	var got []string
	for _, n := range notes {
		got = append(got, n.Version)
	}
	if want := "v2.12.1 v2.12.0"; strings.Join(got, " ") != want {
		t.Fatalf("versions = %q, want %q", got, want)
	}
	if notes[0].Title != "Bugfix 2.12.1" || !notes[0].Published.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("first note = %+v", notes[0])
	}
	if !strings.HasSuffix(notes[0].Body, "… (truncated)") || len(notes[0].Body) > 40+len("\n\n… (truncated)") {
		t.Errorf("body not truncated at MaxBody: %q", notes[0].Body)
	}
	if notes[1].Body != "new" {
		t.Errorf("short body changed: %q", notes[1].Body)
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")
	if len(rec.Errors) > 0 {
		t.Fatal(rec.Errors)
	}
}

func TestGitLabNotesUseInstanceTokenAndEncodedPath(t *testing.T) {
	f, _, gl, _, rec := fixture(t)
	gl.releases["devops%2Fci-cd-components%2Fdeploy-tools"] = []map[string]any{
		{"tag_name": "3.1.0", "name": "3.1.0", "description": "adds x", "released_at": "2026-06-01T00:00:00Z", "_links": map[string]any{"self": "https://git.example.test/devops/ci-cd-components/deploy-tools/-/releases/3.1.0"}},
		{"tag_name": "3.0.0", "name": "3.0.0", "description": "breaks y", "released_at": "2026-05-01T00:00:00Z", "_links": map[string]any{"self": "s"}},
	}
	notes, compare, err := f.Notes(context.Background(), "https://git.example.test/devops/ci-cd-components/deploy-tools", scheme{}, "3.0.0", "3.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if gl.seenAuth != "glpat-x" {
		t.Errorf("instance token not sent: %q", gl.seenAuth)
	}
	if compare != "https://git.example.test/devops/ci-cd-components/deploy-tools/-/compare/3.0.0...3.1.0" {
		t.Errorf("compare = %q", compare)
	}
	if len(notes) != 1 || notes[0].Version != "3.1.0" || notes[0].Body != "adds x" || !strings.HasSuffix(notes[0].URL, "/releases/3.1.0") {
		t.Errorf("notes = %+v", notes)
	}
	if len(rec.Errors) > 0 {
		t.Fatal(rec.Errors)
	}
}

func TestUnknownSourceYieldsNothingAndTouchesNoHost(t *testing.T) {
	f, _, _, rt, rec := fixture(t)
	for _, src := range []string{"", "https://bitbucket.org/x/y", "https://github.com/onlyowner", "not a url"} {
		notes, compare, err := f.Notes(context.Background(), src, scheme{}, "1.0.0", "1.1.0")
		if err != nil || notes != nil || compare != "" {
			t.Errorf("%q: notes=%v compare=%q err=%v", src, notes, compare, err)
		}
	}
	if n := rt.Count("api.github.com") + rt.Count("git.example.test"); n != 0 {
		t.Errorf("%d requests for unknown sources", n)
	}
	if len(rec.Errors) > 0 {
		t.Fatal(rec.Errors)
	}
}

func TestFetchFailureKeepsCompareLink(t *testing.T) {
	f, _, _, _, _ := fixture(t)
	notes, compare, err := f.Notes(context.Background(), "https://github.com/nobody/nothing", scheme{}, "1.0.0", "1.1.0")
	if err == nil {
		t.Fatal("404 must surface as an error")
	}
	if notes != nil || compare != "https://github.com/nobody/nothing/compare/1.0.0...1.1.0" {
		t.Errorf("notes=%v compare=%q", notes, compare)
	}
}

func TestCacheServesSecondCall(t *testing.T) {
	f, gh, _, _, _ := fixture(t)
	f.Cache = &memCache{m: map[string][]byte{}}
	gh.releases["o/r"] = []map[string]any{{"tag_name": "1.1.0", "body": "b", "published_at": "2026-01-01T00:00:00Z"}}
	for i := 0; i < 2; i++ {
		notes, _, err := f.Notes(context.Background(), "https://github.com/o/r.git", scheme{}, "1.0.0", "1.1.0")
		if err != nil || len(notes) != 1 {
			t.Fatalf("call %d: notes=%v err=%v", i, notes, err)
		}
	}
	if gh.calls != 1 {
		t.Errorf("github called %d times, want 1", gh.calls)
	}
	var st stored
	if err := json.Unmarshal(f.Cache.(*memCache).m["notes\x00https://github.com/o/r.git"], &st); err != nil || len(st.Releases) != 1 || !st.Complete {
		t.Errorf("cache payload: %+v %v", st, err)
	}
}

// A project on 2.5.x whose consumer pins the 1.x line has its notes deep
// in the list: pages are read until one release at or below the current
// version is in hand, and a cached list that stops short is read again.
func TestPagesAreReadUntilTheSpanIsCovered(t *testing.T) {
	f, _, gl, _, rec := fixture(t)
	f.Cache = &memCache{m: map[string][]byte{}}
	var list []map[string]any
	for minor := 5; minor >= 0; minor-- {
		for patch := 60; patch >= 0; patch-- {
			list = append(list, map[string]any{"tag_name": fmt.Sprintf("2.%d.%d", minor, patch), "description": "n"})
		}
	}
	list = append(list, map[string]any{"tag_name": "1.63.3", "description": "the one"}, map[string]any{"tag_name": "1.63.2", "description": "n"}, map[string]any{"tag_name": "1.62.17", "description": "current"})
	gl.releases["devops%2Fcdp"] = list // 366 on 2.x, then the three 1.x
	src := "https://git.example.test/devops/cdp"
	notes, _, err := f.Notes(context.Background(), src, scheme{}, "2.5.3", "2.5.5")
	if err != nil || len(notes) != 2 || gl.calls != 1 {
		t.Fatalf("2.x span: notes=%d calls=%d err=%v", len(notes), gl.calls, err)
	}
	notes, _, err = f.Notes(context.Background(), src, scheme{}, "1.62.17", "1.63.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Version != "1.63.3" || notes[0].Body != "the one" {
		t.Errorf("1.x span: %+v", notes)
	}
	// One page was cached but did not reach 1.62.17: four pages read anew.
	if gl.calls != 1+4 {
		t.Errorf("gitlab called %d times, want 5", gl.calls)
	}
	// The cached list now covers everything: no further call.
	if _, _, err := f.Notes(context.Background(), src, scheme{}, "2.0.0", "2.5.5"); err != nil || gl.calls != 5 {
		t.Errorf("third call: calls=%d err=%v", gl.calls, err)
	}
	if len(rec.Errors) > 0 {
		t.Fatal(rec.Errors)
	}
}
