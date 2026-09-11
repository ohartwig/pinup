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
	"fmt"
	"sort"
	"sync"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// Ref is what a datasource needs to find a package.
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

// Fetcher runs lookups with dedupe and bounded parallelism.
type Fetcher struct {
	Registry Registry
	// Parallel bounds the number of lookups in flight. Zero means eight.
	Parallel int
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
	ds, err := f.Registry.Get(ref.Datasource)
	if err != nil {
		return Result{Ref: ref, Warning: &model.Warning{
			Stage: "lookup", Msg: fmt.Sprintf("%s: %v", ref.PackageName, err),
		}}
	}
	rs, err := ds.Releases(ctx, ref)
	if err != nil {
		return Result{
			Ref: ref,
			Releases: &model.ReleaseSet{
				PackageName: ref.PackageName, Datasource: ref.Datasource, Err: err.Error(),
			},
			Warning: &model.Warning{
				Stage: "lookup",
				Msg:   fmt.Sprintf("%s via %s: %v", ref.PackageName, ref.Datasource, err),
			},
		}
	}
	return Result{Ref: ref, Releases: rs}
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
