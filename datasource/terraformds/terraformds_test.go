// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package terraformds

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// registryVersion is one fixture entry on the versions endpoint - all this
// datasource reads from it is the version string itself.
type registryVersion struct{ version string }

// registry is a server that speaks the Terraform Registry Protocol: service
// discovery at a path the fixture controls (proving the client follows
// discovery rather than assuming the documented default), the versions
// endpoint for both providers and modules, the plain metadata endpoint's
// "source" field, and a 404 for an unknown package. The bodies are data; the
// protocol is code, so a client that skipped discovery or read the wrong
// field would fail here rather than in production.
type registry struct {
	mu sync.Mutex

	// providersPath and modulesPath are what discovery advertises. They
	// default to something other than the documented fallback
	// ("/v1/providers", "/v1/modules") precisely so a client that ignored
	// discovery and used the fallback anyway would 404 against this fixture.
	providersPath string
	modulesPath   string
	// discoveryStatus, when non-zero, forces the well-known document to fail
	// with that status instead of answering normally.
	discoveryStatus int
	// discoveryBody, when set, is served verbatim instead of the normal
	// {"modules.v1":...,"providers.v1":...} document - used to exercise an
	// unparsable response.
	discoveryBody string

	providerVersions map[string][]registryVersion // "namespace/name"
	moduleVersions   map[string][]registryVersion // "namespace/name/provider"
	providerSource   map[string]string            // "namespace/name" -> source
	moduleSource     map[string]string            // "namespace/name/provider" -> source

	requests []string
}

func newRegistry() *registry {
	return &registry{
		providersPath:    "/tfr/v2/providers",
		modulesPath:      "/tfr/v2/modules",
		providerVersions: map[string][]registryVersion{},
		moduleVersions:   map[string][]registryVersion{},
		providerSource:   map[string]string{},
		moduleSource:     map[string]string{},
	}
}

func (r *registry) requestCount(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, req := range r.requests {
		if strings.Contains(req, substr) {
			n++
		}
	}
	return n
}

func (r *registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.requests = append(r.requests, req.URL.Path)
	r.mu.Unlock()

	if req.URL.Path == "/.well-known/terraform.json" {
		if r.discoveryStatus != 0 {
			w.WriteHeader(r.discoveryStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.discoveryBody != "" {
			_, _ = w.Write([]byte(r.discoveryBody))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"providers.v1": r.providersPath + "/",
			"modules.v1":   r.modulesPath + "/",
		})
		return
	}

	switch {
	case strings.HasPrefix(req.URL.Path, r.providersPath+"/"):
		r.serveProvider(w, strings.TrimPrefix(req.URL.Path, r.providersPath+"/"))
	case strings.HasPrefix(req.URL.Path, r.modulesPath+"/"):
		r.serveModule(w, strings.TrimPrefix(req.URL.Path, r.modulesPath+"/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (r *registry) serveProvider(w http.ResponseWriter, rest string) {
	switch {
	case strings.HasSuffix(rest, "/versions"):
		key := strings.TrimSuffix(rest, "/versions")
		items, ok := r.providerVersions[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Versions []map[string]any `json:"versions"`
		}
		for _, it := range items {
			body.Versions = append(body.Versions, map[string]any{
				"version":   it.version,
				"protocols": []string{"5.0"},
			})
		}
		writeJSON(w, body)
	default:
		key := rest
		source, ok := r.providerSource[key]
		if !ok {
			if _, known := r.providerVersions[key]; !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		writeJSON(w, map[string]any{"source": source})
	}
}

func (r *registry) serveModule(w http.ResponseWriter, rest string) {
	switch {
	case strings.HasSuffix(rest, "/versions"):
		key := strings.TrimSuffix(rest, "/versions")
		items, ok := r.moduleVersions[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var mod struct {
			Versions []map[string]any `json:"versions"`
		}
		for _, it := range items {
			mod.Versions = append(mod.Versions, map[string]any{"version": it.version})
		}
		writeJSON(w, struct {
			Modules []any `json:"modules"`
		}{Modules: []any{mod}})
	default:
		key := rest
		source, ok := r.moduleSource[key]
		if !ok {
			if _, known := r.moduleVersions[key]; !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		writeJSON(w, map[string]any{"source": source})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// openTofuDocs is a server that speaks OpenTofu's separate docs API
// (api.opentofu.org): the one place a real publish timestamp is measured to
// live, as opposed to the registry's own "discovered" field.
type openTofuDocs struct {
	mu sync.Mutex

	// published maps a docs path ("/registry/docs/providers/ns/name/index.json"
	// or the module equivalent) to version -> RFC3339 published time.
	published map[string]map[string]string
	fail      bool

	requests []string
}

func newOpenTofuDocs() *openTofuDocs {
	return &openTofuDocs{published: map[string]map[string]string{}}
}

func (o *openTofuDocs) requestCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.requests)
}

func (o *openTofuDocs) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	o.mu.Lock()
	o.requests = append(o.requests, req.URL.Path)
	o.mu.Unlock()

	if o.fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	versions, ok := o.published[req.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var body struct {
		Versions []map[string]string `json:"versions"`
	}
	for id, published := range versions {
		body.Versions = append(body.Versions, map[string]string{"id": id, "published": published})
	}
	writeJSON(w, body)
}

// fixture wires a registry (host "registry.opentofu.org" for the default
// tests, since that is the host every measured fact above was captured
// against) and OpenTofu's docs API onto the refusing transport, plus a
// forbidden host that must never be reached.
type fixture struct {
	client *httpx.Client
	reg    *registry
	docs   *openTofuDocs
	rec    *harness.Recorder
	rt     *harness.RefusingTransport
}

func newFixture(t *testing.T, registryHost string) *fixture {
	t.Helper()
	reg := newRegistry()
	docs := newOpenTofuDocs()

	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle(registryHost, reg)
	rt.Handle("api.opentofu.org", docs)
	rt.Forbid("developer.mend.io")

	client := httpx.New(httpx.Options{
		Transport: rt,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	return &fixture{client: client, reg: reg, docs: docs, rec: rec, rt: rt}
}

func TestProviderReleasesCarryOpenTofuTimestampsAndConventionSourceURL(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	f.reg.providerVersions["cloudflare/cloudflare"] = []registryVersion{{version: "5.25.0"}, {version: "2.9.0"}}
	f.docs.published["/registry/docs/providers/cloudflare/cloudflare/index.json"] = map[string]string{
		"v5.25.0": "2026-09-11T20:13:32Z",
		"v2.9.0":  "2020-07-29T22:33:53Z", // measured: Renovate's own releaseTimestamp for this version
	}

	ds := New(Provider, f.client, "https://registry.opentofu.org")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-provider", PackageName: "cloudflare/cloudflare",
		RegistryURLs: []string{"https://registry.opentofu.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}
	f.rt.MustNotHaveBeenCalled("developer.mend.io")

	if len(rs.Releases) != 2 {
		t.Fatalf("got %d releases, want 2: %v", len(rs.Releases), rs.Releases)
	}
	if want := "https://github.com/cloudflare/terraform-provider-cloudflare"; rs.SourceURL != want {
		t.Errorf("SourceURL = %q, want %q", rs.SourceURL, want)
	}
	byVersion := map[string]time.Time{}
	for _, rel := range rs.Releases {
		byVersion[rel.Version] = rel.Timestamp
	}
	want := time.Date(2020, 7, 29, 22, 33, 53, 0, time.UTC)
	if got := byVersion["2.9.0"]; !got.Equal(want) {
		t.Errorf("2.9.0 timestamp = %s, want %s (the docs API's published field, not the registry's discovered field)", got, want)
	}
	if byVersion["5.25.0"].IsZero() {
		t.Error("5.25.0 carries no timestamp")
	}
	if rs.PackageName != "cloudflare/cloudflare" || rs.Datasource != "terraform-provider" || rs.RegistryURL != "https://registry.opentofu.org" {
		t.Errorf("ReleaseSet metadata wrong: %+v", rs)
	}

	// One versions request, one docs request - the second request is the
	// proof that timestamp enrichment actually ran, not just that it did not
	// error.
	if n := f.reg.requestCount("/versions"); n != 1 {
		t.Errorf("made %d versions requests, want 1", n)
	}
	if n := f.docs.requestCount(); n != 1 {
		t.Errorf("made %d docs requests, want 1", n)
	}
}

func TestModuleReleasesCarryOpenTofuTimestampsAndConventionSourceURL(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	f.reg.moduleVersions["terraform-aws-modules/vpc/aws"] = []registryVersion{{version: "6.7.2"}, {version: "6.7.1"}}
	f.docs.published["/registry/docs/modules/terraform-aws-modules/vpc/aws/index.json"] = map[string]string{
		"v6.7.2": "2026-08-28T16:27:38Z",
		"v6.7.1": "2026-08-27T20:33:58Z",
	}

	ds := New(Module, f.client, "https://registry.opentofu.org")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-module", PackageName: "terraform-aws-modules/vpc/aws",
		RegistryURLs: []string{"https://registry.opentofu.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}

	if len(rs.Releases) != 2 {
		t.Fatalf("got %d releases, want 2: %v", len(rs.Releases), rs.Releases)
	}
	if want := "https://github.com/terraform-aws-modules/terraform-aws-vpc"; rs.SourceURL != want {
		t.Errorf("SourceURL = %q, want %q", rs.SourceURL, want)
	}
	for _, rel := range rs.Releases {
		if rel.Timestamp.IsZero() {
			t.Errorf("%s carries no timestamp", rel.Version)
		}
	}
}

func TestNonOpenTofuRegistryUsesMetadataSourceAndZeroTimestamps(t *testing.T) {
	f := newFixture(t, "registry.terraform.io")
	f.reg.providerVersions["hashicorp/aws"] = []registryVersion{{version: "5.0.0"}, {version: "5.1.0"}}
	f.reg.providerSource["hashicorp/aws"] = "https://github.com/hashicorp/terraform-provider-aws"

	// The default registry - no RegistryURLs on the ref at all - so this
	// also exercises New's own default rather than one supplied per-call.
	ds := New(Provider, f.client, "https://registry.terraform.io")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-provider", PackageName: "hashicorp/aws",
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
	if want := "https://github.com/hashicorp/terraform-provider-aws"; rs.SourceURL != want {
		t.Errorf("SourceURL = %q, want %q", rs.SourceURL, want)
	}
	for _, rel := range rs.Releases {
		if !rel.Timestamp.IsZero() {
			t.Errorf("%s carries a timestamp; registry.terraform.io has no measured per-version timestamp", rel.Version)
		}
	}
	// The docs API is OpenTofu-only; a non-opentofu lookup must never touch it.
	if n := f.docs.requestCount(); n != 0 {
		t.Errorf("docs API received %d requests; it must receive none for a non-opentofu registry", n)
	}
}

func TestDiscoveryIsCachedPerRegistry(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	f.reg.providerVersions["a/b"] = []registryVersion{{version: "1.0.0"}}
	f.reg.providerVersions["c/d"] = []registryVersion{{version: "2.0.0"}}

	ds := New(Provider, f.client, "https://registry.opentofu.org")
	for _, pkg := range []string{"a/b", "c/d"} {
		if _, err := ds.Releases(t.Context(), lookup.Ref{
			Datasource: "terraform-provider", PackageName: pkg,
			RegistryURLs: []string{"https://registry.opentofu.org"},
		}); err != nil {
			t.Fatalf("%s: %v", pkg, err)
		}
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}
	if n := f.reg.requestCount("/.well-known/"); n != 1 {
		t.Errorf("discovery was requested %d times across two lookups against the same registry, want 1", n)
	}
	if n := f.reg.requestCount("/versions"); n != 2 {
		t.Errorf("made %d versions requests, want 2", n)
	}
}

func TestDiscoveryFailureFallsBackToDocumentedDefaultAndIsNotCached(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	// The registry serves its versions endpoint at exactly the documented
	// fallback path, not at the fixture's usual /tfr/v2/providers - so a
	// client that ignored the (failing) discovery and fell back correctly
	// still succeeds, while one that gave up on discovery failure would not.
	f.reg.providersPath = "/v1/providers"
	f.reg.discoveryStatus = http.StatusInternalServerError
	f.reg.providerVersions["a/b"] = []registryVersion{{version: "1.0.0"}}

	ds := New(Provider, f.client, "https://registry.opentofu.org")
	for i := range 2 {
		rs, err := ds.Releases(t.Context(), lookup.Ref{
			Datasource: "terraform-provider", PackageName: "a/b",
			RegistryURLs: []string{"https://registry.opentofu.org"},
		})
		if err != nil {
			t.Fatalf("call %d: a failed discovery must still fall back rather than fail the lookup: %v", i, err)
		}
		if len(rs.Releases) != 1 {
			t.Fatalf("call %d: got %d releases via the fallback path, want 1", i, len(rs.Releases))
		}
	}

	// Discovery is attempted again on the second call rather than caching
	// the failure: two lookups against a discovery that always fails must
	// still produce two well-known requests.
	if n := f.reg.requestCount("/.well-known/"); n != 2 {
		t.Errorf("well-known was requested %d times across two lookups after a failure, want 2 (a failure must not be cached)", n)
	}
}

func TestNotFoundNamesPackageAndRegistry(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	// No entry in f.reg.providerVersions for this package: 404.
	ds := New(Provider, f.client, "https://registry.opentofu.org")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-provider", PackageName: "nosuch/provider",
		RegistryURLs: []string{"https://registry.opentofu.org"},
	})
	if err == nil {
		t.Fatal("a missing package resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nosuch/provider") {
		t.Errorf("error must name the package: %q", msg)
	}
	if !strings.Contains(msg, "registry.opentofu.org") {
		t.Errorf("error must name the registry: %q", msg)
	}
}

func TestNotFoundNamesPackageAndRegistryForModules(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	ds := New(Module, f.client, "https://registry.opentofu.org")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-module", PackageName: "nosuch/module/aws",
		RegistryURLs: []string{"https://registry.opentofu.org"},
	})
	if err == nil {
		t.Fatal("a missing package resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nosuch/module/aws") || !strings.Contains(msg, "registry.opentofu.org") {
		t.Errorf("error must name both the package and the registry: %q", msg)
	}
}

func TestMalformedPackageNameErrors(t *testing.T) {
	tests := []struct {
		name    string
		kind    Kind
		pkg     string
		wantMsg string
	}{
		{name: "provider with no slash", kind: Provider, pkg: "cloudflare", wantMsg: "namespace/name"},
		{name: "provider with two slashes", kind: Provider, pkg: "a/b/c", wantMsg: "namespace/name"},
		{name: "module with one slash", kind: Module, pkg: "a/b", wantMsg: "namespace/name/provider"},
		{name: "module with a leading host segment", kind: Module, pkg: "example.com/a/b/c", wantMsg: "host segment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "registry.opentofu.org")
			ds := New(tc.kind, f.client, "https://registry.opentofu.org")
			_, err := ds.Releases(t.Context(), lookup.Ref{PackageName: tc.pkg})
			if err == nil {
				t.Fatalf("%q resolved, want an error", tc.pkg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not name the expected shape %q", err.Error(), tc.wantMsg)
			}
			// A malformed name must fail before any request is made.
			if n := len(f.reg.requests); n != 0 {
				t.Errorf("made %d requests for a name that should have failed validation first", n)
			}
		})
	}
}

func TestDocsAPIFailureStillReturnsReleases(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	f.reg.providerVersions["cloudflare/cloudflare"] = []registryVersion{{version: "5.25.0"}}
	f.docs.fail = true

	ds := New(Provider, f.client, "https://registry.opentofu.org")
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-provider", PackageName: "cloudflare/cloudflare",
		RegistryURLs: []string{"https://registry.opentofu.org"},
	})
	if err != nil {
		t.Fatalf("a failing docs API must not fail the whole lookup: %v", err)
	}
	if len(rs.Releases) != 1 {
		t.Fatalf("got %d releases, want 1", len(rs.Releases))
	}
	if !rs.Releases[0].Timestamp.IsZero() {
		t.Error("timestamp should be zero when the docs API fails")
	}
	if n := f.docs.requestCount(); n != 1 {
		t.Errorf("the docs API must still have been tried once, got %d", n)
	}
}

func TestForbiddenHostReceivesNothing(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	f.reg.providerVersions["a/b"] = []registryVersion{{version: "1.0.0"}}

	ds := New(Provider, f.client, "https://registry.opentofu.org")
	if _, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-provider", PackageName: "a/b",
		RegistryURLs: []string{"https://registry.opentofu.org"},
	}); err != nil {
		t.Fatal(err)
	}
	f.rt.MustNotHaveBeenCalled("developer.mend.io")
	if f.rt.Count("developer.mend.io") != 0 {
		t.Fatal("forbidden host count must be exactly zero")
	}
}

func TestAnUnregisteredHostIsRefused(t *testing.T) {
	f := newFixture(t, "registry.opentofu.org")
	ds := New(Provider, f.client, "https://registry.opentofu.org")
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "terraform-provider", PackageName: "a/b",
		RegistryURLs: []string{"https://other.example"},
	})
	if err == nil {
		t.Error("a lookup against an unregistered host succeeded")
	}
	if !f.rec.Failed() || !f.rec.Mentions("other.example") {
		t.Errorf("the refusing transport did not name the escaping host: %s", f.rec.String())
	}
}
