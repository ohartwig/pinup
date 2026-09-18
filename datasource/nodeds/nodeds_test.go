// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package nodeds

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// testIndex is shaped like nodejs.org/dist/index.json: newest first, a
// "v" prefix on every version, lts either false or the codename.
const testIndex = `[
  {"version":"v24.8.0","date":"2026-09-10","files":["aix-ppc64"],"npm":"11.6.0","lts":false,"security":false},
  {"version":"v22.19.0","date":"2026-08-27","files":["aix-ppc64"],"npm":"10.9.3","lts":"Jod","security":false},
  {"version":"v20.19.5","date":"2026-09-09","files":["aix-ppc64"],"npm":"10.8.2","lts":"Iron","security":false},
  {"version":"v0.1.14","date":"2011-08-26","files":[],"lts":false,"security":false}
]`

type dist struct {
	mu       sync.Mutex
	requests int
	status   int
}

func (d *dist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.requests++
	d.mu.Unlock()
	if r.URL.Path != "/dist/index.json" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if d.status != 0 {
		w.WriteHeader(d.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(testIndex))
}

func newFixture(t *testing.T, status int) (*Datasource, *dist, *harness.Recorder) {
	t.Helper()
	srv := &dist{status: status}
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("nodejs.example", srv)
	client := httpx.New(httpx.Options{
		Transport: rt,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	return New(client, "https://nodejs.example/dist/index.json"), srv, rec
}

func TestTheIndexIsReadOnceAndStrippedOfItsV(t *testing.T) {
	ds, srv, rec := newFixture(t, 0)
	ctx := context.Background()
	ref := lookup.Ref{Datasource: Name, PackageName: "node"}
	rs, err := ds.Releases(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Releases(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	srv.mu.Lock()
	n := srv.requests
	srv.mu.Unlock()
	if n != 1 {
		t.Errorf("the index was fetched %d times, want 1", n)
	}
	if len(rs.Releases) != 4 {
		t.Fatalf("got %d releases, want 4", len(rs.Releases))
	}
	if rs.Releases[0].Version != "24.8.0" {
		t.Errorf("newest = %q, want 24.8.0 without the v", rs.Releases[0].Version)
	}
	if want := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC); !rs.Releases[0].Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", rs.Releases[0].Timestamp, want)
	}
	if rs.PackageName != "node" || rs.SourceURL == "" {
		t.Errorf("packageName %q sourceUrl %q", rs.PackageName, rs.SourceURL)
	}
	if ds.DefaultVersioning() != "node" {
		t.Errorf("default versioning = %q", ds.DefaultVersioning())
	}
}

func TestAFailingIndexIsAnError(t *testing.T) {
	ds, _, _ := newFixture(t, http.StatusBadGateway)
	if _, err := ds.Releases(context.Background(), lookup.Ref{Datasource: Name, PackageName: "node"}); err == nil {
		t.Fatal("a 502 must surface as an error, not an empty release set")
	}
}
