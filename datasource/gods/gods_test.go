// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gods

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// proxy is a server that speaks the Go module proxy protocol closely enough
// to exercise this package: fixed responses per exact request path. The
// bodies are data; the protocol (which path gets hit, in what order) is what
// the assertions below check.
type proxy struct {
	mu sync.Mutex

	lists  map[string]string // request path -> plain-text "@v/list" body
	infos  map[string]string // request path -> JSON "@v/{version}.info" body
	status map[string]int    // request path -> forced status code

	requests []string
}

func newProxy() *proxy {
	return &proxy{
		lists:  map[string]string{},
		infos:  map[string]string{},
		status: map[string]int{},
	}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path
	p.mu.Lock()
	p.requests = append(p.requests, path)
	p.mu.Unlock()

	if code, ok := p.status[path]; ok {
		w.WriteHeader(code)
		if code == http.StatusNotFound {
			_, _ = w.Write([]byte("not found: module lookup disabled by GONOSUMCHECK"))
		}
		return
	}
	if body, ok := p.lists[path]; ok {
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		_, _ = w.Write([]byte(body))
		return
	}
	if body, ok := p.infos[path]; ok {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
		return
	}
	// Every unregistered path - including a probed major that does not exist
	// - answers the way the real proxy does for an unknown module.
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("not found: module lookup disabled by GONOSUMCHECK"))
}

func (p *proxy) requestCount(substr string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.requests {
		if strings.Contains(r, substr) {
			n++
		}
	}
	return n
}

func (p *proxy) requestedPaths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

// goDev is a server that speaks go.dev's release JSON endpoint.
type goDev struct {
	mu sync.Mutex

	body   string
	status int

	requests []string
}

func (g *goDev) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	g.mu.Lock()
	g.requests = append(g.requests, req.URL.String())
	g.mu.Unlock()

	if g.status != 0 {
		w.WriteHeader(g.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(g.body))
}

func (g *goDev) requestCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

// fixture wires the proxy and go.dev fakes onto the refusing transport, plus
// a forbidden host that must never be reached.
type fixture struct {
	client *httpx.Client
	proxy  *proxy
	godev  *goDev
	rec    *harness.Recorder
	rt     *harness.RefusingTransport
}

const forbiddenHost = "attacker.example"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	p := newProxy()
	g := &goDev{}

	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("proxy.example", p)
	rt.Handle("go.example", g)
	rt.Forbid(forbiddenHost)

	client := httpx.New(httpx.Options{
		Transport: rt,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	return &fixture{client: client, proxy: p, godev: g, rec: rec, rt: rt}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture timestamp %q: %v", s, err)
	}
	return tm
}

func TestNameAndDefaultVersioning(t *testing.T) {
	f := newFixture(t)
	mod := New(Module, f.client, "https://proxy.example")
	tc := New(Toolchain, f.client, "https://go.example")

	if got := mod.Name(); got != "go" {
		t.Errorf("Module.Name() = %q, want %q", got, "go")
	}
	if got := tc.Name(); got != "golang-version" {
		t.Errorf("Toolchain.Name() = %q, want %q", got, "golang-version")
	}
	// Measured: Renovate's own lookup log shows "versioning: semver" for
	// both a `require` line and the `toolchain` line.
	if got := mod.DefaultVersioning(); got != "semver" {
		t.Errorf("Module.DefaultVersioning() = %q, want semver", got)
	}
	if got := tc.DefaultVersioning(); got != "semver" {
		t.Errorf("Toolchain.DefaultVersioning() = %q, want semver", got)
	}
}

func TestEncodeModulePathCasing(t *testing.T) {
	tests := []struct{ in, want string }{
		{"github.com/BurntSushi/toml", "github.com/!burnt!sushi/toml"},
		{"github.com/spf13/cobra", "github.com/spf13/cobra"},
		{"gopkg.in/yaml.v3", "gopkg.in/yaml.v3"},
		{"rsc.io/Quote", "rsc.io/!quote"},
	}
	for _, tc := range tests {
		if got := encodeModulePath(tc.in); got != tc.want {
			t.Errorf("encodeModulePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMixedCaseModuleReachesTheEncodedPathOnTheWire proves the case encoding
// actually reaches the request, not just the unit function: the fixture only
// answers the encoded path, so a client that sent the literal mixed-case path
// would 404.
func TestMixedCaseModuleReachesTheEncodedPathOnTheWire(t *testing.T) {
	f := newFixture(t)
	f.proxy.lists["/github.com/!burnt!sushi/toml/@v/list"] = "v0.9.0\nv1.0.0\nv1.1.0\n"
	f.proxy.infos["/github.com/!burnt!sushi/toml/@v/v1.1.0.info"] = `{"Version":"v1.1.0","Time":"2025-01-10T00:00:00Z"}`
	f.proxy.infos["/github.com/!burnt!sushi/toml/@v/v0.9.0.info"] = `{"Version":"v0.9.0","Time":"2016-01-01T00:00:00Z"}`
	// v2 does not exist for this module: the probe must stop at the first 404.
	f.proxy.status["/github.com/!burnt!sushi/toml/v2/@v/list"] = http.StatusNotFound

	ds := New(Module, f.client, "https://proxy.example")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "github.com/BurntSushi/toml",
		RegistryURLs: []string{"https://proxy.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}
	f.rt.MustNotHaveBeenCalled(forbiddenHost)

	if len(rs.Releases) != 3 {
		t.Fatalf("got %d releases, want 3: %v", len(rs.Releases), rs.Releases)
	}
	if want := "https://github.com/BurntSushi/toml"; rs.SourceURL != want {
		t.Errorf("SourceURL = %q, want %q", rs.SourceURL, want)
	}
	if rs.PackageName != "github.com/BurntSushi/toml" || rs.Datasource != "go" || rs.RegistryURL != "https://proxy.example" {
		t.Errorf("ReleaseSet metadata wrong: %+v", rs)
	}

	byVersion := map[string]time.Time{}
	for _, r := range rs.Releases {
		byVersion[r.Version] = r.Timestamp
	}
	if got, want := byVersion["v1.1.0"], mustTime(t, "2025-01-10T00:00:00Z"); !got.Equal(want) {
		t.Errorf("v1.1.0 timestamp = %s, want %s", got, want)
	}
	if !byVersion["v1.0.0"].IsZero() {
		t.Error("v1.0.0 (not the newest of its major) carries a timestamp; only the newest per major should")
	}
	// v0.9.0 is the only version of major 0, so it is that major's newest
	// and does get a timestamp.
	if got, want := byVersion["v0.9.0"], mustTime(t, "2016-01-01T00:00:00Z"); !got.Equal(want) {
		t.Errorf("v0.9.0 timestamp = %s, want %s", got, want)
	}

	// One .info request per distinct major (0 and 1), never for v1.0.0.
	if n := f.proxy.requestCount("/@v/v"); n != 2 {
		t.Errorf(".info requests = %d, want 2 (one per major)", n)
	}
	if n := f.proxy.requestCount("v1.0.0.info"); n != 0 {
		t.Errorf("v1.0.0.info was requested %d times, want 0", n)
	}
}

// TestMajorVersionProbingMergesEveryMajorAndStopsAtFirst404 is the measured
// scenario: after listing a module with no major suffix, probe /v2, /v3, ...
// until a 404, merging every listed version into one release set, and fetch
// ".info" only for the newest version of each distinct major.
func TestMajorVersionProbingMergesEveryMajorAndStopsAtFirst404(t *testing.T) {
	f := newFixture(t)
	f.proxy.lists["/github.com/spf13/cobra/@v/list"] = "v0.0.1\nv1.9.0\nv1.9.1\n"
	f.proxy.lists["/github.com/spf13/cobra/v2/@v/list"] = "v2.0.0\nv2.1.0\n"
	f.proxy.status["/github.com/spf13/cobra/v3/@v/list"] = http.StatusNotFound

	f.proxy.infos["/github.com/spf13/cobra/@v/v0.0.1.info"] = `{"Version":"v0.0.1","Time":"2018-01-01T00:00:00Z"}`
	f.proxy.infos["/github.com/spf13/cobra/@v/v1.9.1.info"] = `{"Version":"v1.9.1","Time":"2025-02-16T23:42:04Z"}`
	f.proxy.infos["/github.com/spf13/cobra/v2/@v/v2.1.0.info"] = `{"Version":"v2.1.0","Time":"2026-06-01T00:00:00Z"}`
	// Present but must never be fetched: not the newest of its major.
	f.proxy.infos["/github.com/spf13/cobra/@v/v1.9.0.info"] = `{"Version":"v1.9.0","Time":"2024-01-01T00:00:00Z"}`
	f.proxy.infos["/github.com/spf13/cobra/v2/@v/v2.0.0.info"] = `{"Version":"v2.0.0","Time":"2026-01-01T00:00:00Z"}`

	ds := New(Module, f.client, "https://proxy.example")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "github.com/spf13/cobra",
		RegistryURLs: []string{"https://proxy.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}

	if len(rs.Releases) != 5 {
		t.Fatalf("got %d releases across three majors, want 5: %v", len(rs.Releases), rs.Releases)
	}
	if want := "https://github.com/spf13/cobra"; rs.SourceURL != want {
		t.Errorf("SourceURL = %q, want %q", rs.SourceURL, want)
	}

	byVersion := map[string]time.Time{}
	for _, r := range rs.Releases {
		byVersion[r.Version] = r.Timestamp
	}
	for _, v := range []string{"v0.0.1", "v1.9.1", "v2.1.0"} {
		if byVersion[v].IsZero() {
			t.Errorf("%s (newest of its major) carries no timestamp", v)
		}
	}
	for _, v := range []string{"v1.9.0", "v2.0.0"} {
		if !byVersion[v].IsZero() {
			t.Errorf("%s (not the newest of its major) carries a timestamp", v)
		}
	}

	// Exactly one request per major's newest version, never the non-newest
	// ones, even though the fixture would have answered them.
	if n := f.proxy.requestCount("/@v/v0.0.1.info"); n != 1 {
		t.Errorf("v0.0.1.info requested %d times, want 1", n)
	}
	if n := f.proxy.requestCount("/@v/v1.9.1.info"); n != 1 {
		t.Errorf("v1.9.1.info requested %d times, want 1", n)
	}
	if n := f.proxy.requestCount("v2/@v/v2.1.0.info"); n != 1 {
		t.Errorf("v2.1.0.info requested %d times, want 1", n)
	}
	if n := f.proxy.requestCount("v1.9.0.info"); n != 0 {
		t.Errorf("v1.9.0.info was requested %d times, want 0", n)
	}
	if n := f.proxy.requestCount("v2.0.0.info"); n != 0 {
		t.Errorf("v2.0.0.info was requested %d times, want 0", n)
	}

	// The probe must stop at the first 404: no /v4 request at all.
	if n := f.proxy.requestCount("/v4/"); n != 0 {
		t.Errorf("probing continued past the first 404: %v", f.proxy.requestedPaths())
	}
	if n := f.proxy.requestCount("/v3/"); n != 1 {
		t.Errorf("the failing probe itself was requested %d times, want 1", n)
	}
}

// TestGopkgInMajorSuffixProbesTheDotVForm is the measured gopkg.in case:
// probing a module already at ".v3" tries ".v4", not "/v4".
func TestGopkgInMajorSuffixProbesTheDotVForm(t *testing.T) {
	f := newFixture(t)
	f.proxy.lists["/gopkg.in/yaml.v3/@v/list"] = "v3.0.0\nv3.0.1\n"
	f.proxy.infos["/gopkg.in/yaml.v3/@v/v3.0.1.info"] = `{"Version":"v3.0.1","Time":"2023-05-01T00:00:00Z"}`
	// gopkg.in/yaml.v4 does not exist: default 404 handles it, but this test
	// also asserts the exact path was hit.
	f.proxy.status["/gopkg.in/yaml.v4/@v/list"] = http.StatusNotFound

	ds := New(Module, f.client, "https://proxy.example")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "gopkg.in/yaml.v3",
		RegistryURLs: []string{"https://proxy.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}

	if len(rs.Releases) != 2 {
		t.Fatalf("got %d releases, want 2", len(rs.Releases))
	}
	if rs.SourceURL != "" {
		t.Errorf("SourceURL = %q, want empty (gopkg.in is not one of the measured hosts)", rs.SourceURL)
	}
	if n := f.proxy.requestCount("/gopkg.in/yaml.v4/@v/list"); n != 1 {
		t.Errorf("the gopkg.in probe must try the .vN form (yaml.v4), got requests: %v", f.proxy.requestedPaths())
	}
	if n := f.proxy.requestCount("yaml.v5"); n != 0 {
		t.Errorf("probing continued past the first 404: %v", f.proxy.requestedPaths())
	}
}

// TestNotFoundNamesModuleAndProxy is the required 404 shape: an error naming
// the module and the proxy, without depending on the body's "not found:"
// prefix.
func TestNotFoundNamesModuleAndProxy(t *testing.T) {
	f := newFixture(t)
	// No entry at all for this module: the ServeHTTP default answers 404.
	ds := New(Module, f.client, "https://proxy.example")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "example.com/nosuch/module",
		RegistryURLs: []string{"https://proxy.example"},
	})
	if err == nil {
		t.Fatal("a missing module resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "example.com/nosuch/module") {
		t.Errorf("error must name the module: %q", msg)
	}
	if !strings.Contains(msg, "https://proxy.example") {
		t.Errorf("error must name the proxy: %q", msg)
	}
}

// TestDatasourceErrorSurfacesRatherThanEmptySet is the required 500 case: a
// datasource failure must be an error, never a silent empty ReleaseSet.
func TestDatasourceErrorSurfacesRatherThanEmptySet(t *testing.T) {
	f := newFixture(t)
	f.proxy.status["/broken/module/@v/list"] = http.StatusInternalServerError

	ds := New(Module, f.client, "https://proxy.example")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "broken/module",
		RegistryURLs: []string{"https://proxy.example"},
	})
	if err == nil {
		t.Fatalf("a 500 from the proxy resolved with %d releases, want an error", len(rs.Releases))
	}
	if !strings.Contains(err.Error(), "broken/module") {
		t.Errorf("error must name the module: %q", err.Error())
	}
}

// TestCommaSeparatedProxyListIsRefused covers the unsupported GOPROXY shape:
// the check must fire before any request is made.
func TestCommaSeparatedProxyListIsRefused(t *testing.T) {
	f := newFixture(t)
	ds := New(Module, f.client, "https://proxy.example")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "github.com/spf13/cobra",
		RegistryURLs: []string{"https://proxy.example,https://goproxy.cn"},
	})
	if err == nil {
		t.Fatal("a comma-separated GOPROXY-style list resolved, want an error")
	}
	if !strings.Contains(err.Error(), ",") {
		t.Errorf("error should name the offending shape: %q", err.Error())
	}
	if len(f.proxy.requestedPaths()) != 0 {
		t.Errorf("a rejected GOPROXY list must not cause any request: %v", f.proxy.requestedPaths())
	}
}

func TestUnregisteredHostIsRefused(t *testing.T) {
	f := newFixture(t)
	ds := New(Module, f.client, "https://proxy.example")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "github.com/spf13/cobra",
		RegistryURLs: []string{"https://other.example"},
	})
	if err == nil {
		t.Error("a lookup against an unregistered host succeeded")
	}
	if !f.rec.Failed() || !f.rec.Mentions("other.example") {
		t.Errorf("the refusing transport did not name the escaping host: %s", f.rec.String())
	}
}

func TestForbiddenHostReceivesNothing(t *testing.T) {
	f := newFixture(t)
	f.proxy.lists["/a/b/@v/list"] = "v1.0.0\n"
	f.proxy.status["/a/b/v2/@v/list"] = http.StatusNotFound

	ds := New(Module, f.client, "https://proxy.example")
	if _, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "a/b",
		RegistryURLs: []string{"https://proxy.example"},
	}); err != nil {
		t.Fatal(err)
	}
	f.rt.MustNotHaveBeenCalled(forbiddenHost)
	if f.rt.Count(forbiddenHost) != 0 {
		t.Fatal("forbidden host count must be exactly zero")
	}
}

func TestDefaultRegistryIsProxyGolangOrgWhenUnset(t *testing.T) {
	ds := New(Module, httpx.New(httpx.Options{
		Transport: harness.NewRefusingTransport(&harness.Recorder{}),
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
	}), "")
	got := ds.registryFor(lookup.Ref{PackageName: "example.com/x"})
	if got != "https://proxy.golang.org" {
		t.Errorf("default registry = %q, want https://proxy.golang.org", got)
	}
}

// --- golang-version ---

const goDevPayload = `[
 {"version":"go1.27.1","stable":true,"files":[{"filename":"go1.27.1.src.tar.gz","kind":"source","sha256":"deadbeef"}]},
 {"version":"go1.27.0","stable":true,"files":[]},
 {"version":"go1.27rc3","stable":false,"files":[{"filename":"go1.27rc3.linux-amd64.tar.gz","kind":"archive"}]},
 {"version":"go1.26.8","stable":true,"files":[]}
]`

func TestToolchainReleasesStripGoPrefixAndKeepUnstable(t *testing.T) {
	f := newFixture(t)
	f.godev.body = goDevPayload

	ds := New(Toolchain, f.client, "https://go.example")
	rs, err := ds.Releases(t.Context(), lookup.Ref{Datasource: "golang-version", PackageName: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}
	f.rt.MustNotHaveBeenCalled(forbiddenHost)

	want := []string{"1.27.1", "1.27.0", "1.27rc3", "1.26.8"}
	if len(rs.Releases) != len(want) {
		t.Fatalf("got %d releases, want %d: %v", len(rs.Releases), len(want), rs.Releases)
	}
	for i, w := range want {
		if rs.Releases[i].Version != w {
			t.Errorf("release[%d] = %q, want %q", i, rs.Releases[i].Version, w)
		}
	}
	if rs.PackageName != "go" || rs.Datasource != "golang-version" || rs.RegistryURL != "https://go.example" {
		t.Errorf("ReleaseSet metadata wrong: %+v", rs)
	}
	for _, r := range rs.Releases {
		if !r.Timestamp.IsZero() {
			t.Errorf("%s carries a timestamp; go.dev's JSON has none", r.Version)
		}
		if r.SourceURL != "" {
			t.Errorf("%s carries a SourceURL; unmeasured for golang-version", r.Version)
		}
	}
	if n := f.godev.requestCount(); n != 1 {
		t.Errorf("go.dev requested %d times, want 1", n)
	}
}

func TestToolchainRejectsUnexpectedPackageName(t *testing.T) {
	f := newFixture(t)
	f.godev.body = goDevPayload

	ds := New(Toolchain, f.client, "https://go.example")
	_, err := ds.Releases(t.Context(), lookup.Ref{Datasource: "golang-version", PackageName: "not-go"})
	if err == nil {
		t.Fatal("an unexpected package name resolved, want an error")
	}
	if n := f.godev.requestCount(); n != 0 {
		t.Errorf("go.dev received %d requests for a rejected package name, want 0", n)
	}
}

func TestToolchainErrorSurfacesRatherThanEmptySet(t *testing.T) {
	f := newFixture(t)
	f.godev.status = http.StatusInternalServerError

	ds := New(Toolchain, f.client, "https://go.example")
	rs, err := ds.Releases(t.Context(), lookup.Ref{Datasource: "golang-version", PackageName: "go"})
	if err == nil {
		t.Fatalf("a 500 from go.dev resolved with %d releases, want an error", len(rs.Releases))
	}
}

func TestDefaultRegistryIsGoDevWhenUnset(t *testing.T) {
	ds := New(Toolchain, httpx.New(httpx.Options{
		Transport: harness.NewRefusingTransport(&harness.Recorder{}),
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
	}), "")
	got := ds.registryFor(lookup.Ref{PackageName: "go"})
	if got != "https://go.dev" {
		t.Errorf("default registry = %q, want https://go.dev", got)
	}
}

// TestAnUntaggedModuleFallsBackToLatest: an empty "@v/list" is not "no
// releases" - the proxy's "@latest" names the pseudo-version of the newest
// commit, and that pseudo-version's commit is the release's digest.
func TestAnUntaggedModuleFallsBackToLatest(t *testing.T) {
	f := newFixture(t)
	f.proxy.lists["/golang.org/x/mobile/@v/list"] = ""
	f.proxy.infos["/golang.org/x/mobile/@latest"] = `{"Version":"v0.0.0-20260908204917-8b95e45f8d3e","Time":"2026-09-08T20:49:17Z"}`
	f.proxy.status["/golang.org/x/mobile/v2/@v/list"] = http.StatusNotFound

	ds := New(Module, f.client, "https://proxy.example")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "go", PackageName: "golang.org/x/mobile",
		RegistryURLs: []string{"https://proxy.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Releases) != 1 {
		t.Fatalf("releases = %+v, want the one @latest names", rs.Releases)
	}
	r := rs.Releases[0]
	if r.Version != "v0.0.0-20260908204917-8b95e45f8d3e" || r.Digest != "8b95e45f8d3e" || r.Timestamp.IsZero() {
		t.Errorf("release = %+v, want the pseudo-version with its commit and time", r)
	}
	if n := f.proxy.requestCount("/@latest"); n != 1 {
		t.Errorf("@latest was requested %d times, want once", n)
	}
}

// A module that lists versions never asks "@latest": the list is the answer.
func TestATaggedModuleDoesNotAskLatest(t *testing.T) {
	f := newFixture(t)
	f.proxy.lists["/a/b/@v/list"] = "v1.0.0\n"
	f.proxy.status["/a/b/v2/@v/list"] = http.StatusNotFound
	ds := New(Module, f.client, "https://proxy.example")
	if _, err := ds.Releases(t.Context(), lookup.Ref{Datasource: "go", PackageName: "a/b", RegistryURLs: []string{"https://proxy.example"}}); err != nil {
		t.Fatal(err)
	}
	if n := f.proxy.requestCount("/@latest"); n != 0 {
		t.Errorf("@latest was requested %d times, want never", n)
	}
}

// instanceTags is a gitlab-tags datasource of the shape gods probes: a
// project path answers with its tags, anything else with the wrapped 404
// gitlabds returns, and one path may be made to fail outright.
type instanceTags struct {
	projects map[string][]string
	broken   string
	asked    []string
}

func (f *instanceTags) Name() string              { return "gitlab-tags" }
func (f *instanceTags) DefaultVersioning() string { return "semver" }
func (f *instanceTags) Releases(_ context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	f.asked = append(f.asked, ref.PackageName)
	if ref.PackageName == f.broken {
		return nil, fmt.Errorf("gitlab-tags: %s: %w", ref.PackageName, &httpx.StatusError{StatusCode: http.StatusBadGateway})
	}
	tags, ok := f.projects[ref.PackageName]
	if !ok {
		return nil, fmt.Errorf("gitlab-tags: 404 for %s - the project does not exist or the token cannot read it: %w",
			ref.PackageName, &httpx.StatusError{StatusCode: http.StatusNotFound})
	}
	rs := &model.ReleaseSet{PackageName: ref.PackageName, Datasource: "gitlab-tags",
		RegistryURL: ref.RegistryURLs[0], SourceURL: ref.RegistryURLs[0] + "/" + ref.PackageName}
	for _, tag := range tags {
		rs.Releases = append(rs.Releases, model.Release{Version: tag, Digest: "sha-" + tag})
	}
	return rs, nil
}

// TestModuleOnTheInstanceIsServedFromTheProjectsTags: a module path under
// the platform's host is a repository there. The project is the longest
// prefix that answers, the rest is the module's directory, and only the
// tags Go gives that directory count - stripped, filtered to the major the
// path names. The proxy is never asked (it would refuse).
func TestModuleOnTheInstanceIsServedFromTheProjectsTags(t *testing.T) {
	tags := &instanceTags{projects: map[string][]string{
		"development/s3mail/s3mail": {"v1.4.3", "v1.5.0", "go/v1.5.0", "go/v1.5.1", "go/v2.0.0", "go/not-a-version", "cli/v1.5.0"},
		"devops/tool":               {"v0.9.0", "v1.0.0", "v2.0.0", "v2.1.0", "v3.0.0"},
	}}
	f := newFixture(t)
	f.rt.Forbid("proxy.example")
	ds := New(Module, f.client, "https://proxy.example").WithInstance("https://git.example", tags)

	for _, tc := range []struct {
		module   string
		versions []string
		asked    []string
	}{
		{"git.example/development/s3mail/s3mail/go", []string{"v1.5.0", "v1.5.1"},
			[]string{"development/s3mail/s3mail/go", "development/s3mail/s3mail"}},
		{"git.example/development/s3mail/s3mail/go/v2", []string{"v2.0.0"},
			[]string{"development/s3mail/s3mail/go", "development/s3mail/s3mail"}},
		{"git.example/development/s3mail/s3mail", []string{"v1.4.3", "v1.5.0"},
			[]string{"development/s3mail/s3mail"}},
		{"git.example/devops/tool", []string{"v0.9.0", "v1.0.0"}, []string{"devops/tool"}},
		{"git.example/devops/tool/v2", []string{"v2.0.0", "v2.1.0"}, []string{"devops/tool"}},
	} {
		tags.asked = nil
		rs, err := ds.Releases(t.Context(), lookup.Ref{Datasource: "go", PackageName: tc.module})
		if err != nil {
			t.Fatalf("%s: %v", tc.module, err)
		}
		var got []string
		for _, r := range rs.Releases {
			got = append(got, r.Version)
			if r.Digest == "" {
				t.Errorf("%s: %s lost the tag's commit", tc.module, r.Version)
			}
		}
		if strings.Join(got, " ") != strings.Join(tc.versions, " ") {
			t.Errorf("%s: versions %v, want %v", tc.module, got, tc.versions)
		}
		if strings.Join(tags.asked, " ") != strings.Join(tc.asked, " ") {
			t.Errorf("%s: probed %v, want %v", tc.module, tags.asked, tc.asked)
		}
		if rs.PackageName != tc.module || rs.Datasource != "go" || rs.RegistryURL != "https://git.example" {
			t.Errorf("%s: set names %q %q %q", tc.module, rs.PackageName, rs.Datasource, rs.RegistryURL)
		}
		if rs.SourceURL != "https://git.example/"+strings.Join(strings.Split(strings.TrimPrefix(tc.module, "git.example/"), "/")[:2], "/") &&
			!strings.HasPrefix(rs.SourceURL, "https://git.example/development/s3mail/s3mail") {
			t.Errorf("%s: source %q", tc.module, rs.SourceURL)
		}
	}
	f.rt.MustNotHaveBeenCalled("proxy.example")
}

// TestModuleOnTheInstanceNobodyAnswersForIsAnError: a path no project
// answers for is an error naming the path and the instance, not an empty
// set; and an outage while probing is that outage, not a 404 walked past.
func TestModuleOnTheInstanceNobodyAnswersForIsAnError(t *testing.T) {
	f := newFixture(t)
	tags := &instanceTags{projects: map[string][]string{}, broken: "devops/down"}
	ds := New(Module, f.client, "https://proxy.example").WithInstance("https://git.example", tags)

	_, err := ds.Releases(t.Context(), lookup.Ref{Datasource: "go", PackageName: "git.example/devops/nosuch/go"})
	if err == nil || !strings.Contains(err.Error(), "git.example/devops/nosuch/go") || !strings.Contains(err.Error(), "git.example") {
		t.Errorf("no project: %v", err)
	}
	if strings.Join(tags.asked, " ") != "devops/nosuch/go devops/nosuch" {
		t.Errorf("probed %v; a single segment is never a project", tags.asked)
	}

	_, err = ds.Releases(t.Context(), lookup.Ref{Datasource: "go", PackageName: "git.example/devops/down/go"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("an outage while probing must surface: %v", err)
	}

	// Another host is the proxy's business, untouched by the instance.
	f.proxy.lists["/example.com/lib/@v/list"] = "v1.0.0\n"
	rs, err := ds.Releases(t.Context(), lookup.Ref{Datasource: "go", PackageName: "example.com/lib", RegistryURLs: []string{"https://proxy.example"}})
	if err != nil || len(rs.Releases) != 1 || len(tags.asked) != 4 {
		t.Errorf("a proxied module: %v %v asked=%v", rs, err, tags.asked)
	}
}
