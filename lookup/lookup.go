// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package lookup declares the Datasource interface and fetches releases for
// extracted dependencies.
//
// Layer 2: it knows what a datasource is and nothing about which ones exist.
// Implementations live under datasource/ and are handed in by wire.
//
// The stage has one performance property that matters more than any other:
// dedupe. The estate's repositories share most of their dependencies - every
// pipeline pins the same handful of CI components, every image derives from
// the same base - so one process looking up each unique (datasource, package,
// registry) once, rather than once per repository, is the main lever on the
// "ninety minutes to minutes" target. Without it that target is unreachable
// and no later optimisation recovers it.
package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// Ref is what a datasource needs to find a package.
// DeclinedError is what a datasource returns when it will not look a
// reference up at all - a registry written as a CI variable, say. The
// dependency records the reason as its lookup failure like any other, but
// the run does not warn: declining is the datasource's decision, made the
// same way every run, not something that went wrong.
type DeclinedError struct{ Reason string }

func (e *DeclinedError) Error() string { return e.Reason }

type Ref struct {
	Datasource   string
	PackageName  string
	RegistryURLs []string
}

// Key is the dedupe identity: the same package from the same datasource at
// the same registry is one lookup however many dependencies reference it.
func (r Ref) Key() string {
	reg := ""
	if len(r.RegistryURLs) > 0 {
		reg = r.RegistryURLs[0]
	}
	return r.Datasource + "\x00" + r.PackageName + "\x00" + reg
}

// Datasource lists the releases of a package.
type Datasource interface {
	Name() string

	// Releases returns every known release. A datasource that cannot reach
	// its registry returns an error; the stage turns that into a plan
	// warning and a ReleaseSet with Err set, never into a failed run.
	Releases(ctx context.Context, ref Ref) (*model.ReleaseSet, error)

	// DefaultVersioning is the scheme to use when neither the dependency nor
	// a rule names one.
	DefaultVersioning() string
}

// DigestSource is a datasource that can name the content digest of one
// version - a container registry, where a tag is a moving pointer and the
// digest is what actually gets pulled. Releases deliberately does not fetch
// digests: one manifest request per tag would turn a lookup into hundreds.
type DigestSource interface {
	Digest(ctx context.Context, ref Ref, version string) (string, error)
}

// Registry maps a datasource name to its implementation. wire fills it.
type Registry map[string]Datasource

// Get resolves a datasource name.
func (r Registry) Get(name string) (Datasource, error) {
	if d, ok := r[name]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("unknown datasource %q", name)
}

// Names returns the registered names, sorted.
func (r Registry) Names() []string {
	out := make([]string, 0, len(r))
	for n := range r {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Result is one lookup's outcome, keyed for the dependencies that asked.
type Result struct {
	Ref      Ref
	Releases *model.ReleaseSet
	// Warning is set when the lookup failed. It is recorded once per unique
	// ref, not once per dependency that shared it.
	Warning *model.Warning
}

// Cache is what the fetcher needs from a store: TTL'd release sets and the
// permanent first-seen record. cache.Store satisfies it; a nil Cache means
// every lookup is cold.
type Cache interface {
	GetReleases(key string, ttl time.Duration, now time.Time) (payload []byte, fresh bool, err error)
	PutReleases(key string, payload []byte, now time.Time) error
	// FirstSeenAll records every version of one key that is not yet known
	// and returns the first-seen moment of each, in one transaction.
	FirstSeenAll(key string, versions []string, now time.Time) (map[string]time.Time, error)
}

// Fetcher runs lookups with dedupe and bounded parallelism.
type Fetcher struct {
	Registry Registry
	// Parallel bounds the number of lookups in flight. Zero means eight.
	Parallel int

	// Cache, when set, answers fresh entries without a round trip and
	// serves a stale entry - marked, and with a warning - when the
	// datasource fails. TTL is how long an entry counts as fresh; zero
	// means one hour. Now is required when Cache is set.
	Cache Cache
	TTL   time.Duration
	Now   time.Time
	// Bypass, when set, names refs whose cached answer is not to be used:
	// the release fast lane must see the tag that was pushed a minute ago,
	// not last hour's list. The fresh answer is still written back.
	Bypass func(Ref) bool

	mu       sync.Mutex
	problems []string
}

// Problems reports what went wrong beside the lookups themselves - a
// cache that could not be written, so the next run pays for the fetch
// again. A lookup failure is on its Result; these are on nobody's, and
// would otherwise be on nobody's plan either.
func (f *Fetcher) Problems() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.problems...)
}

func (f *Fetcher) problem(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.problems = append(f.problems, fmt.Sprintf(format, args...))
}

// Fetch looks up every unique ref among the dependencies once.
//
// Dependencies that carry a SkipReason are not looked up - a decision was
// already made about them, and asking a registry about a value that could
// never resolve is a wasted round trip that would then need its own warning.
func (f *Fetcher) Fetch(ctx context.Context, deps []model.Dependency) map[string]Result {
	unique := map[string]Ref{}
	for _, d := range deps {
		if d.SkipReason != "" || d.Datasource == "" {
			continue
		}
		ref := RefOf(d)
		unique[ref.Key()] = ref
	}

	parallel := f.Parallel
	if parallel <= 0 {
		parallel = 8
	}
	sem := make(chan struct{}, parallel)
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := make(map[string]Result, len(unique))

	for key, ref := range unique {
		wg.Add(1)
		go func(key string, ref Ref) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res := f.one(ctx, ref)
			mu.Lock()
			out[key] = res
			mu.Unlock()
		}(key, ref)
	}
	wg.Wait()
	return out
}

func (f *Fetcher) one(ctx context.Context, ref Ref) Result {
	key := ref.Key()
	var stale *model.ReleaseSet
	if f.Cache != nil && (f.Bypass == nil || !f.Bypass(ref)) {
		payload, fresh, cerr := f.Cache.GetReleases(key, f.ttl(), f.Now)
		if cerr == nil && payload != nil {
			var cached model.ReleaseSet
			if json.Unmarshal(payload, &cached) == nil {
				if fresh {
					cached.FromCache = true
					f.recordFirstSeen(key, &cached)
					return Result{Ref: ref, Releases: &cached}
				}
				stale = &cached
			}
		}
	}

	ds, err := f.Registry.Get(ref.Datasource)
	var rs *model.ReleaseSet
	if err == nil {
		rs, err = ds.Releases(ctx, ref)
	}
	if err == nil && f.Cache != nil {
		rs.FetchedAt = f.Now
		if payload, jerr := json.Marshal(rs); jerr == nil {
			// A cache write failure is not a lookup failure: the answer is
			// in hand, only the next run pays for it again - and is told.
			if cerr := f.Cache.PutReleases(key, payload, f.Now); cerr != nil {
				f.problem("cache: %s: %v", key, cerr)
			}
		}
		f.recordFirstSeen(key, rs)
	}
	if err != nil && stale != nil {
		// Stale-while-revalidate: an old answer beats no answer, as long
		// as the plan says so. The warning is what keeps a registry that
		// has been down for a week from looking like a quiet one.
		stale.FromCache = true
		return Result{Ref: ref, Releases: stale, Warning: &model.Warning{
			Stage: "lookup",
			Msg: fmt.Sprintf("%s via %s: %v; using releases cached at %s",
				ref.PackageName, ref.Datasource, err, stale.FetchedAt.UTC().Format(time.RFC3339)),
		}}
	}
	if err != nil {
		// Same shape whether the datasource is missing or failed: a
		// ReleaseSet with Err, so the planner writes "lookup failed: ..."
		// and the dependency is visibly not looked up, rather than
		// silently absent from the results.
		res := Result{
			Ref: ref,
			Releases: &model.ReleaseSet{
				PackageName: ref.PackageName, Datasource: ref.Datasource, Err: err.Error(),
			},
		}
		// A datasource that declines by design - a registry it cannot
		// name - is a recorded decision, not a failure worth a warning
		// on every run.
		var declined *DeclinedError
		if !errors.As(err, &declined) {
			res.Warning = &model.Warning{
				Stage: "lookup",
				Msg:   fmt.Sprintf("%s via %s: %v", ref.PackageName, ref.Datasource, err),
			}
		}
		return res
	}
	return Result{Ref: ref, Releases: rs}
}

// Digest answers the content digest of one version through the dependency's
// datasource, cached like a release set: a tag that moved is a digest
// update, and the cache TTL bounds how late that is noticed. A datasource
// that offers no digests is an error, not an empty answer - the planner
// must not write a value whose digest it could not learn.
func (f *Fetcher) Digest(ctx context.Context, d model.Dependency, version string) (string, error) {
	ref := RefOf(d)
	key := "digest\x00" + ref.Key() + "\x00" + version
	if f.Cache != nil && (f.Bypass == nil || !f.Bypass(ref)) {
		if payload, fresh, err := f.Cache.GetReleases(key, f.ttl(), f.Now); err == nil && fresh && len(payload) > 0 {
			return string(payload), nil
		}
	}
	ds, err := f.Registry.Get(ref.Datasource)
	if err != nil {
		return "", err
	}
	src, ok := ds.(DigestSource)
	if !ok {
		return "", fmt.Errorf("%s offers no digests", ref.Datasource)
	}
	digest, err := src.Digest(ctx, ref, version)
	if err != nil {
		return "", err
	}
	if digest == "" {
		return "", fmt.Errorf("%s answered an empty digest for %s %s", ref.Datasource, ref.PackageName, version)
	}
	if f.Cache != nil {
		if cerr := f.Cache.PutReleases(key, []byte(digest), f.Now); cerr != nil {
			f.problem("cache: %s: %v", key, cerr)
		}
	}
	return digest, nil
}

func (f *Fetcher) ttl() time.Duration {
	if f.TTL > 0 {
		return f.TTL
	}
	return time.Hour
}

// recordFirstSeen stamps every release with the moment this cache first
// saw it. A release that carries no timestamp from its datasource gets its
// age from here; one that does keeps both, and the planner prefers the
// datasource's.
func (f *Fetcher) recordFirstSeen(key string, rs *model.ReleaseSet) {
	if f.Cache == nil {
		return
	}
	versions := make([]string, 0, len(rs.Releases))
	for _, r := range rs.Releases {
		versions = append(versions, r.Version)
	}
	seen, err := f.Cache.FirstSeenAll(key, versions, f.Now)
	if err != nil {
		return
	}
	for i := range rs.Releases {
		rs.Releases[i].FirstSeen = seen[rs.Releases[i].Version]
	}
}

// RefOf builds the lookup identity for a dependency. PackageName wins over
// DepName when set - the custom regex manager fills it from a template
// precisely so the thing looked up can differ from the thing displayed.
func RefOf(d model.Dependency) Ref {
	name := d.PackageName
	if name == "" {
		name = d.DepName
	}
	return Ref{Datasource: d.Datasource, PackageName: name, RegistryURLs: d.RegistryURLs}
}
