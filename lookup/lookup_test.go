// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package lookup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
)

var now = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

// countingDS answers from a table and counts calls per package.
type countingDS struct {
	mu       sync.Mutex
	calls    map[string]int
	releases map[string][]string
	err      error
}

func (c *countingDS) Name() string              { return "fake" }
func (c *countingDS) DefaultVersioning() string { return "semver" }
func (c *countingDS) Releases(_ context.Context, ref Ref) (*model.ReleaseSet, error) {
	c.mu.Lock()
	c.calls[ref.PackageName]++
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	rs := &model.ReleaseSet{PackageName: ref.PackageName, Datasource: "fake"}
	for _, v := range c.releases[ref.PackageName] {
		rs.Releases = append(rs.Releases, model.Release{Version: v})
	}
	return rs, nil
}

// memCache is the Cache interface over maps, with the same freshness rule
// as the bbolt store.
type memCache struct {
	releases map[string]struct {
		payload []byte
		at      time.Time
	}
	seen map[string]time.Time
	// PutErr, when set, is what every write answers.
	PutErr error
}

func newMemCache() *memCache {
	return &memCache{releases: map[string]struct {
		payload []byte
		at      time.Time
	}{}, seen: map[string]time.Time{}}
}

func (m *memCache) GetReleases(key string, ttl time.Duration, now time.Time) ([]byte, bool, error) {
	e, ok := m.releases[key]
	if !ok {
		return nil, false, nil
	}
	return e.payload, !now.After(e.at.Add(ttl)), nil
}

func (m *memCache) PutReleases(key string, payload []byte, now time.Time) error {
	if m.PutErr != nil {
		return m.PutErr
	}
	m.releases[key] = struct {
		payload []byte
		at      time.Time
	}{payload, now}
	return nil
}

func (m *memCache) FirstSeenAll(key string, versions []string, now time.Time) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for _, v := range versions {
		k := key + "\x00" + v
		if _, ok := m.seen[k]; !ok {
			m.seen[k] = now
		}
		out[v] = m.seen[k]
	}
	return out, nil
}

func dep(name, ds string) model.Dependency {
	return model.Dependency{DepName: name, Datasource: ds, CurrentValue: "1.0.0", CustomManager: model.NoCustomManager}
}

func TestDedupesAcrossDependencies(t *testing.T) {
	ds := &countingDS{calls: map[string]int{}, releases: map[string][]string{"a": {"1.0.0", "1.1.0"}}}
	f := &Fetcher{Registry: Registry{"fake": ds}}
	deps := []model.Dependency{dep("a", "fake"), dep("a", "fake"), dep("a", "fake")}
	res := f.Fetch(context.Background(), deps)
	if len(res) != 1 || ds.calls["a"] != 1 {
		t.Fatalf("three dependencies on one package are one lookup: results=%d calls=%d", len(res), ds.calls["a"])
	}
	r := res[RefOf(deps[0]).Key()]
	if r.Releases == nil || len(r.Releases.Releases) != 2 {
		t.Errorf("result %+v", r)
	}
}

func TestSkippedAndUnknownDependenciesAreNotLookedUp(t *testing.T) {
	ds := &countingDS{calls: map[string]int{}, releases: map[string][]string{}}
	f := &Fetcher{Registry: Registry{"fake": ds}}
	skipped := dep("a", "fake")
	skipped.SkipReason = "decided already"
	res := f.Fetch(context.Background(), []model.Dependency{skipped, dep("b", "nonesuch")})
	if ds.calls["a"] != 0 {
		t.Error("a skipped dependency must not be looked up")
	}
	r := res[RefOf(dep("b", "nonesuch")).Key()]
	if r.Releases == nil || r.Releases.Err == "" || r.Warning == nil {
		t.Errorf("an unknown datasource is a lookup failure with a warning, got %+v", r)
	}
}

// A cache that cannot be written does not fail the lookup - the answer is
// in hand - but the fetcher says so, for the plan.
func TestCacheWriteFailureIsReportedNotSwallowed(t *testing.T) {
	ds := &countingDS{calls: map[string]int{}, releases: map[string][]string{"a": {"1.0.0"}}}
	c := newMemCache()
	c.PutErr = errors.New("disk full")
	f := &Fetcher{Registry: Registry{"fake": ds}, Cache: c, TTL: time.Hour, Now: now}
	res := f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})
	if r := res[RefOf(dep("a", "fake")).Key()]; r.Warning != nil || len(r.Releases.Releases) != 1 {
		t.Fatalf("the lookup itself must succeed: %+v", r)
	}
	if p := f.Problems(); len(p) != 1 || !strings.Contains(p[0], "disk full") {
		t.Errorf("problems = %v", p)
	}
}

func TestFreshCacheHitIssuesNoRequest(t *testing.T) {
	ds := &countingDS{calls: map[string]int{}, releases: map[string][]string{"a": {"1.0.0", "1.1.0"}}}
	c := newMemCache()
	f := &Fetcher{Registry: Registry{"fake": ds}, Cache: c, TTL: time.Hour, Now: now}
	f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})
	f.Now = now.Add(30 * time.Minute)
	res := f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})
	if ds.calls["a"] != 1 {
		t.Fatalf("the second lookup within the TTL must come from the cache, got %d calls", ds.calls["a"])
	}
	r := res[RefOf(dep("a", "fake")).Key()]
	if !r.Releases.FromCache || len(r.Releases.Releases) != 2 {
		t.Errorf("cached result %+v", r.Releases)
	}
	// firstseen is the moment of the FIRST observation, not of this one.
	if got := r.Releases.Releases[1].FirstSeen; !got.Equal(now) {
		t.Errorf("firstSeen = %v, want %v", got, now)
	}
}

func TestStaleEntryIsRefetchedAndServedOnFailure(t *testing.T) {
	ds := &countingDS{calls: map[string]int{}, releases: map[string][]string{"a": {"1.0.0"}}}
	c := newMemCache()
	f := &Fetcher{Registry: Registry{"fake": ds}, Cache: c, TTL: time.Hour, Now: now}
	f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})

	f.Now = now.Add(2 * time.Hour)
	ds.releases["a"] = []string{"1.0.0", "2.0.0"}
	res := f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})
	if ds.calls["a"] != 2 {
		t.Fatalf("a stale entry must be refetched, got %d calls", ds.calls["a"])
	}
	if r := res[RefOf(dep("a", "fake")).Key()]; r.Releases.FromCache || len(r.Releases.Releases) != 2 {
		t.Errorf("refetched result %+v", r.Releases)
	}

	f.Now = now.Add(4 * time.Hour)
	ds.err = errors.New("connection refused")
	res = f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})
	r := res[RefOf(dep("a", "fake")).Key()]
	if r.Releases == nil || !r.Releases.FromCache || len(r.Releases.Releases) != 2 {
		t.Fatalf("on failure the stale entry is served, marked: %+v", r.Releases)
	}
	if r.Warning == nil || !strings.Contains(r.Warning.Msg, "connection refused") || !strings.Contains(r.Warning.Msg, "cached at") {
		t.Errorf("serving stale must warn with both the failure and the cache time: %+v", r.Warning)
	}
}

func TestFailureWithNoCacheIsAnErrorResult(t *testing.T) {
	ds := &countingDS{calls: map[string]int{}, err: errors.New("boom")}
	f := &Fetcher{Registry: Registry{"fake": ds}, Cache: newMemCache(), Now: now}
	res := f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})
	r := res[RefOf(dep("a", "fake")).Key()]
	if r.Releases == nil || r.Releases.Err != "boom" || r.Warning == nil {
		t.Errorf("got %+v", r)
	}
}

// A datasource that declines by design records the reason on the release
// set - the planner still says "lookup failed: ..." - but raises no
// warning: it is the same decision every run, not something that broke.
func TestADeclinedLookupIsRecordedWithoutAWarning(t *testing.T) {
	ds := &countingDS{calls: map[string]int{}, err: &DeclinedError{Reason: "registry is a CI variable"}}
	f := &Fetcher{Registry: Registry{"fake": ds}, Cache: newMemCache(), Now: now}
	res := f.Fetch(context.Background(), []model.Dependency{dep("a", "fake")})
	r := res[RefOf(dep("a", "fake")).Key()]
	if r.Releases == nil || r.Releases.Err != "registry is a CI variable" {
		t.Errorf("the reason is not recorded: %+v", r)
	}
	if r.Warning != nil {
		t.Errorf("a declined lookup warned: %+v", r.Warning)
	}
	// And a wrapped one is still recognised.
	ds.err = fmt.Errorf("gitlab-tags: %w", &DeclinedError{Reason: "declined"})
	res = f.Fetch(context.Background(), []model.Dependency{dep("b", "fake")})
	if r := res[RefOf(dep("b", "fake")).Key()]; r.Warning != nil {
		t.Errorf("a wrapped declined lookup warned: %+v", r.Warning)
	}
}

func TestPackageNameWinsOverDepNameForTheLookup(t *testing.T) {
	d := dep("shown", "fake")
	d.PackageName = "looked-up"
	if ref := RefOf(d); ref.PackageName != "looked-up" {
		t.Errorf("RefOf = %+v", ref)
	}
	if RefOf(dep("a", "fake")).Key() == RefOf(dep("b", "fake")).Key() {
		t.Error("different packages must not share a key")
	}
	withReg := dep("a", "fake")
	withReg.RegistryURLs = []string{"https://x"}
	if RefOf(withReg).Key() == RefOf(dep("a", "fake")).Key() {
		t.Error("a different registry is a different lookup")
	}
}

type digestDS struct {
	countingDS
	digests map[string]string
	asked   int
}

func (d *digestDS) Digest(_ context.Context, ref Ref, version string) (string, error) {
	d.asked++
	if v, ok := d.digests[version]; ok {
		return v, nil
	}
	return "", errors.New("no such tag")
}

// A digest is fetched through the datasource once per version and then
// served from the cache within the TTL; a datasource without digests, an
// unknown tag and an empty answer are all errors, never a value the
// planner could write.
func TestDigestIsCachedPerVersionAndNeverEmpty(t *testing.T) {
	ds := &digestDS{countingDS: countingDS{calls: map[string]int{}}, digests: map[string]string{"1.0": "sha256:aa", "empty": ""}}
	f := &Fetcher{Registry: Registry{"fake": ds}, Cache: newMemCache(), Now: now}
	d := dep("a", "fake")
	for range 3 {
		got, err := f.Digest(context.Background(), d, "1.0")
		if err != nil || got != "sha256:aa" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if ds.asked != 1 {
		t.Errorf("the datasource was asked %d times for one version, want 1", ds.asked)
	}
	if _, err := f.Digest(context.Background(), d, "9.9"); err == nil {
		t.Error("an unknown tag yielded a digest")
	}
	if _, err := f.Digest(context.Background(), d, "empty"); err == nil {
		t.Error("an empty digest was accepted")
	}
	plain := &Fetcher{Registry: Registry{"fake": &countingDS{calls: map[string]int{}}}, Now: now}
	if _, err := plain.Digest(context.Background(), d, "1.0"); err == nil {
		t.Error("a datasource without digests answered one")
	}
}
