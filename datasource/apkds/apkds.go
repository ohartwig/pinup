// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package apkds is the native apk datasource: it reads APKINDEX.tar.gz
// files straight from the repositories `apk` itself resolves against, and
// answers a package's versions from them.
//
// It replaces the Node sidecar the Renovate runner needed because Renovate
// has no apk datasource, and it keeps that sidecar's two rules, measured
// against it (docs/tasks.md P1a.9):
//
//   - installable is what at least one configured mirror carries, per
//     architecture - the union across mirrors, which is how apk decides;
//   - a version has to exist on every architecture the images are built
//     for - the intersection across architectures - because a pin one
//     architecture does not know blows the build up on the other.
//
// A View is one such rule set under one datasource name: custom.wolfi is
// the union of the public Wolfi repository and the estate's mirror,
// custom.koh-apk the estate's own packages only, which apk reaches through a
// tagged repository and must not be satisfied from the public one.
//
// An index that parses to no packages is an error, never an empty answer.
//
// A repository may also publish "<mirror>/withdrawn.json", the versions it
// has withdrawn (koh wolfi-packages, 2026-09-28):
//
//	{"version":1,"withdrawn":[{"package":"grafana-alloy","version":"1.19.2-r6",
//	  "ids":["CVE-2026-33997"],"replacement":"1.20.0-r0","reason":"…",
//	  "date":"2026-09-28T00:00:00Z","source":"auto"}]}
//
// A withdrawn version is left out of the releases even when an index still
// carries it, and the entry travels on the release set so the plan can move
// a dependency off it. A 404 is a repository without a list; any other
// failure is a warning on the release set, never a failed lookup.
package apkds

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ohartwig/pinup/apkindex"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// View names the repositories and architectures a datasource resolves
// against.
type View struct {
	// Mirrors are repository base URLs; "<mirror>/<arch>/APKINDEX.tar.gz"
	// is what is fetched.
	Mirrors []string
	// Arches are the architectures a version must exist on.
	Arches []string
}

// Datasource serves one view under one name.
type Datasource struct {
	name   string
	view   View
	client *httpx.Client

	// Indexes are fetched once per process and shared by every package
	// looked up through this datasource: an index is megabytes, a
	// repository has dozens of pins.
	mu      sync.Mutex
	indexes map[string]*apkindex.Index // url -> index
	errors  map[string]error
	// withdrawn is each mirror's withdrawal list, fetched once: mirror ->
	// package -> entries. lists holds the read error, if any, per mirror.
	withdrawn map[string]map[string][]model.Withdrawal
	lists     map[string]error
}

// New returns a datasource of the given name over view.
func New(name string, client *httpx.Client, view View) *Datasource {
	return &Datasource{name: name, view: view, client: client, indexes: map[string]*apkindex.Index{}, errors: map[string]error{},
		withdrawn: map[string]map[string][]model.Withdrawal{}, lists: map[string]error{}}
}

func (d *Datasource) Name() string { return d.name }

// DefaultVersioning is apk: "1.2.3-r4", the revision compared numerically.
func (d *Datasource) DefaultVersioning() string { return "apk" }

// Releases answers the versions of ref.PackageName that every architecture
// of the view carries on at least one mirror.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	if len(d.view.Mirrors) == 0 || len(d.view.Arches) == 0 {
		return nil, fmt.Errorf("%s: the view names no mirrors or no architectures", d.name)
	}
	var perArch []*apkindex.Index
	for _, arch := range d.view.Arches {
		var mirrors []*apkindex.Index
		for _, m := range d.view.Mirrors {
			idx, err := d.index(ctx, m+"/"+arch+"/APKINDEX.tar.gz")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", d.name, err)
			}
			mirrors = append(mirrors, idx)
		}
		perArch = append(perArch, apkindex.Union(mirrors...))
	}
	common := apkindex.Intersect(perArch...)
	rs := &model.ReleaseSet{PackageName: ref.PackageName, Datasource: d.name, RegistryURL: d.view.Mirrors[0]}
	gone := map[string]bool{}
	var listErrs []string
	for _, m := range d.view.Mirrors {
		entries, err := d.withdrawals(ctx, m)
		if err != nil {
			listErrs = append(listErrs, err.Error())
			continue
		}
		for _, w := range entries[ref.PackageName] {
			if !gone[w.Version] {
				gone[w.Version] = true
				rs.Withdrawn = append(rs.Withdrawn, w)
			}
		}
	}
	rs.WithdrawnErr = strings.Join(listErrs, "; ")
	for _, v := range common.Versions(ref.PackageName) {
		if !gone[v] {
			rs.Releases = append(rs.Releases, model.Release{Version: v})
		}
	}
	rs.NewerStream = newerStream(common, ref.PackageName)
	return rs, nil
}

// index fetches and parses one APKINDEX, once. A failure is remembered
// too: a mirror that answered 503 is not asked again for every pin.
func (d *Datasource) index(ctx context.Context, url string) (*apkindex.Index, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if idx, ok := d.indexes[url]; ok {
		return idx, nil
	}
	if err, ok := d.errors[url]; ok {
		return nil, err
	}
	resp, err := d.client.Get(ctx, url, httpx.ReqOptions{})
	if err != nil {
		d.errors[url] = fmt.Errorf("%s: %w", url, err)
		return nil, d.errors[url]
	}
	idx, err := apkindex.Parse(bytes.NewReader(resp.Body))
	if err != nil {
		d.errors[url] = fmt.Errorf("%s: %w", url, err)
		return nil, d.errors[url]
	}
	d.indexes[url] = idx
	return idx, nil
}

// withdrawnList is withdrawn.json as a repository publishes it.
type withdrawnList struct {
	Version   int `json:"version"`
	Withdrawn []struct {
		model.Withdrawal `json:",inline"`
		Package          string `json:"package"`
	} `json:"withdrawn"`
}

// withdrawals reads one mirror's withdrawal list, once, keyed by package.
// A mirror without one (404) has none; any other failure is returned and
// remembered, so a broken list is reported, not refetched per package.
func (d *Datasource) withdrawals(ctx context.Context, mirror string) (map[string][]model.Withdrawal, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if w, ok := d.withdrawn[mirror]; ok {
		return w, nil
	}
	if err, ok := d.lists[mirror]; ok {
		return nil, err
	}
	url := mirror + "/withdrawn.json"
	byPkg := map[string][]model.Withdrawal{}
	resp, err := d.client.Get(ctx, url, httpx.ReqOptions{})
	if se, ok := errors.AsType[*httpx.StatusError](err); ok && se.StatusCode == http.StatusNotFound {
		d.withdrawn[mirror] = byPkg
		return byPkg, nil
	}
	if err != nil {
		d.lists[mirror] = fmt.Errorf("withdrawal list %s: %w", url, err)
		return nil, d.lists[mirror]
	}
	var list withdrawnList
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		d.lists[mirror] = fmt.Errorf("withdrawal list %s: %w", url, err)
		return nil, d.lists[mirror]
	}
	if list.Version != 1 {
		d.lists[mirror] = fmt.Errorf("withdrawal list %s: version %d, this pinup reads 1", url, list.Version)
		return nil, d.lists[mirror]
	}
	for _, e := range list.Withdrawn {
		if e.Package == "" || e.Version == "" {
			continue
		}
		byPkg[e.Package] = append(byPkg[e.Package], e.Withdrawal)
	}
	d.withdrawn[mirror] = byPkg
	return byPkg, nil
}

// seriesName splits a package name of a series - "kubectl-1.36",
// "mysql-9.7-client", "valkey-9.1-cli", "php-8.5" - into the base, the
// series and the suffix. A name without a series ("valkey-cli", "npm")
// is base and suffix alone, so the family it may have moved into
// ("valkey-9.1-cli", "npm-12") is still found.
var seriesRE = regexp.MustCompile(`^([a-z][a-z0-9+]*(?:-[a-z][a-z0-9+]*)*?)(?:-(\d+(?:\.\d+)*))?((?:-[a-z][a-z0-9]*)*)$`)

func seriesName(pkg string) (base, series, suffix string, ok bool) {
	m := seriesRE.FindStringSubmatch(pkg)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// seriesLess orders two dotted series numerically: 9.7 < 9.10, "" < any.
func seriesLess(a, b string) bool {
	if a == "" || b == "" {
		return a == "" && b != ""
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

// newerStream finds, in the same index, the highest series of the family
// the package belongs to, when it is higher than the package's own. Wolfi
// retires a series by throwing its recipe out of the tree: the index goes
// on serving the last build, its security feed goes on naming fixes that
// are never built, and a pin on that series ages without a single
// warning from its own releases (mariadb-11.8-client, 2026-09-18). The
// index is the one place the whole family is visible.
func newerStream(idx *apkindex.Index, pkg string) *model.Stream {
	base, series, suffix, ok := seriesName(pkg)
	if !ok {
		return nil
	}
	best := ""
	bestSeries := series
	for _, other := range idx.Packages() {
		if other == pkg {
			continue
		}
		ob, os, osuf, ok := seriesName(other)
		if !ok || ob != base || osuf != suffix || os == "" || !seriesLess(bestSeries, os) {
			continue
		}
		best, bestSeries = other, os
	}
	if best == "" {
		return nil
	}
	return &model.Stream{Package: best, Versions: idx.Versions(best)}
}
