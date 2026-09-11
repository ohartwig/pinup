// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package packagist

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/fake/harness"
	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
)

const (
	packagistHost = "packagist.fake"
	gitlabHost    = "gitlab.fake"
	privateToken  = "s3cr3t-group-token"
)

// packagistFake speaks the Composer 2 protocol: a packages.json naming a
// metadata-url template, and one p2 document per package (or per
// "pkg~dev" variant) it knows about. Registering nothing for a path is what
// makes it 404, exactly like the real registry answering "no such package".
type packagistFake struct {
	mu       sync.Mutex
	docs     map[string]p2Doc // key: package variant, e.g. "vendor1/pkg" or "vendor1/pkg~dev"
	requests []string
}

// p2Doc is one metadata-url document's fixture: a list of entry fragments
// (already shaped as they would be on the wire - full or partial) plus
// whether the document as a whole is minified.
type p2Doc struct {
	minified bool
	entries  []map[string]any
}

func (f *packagistFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.RequestURI())
	f.mu.Unlock()

	switch {
	case r.URL.Path == "/packages.json":
		writeJSON(w, map[string]any{"metadata-url": "/p2/%package%.json"})

	case strings.HasPrefix(r.URL.Path, "/p2/") && strings.HasSuffix(r.URL.Path, ".json"):
		variant := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/p2/"), ".json")
		doc, ok := f.docs[variant]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := map[string]any{"packages": map[string]any{variant: doc.entries}}
		if doc.minified {
			body["minified"] = minifiedComposer2
		}
		writeJSON(w, body)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *packagistFake) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// gitlabFake speaks the "small repository" inline packages.json format,
// gated by the PRIVATE-TOKEN header the estate's group composer registry
// requires. There is no per-package endpoint: everything the registry
// knows is in the one document, so a package it does not carry is simply
// absent from the map rather than a distinct 404.
type gitlabFake struct {
	mu       sync.Mutex
	token    string
	packages map[string]map[string]map[string]any // pkg -> version -> fields
	requests []string
}

func (f *gitlabFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.RequestURI())
	f.mu.Unlock()

	if r.Header.Get("PRIVATE-TOKEN") != f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/packages.json" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"packages": f.packages})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// newFixture wires both fakes behind a RefusingTransport, with a HostRule
// that sends the correct PRIVATE-TOKEN to the GitLab fake, and forbids
// developer.mend.io so a client that reached out to it (a leftover from
// some other datasource's dependency graph) would be caught here too.
func newFixture(t *testing.T) (*httpx.Client, *packagistFake, *gitlabFake, *harness.Recorder, *harness.RefusingTransport) {
	t.Helper()
	pf := &packagistFake{docs: map[string]p2Doc{}}
	gf := &gitlabFake{token: privateToken, packages: map[string]map[string]map[string]any{}}

	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle(packagistHost, pf)
	rt.Handle(gitlabHost, gf)
	rt.Forbid("developer.mend.io")

	client := httpx.New(httpx.Options{
		Transport: rt,
		HostRules: []httpx.HostRule{
			{MatchHost: gitlabHost, Token: privateToken, HeaderName: "PRIVATE-TOKEN"},
		},
		Now:   func() time.Time { return time.Unix(0, 0) },
		Sleep: func(time.Duration) {},
	})
	return client, pf, gf, rec, rt
}

func TestMinifiedEntriesAreExpanded(t *testing.T) {
	client, pf, _, rec, rt := newFixture(t)
	pf.docs["vendor1/minipkg"] = p2Doc{
		minified: true,
		entries: []map[string]any{
			{
				"version": "1.0.0", "version_normalized": "1.0.0.0",
				"time":   "2024-01-01T00:00:00Z",
				"source": map[string]any{"type": "git", "url": "https://example.test/minipkg.git", "reference": "aaa"},
			},
			// Only version changes; time and source must be inherited from
			// the previous, already-expanded entry.
			{"version": "1.1.0", "time": "2024-02-01T00:00:00Z"},
			// Only version changes again; both time and source must be
			// inherited from entry 2 (itself carrying entry 1's source).
			{"version": "1.2.0"},
		},
	}
	// "vendor1/minipkg~dev" is left unregistered: the fake 404s it, exactly
	// like a package that has no dev branches on the real registry.

	ds := New(client)
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/minipkg",
		RegistryURLs: []string{"https://" + packagistHost},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	wantVersions := []string{"1.0.0", "1.1.0", "1.2.0"}
	if len(rs.Releases) != len(wantVersions) {
		t.Fatalf("got %d releases, want %d: %+v", len(rs.Releases), len(wantVersions), rs.Releases)
	}
	for i, v := range wantVersions {
		if rs.Releases[i].Version != v {
			t.Errorf("release %d version = %q, want %q", i, rs.Releases[i].Version, v)
		}
	}

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	wantTimes := []time.Time{t1, t2, t2} // entry 3 inherits entry 2's time
	for i, want := range wantTimes {
		if !rs.Releases[i].Timestamp.Equal(want) {
			t.Errorf("release %d (%s) timestamp = %v, want %v (minified inheritance)",
				i, rs.Releases[i].Version, rs.Releases[i].Timestamp, want)
		}
	}

	if rs.SourceURL != "https://example.test/minipkg.git" {
		t.Errorf("SourceURL = %q, want the inherited source (entries 2 and 3 never carry their own)", rs.SourceURL)
	}
	// packages.json + the minified p2 document + the (404) ~dev document.
	if n := rt.Count(packagistHost); n != 3 {
		t.Errorf("packagist requests = %d, want 3", n)
	}
}

func TestFirstPartyPackageFoundOnGitLabSkipsPackagist(t *testing.T) {
	client, pf, gf, rec, rt := newFixture(t)
	gf.packages["vendor1/firstparty"] = map[string]map[string]any{
		"2.0.0": {"time": "2024-03-01T00:00:00Z", "source": map[string]any{"url": "https://gitlab.fake/vendor1/firstparty.git"}},
	}

	ds := New(client)
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/firstparty",
		RegistryURLs: []string{"https://" + gitlabHost, "https://" + packagistHost},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	if len(rs.Releases) != 1 || rs.Releases[0].Version != "2.0.0" {
		t.Fatalf("releases = %+v, want a single 2.0.0", rs.Releases)
	}
	if rs.RegistryURL != "https://"+gitlabHost {
		t.Errorf("RegistryURL = %q, want the GitLab registry that answered", rs.RegistryURL)
	}
	if n := rt.Count(gitlabHost); n != 1 {
		t.Errorf("gitlab requests = %d, want 1", n)
	}
	if n := rt.Count(packagistHost); n != 0 {
		t.Errorf("packagist requests = %d, want 0 - a first-party hit must never fall through", n)
	}
	if n := pf.requestCount(); n != 0 {
		t.Errorf("packagist fake observed %d requests, want 0", n)
	}
}

func TestPublicPackageFallsThroughToPackagistAfterGitLabMiss(t *testing.T) {
	client, pf, _, rec, rt := newFixture(t)
	pf.docs["vendor1/public"] = p2Doc{
		entries: []map[string]any{
			{"version": "3.0.0", "time": "2024-04-01T00:00:00Z", "source": map[string]any{"url": "https://packagist.fake/vendor1/public.git"}},
		},
	}
	// gf carries nothing for "vendor1/public": absence from its inline
	// packages map is how the "small repository" format says "not mine".

	ds := New(client)
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/public",
		RegistryURLs: []string{"https://" + gitlabHost, "https://" + packagistHost},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	if len(rs.Releases) != 1 || rs.Releases[0].Version != "3.0.0" {
		t.Fatalf("releases = %+v, want a single 3.0.0", rs.Releases)
	}
	if rs.RegistryURL != "https://"+packagistHost {
		t.Errorf("RegistryURL = %q, want packagist (the one that actually answered)", rs.RegistryURL)
	}
	if n := rt.Count(gitlabHost); n != 1 {
		t.Errorf("gitlab requests = %d, want 1 (the miss)", n)
	}
	// packages.json + the stable p2 document + the (404) ~dev document.
	if n := rt.Count(packagistHost); n != 3 {
		t.Errorf("packagist requests = %d, want 3", n)
	}
}

func TestUnauthorizedOnPrivateRegistryDoesNotFallThrough(t *testing.T) {
	pf := &packagistFake{docs: map[string]p2Doc{
		"vendor1/firstparty": {entries: []map[string]any{{"version": "1.0.0"}}},
	}}
	gf := &gitlabFake{token: privateToken, packages: map[string]map[string]map[string]any{
		"vendor1/firstparty": {"1.0.0": {}},
	}}

	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle(packagistHost, pf)
	rt.Handle(gitlabHost, gf)
	rt.Forbid("developer.mend.io")

	// Deliberately no HostRule for gitlabHost: the request carries no
	// PRIVATE-TOKEN at all, so the fake answers 401 exactly as the real
	// registry would for a bad or missing token.
	client := httpx.New(httpx.Options{
		Transport: rt,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})

	ds := New(client)
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/firstparty",
		RegistryURLs: []string{"https://" + gitlabHost, "https://" + packagistHost},
	})
	if err == nil {
		t.Fatal("a 401 from the private registry resolved")
	}
	if !strings.Contains(err.Error(), gitlabHost) {
		t.Errorf("error must name the failing registry: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error must name the status: %q", err.Error())
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	if n := rt.Count(gitlabHost); n != 1 {
		t.Errorf("gitlab requests = %d, want 1", n)
	}
	if n := rt.Count(packagistHost); n != 0 {
		t.Errorf("packagist requests = %d, want 0 - an auth failure must not fall through", n)
	}
}

func TestPackageInNoRegistryNamesBoth(t *testing.T) {
	client, _, _, rec, rt := newFixture(t)
	// Neither fake carries "vendor1/nowhere" anywhere.

	ds := New(client)
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/nowhere",
		RegistryURLs: []string{"https://" + gitlabHost, "https://" + packagistHost},
	})
	if err == nil {
		t.Fatal("a package present in neither registry resolved")
	}
	if !strings.Contains(err.Error(), "vendor1/nowhere") {
		t.Errorf("error must name the package: %q", err.Error())
	}
	if !strings.Contains(err.Error(), gitlabHost) || !strings.Contains(err.Error(), packagistHost) {
		t.Errorf("error must name both registries tried: %q", err.Error())
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	if n := rt.Count(gitlabHost); n != 1 {
		t.Errorf("gitlab requests = %d, want 1", n)
	}
	// packages.json + the 404'd stable p2 document (no ~dev attempt once
	// the stable lookup itself already came back not-found).
	if n := rt.Count(packagistHost); n != 2 {
		t.Errorf("packagist requests = %d, want 2", n)
	}
}

func TestDevVariantIsFetchedAndMerged(t *testing.T) {
	client, pf, _, rec, rt := newFixture(t)
	pf.docs["vendor1/devpkg"] = p2Doc{
		entries: []map[string]any{
			{"version": "1.0.0", "time": "2024-01-01T00:00:00Z"},
		},
	}
	pf.docs["vendor1/devpkg~dev"] = p2Doc{
		entries: []map[string]any{
			{"version": "dev-main", "time": "2024-05-01T00:00:00Z", "source": map[string]any{"url": "https://packagist.fake/vendor1/devpkg.git"}},
		},
	}

	ds := New(client)
	rs, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/devpkg",
		RegistryURLs: []string{"https://" + packagistHost},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("developer.mend.io")

	versions := make([]string, len(rs.Releases))
	for i, r := range rs.Releases {
		versions[i] = r.Version
	}
	if !slices.Contains(versions, "1.0.0") || !slices.Contains(versions, "dev-main") {
		t.Fatalf("releases = %v, want both the stable and dev-main versions", versions)
	}
	if len(versions) != 2 {
		t.Fatalf("releases = %v, want exactly 2", versions)
	}
	// packages.json, the stable p2 document, and the separate ~dev document.
	if n := rt.Count(packagistHost); n != 3 {
		t.Errorf("packagist requests = %d, want 3 (packages.json + stable + ~dev)", n)
	}
}

func TestUnregisteredHostIsRefused(t *testing.T) {
	client, _, _, rec, _ := newFixture(t)
	ds := New(client)
	_, err := ds.Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/whatever",
		RegistryURLs: []string{"https://other.example"},
	})
	if err == nil {
		t.Error("a lookup against an unregistered host succeeded")
	}
	if !rec.Failed() || !rec.Mentions("other.example") {
		t.Errorf("the refusing transport did not name the escaping host: %s", rec.String())
	}
}

func TestMetadataURLMayBeAbsolute(t *testing.T) {
	if got := resolveMetadataURL("https://repo.packagist.org", "https://repo.packagist.org/p2/x/y.json"); got != "https://repo.packagist.org/p2/x/y.json" {
		t.Errorf("absolute: %s", got)
	}
	if got := resolveMetadataURL("https://git.example.org/api/v4/group/175/-/packages/composer/", "/p2/%package%.json"); got != "https://git.example.org/api/v4/group/175/-/packages/composer/p2/%package%.json" {
		t.Errorf("relative: %s", got)
	}
}

// GitLab documents a group registry as ".../composer/packages.json", and
// the estate's composer.json files write it that way: the root document's
// own name in the URL is not a path to append to.
func TestARegistryURLNamingPackagesJSONIsTheRegistry(t *testing.T) {
	client, _, gf, rec, _ := newFixture(t)
	gf.packages["vendor1/firstparty"] = map[string]map[string]any{
		"2.0.0": {"time": "2024-03-01T00:00:00Z"},
	}
	rs, err := New(client).Releases(t.Context(), lookup.Ref{
		Datasource: "packagist", PackageName: "vendor1/firstparty",
		RegistryURLs: []string{"https://" + gitlabHost + "/packages.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	if len(rs.Releases) != 1 || rs.RegistryURL != "https://"+gitlabHost {
		t.Errorf("releases %+v registry %q", rs.Releases, rs.RegistryURL)
	}
}
