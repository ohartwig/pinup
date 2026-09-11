// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package packagist implements the packagist datasource against the
// Composer repository protocol - not against Packagist.org's specific
// implementation of it.
//
// The protocol is a public specification (packagist.org/apidoc,
// getcomposer.org/doc/05-repositories.md), and any host that speaks it -
// packagist.org itself, or the estate's private GitLab group composer
// registry - answers the same way: a "packages.json" root document naming
// either a Composer 2 metadata-url template or an inline package list.
// Nothing here is copied out of Renovate's tree; see CLAUDE.md's licensing
// section for why that separation matters in this repository.
//
// A dependency's composer.json can declare more than one repository, tried
// in order until one has the package (repositories.go's "canonical" flag
// aside - out of scope here, since pinup does not parse composer.json's
// repository config itself, only receives the resolved list as
// lookup.Ref.RegistryURLs). The estate's own GitLab registry is always
// first in that list and requires a token; a 401/403 there is a hard
// failure naming the registry; only a 404 on the package's own metadata
// means "not here, try the next one".
package packagist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// defaultRegistry is used when a dependency's composer.json declares no
// repositories at all - Composer's own default.
const defaultRegistry = "https://repo.packagist.org"

// minifiedComposer2 is the only "minified" value the Composer 2 protocol
// defines. Anything else is treated as not minified: expanding entries that
// were not actually minified would wrongly carry stale keys forward.
const minifiedComposer2 = "composer/2.0"

// Datasource implements the packagist datasource.
type Datasource struct {
	client *httpx.Client
}

// New returns a packagist datasource using client for every registry a
// dependency names.
func New(client *httpx.Client) *Datasource {
	return &Datasource{client: client}
}

func (d *Datasource) Name() string { return "packagist" }

// DefaultVersioning is composer - Packagist versions are Composer version
// constraints/tags, not plain semver (a leading "v" and branch aliases like
// "dev-main" are both legal composer versions).
func (d *Datasource) DefaultVersioning() string { return "composer" }

// Releases tries every registry in ref.RegistryURLs in order, returning the
// first one that has the package. An empty list means packagist.org alone.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	registries := ref.RegistryURLs
	if len(registries) == 0 {
		registries = []string{defaultRegistry}
	}

	tried := make([]string, 0, len(registries))
	for _, reg := range registries {
		base := strings.TrimRight(reg, "/")
		tried = append(tried, base)

		rs, found, err := d.lookupOne(ctx, base, ref.PackageName)
		if err != nil {
			// A registry that answered but refused (auth) or spoke a
			// protocol this datasource does not implement is a hard
			// failure: falling through here would silently look a
			// first-party package up on the wrong, public registry.
			return nil, err
		}
		if found {
			return rs, nil
		}
	}
	return nil, fmt.Errorf("packagist: %s not found in any registry (tried %s)",
		ref.PackageName, strings.Join(tried, ", "))
}

// lookupOne fetches base's packages.json and dispatches to whichever
// repository format it declares. found is false only for "this registry
// does not have the package" (a 404 on the package's own metadata); every
// other problem is returned as an error.
func (d *Datasource) lookupOne(ctx context.Context, base, pkg string) (*model.ReleaseSet, bool, error) {
	resp, err := d.client.Get(ctx, base+"/packages.json", httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		if ae := authError(base, "packages.json", err); ae != nil {
			return nil, false, ae
		}
		return nil, false, fmt.Errorf("packagist: registry %s: fetching packages.json: %w", base, err)
	}

	var root struct {
		MetadataURL      string          `json:"metadata-url"`
		ProvidersURL     string          `json:"providers-url"`
		ProviderIncludes json.RawMessage `json:"provider-includes"`
		Packages         json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(resp.Body, &root); err != nil {
		return nil, false, fmt.Errorf("packagist: registry %s: parsing packages.json: %w", base, err)
	}

	switch {
	case root.MetadataURL != "":
		return d.lookupP2(ctx, base, pkg, root.MetadataURL)
	case len(root.Packages) > 0 && string(root.Packages) != "null":
		return lookupInline(base, pkg, root.Packages)
	case root.ProvidersURL != "" || len(root.ProviderIncludes) > 0:
		return nil, false, fmt.Errorf(
			"packagist: registry %s uses the Composer 1 providers-url/provider-includes protocol, which pinup does not implement",
			base)
	default:
		return nil, false, fmt.Errorf("packagist: registry %s: packages.json declares no recognised repository format", base)
	}
}

// lookupP2 fetches the Composer 2 metadata-url document for pkg, plus its
// "~dev" companion, and merges both into one release set.
func (d *Datasource) lookupP2(ctx context.Context, base, pkg, metadataURLTemplate string) (*model.ReleaseSet, bool, error) {
	entries, found, err := d.fetchP2(ctx, base, metadataURLTemplate, pkg)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	// The dev variant is a separate document under a "~dev" suffixed
	// package name; a package with no dev branches simply 404s here, which
	// is not "not found" for the package as a whole - the stable metadata
	// already resolved.
	devEntries, devFound, err := d.fetchP2(ctx, base, metadataURLTemplate, pkg+"~dev")
	if err != nil {
		return nil, false, err
	}
	if devFound {
		entries = append(entries, devEntries...)
	}
	return buildReleaseSet(base, pkg, entries), true, nil
}

// fetchP2 fetches and expands one metadata-url document, for either pkg
// itself or its "pkg~dev" variant.
func (d *Datasource) fetchP2(ctx context.Context, base, metadataURLTemplate, pkgVariant string) ([]releaseEntry, bool, error) {
	url := base + strings.ReplaceAll(metadataURLTemplate, "%package%", pkgVariant)
	resp, err := d.client.Get(ctx, url, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		if ae := authError(base, "metadata for "+pkgVariant, err); ae != nil {
			return nil, false, ae
		}
		return nil, false, fmt.Errorf("packagist: registry %s: fetching metadata for %s: %w", base, pkgVariant, err)
	}

	var doc struct {
		Packages map[string][]map[string]json.RawMessage `json:"packages"`
		Minified string                                  `json:"minified"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return nil, false, fmt.Errorf("packagist: registry %s: parsing metadata for %s: %w", base, pkgVariant, err)
	}

	list, ok := doc.Packages[pkgVariant]
	if !ok || len(list) == 0 {
		return nil, false, nil
	}
	if doc.Minified == minifiedComposer2 {
		list = expandMinified(list)
	}

	entries := make([]releaseEntry, 0, len(list))
	for _, raw := range list {
		e, err := decodeEntry(raw)
		if err != nil {
			return nil, false, fmt.Errorf("packagist: registry %s: decoding %s: %w", base, pkgVariant, err)
		}
		entries = append(entries, e)
	}
	return entries, true, nil
}

// lookupInline reads a small repository's packages.json that lists every
// version inline, keyed by version string:
// {"packages": {"vendor/name": {"1.0.0": {...}, "dev-main": {...}}}}.
func lookupInline(base, pkg string, raw json.RawMessage) (*model.ReleaseSet, bool, error) {
	var all map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, false, fmt.Errorf("packagist: registry %s: parsing inline packages: %w", base, err)
	}
	versions, ok := all[pkg]
	if !ok {
		return nil, false, nil
	}

	// Map iteration order is random; sort so the release list order is
	// stable across runs regardless of it.
	keys := slices.Sorted(maps.Keys(versions))

	entries := make([]releaseEntry, 0, len(keys))
	for _, k := range keys {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(versions[k], &fields); err != nil {
			return nil, false, fmt.Errorf("packagist: registry %s: decoding %s@%s: %w", base, pkg, k, err)
		}
		e, err := decodeEntry(fields)
		if err != nil {
			return nil, false, fmt.Errorf("packagist: registry %s: decoding %s@%s: %w", base, pkg, k, err)
		}
		if e.Version == "" {
			e.Version = k
		}
		entries = append(entries, e)
	}
	return buildReleaseSet(base, pkg, entries), true, nil
}

// releaseEntry is one version's worth of fields this datasource cares
// about, after minified expansion.
type releaseEntry struct {
	Version   string
	Timestamp time.Time
	SourceURL string
}

// decodeEntry pulls version, time and source.url out of one (already fully
// expanded) metadata entry. Every entry is kept regardless of stability or
// a "dev-" prefix - CLAUDE.md's layering puts that call with the composer
// versioning scheme, not this datasource.
func decodeEntry(raw map[string]json.RawMessage) (releaseEntry, error) {
	var e releaseEntry
	if v, ok := raw["version"]; ok {
		if err := json.Unmarshal(v, &e.Version); err != nil {
			return e, fmt.Errorf("version: %w", err)
		}
	}
	if v, ok := raw["time"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return e, fmt.Errorf("time: %w", err)
		}
		if s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return e, fmt.Errorf("time %q is not RFC 3339: %w", s, err)
			}
			e.Timestamp = t
		}
	}
	if v, ok := raw["source"]; ok {
		var src struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(v, &src); err != nil {
			return e, fmt.Errorf("source: %w", err)
		}
		e.SourceURL = src.URL
	}
	return e, nil
}

// expandMinified expands a Composer 2 minified entry list in place: every
// entry after the first inherits the keys it does not carry from the
// previous (already expanded) entry.
func expandMinified(list []map[string]json.RawMessage) []map[string]json.RawMessage {
	out := make([]map[string]json.RawMessage, len(list))
	var prev map[string]json.RawMessage
	for i, e := range list {
		merged := make(map[string]json.RawMessage, len(prev)+len(e))
		for k, v := range prev {
			merged[k] = v
		}
		for k, v := range e {
			merged[k] = v
		}
		out[i] = merged
		prev = merged
	}
	return out
}

// buildReleaseSet turns decoded entries into a ReleaseSet. SourceURL is the
// newest entry's source.url: the one with the latest timestamp that carries
// a source at all, falling back to the first entry with a source when no
// entry carries a timestamp.
func buildReleaseSet(base, pkg string, entries []releaseEntry) *model.ReleaseSet {
	rs := &model.ReleaseSet{
		PackageName: pkg,
		Datasource:  "packagist",
		RegistryURL: base,
	}
	var newest time.Time
	for _, e := range entries {
		rs.Releases = append(rs.Releases, model.Release{
			Version:   e.Version,
			Timestamp: e.Timestamp,
		})
		if e.SourceURL == "" {
			continue
		}
		if rs.SourceURL == "" || e.Timestamp.After(newest) {
			rs.SourceURL = e.SourceURL
			newest = e.Timestamp
		}
	}
	return rs
}

// isNotFound reports whether err is a 404 - "this registry does not have
// the package", not a failure worth stopping the search over.
func isNotFound(err error) bool {
	se, ok := errors.AsType[*httpx.StatusError](err)
	return ok && se.StatusCode == http.StatusNotFound
}

// authError turns a 401/403 into an error naming the registry and what was
// being fetched, so a token that cannot read the estate's private registry
// fails loudly instead of quietly falling through to packagist.org. It
// returns nil for anything else, including a 404 (handled by isNotFound)
// and network or 5xx failures (left to the generic wrap at the call site).
func authError(base, what string, err error) error {
	se, ok := errors.AsType[*httpx.StatusError](err)
	if !ok {
		return nil
	}
	if se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden {
		return fmt.Errorf("packagist: registry %s: %d fetching %s - the token cannot read this registry",
			base, se.StatusCode, what)
	}
	return nil
}
