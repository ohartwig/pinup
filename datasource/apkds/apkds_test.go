// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package apkds

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// indexOf renders an APKINDEX.tar.gz carrying the given package versions.
func indexOf(t *testing.T, pkgs map[string][]string) []byte {
	t.Helper()
	var text strings.Builder
	for name, vs := range pkgs {
		for _, v := range vs {
			text.WriteString("C:Q1abcdef\nP:" + name + "\nV:" + v + "\nA:x86_64\n\n")
		}
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	body := text.String()
	if err := tw.WriteHeader(&tar.Header{Name: "APKINDEX", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// mirror serves "/<arch>/APKINDEX.tar.gz" per architecture and counts.
type mirror struct {
	arches map[string][]byte
	hits   int
	status int
}

func (m *mirror) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.hits++
	if m.status != 0 {
		w.WriteHeader(m.status)
		return
	}
	// ".../<arch>/APKINDEX.tar.gz", whatever repository path precedes it.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-1] != "APKINDEX.tar.gz" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, ok := m.arches[parts[len(parts)-2]]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Write(body)
}

func client(rt http.RoundTripper) *httpx.Client {
	return httpx.New(httpx.Options{Transport: rt, Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}})
}

func versions(t *testing.T, ds *Datasource, pkg string) []string {
	t.Helper()
	rs, err := ds.Releases(context.Background(), lookup.Ref{Datasource: ds.Name(), PackageName: pkg})
	if err != nil {
		t.Fatalf("%s: %v", pkg, err)
	}
	var out []string
	for _, r := range rs.Releases {
		out = append(out, r.Version)
	}
	return out
}

// The sidecar's two rules, and the two measured cases from the task: a
// package on one architecture only is absent, a package in one mirror only
// is present.
func TestUnionOverMirrorsIntersectionOverArches(t *testing.T) {
	public := &mirror{arches: map[string][]byte{
		"x86_64":  indexOf(t, map[string][]string{"go-1.27": {"1.27.0-r0", "1.27.1-r0"}, "onlyx86": {"1.0.0-r0"}}),
		"aarch64": indexOf(t, map[string][]string{"go-1.27": {"1.27.0-r0", "1.27.1-r0", "1.27.2-r0"}}),
	}}
	own := &mirror{arches: map[string][]byte{
		"x86_64":  indexOf(t, map[string][]string{"php-frankenphp-8.4": {"8.4.10-r0"}, "go-1.27": {"1.27.2-r0"}}),
		"aarch64": indexOf(t, map[string][]string{"php-frankenphp-8.4": {"8.4.10-r0"}}),
	}}
	rt := harness.NewRefusingTransport(t).
		Handle("packages.wolfi.dev", public).
		Handle("mirror.example.org", own).
		Forbid("127.0.0.1:8099")
	wolfi := New("custom.wolfi", client(rt), View{
		Mirrors: []string{"https://packages.wolfi.dev/os", "https://mirror.example.org"},
		Arches:  []string{"x86_64", "aarch64"},
	})
	// go-1.27: 1.27.2-r0 is on aarch64 publicly and on x86_64 through the
	// own mirror - present, by the union.
	if got := strings.Join(versions(t, wolfi, "go-1.27"), ","); got != "1.27.0-r0,1.27.1-r0,1.27.2-r0" {
		t.Errorf("go-1.27 = %s", got)
	}
	// onlyx86: absent, by the intersection.
	if got := versions(t, wolfi, "onlyx86"); len(got) != 0 {
		t.Errorf("a package on one architecture only must be absent, got %v", got)
	}
	// php-frankenphp-8.4: in the own mirror only - present.
	if got := strings.Join(versions(t, wolfi, "php-frankenphp-8.4"), ","); got != "8.4.10-r0" {
		t.Errorf("php-frankenphp-8.4 = %s", got)
	}
	// Four indexes, fetched once each however many packages are asked.
	if public.hits != 2 || own.hits != 2 {
		t.Errorf("index fetches: public %d, own %d; want 2 and 2", public.hits, own.hits)
	}
	rt.MustNotHaveBeenCalled("127.0.0.1:8099")

	// The own view sees the own mirror only: go-1.27 has one version there
	// on x86_64 and none on aarch64 - absent.
	koh := New("custom.koh-apk", client(rt), View{Mirrors: []string{"https://mirror.example.org"}, Arches: []string{"x86_64", "aarch64"}})
	if got := versions(t, koh, "go-1.27"); len(got) != 0 {
		t.Errorf("koh view must not see the public repository, got %v", got)
	}
	if got := strings.Join(versions(t, koh, "php-frankenphp-8.4"), ","); got != "8.4.10-r0" {
		t.Errorf("koh php-frankenphp-8.4 = %s", got)
	}
}

func TestEmptyIndexAndFailingMirrorAreErrors(t *testing.T) {
	empty := &mirror{arches: map[string][]byte{"x86_64": indexOf(t, map[string][]string{})}}
	rt := harness.NewRefusingTransport(t).Handle("empty.example.org", empty)
	ds := New("custom.wolfi", client(rt), View{Mirrors: []string{"https://empty.example.org"}, Arches: []string{"x86_64"}})
	if _, err := ds.Releases(context.Background(), lookup.Ref{PackageName: "x"}); err == nil {
		t.Fatal("an index with no packages must be an error, not an empty answer")
	}
	// Remembered: the second package does not fetch again.
	ds.Releases(context.Background(), lookup.Ref{PackageName: "y"})
	if empty.hits != 1 {
		t.Errorf("a failed index was fetched %d times", empty.hits)
	}

	down := &mirror{status: http.StatusServiceUnavailable}
	rt2 := harness.NewRefusingTransport(t).Handle("down.example.org", down)
	ds2 := New("custom.wolfi", client(rt2), View{Mirrors: []string{"https://down.example.org"}, Arches: []string{"x86_64"}})
	if _, err := ds2.Releases(context.Background(), lookup.Ref{PackageName: "x"}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("a mirror that is down is an error naming the status, got %v", err)
	}
}
