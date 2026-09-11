// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package npmds

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/fake/harness"
	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
)

// npmRegistry is a server that speaks the npm Registry API's document GET
// endpoint at a fixed path: whatever the client's URL.EscapedPath() carries
// keys the fixture's fixed responses, so a client that decoded a scoped
// name's "%2F" before building the request URL would fail to find the
// document, not merely get the wrong bytes.
type npmRegistry struct {
	mu sync.Mutex

	docs        map[string]string // EscapedPath -> full registry document JSON
	requireAuth map[string]string // EscapedPath -> the bearer token that must be presented
	status      map[string]int    // EscapedPath -> status code to force, bypassing docs/auth

	requests []string
}

func (r *npmRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.EscapedPath()

	r.mu.Lock()
	r.requests = append(r.requests, path)
	r.mu.Unlock()

	if code, forced := r.status[path]; forced {
		w.WriteHeader(code)
		return
	}
	if want, needsAuth := r.requireAuth[path]; needsAuth {
		if got := req.Header.Get("Authorization"); got != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	body, ok := r.docs[path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (r *npmRegistry) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.requests {
		if p == path {
			n++
		}
	}
	return n
}

// animejsDoc is a full registry document (the superset shape) for a
// three-version package with one deprecated version and a "git+...git"
// repository URL, matching what registry.npmjs.org actually publishes.
const animejsDoc = `{
	"versions": {
		"1.0.0": {},
		"1.1.0": {"deprecated": "use anime 2.x instead"},
		"2.0.0": {}
	},
	"time": {
		"created": "2017-01-01T00:00:00.000Z",
		"modified": "2019-01-01T00:00:00.000Z",
		"1.0.0": "2017-06-01T12:00:00.000Z",
		"1.1.0": "2018-06-01T12:00:00.000Z",
		"2.0.0": "2019-01-01T12:00:00.000Z"
	},
	"dist-tags": {"latest": "2.0.0"},
	"repository": {"url": "git+https://github.com/juliangarnier/anime.git"}
}`

// scopedDevDoc is the minimal document for the scoped package served from
// the GitLab-shaped registry.
const scopedDevDoc = `{"versions": {"1.0.0": {}}, "dist-tags": {"latest": "1.0.0"}}`

const scopedToken = "s3cr3t-token"

func newFixture(t *testing.T) (*httpx.Client, *npmRegistry, *harness.Recorder, *harness.RefusingTransport) {
	t.Helper()
	reg := &npmRegistry{
		docs:        map[string]string{},
		requireAuth: map[string]string{},
		status:      map[string]int{},
	}
	reg.docs["/animejs"] = animejsDoc
	reg.docs["/api/v4/packages/npm/@moselwal%2Fdev"] = scopedDevDoc
	reg.requireAuth["/api/v4/packages/npm/@moselwal%2Fdev"] = scopedToken
	reg.status["/unauthorized-pkg"] = http.StatusUnauthorized

	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("registry.npmjs.org", reg)
	rt.Handle("git.ole-hartwig.eu", reg)
	rt.Forbid("developer.mend.io")

	client := httpx.New(httpx.Options{
		Transport: rt,
		HostRules: []httpx.HostRule{
			{MatchHost: "git.ole-hartwig.eu", Token: scopedToken},
		},
		Now:   func() time.Time { return time.Unix(0, 0) },
		Sleep: func(time.Duration) {},
	})
	return client, reg, rec, rt
}

func TestReleasesVersionsTimestampsDeprecatedAndSourceURL(t *testing.T) {
	client, reg, rec, rt := newFixture(t)
	ds := New(client)

	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: Name, PackageName: "animejs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	if n := rt.Count("registry.npmjs.org"); n != 1 {
		t.Errorf("registry.npmjs.org received %d requests, want 1", n)
	}
	if n := reg.count("/animejs"); n != 1 {
		t.Errorf("/animejs served %d times, want 1", n)
	}

	if len(rs.Releases) != 3 {
		t.Fatalf("got %d releases, want 3: %v", len(rs.Releases), rs.Releases)
	}

	byVersion := make(map[string]struct {
		timestamp  time.Time
		deprecated bool
	}, len(rs.Releases))
	for _, r := range rs.Releases {
		byVersion[r.Version] = struct {
			timestamp  time.Time
			deprecated bool
		}{r.Timestamp, r.Deprecated}
	}

	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		got, ok := byVersion[v]
		if !ok {
			t.Errorf("version %q missing from releases", v)
			continue
		}
		if got.timestamp.IsZero() {
			t.Errorf("%s carries no timestamp", v)
		}
	}

	if !byVersion["1.0.0"].timestamp.Equal(time.Date(2017, 6, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("1.0.0 timestamp = %v, want 2017-06-01T12:00:00Z", byVersion["1.0.0"].timestamp)
	}

	if byVersion["1.0.0"].deprecated || byVersion["2.0.0"].deprecated {
		t.Error("1.0.0 and 2.0.0 must not be reported deprecated")
	}
	if !byVersion["1.1.0"].deprecated {
		t.Error("1.1.0 carries a non-empty deprecated string and must be reported deprecated")
	}

	const wantSource = "https://github.com/juliangarnier/anime"
	if rs.SourceURL != wantSource {
		t.Errorf("SourceURL = %q, want %q (git+ prefix and .git suffix stripped)", rs.SourceURL, wantSource)
	}
}

func TestScopedNameIsEncodedAndServedFromItsOwnRegistry(t *testing.T) {
	client, reg, rec, rt := newFixture(t)
	ds := New(client)

	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource:   Name,
		PackageName:  "@moselwal/dev",
		RegistryURLs: []string{"https://git.ole-hartwig.eu/api/v4/packages/npm/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	if len(rs.Releases) != 1 || rs.Releases[0].Version != "1.0.0" {
		t.Fatalf("got releases %v, want exactly [1.0.0]", rs.Releases)
	}

	if n := rt.Count("registry.npmjs.org"); n != 0 {
		t.Errorf("registry.npmjs.org received %d requests, want 0 - the scoped package names its own registry", n)
	}
	if n := rt.Count("git.ole-hartwig.eu"); n != 1 {
		t.Errorf("git.ole-hartwig.eu received %d requests, want 1", n)
	}
	if n := reg.count("/api/v4/packages/npm/@moselwal%2Fdev"); n != 1 {
		t.Errorf("the scoped path was served %d times, want 1 - the encoded @moselwal%%2Fdev path must be hit exactly", n)
	}
}

func TestNotFoundNamesBothPossibilities(t *testing.T) {
	client, _, _, _ := newFixture(t)
	ds := New(client)

	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: Name, PackageName: "nosuchpkg",
	})
	if err == nil {
		t.Fatal("a missing package resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "cannot read") {
		t.Errorf("the 404 error must name both readings: %q", msg)
	}
}

func TestUnauthorizedNamesTheToken(t *testing.T) {
	client, _, _, _ := newFixture(t)
	ds := New(client)

	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: Name, PackageName: "unauthorized-pkg",
	})
	if err == nil {
		t.Fatal("a 401 response resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "token") || !strings.Contains(msg, "cannot read") {
		t.Errorf("the 401 error must name the token as unable to read the registry: %q", msg)
	}
}

func TestAnUnregisteredHostIsRefused(t *testing.T) {
	client, _, rec, _ := newFixture(t)
	ds := New(client)

	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: Name, PackageName: "animejs",
		RegistryURLs: []string{"https://other.example"},
	})
	if err == nil {
		t.Error("a lookup against an unregistered host succeeded")
	}
	if !rec.Failed() || !rec.Mentions("other.example") {
		t.Errorf("the refusing transport did not name the escaping host: %s", rec.String())
	}
}
