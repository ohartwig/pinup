// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package helmds

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// testIndex is a small, hand-built index.yaml shaped like the real
// grafana/helm-charts index (an excerpt of which was used to derive the
// sourceURL and timestamp rules below), with two charts of three versions
// each and the edge cases those rules need:
//
//   - grafana's newest entry carries two sources; a lookup must prefer
//     sources[0] over home.
//   - loki's newest entry carries no sources at all; a lookup must fall
//     back to home.
//   - "bare" carries neither sources nor home, for the empty case.
//   - grafana's oldest entry carries no "created"; loki's middle entry
//     carries an unparsable one. Both must leave Timestamp zero rather
//     than fail the whole lookup.
const testIndex = `apiVersion: v1
entries:
  grafana:
  - version: "10.5.15"
    created: "2026-01-30T07:10:18.070791735Z"
    digest: c08c87969270402e7d5227edb8385af67cc32ee34817bff89773ad02303b5d79
    home: https://grafana.com
    sources:
    - https://github.com/grafana/grafana
    - https://github.com/grafana/helm-charts
  - version: "10.5.14"
    created: "2026-01-29T12:44:44.133409948Z"
    digest: 0f8b67ae0bc8341e7f2f6dc5d0fcd2e84126f81dbb7e031918f8d1417615ea18
  - version: "10.5.13"
    home: https://grafana.com
  loki:
  - version: "6.24.0"
    created: "2026-02-01T00:00:00Z"
    home: https://grafana.github.io/helm-charts
  - version: "6.23.0"
    created: "not-a-timestamp"
  - version: "6.22.0"
  bare:
  - version: "1.0.0"
`

// chartRepo is a server that speaks just enough of a Helm chart repository
// to be useful: GET /index.yaml returns a fixed body (or a fixed status),
// and every request is counted so a test can prove the index is fetched
// once, not once per chart.
type chartRepo struct {
	mu       sync.Mutex
	requests int
	status   int // 0 means 200
	body     string
}

func (c *chartRepo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.requests++
	c.mu.Unlock()

	if r.URL.Path != "/index.yaml" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if c.status != 0 {
		w.WriteHeader(c.status)
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write([]byte(c.body))
}

func (c *chartRepo) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

func newFixture(t *testing.T, body string, status int) (*Datasource, *chartRepo, *harness.Recorder) {
	t.Helper()
	srv := &chartRepo{body: body, status: status}
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("charts.example", srv)

	client := httpx.New(httpx.Options{
		Transport: rt,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	return New(client), srv, rec
}

func TestTwoChartsFromOneRepoFetchTheIndexOnce(t *testing.T) {
	ds, srv, rec := newFixture(t, testIndex, 0)
	ctx := context.Background()

	grafana, err := ds.Releases(ctx, lookup.Ref{
		Datasource: "helm", PackageName: "grafana", RegistryURLs: []string{"https://charts.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	loki, err := ds.Releases(ctx, lookup.Ref{
		Datasource: "helm", PackageName: "loki", RegistryURLs: []string{"https://charts.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}

	if n := srv.count(); n != 1 {
		t.Errorf("the index was fetched %d times, want 1 - two charts from one repository must share the fetch", n)
	}

	if len(grafana.Releases) != 3 {
		t.Fatalf("grafana: got %d releases, want 3", len(grafana.Releases))
	}
	if len(loki.Releases) != 3 {
		t.Fatalf("loki: got %d releases, want 3", len(loki.Releases))
	}

	// sources[0] wins over home for the newest entry.
	if want := "https://github.com/grafana/grafana"; grafana.SourceURL != want {
		t.Errorf("grafana sourceUrl = %q, want %q", grafana.SourceURL, want)
	}
	// No sources on loki's newest entry: home wins.
	if want := "https://grafana.github.io/helm-charts"; loki.SourceURL != want {
		t.Errorf("loki sourceUrl = %q, want %q", loki.SourceURL, want)
	}

	first := grafana.Releases[0]
	if first.Version != "10.5.15" {
		t.Errorf("grafana.Releases[0].Version = %q, want 10.5.15", first.Version)
	}
	if first.Digest != "c08c87969270402e7d5227edb8385af67cc32ee34817bff89773ad02303b5d79" {
		t.Errorf("grafana.Releases[0].Digest = %q", first.Digest)
	}
	wantTS, _ := time.Parse(time.RFC3339Nano, "2026-01-30T07:10:18.070791735Z")
	if !first.Timestamp.Equal(wantTS) {
		t.Errorf("grafana.Releases[0].Timestamp = %v, want %v", first.Timestamp, wantTS)
	}

	// The oldest grafana entry carries no "created": Timestamp stays zero.
	last := grafana.Releases[2]
	if last.Version != "10.5.13" {
		t.Fatalf("grafana.Releases[2].Version = %q, want 10.5.13", last.Version)
	}
	if !last.Timestamp.IsZero() {
		t.Errorf("grafana.Releases[2] (no created field) got a timestamp: %v", last.Timestamp)
	}

	// loki's middle entry carries an unparsable "created": also zero, not
	// a failed lookup.
	middle := loki.Releases[1]
	if middle.Version != "6.23.0" {
		t.Fatalf("loki.Releases[1].Version = %q, want 6.23.0", middle.Version)
	}
	if !middle.Timestamp.IsZero() {
		t.Errorf("loki.Releases[1] (unparsable created) got a timestamp: %v", middle.Timestamp)
	}
}

func TestNeitherSourcesNorHomeIsAnEmptySourceURL(t *testing.T) {
	ds, _, rec := newFixture(t, testIndex, 0)
	rs, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "helm", PackageName: "bare", RegistryURLs: []string{"https://charts.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatal(rec.String())
	}
	if rs.SourceURL != "" {
		t.Errorf("sourceUrl = %q, want empty when neither sources nor home is set", rs.SourceURL)
	}
}

func TestATrailingSlashOnTheRepositoryIsTrimmed(t *testing.T) {
	ds, srv, _ := newFixture(t, testIndex, 0)
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "helm", PackageName: "grafana", RegistryURLs: []string{"https://charts.example/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.count(); n != 1 {
		t.Errorf("got %d requests, want 1", n)
	}
}

func TestNoRegistryURLNamesThePackage(t *testing.T) {
	ds := New(httpx.New(httpx.Options{Now: time.Now, Sleep: func(time.Duration) {}}))
	_, err := ds.Releases(context.Background(), lookup.Ref{Datasource: "helm", PackageName: "grafana"})
	if err == nil {
		t.Fatal("a lookup with no registry succeeded")
	}
	if !strings.Contains(err.Error(), "grafana") {
		t.Errorf("error does not name the package: %q", err)
	}
}

func TestAChartAbsentFromTheIndexNamesChartAndRepository(t *testing.T) {
	ds, _, _ := newFixture(t, testIndex, 0)
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "helm", PackageName: "nosuch", RegistryURLs: []string{"https://charts.example"},
	})
	if err == nil {
		t.Fatal("a missing chart resolved")
	}
	if !strings.Contains(err.Error(), "nosuch") || !strings.Contains(err.Error(), "charts.example") {
		t.Errorf("error does not name both chart and repository: %q", err)
	}
}

func TestAnUnreachableIndexIsAnError(t *testing.T) {
	ds, _, _ := newFixture(t, "", http.StatusInternalServerError)
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "helm", PackageName: "grafana", RegistryURLs: []string{"https://charts.example"},
	})
	if err == nil {
		t.Fatal("a 500 from the repository resolved")
	}
}

func TestAnUnparsableIndexIsAnError(t *testing.T) {
	ds, _, _ := newFixture(t, "not: [valid: yaml", 0)
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "helm", PackageName: "grafana", RegistryURLs: []string{"https://charts.example"},
	})
	if err == nil {
		t.Fatal("an unparsable index resolved")
	}
}

func TestAnIndexWithNoEntriesIsAnError(t *testing.T) {
	ds, _, _ := newFixture(t, "apiVersion: v1\n", 0)
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "helm", PackageName: "grafana", RegistryURLs: []string{"https://charts.example"},
	})
	if err == nil {
		t.Fatal("an index with no entries resolved")
	}
}

func TestAnUnregisteredHostIsRefused(t *testing.T) {
	ds, _, rec := newFixture(t, testIndex, 0)
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "helm", PackageName: "grafana", RegistryURLs: []string{"https://other.example"},
	})
	if err == nil {
		t.Error("a lookup against an unregistered host succeeded")
	}
	if !rec.Failed() || !rec.Mentions("other.example") {
		t.Errorf("the refusing transport did not name the escaping host: %s", rec.String())
	}
}

func TestDefaultVersioningIsSemver(t *testing.T) {
	ds := New(httpx.New(httpx.Options{Now: time.Now, Sleep: func(time.Duration) {}}))
	if v := ds.DefaultVersioning(); v != "semver" {
		t.Errorf("DefaultVersioning() = %q, want semver", v)
	}
	if ds.Name() != "helm" {
		t.Errorf("Name() = %q, want helm", ds.Name())
	}
}
