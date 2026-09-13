// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package helmds implements the helm datasource: a Helm chart repository's
// index.yaml, which lists every version of every chart the repository
// serves in one document.
//
// The index format is Helm's own public specification
// (helm.sh/docs/topics/chart_repository), not Renovate internals: nothing
// here is copied out of the Renovate tree, only observed against a real
// index (an excerpt of artifacthub.io's mirror of
// https://grafana.github.io/helm-charts) and against the documented shape.
// See CLAUDE.md's licensing section for why that distinction matters in
// this repository.
//
// Unlike the GitLab and GitHub datasources, a chart has no default
// registry: Renovate requires registryUrls for helm, and so does this
// datasource - there is no host to fall back to.
package helmds

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/yamlx"
)

// Datasource is the helm datasource.
type Datasource struct {
	client *httpx.Client

	mu sync.Mutex
	// indexes memoises one repository's index.yaml per Datasource instance:
	// several charts pinned against the same repository - the common case,
	// since a chart repository like grafana's carries dozens of charts -
	// share one fetch rather than issuing one per chart.
	indexes map[string]func() (map[string]any, error)
}

// New returns a helm datasource using client to fetch index.yaml documents.
func New(client *httpx.Client) *Datasource {
	return &Datasource{client: client, indexes: map[string]func() (map[string]any, error){}}
}

func (d *Datasource) Name() string { return "helm" }

// DefaultVersioning names semver. Renovate's own "helm" versioning scheme
// is semver with loose parsing (a chart version that is not quite valid
// semver is still accepted); pinup has no such scheme yet, so this answers
// with plain semver until a chart in the estate needs the looser parsing -
// at which point the difference will be measured, not assumed.
func (d *Datasource) DefaultVersioning() string { return "semver" }

// Releases fetches the chart's entry from its repository's index.yaml.
// ref.RegistryURLs[0] is the repository; ref.PackageName is the chart name.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	if len(ref.RegistryURLs) == 0 || ref.RegistryURLs[0] == "" {
		// A helm lookup without a repository cannot be answered: unlike
		// gitlab-tags or github-releases, there is no host this datasource
		// defaults to.
		return nil, fmt.Errorf("helm: no chart repository configured for %s", ref.PackageName)
	}
	repo := strings.TrimRight(ref.RegistryURLs[0], "/")

	entries, err := d.index(ctx, repo)
	if err != nil {
		return nil, err
	}
	raw, ok := entries[ref.PackageName]
	if !ok {
		return nil, fmt.Errorf("helm: chart %q not found in repository %s", ref.PackageName, repo)
	}
	versions, ok := raw.([]any)
	if !ok || len(versions) == 0 {
		return nil, fmt.Errorf("helm: chart %q not found in repository %s", ref.PackageName, repo)
	}

	rs := &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  "helm",
		RegistryURL: repo,
		// Measured on the excerpt, not on Renovate's own behaviour: the
		// first (newest) entry's sources[0] wins over home, which wins
		// over nothing at all.
		SourceURL: sourceURLOf(versions[0]),
	}
	for _, v := range versions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		rel := model.Release{
			Version: asString(m["version"]),
			Digest:  asString(m["digest"]),
		}
		if created := asString(m["created"]); created != "" {
			if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
				rel.Timestamp = t
			}
			// A missing or unparsable created field leaves Timestamp at
			// its zero value; the planner falls back to first-seen age.
		}
		rs.Releases = append(rs.Releases, rel)
	}
	return rs, nil
}

// sourceURLOf reads sources[0], falling back to home, from one chart
// version entry.
func sourceURLOf(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if sources, ok := m["sources"].([]any); ok && len(sources) > 0 {
		if s := asString(sources[0]); s != "" {
			return s
		}
	}
	return asString(m["home"])
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// index returns repo's parsed entries map, fetching index.yaml at most
// once per repository for the lifetime of this Datasource. The map is
// guarded by mu; each entry is itself a memoised computation, so concurrent
// callers for the same repository block on one fetch instead of racing.
func (d *Datasource) index(ctx context.Context, repo string) (map[string]any, error) {
	d.mu.Lock()
	fn, ok := d.indexes[repo]
	if !ok {
		client := d.client
		fn = sync.OnceValues(func() (map[string]any, error) { return fetchIndex(ctx, client, repo) })
		d.indexes[repo] = fn
	}
	d.mu.Unlock()
	return fn()
}

// fetchIndex retrieves and parses one repository's index.yaml.
func fetchIndex(ctx context.Context, client *httpx.Client, repo string) (map[string]any, error) {
	resp, err := client.Get(ctx, repo+"/index.yaml", httpx.ReqOptions{})
	if err != nil {
		return nil, fmt.Errorf("helm: index of %s: %w", repo, err)
	}
	var doc map[string]any
	if err := yamlx.Unmarshal(resp.Body, &doc); err != nil {
		return nil, fmt.Errorf("helm: index of %s: %w", repo, err)
	}
	entries, ok := doc["entries"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("helm: index of %s carries no entries", repo)
	}
	return entries, nil
}
