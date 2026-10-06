// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package osv

import (
	"archive/zip"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
)

// A component of the installation, named by purl the way a CSAF product
// tree names it, and a composer package named by ecosystem and name.
const componentRecord = `{"id":"KOH-2026-0001","aliases":["CVE-2026-90001"],"summary":"lint-tools leaks the job token","published":"2026-10-01T00:00:00Z","modified":"2026-10-01T00:00:00Z",
 "affected":[{"package":{"ecosystem":"GitLab","name":"devops/ci-cd-components/lint-tools","purl":"pkg:gitlab/devops/ci-cd-components/lint-tools"},
  "ranges":[{"type":"SEMVER","events":[{"introduced":"1.30.0"},{"fixed":"1.36.27"}]}]}]}`
const composerRecord = `{"id":"KOH-2026-0002","aliases":["GHSA-xxxx-yyyy-zzzz"],"affected":[{"package":{"ecosystem":"Packagist","name":"moselwal/dev"},
  "ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"5.2.0"}]}]}]}`

func TestParseRecords(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		ids      []string
		err      bool
	}{
		{"one record", componentRecord, []string{"KOH-2026-0001"}, false},
		{"an array", "[" + componentRecord + "," + composerRecord + "]", []string{"KOH-2026-0001", "KOH-2026-0002"}, false},
		{"a vulns object", `{"vulns":[` + composerRecord + `]}`, []string{"KOH-2026-0002"}, false},
		{"empty", "  ", nil, false},
		{"not a record", `{"foo":1}`, nil, true},
		{"a record without an id", `[{"summary":"x"}]`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs, err := ParseRecords([]byte(tc.in))
			if (err != nil) != tc.err {
				t.Fatalf("err %v", err)
			}
			var ids []string
			for _, r := range rs {
				ids = append(ids, r.ID())
			}
			if !slices.Equal(ids, tc.ids) {
				t.Errorf("ids %v, want %v", ids, tc.ids)
			}
		})
	}
}

func TestPurlBase(t *testing.T) {
	for in, want := range map[string]string{
		"pkg:composer/koh/x@1.0.0?arch=x#sub": "pkg:composer/koh/x",
		"pkg:GitLab/Devops/X":                 "pkg:gitlab/devops/x",
		"pkg:npm/%40scope/name@1.2.3":         "pkg:npm/%40scope/name",
		"composer/koh/x":                      "",
		"pkg:nothing":                         "",
	} {
		if got := PurlBase(in); got != want {
			t.Errorf("PurlBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// A record matches by purl or by ecosystem and name, and its ranges are
// walked as OSV's answers are: the fix of the range that contains the
// version, nothing for a version outside it.
func TestRecordEvaluate(t *testing.T) {
	comp, _ := ParseRecords([]byte(componentRecord))
	comps, _ := ParseRecords([]byte(composerRecord))
	for _, tc := range []struct {
		name                string
		r                   Record
		purl, eco, pkg, ver string
		hit                 bool
		fixed               string
	}{
		{"by purl, inside", comp[0], "pkg:gitlab/devops/ci-cd-components/lint-tools", "GitLab", "devops/ci-cd-components/lint-tools", "1.36.26", true, "1.36.27"},
		{"by purl, fixed", comp[0], "pkg:gitlab/devops/ci-cd-components/lint-tools", "GitLab", "devops/ci-cd-components/lint-tools", "1.36.27", false, ""},
		{"by purl, before", comp[0], "pkg:gitlab/devops/ci-cd-components/lint-tools", "GitLab", "devops/ci-cd-components/lint-tools", "1.29.0", false, ""},
		{"by ecosystem and name", comps[0], "pkg:composer/moselwal/dev", "Packagist", "moselwal/dev", "5.1.0", true, "5.2.0"},
		{"another package", comps[0], "pkg:composer/moselwal/other", "Packagist", "moselwal/other", "5.1.0", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, hit := tc.r.Evaluate(tc.purl, tc.eco, tc.pkg, tc.ver, testScheme{})
			if hit != tc.hit || a.Fixed != tc.fixed {
				t.Errorf("hit %v fixed %q, want %v %q", hit, a.Fixed, tc.hit, tc.fixed)
			}
			if hit && len(a.Aliases) == 0 {
				t.Errorf("the aliases are lost: %+v", a)
			}
		})
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

type feedServer struct {
	files map[string][]byte
	hits  map[string]int
}

func (s *feedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits[r.URL.Path]++
	body, ok := s.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(body)
}

// The feed reads an OSV export (zip), a .json document and a file://
// directory, once each; a source that cannot be read is a warning and the
// others still answer. gitlab-packages is asked by its composer name.
func TestPrivateFeedAnswersLikeOSV(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "KOH-2026-0003.json"), []byte(`{"id":"KOH-2026-0003","affected":[{"package":{"purl":"pkg:npm/%40koh/ui"},"versions":["2.0.0"]}]}`), 0o644)
	srv := &feedServer{hits: map[string]int{}, files: map[string][]byte{
		"/feed/all.zip":   zipOf(t, map[string]string{"KOH-2026-0001.json": componentRecord, "README.md": "not a record"}),
		"/feed/more.json": []byte(`{"vulns":[` + composerRecord + `]}`),
	}}
	rt := harness.NewRefusingTransport(t).Handle("feed.example.org", srv)
	hc := httpx.New(httpx.Options{Transport: rt, Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}})
	f := &PrivateFeed{HTTP: hc, Sources: []string{"https://feed.example.org/feed/all.zip", "https://feed.example.org/feed/more.json", "file://" + dir, "https://feed.example.org/feed/gone.json"}}
	vs := registry()
	queries := []PrivateQuery{
		{Datasource: "gitlab-tags", PackageName: "devops/ci-cd-components/lint-tools", Version: "1.36.26", Versioning: "test"},
		{Datasource: "gitlab-packages", PackageName: "development/moselwal/dev:moselwal/dev", Version: "5.1.0", Versioning: "test"},
		{Datasource: "npm", PackageName: "@koh/ui", Version: "2.0.0", Versioning: "test"},
		{Datasource: "docker", PackageName: "registry.example.org/x", Version: "1.0.0", Versioning: "test"},
		{Datasource: "gitlab-tags", PackageName: "devops/ci-cd-components/lint-tools", Version: "^1.0", Versioning: "test"},
	}
	got, warns := f.Check(t.Context(), vs, queries)
	ids := func(as []Advisory) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.ID)
		}
		return out
	}
	for i, want := range [][]string{{"KOH-2026-0001"}, {"KOH-2026-0002"}, {"KOH-2026-0003"}, nil, nil} {
		if !slices.Equal(ids(got[i]), want) {
			t.Errorf("query %d: %v, want %v", i, ids(got[i]), want)
		}
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "gone.json") {
		t.Errorf("warnings %v, want one for gone.json", warns)
	}
	f.Check(t.Context(), vs, queries)
	if srv.hits["/feed/all.zip"] != 1 || srv.hits["/feed/more.json"] != 1 {
		t.Errorf("sources read %v times, want once each", srv.hits)
	}
	if !f.Asks("gitlab-releases") || f.Asks("docker") || (&PrivateFeed{}).Asks("npm") || (*PrivateFeed)(nil).Asks("npm") {
		t.Error("Asks: a configured feed asks the datasources it names, an empty or nil one nothing")
	}
}

// An explicit mapping (packageRules[].osvPackage) is asked in its own
// ecosystem even where the datasource has one; without Explicit the
// datasource's ecosystem wins and a configured one only fills a gap.
func TestExplicitMappingWinsOverTheDatasource(t *testing.T) {
	for _, tc := range []struct {
		q    Query
		want string
	}{
		{Query{Datasource: "npm", Ecosystem: "Go", Explicit: true, Versioning: "npm"}, "Go"},
		{Query{Datasource: "npm", Ecosystem: "Go", Versioning: "npm"}, "npm"},
		{Query{Datasource: "github-releases", Ecosystem: "Go", Explicit: true, Versioning: "semver"}, "Go"},
		{Query{Datasource: "github-releases", Versioning: "semver"}, ""},
	} {
		if got := ecosystemOf(tc.q); got != tc.want {
			t.Errorf("ecosystemOf(%+v) = %q, want %q", tc.q, got, tc.want)
		}
	}
}
