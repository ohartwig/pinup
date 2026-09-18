// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package osv

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/semverx"
	"github.com/ohartwig/pinup/versioning"
)

// testScheme is a small strict-semver scheme on top of the L0 parser. osv is
// L2 and may not import the L3 schemes - versioning/semver - which is the
// right constraint: what is under test here is this package's own range
// evaluation and batching, not any scheme's parsing (planner_test.go does the
// same for the same reason).
type testScheme struct{}

func (testScheme) Name() string               { return "test" }
func (testScheme) IsValid(v string) bool      { _, ok := semverx.Parse(v); return ok }
func (testScheme) IsVersion(v string) bool    { _, ok := semverx.Parse(v); return ok }
func (testScheme) IsStable(v string) bool     { p, ok := semverx.Parse(v); return ok && len(p.Pre) == 0 }
func (testScheme) Major(v string) (int, bool) { p, ok := semverx.Parse(v); return p.Major, ok }
func (testScheme) Minor(v string) (int, bool) { p, ok := semverx.Parse(v); return p.Minor, ok }
func (testScheme) Patch(v string) (int, bool) { p, ok := semverx.Parse(v); return p.Patch, ok }
func (testScheme) Compare(a, b string) int {
	pa, oka := semverx.Parse(a)
	pb, okb := semverx.Parse(b)
	if !oka || !okb {
		return 0
	}
	return semverx.CompareVersions(pa, pb)
}
func (t testScheme) Equal(a, b string) bool       { return t.Compare(a, b) == 0 }
func (t testScheme) Satisfies(v, rng string) bool { return t.Equal(v, rng) }
func (testScheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	return target, nil
}

func registry() versioning.Registry { return versioning.Registry{"test": testScheme{}} }

// fakeOSV speaks just enough of the OSV protocol - POST /v1/querybatch and
// GET /v1/vulns/{id} - to drive this package's own logic, keyed by the tuple
// a real server would key its index by: (name, ecosystem, version).
type fakeOSV struct {
	mu sync.Mutex

	vulns     map[string][]vulnRef
	docs      map[string]vulnDoc
	getStatus map[string]int
	getCalls  map[string]int

	batchStatus int
	batchCalls  int
	batchSizes  []int
}

func newFakeOSV() *fakeOSV {
	return &fakeOSV{
		vulns:     map[string][]vulnRef{},
		docs:      map[string]vulnDoc{},
		getStatus: map[string]int{},
		getCalls:  map[string]int{},
	}
}

func tupleKey(name, eco, version string) string { return name + "|" + eco + "|" + version }

func (f *fakeOSV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
		f.serveQueryBatch(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
		f.serveVuln(w, r, strings.TrimPrefix(r.URL.Path, "/v1/vulns/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeOSV) serveQueryBatch(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req batchRequest
	_ = json.Unmarshal(body, &req)

	f.mu.Lock()
	f.batchCalls++
	f.batchSizes = append(f.batchSizes, len(req.Queries))
	status := f.batchStatus
	f.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		return
	}

	f.mu.Lock()
	resp := batchResponse{Results: make([]batchResult, len(req.Queries))}
	for i, q := range req.Queries {
		resp.Results[i] = batchResult{Vulns: f.vulns[tupleKey(q.Package.Name, q.Package.Ecosystem, q.Version)]}
	}
	f.mu.Unlock()

	out, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (f *fakeOSV) serveVuln(w http.ResponseWriter, _ *http.Request, id string) {
	f.mu.Lock()
	f.getCalls[id]++
	status, forced := f.getStatus[id]
	doc, ok := f.docs[id]
	f.mu.Unlock()

	if forced {
		w.WriteHeader(status)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	out, _ := json.Marshal(doc)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (f *fakeOSV) getCallCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls[id]
}

// memStore is the trivial in-memory Store a caller (wire, in the real
// binary) would back with something durable.
type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	return b, ok
}

func (s *memStore) Put(key string, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = append([]byte(nil), payload...)
}

// newFixture wires a Client to a fakeOSV behind fake/harness's refusing
// transport, on host "osv.example". "evil.example" is registered as
// forbidden so a test can positively prove nothing ever reaches past OSV's
// own host.
func newFixture(t *testing.T, srv *fakeOSV) (*Client, *harness.Recorder, *harness.RefusingTransport) {
	t.Helper()
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("osv.example", srv)
	rt.Forbid("evil.example")

	client := &Client{Transport: rt, Base: "https://osv.example"}
	return client, rec, rt
}

func rangeOf(introduced string, fixed string) rangeEntry {
	ev := []event{{Introduced: introduced}}
	if fixed != "" {
		ev = append(ev, event{Fixed: fixed})
	}
	return rangeEntry{Type: "SEMVER", Events: ev}
}

func TestCheckCoversTheMeasuredProtocolAndRangeShapes(t *testing.T) {
	srv := newFakeOSV()

	// ADV-MULTI affects two packages with different fixes each - only the
	// matching package's range may decide the Fixed this package reports.
	srv.docs["ADV-MULTI"] = vulnDoc{
		ID: "ADV-MULTI", Summary: "affects pkga and pkgb differently",
		Published: "2020-01-01T00:00:00Z", Modified: "2020-06-01T00:00:00Z",
		Severity: []severityEntry{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
		Affected: []affected{
			{Package: pkgRef{Name: "pkga", Ecosystem: "npm"}, Ranges: []rangeEntry{rangeOf("1.0.0", "2.0.0")}},
			{Package: pkgRef{Name: "pkgb", Ecosystem: "npm"}, Ranges: []rangeEntry{rangeOf("1.0.0", "5.0.0")}},
		},
	}
	// A second advisory on pkga alone, with a higher fix - Bound must pick
	// the highest of the two, not the first found.
	srv.docs["ADV-SECOND"] = vulnDoc{
		ID: "ADV-SECOND", Summary: "second pkga advisory",
		Published: "2021-01-01T00:00:00Z", Modified: "2021-01-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "pkga", Ecosystem: "npm"}, Ranges: []rangeEntry{rangeOf("1.0.0", "2.5.0")}},
		},
	}
	srv.docs["ADV-ECOSYS"] = vulnDoc{
		ID: "ADV-ECOSYS", Modified: "2022-01-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "pkgc", Ecosystem: "npm"},
				Ranges: []rangeEntry{{Type: "ECOSYSTEM", Events: []event{{Introduced: "1.0.0"}, {Fixed: "1.5.0"}}}}},
		},
	}
	srv.docs["ADV-VERSIONS"] = vulnDoc{
		ID: "ADV-VERSIONS", Modified: "2022-02-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "pkgd", Ecosystem: "npm"}, Versions: []string{"2.0.0", "2.0.1"}},
		},
	}
	srv.docs["ADV-UNFIXED"] = vulnDoc{
		ID: "ADV-UNFIXED", Modified: "2022-03-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "pkge", Ecosystem: "npm"},
				Ranges: []rangeEntry{{Type: "SEMVER", Events: []event{{Introduced: "1.0.0"}, {LastAffected: "1.9.0"}}}}},
		},
	}
	srv.docs["ADV-ZERO"] = vulnDoc{
		ID: "ADV-ZERO", Modified: "2022-04-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "pkgf", Ecosystem: "npm"}, Ranges: []rangeEntry{rangeOf("0", "3.0.0")}},
		},
	}
	srv.docs["ADV-BELOW"] = vulnDoc{
		ID: "ADV-BELOW", Modified: "2022-05-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "pkgg", Ecosystem: "npm"}, Ranges: []rangeEntry{rangeOf("5.0.0", "6.0.0")}},
		},
	}
	srv.docs["ADV-GO"] = vulnDoc{
		ID: "ADV-GO", Modified: "2022-06-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "gopkg", Ecosystem: "Go"}, Ranges: []rangeEntry{rangeOf("1.0.0", "1.3.0")}},
		},
	}
	srv.docs["ADV-BROKEN"] = vulnDoc{ID: "ADV-BROKEN"}
	srv.getStatus["ADV-BROKEN"] = http.StatusInternalServerError

	srv.vulns[tupleKey("pkga", "npm", "1.5.0")] = []vulnRef{
		{ID: "ADV-MULTI", Modified: "2020-06-01T00:00:00Z"},
		{ID: "ADV-SECOND", Modified: "2021-01-01T00:00:00Z"},
	}
	srv.vulns[tupleKey("pkgb", "npm", "3.0.0")] = []vulnRef{{ID: "ADV-MULTI", Modified: "2020-06-01T00:00:00Z"}}
	srv.vulns[tupleKey("pkgc", "npm", "1.2.0")] = []vulnRef{{ID: "ADV-ECOSYS", Modified: "2022-01-01T00:00:00Z"}}
	srv.vulns[tupleKey("pkgd", "npm", "2.0.0")] = []vulnRef{{ID: "ADV-VERSIONS", Modified: "2022-02-01T00:00:00Z"}}
	srv.vulns[tupleKey("pkge", "npm", "1.5.0")] = []vulnRef{{ID: "ADV-UNFIXED", Modified: "2022-03-01T00:00:00Z"}}
	srv.vulns[tupleKey("pkgf", "npm", "0.5.0")] = []vulnRef{{ID: "ADV-ZERO", Modified: "2022-04-01T00:00:00Z"}}
	srv.vulns[tupleKey("pkgg", "npm", "1.0.0")] = []vulnRef{{ID: "ADV-BELOW", Modified: "2022-05-01T00:00:00Z"}}
	// Go's version is queried with the leading "v" stripped.
	srv.vulns[tupleKey("gopkg", "Go", "1.2.3")] = []vulnRef{{ID: "ADV-GO", Modified: "2022-06-01T00:00:00Z"}}
	srv.vulns[tupleKey("brokenpkg", "npm", "1.0.0")] = []vulnRef{{ID: "ADV-BROKEN", Modified: ""}}

	client, rec, rt := newFixture(t, srv)

	queries := []Query{
		{Datasource: "npm", PackageName: "pkga", Version: "1.5.0", Versioning: "test"},
		{Datasource: "docker", PackageName: "dockerpkg", Version: "1.0", Versioning: "test"},
		{Datasource: "npm", PackageName: "rangepkg", Version: "^1.2.5", Versioning: "test"},
		{Datasource: "npm", PackageName: "pkgb", Version: "3.0.0", Versioning: "test"},
		{Datasource: "npm", PackageName: "pkgc", Version: "1.2.0", Versioning: "test"},
		{Datasource: "npm", PackageName: "pkgd", Version: "2.0.0", Versioning: "test"},
		{Datasource: "npm", PackageName: "pkge", Version: "1.5.0", Versioning: "test"},
		{Datasource: "npm", PackageName: "pkgf", Version: "0.5.0", Versioning: "test"},
		{Datasource: "npm", PackageName: "pkgg", Version: "1.0.0", Versioning: "test"},
		{Datasource: "go", PackageName: "gopkg", Version: "v1.2.3", Versioning: "test"},
		{Datasource: "npm", PackageName: "badschemepkg", Version: "1.0.0", Versioning: "no-such-scheme"},
		{Datasource: "npm", PackageName: "brokenpkg", Version: "1.0.0", Versioning: "test"},
	}

	findings, err := client.Check(t.Context(), registry(), queries)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	rt.MustNotHaveBeenCalled("evil.example")

	if len(findings) != len(queries) {
		t.Fatalf("got %d findings, want %d (one per query, in order)", len(findings), len(queries))
	}
	for i, f := range findings {
		if f.Query.PackageName != queries[i].PackageName {
			t.Fatalf("finding %d is for %q, want %q - query order not preserved", i, f.Query.PackageName, queries[i].PackageName)
		}
	}
	byName := make(map[string]Finding, len(findings))
	for _, f := range findings {
		byName[f.Query.PackageName] = f
	}

	// pkga: two advisories, Bound is the higher of the two fixes.
	pkga := byName["pkga"]
	if len(pkga.Advisories) != 2 {
		t.Fatalf("pkga: got %d advisories, want 2: %+v", len(pkga.Advisories), pkga.Advisories)
	}
	if pkga.Bound != "2.5.0" {
		t.Errorf("pkga: Bound = %q, want 2.5.0 (the higher of 2.0.0 and 2.5.0)", pkga.Bound)
	}

	// pkgb: only ADV-MULTI applies, and with pkgb's own fix, not pkga's.
	pkgb := byName["pkgb"]
	if len(pkgb.Advisories) != 1 || pkgb.Advisories[0].Fixed != "5.0.0" {
		t.Fatalf("pkgb: got %+v, want exactly ADV-MULTI fixed at 5.0.0", pkgb.Advisories)
	}
	if pkgb.Bound != "5.0.0" {
		t.Errorf("pkgb: Bound = %q, want 5.0.0", pkgb.Bound)
	}
	if pkga.Advisories[0].Severity == "" && pkga.Advisories[1].Severity == "" {
		t.Error("neither pkga advisory carries the severity score from ADV-MULTI")
	}

	// The advisory shared by pkga and pkgb is fetched exactly once.
	if n := srv.getCallCount("ADV-MULTI"); n != 1 {
		t.Errorf("ADV-MULTI fetched %d times, want 1 (shared by two packages)", n)
	}

	// docker has no OSV ecosystem: not queried, and distinguishable from
	// "queried, nothing found".
	dockerpkg := byName["dockerpkg"]
	if dockerpkg.Ecosystem != "" {
		t.Errorf("dockerpkg: Ecosystem = %q, want \"\" (docker has no OSV ecosystem)", dockerpkg.Ecosystem)
	}
	if len(dockerpkg.Warnings) != 0 {
		t.Errorf("dockerpkg: got warnings %v, want none - \"no ecosystem\" is not a failure", dockerpkg.Warnings)
	}

	// A range current value is not a single version: not queried, but the
	// ecosystem is still resolved (npm is a supported ecosystem; the value
	// just cannot be looked up).
	rangepkg := byName["rangepkg"]
	if rangepkg.Ecosystem != "npm" {
		t.Errorf("rangepkg: Ecosystem = %q, want npm", rangepkg.Ecosystem)
	}
	if len(rangepkg.Advisories) != 0 || rangepkg.Bound != "" {
		t.Errorf("rangepkg: got %+v, want no advisories and no bound", rangepkg)
	}
	if len(rangepkg.Warnings) == 0 {
		t.Error("rangepkg: want a warning explaining the range was not queried")
	}

	// ECOSYSTEM range type.
	pkgc := byName["pkgc"]
	if len(pkgc.Advisories) != 1 || pkgc.Advisories[0].Fixed != "1.5.0" {
		t.Errorf("pkgc (ECOSYSTEM range): got %+v, want fixed 1.5.0", pkgc.Advisories)
	}

	// Explicit "versions" list: affected, but no fix can be inferred from it.
	pkgd := byName["pkgd"]
	if len(pkgd.Advisories) != 1 || pkgd.Advisories[0].Fixed != "" {
		t.Errorf("pkgd (versions list): got %+v, want one advisory with no fix", pkgd.Advisories)
	}
	if pkgd.Bound != "" {
		t.Errorf("pkgd: Bound = %q, want \"\" - a versions-list match names no fix", pkgd.Bound)
	}

	// last_affected with no fixed: affected and explicitly unfixed.
	pkge := byName["pkge"]
	if len(pkge.Advisories) != 1 || pkge.Advisories[0].Fixed != "" {
		t.Errorf("pkge (last_affected, unfixed): got %+v, want one advisory with no fix", pkge.Advisories)
	}
	if pkge.Bound != "" {
		t.Errorf("pkge: Bound = %q, want \"\" - an unfixed advisory contributes no bound", pkge.Bound)
	}

	// introduced: "0" means from the beginning of time.
	pkgf := byName["pkgf"]
	if len(pkgf.Advisories) != 1 || pkgf.Advisories[0].Fixed != "3.0.0" {
		t.Errorf("pkgf (introduced \"0\"): got %+v, want fixed 3.0.0", pkgf.Advisories)
	}

	// A version below every introduced is not affected at all.
	pkgg := byName["pkgg"]
	if len(pkgg.Advisories) != 0 || pkgg.Bound != "" {
		t.Errorf("pkgg (below every introduced): got %+v, want no advisories and no bound", pkgg)
	}

	// Go: the leading "v" is stripped before querying and before comparing.
	gopkg := byName["gopkg"]
	if gopkg.Ecosystem != "Go" {
		t.Errorf("gopkg: Ecosystem = %q, want Go", gopkg.Ecosystem)
	}
	if len(gopkg.Advisories) != 1 || gopkg.Bound != "1.3.0" {
		t.Errorf("gopkg: got %+v, want one advisory bounding at 1.3.0", gopkg)
	}

	// An unknown versioning scheme is reported and skipped, not fatal.
	badscheme := byName["badschemepkg"]
	if len(badscheme.Warnings) == 0 {
		t.Error("badschemepkg: want a warning naming the unknown versioning scheme")
	}
	if len(badscheme.Advisories) != 0 {
		t.Errorf("badschemepkg: got advisories %+v, want none", badscheme.Advisories)
	}

	// A failed single advisory fetch drops that advisory and records why,
	// without failing findings for the rest of the batch.
	broken := byName["brokenpkg"]
	if len(broken.Advisories) != 0 {
		t.Errorf("brokenpkg: got advisories %+v, want none - the fetch failed", broken.Advisories)
	}
	if !containsSubstring(broken.Warnings, "ADV-BROKEN") {
		t.Errorf("brokenpkg: warnings %v do not name the advisory that failed to fetch", broken.Warnings)
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func TestQueryBatchSplitsAt1000(t *testing.T) {
	srv := newFakeOSV()
	client, rec, _ := newFixture(t, srv)

	const n = 1001
	queries := make([]Query, n)
	for i := range n {
		queries[i] = Query{
			Datasource: "npm", PackageName: fmt.Sprintf("pkg%04d", i),
			Version: "1.0.0", Versioning: "test",
		}
	}

	findings, err := client.Check(t.Context(), registry(), queries)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	if len(findings) != n {
		t.Fatalf("got %d findings, want %d", len(findings), n)
	}
	for i, f := range findings {
		if len(f.Advisories) != 0 || f.Bound != "" {
			t.Fatalf("finding %d: got %+v, want empty (nothing registered for this package)", i, f)
		}
	}

	if srv.batchCalls != 2 {
		t.Fatalf("querybatch was called %d times, want 2 for %d queries (OSV's limit is 1000 per request)", srv.batchCalls, n)
	}
	if len(srv.batchSizes) != 2 || srv.batchSizes[0] != 1000 || srv.batchSizes[1] != 1 {
		t.Errorf("batch sizes were %v, want [1000, 1]", srv.batchSizes)
	}
}

func TestQuerybatch500IsAnErrorForTheWholeCall(t *testing.T) {
	srv := newFakeOSV()
	srv.batchStatus = http.StatusInternalServerError
	client, _, _ := newFixture(t, srv)

	_, err := client.Check(t.Context(), registry(), []Query{
		{Datasource: "npm", PackageName: "pkga", Version: "1.0.0", Versioning: "test"},
	})
	if err == nil {
		t.Fatal("a 500 from querybatch resolved; it must fail the whole call")
	}
}

func TestStoreHitAvoidsFetchOnASecondCall(t *testing.T) {
	srv := newFakeOSV()
	srv.docs["ADV-CACHE"] = vulnDoc{
		ID: "ADV-CACHE", Modified: "2023-01-01T00:00:00Z",
		Affected: []affected{
			{Package: pkgRef{Name: "pkgx", Ecosystem: "npm"}, Ranges: []rangeEntry{rangeOf("1.0.0", "2.0.0")}},
		},
	}
	srv.vulns[tupleKey("pkgx", "npm", "1.5.0")] = []vulnRef{{ID: "ADV-CACHE", Modified: "2023-01-01T00:00:00Z"}}

	store := newMemStore()
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("osv.example", srv)
	client := &Client{Transport: rt, Base: "https://osv.example", Store: store}

	queries := []Query{{Datasource: "npm", PackageName: "pkgx", Version: "1.5.0", Versioning: "test"}}

	first, err := client.Check(t.Context(), registry(), queries)
	if err != nil {
		t.Fatalf("first Check: %v", err)
	}
	if len(first[0].Advisories) != 1 || first[0].Bound != "2.0.0" {
		t.Fatalf("first Check: got %+v, want one advisory bounding at 2.0.0", first[0])
	}
	if n := srv.getCallCount("ADV-CACHE"); n != 1 {
		t.Fatalf("after the first call, ADV-CACHE was fetched %d times, want 1", n)
	}

	second, err := client.Check(t.Context(), registry(), queries)
	if err != nil {
		t.Fatalf("second Check: %v", err)
	}
	if len(second[0].Advisories) != 1 || second[0].Bound != "2.0.0" {
		t.Fatalf("second Check: got %+v, want the same answer as the first", second[0])
	}
	if n := srv.getCallCount("ADV-CACHE"); n != 1 {
		t.Errorf("after the second call, ADV-CACHE was fetched %d times, want still 1 - Store should have answered it", n)
	}
}

func TestEcosystem(t *testing.T) {
	for _, c := range []struct {
		datasource, want string
	}{
		{"npm", "npm"},
		{"packagist", "Packagist"},
		{"go", "Go"},
		{"pypi", "PyPI"},
		{"maven", "Maven"},
		{"crate", "crates.io"},
		{"rubygems", "RubyGems"},
		{"nuget", "NuGet"},
		{"docker", ""},
		{"gitlab-tags", ""},
		{"github-releases", ""},
		{"terraform-provider", ""},
		{"custom.regex", ""},
		{"apk", ""},
		{"", ""},
	} {
		if got := Ecosystem(c.datasource); got != c.want {
			t.Errorf("Ecosystem(%q) = %q, want %q", c.datasource, got, c.want)
		}
	}
}
