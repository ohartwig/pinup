// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package dockerds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// hubRegistry fakes a Docker Registry HTTP API V2 host: it speaks the bearer
// challenge, validates the token's scope against the repository being
// fetched, and paginates tags/list at a forced page size of two so
// pagination is exercised on every run rather than only past a hundred tags.
//
// The token format is our own fixture convention - "token-for-<repository>" -
// chosen so the fake can check scope without decoding a real JWT. What is
// under test is the client's protocol handling, not a token codec.
type hubRegistry struct {
	mu        sync.Mutex
	tags      map[string][]string
	manifests map[string]manifestFixture
	requests  []string
}

type manifestFixture struct {
	digest string
	body   []byte
}

func (h *hubRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.requests = append(h.requests, r.URL.RequestURI())
	h.mu.Unlock()

	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/tags/list"):
		repo := strings.TrimSuffix(strings.TrimPrefix(path, "/v2/"), "/tags/list")
		h.handleTags(w, r, repo)
	case strings.Contains(path, "/manifests/"):
		idx := strings.Index(path, "/manifests/")
		repo := strings.TrimPrefix(path[:idx], "/v2/")
		tag := path[idx+len("/manifests/"):]
		h.handleManifest(w, r, repo, tag)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// checkAuth is shared by tags and manifest handling: both live under the
// same "repository:<repo>:pull" scope on a real registry.
func (h *hubRegistry) checkAuth(w http.ResponseWriter, r *http.Request, repo string) bool {
	want := "Bearer token-for-" + repo
	if r.Header.Get("Authorization") == want {
		return true
	}
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:%s:pull"`, repo))
	w.WriteHeader(http.StatusUnauthorized)
	return false
}

func (h *hubRegistry) handleTags(w http.ResponseWriter, r *http.Request, repo string) {
	if repo == "library/ratelimited" {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"code":"TOOMANYREQUESTS","message":"pull rate limit exceeded"}]}`))
		return
	}
	if !h.checkAuth(w, r, repo) {
		return
	}

	h.mu.Lock()
	tags, ok := h.tags[repo]
	h.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	const size = 2
	start := 0
	if last := r.URL.Query().Get("last"); last != "" {
		for i, t := range tags {
			if t == last {
				start = i + 1
				break
			}
		}
	}
	end := min(start+size, len(tags))
	page := tags[start:end]
	if end < len(tags) {
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?n=1000&last=%s>; rel="next"`, repo, page[len(page)-1]))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": page})
}

func (h *hubRegistry) handleManifest(w http.ResponseWriter, r *http.Request, repo, tag string) {
	if !h.checkAuth(w, r, repo) {
		return
	}
	h.mu.Lock()
	m, ok := h.manifests[repo+":"+tag]
	h.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Docker-Content-Digest", m.digest)
	w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	_, _ = w.Write(m.body)
}

// hubAuth fakes a token realm: it mints "token-for-<repo>" from the scope
// query parameter, the same way auth.docker.io mints a real bearer token
// from the challenge's scope. No credentials are required, matching Docker
// Hub's anonymous pull.
type hubAuth struct {
	mu       sync.Mutex
	requests []string
}

func (a *hubAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.requests = append(a.requests, r.URL.RequestURI())
	a.mu.Unlock()

	scope := r.URL.Query().Get("scope")
	const prefix, suffix = "repository:", ":pull"
	repo := strings.TrimSuffix(strings.TrimPrefix(scope, prefix), suffix)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": "token-for-" + repo})
}

func newHubFixture(t *testing.T) (*Datasource, *hubRegistry, *hubAuth, *harness.RefusingTransport) {
	t.Helper()
	hub := &hubRegistry{
		tags: map[string][]string{
			"library/alpine": {"3.18", "3.19", "3.20", "3.21", "edge"},
			"library/nginx":  {"1.27"},
		},
		manifests: map[string]manifestFixture{
			"library/alpine:3.20": {digest: "sha256:" + strings.Repeat("a", 64), body: []byte(`{"schemaVersion":2}`)},
		},
	}
	auth := &hubAuth{}
	rt := harness.NewRefusingTransport(t).
		Handle("registry-1.docker.io", hub).
		Handle("auth.docker.io", auth).
		Forbid("developer.mend.io")
	return New(rt, nil), hub, auth, rt
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		name         string
		wantRegistry string
		wantRepo     string
	}{
		{"alpine", "https://registry-1.docker.io", "library/alpine"},
		{"binwiederhier/ntfy", "https://registry-1.docker.io", "binwiederhier/ntfy"},
		{"docker.io/binwiederhier/ntfy", "https://registry-1.docker.io", "binwiederhier/ntfy"},
		{"ghcr.io/oras-project/oras", "https://ghcr.io", "oras-project/oras"},
		{"registry.ole-hartwig.eu/devops/ci-mirrors/alpine", "https://registry.ole-hartwig.eu", "devops/ci-mirrors/alpine"},
		{"registry.gitlab.com/gitlab-org/fleeting/plugins/aws", "https://registry.gitlab.com", "gitlab-org/fleeting/plugins/aws"},
	} {
		registry, repo := Parse(c.name)
		if registry != c.wantRegistry || repo != c.wantRepo {
			t.Errorf("Parse(%q) = (%q, %q), want (%q, %q)", c.name, registry, repo, c.wantRegistry, c.wantRepo)
		}
	}
}

// TestHubAnonymousFlow is the cold-lookup path for a completely unauthenticated
// image: challenge, token, three pages of tags. The exact request count is
// the point - a client that stopped paginating early or re-fetched a token it
// already had would pass a looser test.
func TestHubAnonymousFlow(t *testing.T) {
	ds, hub, auth, rt := newHubFixture(t)

	rs, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "alpine"})
	if err != nil {
		t.Fatalf("cold lookup failed: %v", err)
	}
	if len(rs.Releases) != 5 {
		t.Errorf("got %d releases, want 5", len(rs.Releases))
	}
	for _, rel := range rs.Releases {
		if !rel.Timestamp.IsZero() {
			t.Errorf("release %s carries a timestamp the registry never sent", rel.Version)
		}
	}

	// Cold: one failed (unauthenticated) attempt plus three successful pages
	// against the registry, and exactly one token request.
	if n := rt.Count("registry-1.docker.io"); n != 4 {
		t.Errorf("cold lookup made %d registry requests, want 4: %v", n, hub.requests)
	}
	if n := rt.Count("auth.docker.io"); n != 1 {
		t.Errorf("cold lookup made %d token requests, want 1: %v", n, auth.requests)
	}

	// Warm: the token is cached, so this is three requests and zero more
	// token fetches.
	if _, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "alpine"}); err != nil {
		t.Fatalf("warm lookup failed: %v", err)
	}
	if n := rt.Count("registry-1.docker.io"); n != 7 {
		t.Errorf("after a warm lookup, registry requests = %d, want 7 (4 + 3)", n)
	}
	if n := rt.Count("auth.docker.io"); n != 1 {
		t.Errorf("warm lookup issued a token request; token was not cached (auth.docker.io count = %d)", n)
	}

	rt.MustNotHaveBeenCalled("developer.mend.io")
}

// TestScopeMismatchGetsAFreshToken seeds the cache with a token that is
// valid for a different repository, then asks for a repository it does not
// cover. The fake rejects it exactly as a real registry would (401,
// insufficient scope); the datasource must fetch a new one rather than
// failing the lookup.
func TestScopeMismatchGetsAFreshToken(t *testing.T) {
	ds, hub, auth, rt := newHubFixture(t)

	registry, _ := Parse("alpine")
	// A token minted for library/alpine, planted directly under the cache
	// key for library/nginx - the situation a caching bug (keying on
	// registry alone, say) would produce.
	ds.tokens[tokenKey{registry: registry, repository: "library/nginx"}] = "token-for-library/alpine"

	rs, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "nginx"})
	if err != nil {
		t.Fatalf("lookup with a wrongly-scoped cached token failed: %v", err)
	}
	if len(rs.Releases) != 1 || rs.Releases[0].Version != "1.27" {
		t.Errorf("got %v, want a single 1.27 release", rs.Releases)
	}

	// One rejected attempt with the planted token, one retry with a freshly
	// minted one; exactly one token request to fetch the replacement.
	if n := rt.Count("registry-1.docker.io"); n != 2 {
		t.Errorf("registry requests = %d, want 2: %v", n, hub.requests)
	}
	if n := rt.Count("auth.docker.io"); n != 1 {
		t.Errorf("token requests = %d, want 1: %v", n, auth.requests)
	}
}

func TestDigestReturnsContentDigestHeader(t *testing.T) {
	ds, _, _, _ := newHubFixture(t)

	digest, err := ds.Digest(context.Background(), lookup.Ref{Datasource: Name, PackageName: "alpine"}, "3.20")
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	want := "sha256:" + strings.Repeat("a", 64)
	if digest != want {
		t.Errorf("Digest = %q, want %q", digest, want)
	}
}

func TestNotFoundNamesBothPossibilities(t *testing.T) {
	ds, _, _, _ := newHubFixture(t)

	_, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "no-such-image"})
	if err == nil {
		t.Fatal("a nonexistent repository resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "cannot pull") {
		t.Errorf("the 404 error must name both readings: %q", msg)
	}
}

func TestRateLimitIsReportedDistinctly(t *testing.T) {
	ds, _, _, _ := newHubFixture(t)

	_, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "ratelimited"})
	if err == nil {
		t.Fatal("a rate-limited lookup resolved")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error does not name rate limiting: %q", err.Error())
	}
}

// glRegistry and glRealm fake a self-hosted GitLab container registry: the
// registry challenges with a realm on a DIFFERENT host, and that realm
// requires HTTP basic auth - the shape registry.ole-hartwig.eu / a
// git.ole-hartwig.eu JWT realm actually has, per CLAUDE.md.
type glRegistry struct {
	mu       sync.Mutex
	tags     map[string][]string
	requests []string
	// challenge, when set, replaces the standard one; %s is the repository.
	challenge string
}

func (g *glRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests = append(g.requests, r.URL.RequestURI())
	g.mu.Unlock()

	path := r.URL.Path
	if !strings.HasSuffix(path, "/tags/list") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	repo := strings.TrimSuffix(strings.TrimPrefix(path, "/v2/"), "/tags/list")

	want := "Bearer token-for-" + repo
	if r.Header.Get("Authorization") != want {
		challenge := g.challenge
		if challenge == "" {
			challenge = `Bearer realm="https://git.ole-hartwig.eu/jwt/auth",service="container_registry",scope="repository:%s:pull"`
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(challenge, repo))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	g.mu.Lock()
	tags := g.tags[repo]
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
}

// glRealm requires HTTP basic auth, unlike the anonymous Docker Hub realm.
type glRealm struct {
	mu          sync.Mutex
	requests    int
	sawBasic    bool
	sawUser     string
	wantUser    string
	wantPass    string
	rejectCalls int
}

func (g *glRealm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests++
	g.mu.Unlock()

	user, pass, ok := r.BasicAuth()
	if !ok || user != g.wantUser || pass != g.wantPass {
		g.mu.Lock()
		g.rejectCalls++
		g.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	g.mu.Lock()
	g.sawBasic = true
	g.sawUser = user
	g.mu.Unlock()

	scope := r.URL.Query().Get("scope")
	const prefix, suffix = "repository:", ":pull"
	repo := strings.TrimSuffix(strings.TrimPrefix(scope, prefix), suffix)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": "token-for-" + repo})
}

func TestGitLabRegistryFlowSendsBasicAuthOnlyToRealm(t *testing.T) {
	registry := &glRegistry{tags: map[string][]string{"devops/ci-mirrors/alpine": {"1.0.0"}}}
	realm := &glRealm{wantUser: "gitlab-ci-token", wantPass: "glcbt-secret"}
	rt := harness.NewRefusingTransport(t).
		Handle("registry.ole-hartwig.eu", registry).
		Handle("git.ole-hartwig.eu", realm)

	creds := func(registry string, realm *url.URL) (string, string, bool) {
		if registry == "registry.ole-hartwig.eu" && realm.Host == "git.ole-hartwig.eu" {
			return "gitlab-ci-token", "glcbt-secret", true
		}
		return "", "", false
	}
	ds := New(rt, creds)

	ref := lookup.Ref{
		Datasource:   Name,
		PackageName:  "devops/ci-mirrors/alpine",
		RegistryURLs: []string{"https://registry.ole-hartwig.eu"},
	}
	rs, err := ds.Releases(context.Background(), ref)
	if err != nil {
		t.Fatalf("lookup with credentials failed: %v", err)
	}
	if len(rs.Releases) != 1 || rs.Releases[0].Version != "1.0.0" {
		t.Errorf("got %v, want a single 1.0.0 release", rs.Releases)
	}
	if !realm.sawBasic {
		t.Error("the realm never saw basic auth")
	}
	if realm.sawUser != "gitlab-ci-token" {
		t.Errorf("realm saw user %q", realm.sawUser)
	}
	// The registry itself must never see basic auth credentials - only the
	// bearer token minted by the realm.
	if n := rt.Count("registry.ole-hartwig.eu"); n != 2 {
		t.Errorf("registry requests = %d, want 2 (challenge + authenticated retry)", n)
	}
}

func TestGitLabRealmRejectsMissingCredentials(t *testing.T) {
	registry := &glRegistry{tags: map[string][]string{"devops/ci-mirrors/alpine": {"1.0.0"}}}
	realm := &glRealm{wantUser: "gitlab-ci-token", wantPass: "glcbt-secret"}
	rt := harness.NewRefusingTransport(t).
		Handle("registry.ole-hartwig.eu", registry).
		Handle("git.ole-hartwig.eu", realm)

	// No credentials configured: the realm must refuse, and the lookup must
	// fail rather than silently proceeding unauthenticated.
	ds := New(rt, nil)

	ref := lookup.Ref{
		Datasource:   Name,
		PackageName:  "devops/ci-mirrors/alpine",
		RegistryURLs: []string{"https://registry.ole-hartwig.eu"},
	}
	_, err := ds.Releases(context.Background(), ref)
	if err == nil {
		t.Fatal("a lookup against a realm that requires credentials succeeded without any")
	}
	if realm.rejectCalls == 0 {
		t.Error("the realm was never actually asked, so this proves nothing about the credential gate")
	}
	if realm.sawBasic {
		t.Error("the realm reports having seen valid basic auth despite no credentials being configured")
	}
}

// A registry a configuration names can name the instance's realm with a
// scope of its choosing. The credential is withheld: the registry is not
// the estate's, the scope is not the lookup's, or the realm is not TLS.
func TestRealmCredentialsAreBoundToRegistryScopeAndTLS(t *testing.T) {
	for _, tc := range []struct {
		name      string
		registry  string
		challenge string
	}{
		{"a foreign registry naming our realm", "evil.example.net",
			`Bearer realm="https://git.ole-hartwig.eu/jwt/auth",service="container_registry",scope="repository:%s:pull"`},
		{"our registry asking for push", "registry.ole-hartwig.eu",
			`Bearer realm="https://git.ole-hartwig.eu/jwt/auth",service="container_registry",scope="repository:%s:pull,push"`},
		{"our registry asking for another repository", "registry.ole-hartwig.eu",
			`Bearer realm="https://git.ole-hartwig.eu/jwt/auth",service="container_registry",scope="repository:devops/images/secret:pull"`},
		{"a realm over plain http", "registry.ole-hartwig.eu",
			`Bearer realm="http://git.ole-hartwig.eu/jwt/auth",service="container_registry",scope="repository:%s:pull"`},
	} {
		registry := &glRegistry{tags: map[string][]string{"devops/ci-mirrors/alpine": {"1.0.0"}}, challenge: tc.challenge}
		realm := &glRealm{wantUser: "gitlab-ci-token", wantPass: "glcbt-secret"}
		rt := harness.NewRefusingTransport(t).Handle(tc.registry, registry).Handle("git.ole-hartwig.eu", realm)
		creds := func(reg string, r *url.URL) (string, string, bool) {
			if reg == "registry.ole-hartwig.eu" && r.Host == "git.ole-hartwig.eu" && r.Scheme == "https" {
				return "gitlab-ci-token", "glcbt-secret", true
			}
			return "", "", false
		}
		ds := New(rt, creds)
		_, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "devops/ci-mirrors/alpine", RegistryURLs: []string{"https://" + tc.registry}})
		if err == nil {
			t.Errorf("%s: the lookup succeeded, so a token was minted", tc.name)
		}
		if realm.sawBasic {
			t.Errorf("%s: the realm saw the credential", tc.name)
		}
	}
}

// listServer serves one withdrawn-images.json (or a status) and counts reads.
type listServer struct {
	status int
	body   []byte
	hits   int
}

func (l *listServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	l.hits++
	if l.status != 0 {
		w.WriteHeader(l.status)
		return
	}
	_, _ = w.Write(l.body)
}

func withdrawnDS(t *testing.T, list *listServer) (*Datasource, lookup.Ref) {
	t.Helper()
	registry := &glRegistry{tags: map[string][]string{
		"devops/images/crowdsec": {"2.8.6", "2.8.9"},
		"devops/images/other":    {"1.0.0"},
	}}
	realm := &glRealm{wantUser: "gitlab-ci-token", wantPass: "glcbt-secret"}
	rt := harness.NewRefusingTransport(t).
		Handle("registry.ole-hartwig.eu", registry).
		Handle("git.ole-hartwig.eu", realm).
		Handle("lists.example.org", list)
	creds := func(registry string, realm *url.URL) (string, string, bool) {
		return "gitlab-ci-token", "glcbt-secret", registry == "registry.ole-hartwig.eu"
	}
	hc := httpx.New(httpx.Options{Transport: rt, Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}})
	ds := New(rt, creds).WithWithdrawals(hc, "https://lists.example.org/withdrawn-images.json")
	return ds, lookup.Ref{Datasource: Name, PackageName: "registry.ole-hartwig.eu/devops/images/crowdsec"}
}

// A withdrawn tag leaves the releases and travels as ReleaseSet.Withdrawn,
// where the run moves a dependency still on it (2026-09-29, crowdsec 2.8.6
// with CVE-2026-32286 against 2.8.9 without it). Other images keep theirs,
// and the list is read once.
func TestWithdrawnImageTagsLeaveTheReleases(t *testing.T) {
	list := &listServer{body: []byte(`{"version":1,"withdrawn":[{"image":"registry.ole-hartwig.eu/devops/images/crowdsec","version":"2.8.6","digest":"sha256:` + strings.Repeat("a", 64) + `","ids":["CVE-2026-32286"],"replacement":"2.8.9","reason":"fixable HIGH","date":"2026-09-29T00:00:00Z","source":"auto"}]}`)}
	ds, ref := withdrawnDS(t, list)
	rs, err := ds.Releases(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Releases) != 1 || rs.Releases[0].Version != "2.8.9" {
		t.Errorf("releases = %+v, want 2.8.9 alone", rs.Releases)
	}
	if len(rs.Withdrawn) != 1 || rs.Withdrawn[0].Version != "2.8.6" || rs.Withdrawn[0].Replacement != "2.8.9" || rs.Withdrawn[0].IDs[0] != "CVE-2026-32286" {
		t.Errorf("withdrawn = %+v", rs.Withdrawn)
	}
	other, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "registry.ole-hartwig.eu/devops/images/other"})
	if err != nil || len(other.Releases) != 1 || len(other.Withdrawn) != 0 {
		t.Errorf("other image = %+v, %v", other, err)
	}
	// The list speaks for every image on the registry it names, withdrawn
	// or not: both carry it as their withdrawal list.
	if rs.WithdrawalList != "https://lists.example.org/withdrawn-images.json" || other.WithdrawalList != rs.WithdrawalList {
		t.Errorf("withdrawal list: crowdsec %q, other %q", rs.WithdrawalList, other.WithdrawalList)
	}
	if list.hits != 1 {
		t.Errorf("list read %d times, want once", list.hits)
	}
}

// No list published is nothing withdrawn; an unreadable one is a warning on
// the release set and the releases come back whole.
func TestWithdrawnImagesListFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		list    *listServer
		wantErr bool
	}{
		"absent":       {&listServer{status: http.StatusNotFound}, false},
		"server error": {&listServer{status: http.StatusInternalServerError}, true},
		"garbage":      {&listServer{body: []byte("{not json")}, true},
		"version 2":    {&listServer{body: []byte(`{"version":2,"withdrawn":[]}`)}, true},
	} {
		ds, ref := withdrawnDS(t, tc.list)
		rs, err := ds.Releases(context.Background(), ref)
		if err != nil {
			t.Fatalf("%s: the lookup failed: %v", name, err)
		}
		if len(rs.Releases) != 2 || len(rs.Withdrawn) != 0 {
			t.Errorf("%s: releases %+v withdrawn %+v", name, rs.Releases, rs.Withdrawn)
		}
		if (rs.WithdrawnErr != "") != tc.wantErr {
			t.Errorf("%s: WithdrawnErr = %q", name, rs.WithdrawnErr)
		}
		if rs.WithdrawalList != "" {
			t.Errorf("%s: no readable list speaks for the image, got %q", name, rs.WithdrawalList)
		}
	}
}

// A list that names images on one registry says nothing about an image on
// another: docker.io keeps no withdrawal list here.
func TestWithdrawalListSpeaksForItsRegistryOnly(t *testing.T) {
	list := &listServer{body: []byte(`{"version":1,"withdrawn":[{"image":"registry.example.net/devops/images/x","version":"1.0.0","replacement":"1.0.1","ids":["CVE-1"],"reason":"r","date":"2026-09-29T00:00:00Z","source":"auto"}]}`)}
	ds, ref := withdrawnDS(t, list)
	rs, err := ds.Releases(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if rs.WithdrawalList != "" {
		t.Errorf("a list for registry.example.net speaks for registry.ole-hartwig.eu: %q", rs.WithdrawalList)
	}
}
