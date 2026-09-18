// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package pypids

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// testDoc is shaped like GET https://pypi.org/pypi/PyYAML/json, cut down
// to the fields read here: three releases, one of them yanked in every
// file, one with two files uploaded at different times.
const testDoc = `{
  "info": {"name": "PyYAML", "version": "6.0.3", "home_page": "https://pyyaml.org/",
           "project_urls": {"Homepage": "https://pyyaml.org/", "Source Code": "https://github.com/yaml/pyyaml",
                            "Bug Tracker": "https://github.com/yaml/pyyaml/issues"}},
  "releases": {
    "6.0.1": [{"upload_time_iso_8601": "2023-07-18T00:00:00.000000Z", "yanked": false}],
    "6.0.2": [{"upload_time_iso_8601": "2024-08-06T10:00:00.000000Z", "yanked": true}],
    "6.0.3": [{"upload_time_iso_8601": "2025-09-25T21:00:00.000000Z", "yanked": false},
              {"upload_time_iso_8601": "2025-09-25T20:30:00.000000Z", "yanked": false}]
  }
}`

type index struct {
	path   string
	status int
}

func (i *index) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	i.path = r.URL.Path
	if i.status != 0 {
		w.WriteHeader(i.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(testDoc))
}

func newFixture(t *testing.T, status int) (*Datasource, *index, *harness.Recorder) {
	t.Helper()
	srv := &index{status: status}
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("pypi.example", srv)
	client := httpx.New(httpx.Options{
		Transport: rt,
		Now:       func() time.Time { return time.Unix(0, 0) },
		Sleep:     func(time.Duration) {},
	})
	return New(client), srv, rec
}

func TestAProjectIsAskedForByItsNormalisedName(t *testing.T) {
	ds, srv, rec := newFixture(t, 0)
	rs, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: Name, PackageName: "PyYAML", RegistryURLs: []string{"https://pypi.example/pypi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	if srv.path != "/pypi/pyyaml/json" {
		t.Errorf("asked for %q, want /pypi/pyyaml/json", srv.path)
	}
	if len(rs.Releases) != 3 {
		t.Fatalf("got %d releases, want 3", len(rs.Releases))
	}
	byVersion := map[string]int{}
	for i, r := range rs.Releases {
		byVersion[r.Version] = i
	}
	if r := rs.Releases[byVersion["6.0.2"]]; !r.Deprecated {
		t.Error("a release whose every file is yanked must be deprecated")
	}
	r := rs.Releases[byVersion["6.0.3"]]
	if r.Deprecated {
		t.Error("6.0.3 is not yanked")
	}
	if want := time.Date(2025, 9, 25, 20, 30, 0, 0, time.UTC); !r.Timestamp.Equal(want) {
		t.Errorf("6.0.3 timestamp = %v, want the earliest upload %v", r.Timestamp, want)
	}
	if want := "https://github.com/yaml/pyyaml"; rs.SourceURL != want {
		t.Errorf("sourceUrl = %q, want %q", rs.SourceURL, want)
	}
	if rs.PackageName != "PyYAML" {
		t.Errorf("packageName = %q, want the name as asked", rs.PackageName)
	}
	if ds.DefaultVersioning() != "pep440" {
		t.Errorf("default versioning = %q", ds.DefaultVersioning())
	}
}

func TestAnUnknownProjectIsAnError(t *testing.T) {
	ds, _, _ := newFixture(t, http.StatusNotFound)
	if _, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: Name, PackageName: "no-such", RegistryURLs: []string{"https://pypi.example/pypi"},
	}); err == nil {
		t.Fatal("a 404 must surface as an error")
	}
}

func TestNormalize(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"PyYAML", "pyyaml"}, {"git_filter-repo", "git-filter-repo"}, {"Zope.Interface", "zope-interface"}, {"boto3", "boto3"},
	} {
		if got := normalize(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
