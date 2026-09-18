// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package nodeds implements the node-version datasource: the releases of
// Node.js itself, as nodejs.org publishes them at dist/index.json - one
// document, every release ever, with its date and whether it is an LTS
// line. What the npm manager's engines.node, .nvmrc and .node-version
// name (measured 2026-09-15: four dependencies in the estate went without
// a lookup for want of it).
package nodeds

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// Name is the datasource's name in a configuration.
const Name = "node-version"

// defaultIndex is where nodejs.org lists every release.
const defaultIndex = "https://nodejs.org/dist/index.json"

// Datasource serves Node.js releases from the index.
type Datasource struct {
	client *httpx.Client
	index  string

	mu   sync.Mutex
	once map[string]*sync.Once
	set  map[string]*model.ReleaseSet
	err  map[string]error
}

// New returns the datasource; an empty index means nodejs.org's.
func New(client *httpx.Client, index string) *Datasource {
	if index == "" {
		index = defaultIndex
	}
	return &Datasource{client: client, index: index, once: map[string]*sync.Once{}, set: map[string]*model.ReleaseSet{}, err: map[string]error{}}
}

func (d *Datasource) Name() string { return Name }

// DefaultVersioning is "node": Renovate's scheme for Node.js, which pinup
// serves as npm's - Node.js versions are plain semver and engines ranges
// are npm ranges.
func (d *Datasource) DefaultVersioning() string { return "node" }

// Releases reads the index once per process - it is one document for
// every package name, and "node" is the only name that reaches here.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	index := d.index
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		index = strings.TrimRight(ref.RegistryURLs[0], "/") + "/index.json"
	}
	d.mu.Lock()
	once, ok := d.once[index]
	if !ok {
		once = &sync.Once{}
		d.once[index] = once
	}
	d.mu.Unlock()
	once.Do(func() {
		rs, err := d.fetch(ctx, index)
		d.mu.Lock()
		d.set[index], d.err[index] = rs, err
		d.mu.Unlock()
	})
	d.mu.Lock()
	rs, err := d.set[index], d.err[index]
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := *rs
	out.PackageName = ref.PackageName
	return &out, nil
}

func (d *Datasource) fetch(ctx context.Context, index string) (*model.ReleaseSet, error) {
	resp, err := d.client.Get(ctx, index, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("node-version: %s: %w", index, err)
	}
	var items []struct {
		Version string          `json:"version"`
		Date    string          `json:"date"`
		LTS     json.RawMessage `json:"lts"`
	}
	if err := json.Unmarshal(resp.Body, &items); err != nil {
		return nil, fmt.Errorf("node-version: %s: %w", index, err)
	}
	rs := &model.ReleaseSet{Datasource: Name, RegistryURL: index, SourceURL: "https://github.com/nodejs/node"}
	for _, it := range items {
		r := model.Release{Version: strings.TrimPrefix(it.Version, "v")}
		if t, err := time.Parse("2006-01-02", it.Date); err == nil {
			r.Timestamp = t.UTC()
		}
		rs.Releases = append(rs.Releases, r)
	}
	if len(rs.Releases) == 0 {
		return nil, fmt.Errorf("node-version: %s lists no releases", index)
	}
	return rs, nil
}
