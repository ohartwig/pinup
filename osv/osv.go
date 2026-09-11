// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package osv asks OSV (https://osv.dev), a public vulnerability database, which
// advisories affect a dependency's current version and what the lowest fixing
// version is. Nothing here is derived from Renovate's implementation - the
// protocol is documented (https://ossf.github.io/osv-schema/) and OSV's own API
// reference, and behaviour is re-implemented from those, and from what was
// measured of Renovate's decisions over the same lookups. See CLAUDE.md's
// licensing section for why that distinction matters in this repository.
//
// # Why this package does not use httpx
//
// httpx.Client offers GET only, with a fixed set of conditional headers and no
// way to attach a per-request body. Asking OSV which advisories apply to a
// batch of packages is a POST with a JSON body ("/v1/querybatch"); fetching one
// advisory's full record is a plain GET ("/v1/vulns/{id}"). Rather than bend
// httpx to a shape it was not built for, this package takes an
// http.RoundTripper directly (so tests can still use fake/harness) and does its
// own minimal, context-aware request helper - the same way platform/gitlab and
// datasource/dockerds do for their own protocols.
//
// # Layer
//
// osv is L2: it may import versioning (L1) but nothing from lookup, planner
// or datasource/* - it knows nothing about how a dependency's current
// version was extracted, only what version string and versioning scheme it
// was asked about.
package osv

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// defaultBase is OSV's public API. A test or an air-gapped mirror overrides
// it via Client.Base.
const defaultBase = "https://api.osv.dev"

// maxBatch is OSV's own limit on queries per querybatch request (measured
// against the documented API). A larger set is split into several requests.
const maxBatch = 1000

// bodyLimit caps how much of a response this package reads into memory. A
// single advisory document is at most a few tens of kilobytes; a much larger
// response is a sign something is wrong, not a legitimate answer to buffer
// whole.
const bodyLimit = 8 << 20

// Store caches advisory documents by "id|modified", so an advisory already
// seen - by this run or an earlier one - is not fetched again. A nil Store
// on Client means every advisory is fetched fresh every call.
type Store interface {
	Get(key string) (payload []byte, ok bool)
	Put(key string, payload []byte)
}

// Query is one dependency to check: its current version, in the versioning
// scheme it was extracted under.
type Query struct {
	Datasource  string
	PackageName string
	Version     string
	Versioning  string
}

// Advisory is one OSV record that affects a Query's current version.
type Advisory struct {
	ID        string
	Aliases   []string
	Summary   string
	Severity  string
	Published time.Time
	Modified  time.Time
	// Fixed is the lowest version, in the range that contains the queried
	// version, that is no longer affected. It is empty when the advisory
	// affects the queried version but names no fix for that range.
	Fixed string
}

// Finding is one Query's outcome, in the order Check was asked about it.
type Finding struct {
	Query Query
	// Ecosystem is the OSV ecosystem name the query was resolved to, or ""
	// when the datasource has none - a dependency this package does not
	// query at all, which is not the same thing as "no advisories".
	Ecosystem string
	// Advisories are every OSV record whose affected range covers the
	// queried version, for the package and ecosystem actually queried.
	Advisories []Advisory
	// Bound is the highest Fixed among Advisories, or "" when none of them
	// name a fix for the range that contains the current version.
	Bound string
	// Warnings records a query that was skipped, and an advisory that could
	// not be fetched, without failing the whole call.
	Warnings []string
}

// Client asks OSV about a batch of packages and fetches the advisories
// behind the answer.
type Client struct {
	// Transport is the underlying http.RoundTripper. nil means
	// http.DefaultTransport.
	Transport http.RoundTripper
	// Base is OSV's API root. "" means the public instance.
	Base string
	// Store, when set, is consulted before fetching an advisory and filled
	// in after - keyed "id|modified", so a record that has not changed is
	// never fetched twice, across calls or across processes that share it.
	Store Store
}

func (c *Client) base() string { return strings.TrimRight(cmp.Or(c.Base, defaultBase), "/") }

func (c *Client) httpClient() *http.Client {
	return &http.Client{Transport: cmp.Or(c.Transport, http.DefaultTransport)}
}

// ecosystems maps a pinup datasource name to the OSV ecosystem name it
// corresponds to. npm -> "npm" and packagist -> "Packagist" are measured,
// against the queries Renovate actually sent for
// lodash/guzzlehttp/symfony (osvVulnerabilityAlerts: true). The rest are
// OSV's documented, canonical ecosystem names
// (https://ossf.github.io/osv-schema/#appendix-ecosystems) and are
// unmeasured against Renovate's own choices.
var ecosystems = map[string]string{
	"npm":       "npm",
	"packagist": "Packagist",
	"go":        "Go",
	"pypi":      "PyPI",
	"maven":     "Maven",
	"crate":     "crates.io",
	"rubygems":  "RubyGems",
	"nuget":     "NuGet",
}

// Ecosystem maps a pinup datasource name to the OSV ecosystem it corresponds
// to, or "" when OSV has no ecosystem for that datasource at all (docker,
// gitlab-*, github-*, terraform-*, custom.*, apk, ...): such a dependency is
// not queried, which Finding.Ecosystem reports as "" so a caller can tell it
// apart from "queried, no advisories found".
func Ecosystem(datasource string) string { return ecosystems[datasource] }

// Check asks OSV which advisories affect each query's current version, and
// what the lowest fixing version is for the range that contains it.
//
// Findings come back one per query, in the same order, including the
// queries this package never sent to OSV at all: one whose datasource has no
// OSV ecosystem (Ecosystem "" ), and one whose current value is not a single
// version under its own versioning scheme - a range such as "^1.2.5", which
// Renovate itself does not run a vulnerability lookup against either.
func (c *Client) Check(ctx context.Context, vs versioning.Registry, queries []Query) ([]Finding, error) {
	findings := make([]Finding, len(queries))

	// queued remembers, for each query actually sent to OSV, the version
	// string that was sent (which may differ from Query.Version - Go's
	// leading "v" is stripped) and the scheme to evaluate its ranges with.
	type queued struct {
		findingIdx int
		version    string
		scheme     versioning.Versioning
	}
	var toQuery []queued
	var batch []batchQuery

	for i, q := range queries {
		eco := Ecosystem(q.Datasource)
		findings[i] = Finding{Query: q, Ecosystem: eco}
		if eco == "" {
			continue // no OSV ecosystem for this datasource: not queried.
		}
		scheme, err := vs.Get(q.Versioning)
		if err != nil {
			findings[i].Warnings = append(findings[i].Warnings,
				fmt.Sprintf("osv: %s: %v, not queried", q.PackageName, err))
			continue
		}
		version := q.Version
		if eco == "Go" {
			// OSV's Go advisories are keyed on the module version without
			// its leading "v" (unmeasured - Go was not part of the
			// Renovate capture this package's protocol was measured
			// against).
			version = strings.TrimPrefix(version, "v")
		}
		if !scheme.IsVersion(version) {
			// A range such as "^1.2.5" is not a single version to look up;
			// Renovate itself skips these ("Skipping vulnerability lookup
			// for package ... due to unsupported version ...").
			findings[i].Warnings = append(findings[i].Warnings,
				fmt.Sprintf("osv: skipping vulnerability lookup for %s due to unsupported version %s", q.PackageName, q.Version))
			continue
		}
		toQuery = append(toQuery, queued{findingIdx: i, version: version, scheme: scheme})
		batch = append(batch, batchQuery{Package: pkgRef{Name: q.PackageName, Ecosystem: eco}, Version: version})
	}

	results, err := c.queryBatch(ctx, batch)
	if err != nil {
		return nil, err
	}

	docs, fetchErr := c.fetchAdvisories(ctx, results)

	for i, qd := range toQuery {
		f := &findings[qd.findingIdx]
		var bestFixed string
		for _, ref := range results[i].Vulns {
			key := ref.ID + "|" + ref.Modified
			if err, failed := fetchErr[key]; failed {
				f.Warnings = append(f.Warnings, fmt.Sprintf("osv: fetch advisory %s: %v", ref.ID, err))
				continue
			}
			adv, affected := evaluateAdvisory(docs[key], f.Query.PackageName, f.Ecosystem, qd.version, qd.scheme)
			if !affected {
				continue
			}
			f.Advisories = append(f.Advisories, adv)
			if adv.Fixed == "" {
				continue
			}
			if bestFixed == "" || qd.scheme.Compare(adv.Fixed, bestFixed) > 0 {
				bestFixed = adv.Fixed
			}
		}
		f.Bound = bestFixed
	}
	return findings, nil
}

// queryBatch sends every query OSV needs to answer, split into requests of
// at most maxBatch queries (OSV's own limit), and returns the results
// concatenated back into request order. A batch of zero queries makes no
// request at all - a run whose dependencies map to no OSV ecosystem must not
// touch the network.
func (c *Client) queryBatch(ctx context.Context, queries []batchQuery) ([]batchResult, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	out := make([]batchResult, 0, len(queries))
	for start := 0; start < len(queries); start += maxBatch {
		end := min(start+maxBatch, len(queries))
		var resp batchResponse
		if err := c.postJSON(ctx, "/v1/querybatch", batchRequest{Queries: queries[start:end]}, &resp); err != nil {
			return nil, fmt.Errorf("osv: querybatch: %w", err)
		}
		out = append(out, resp.Results...)
	}
	return out, nil
}

// fetchAdvisories fetches every distinct advisory named across results,
// once each - within this call by a map, and across calls through Store,
// both keyed "id|modified". It returns the decoded documents and, for a
// fetch that failed, the error to report instead of a document.
func (c *Client) fetchAdvisories(ctx context.Context, results []batchResult) (map[string]*vulnDoc, map[string]error) {
	docs := make(map[string]*vulnDoc)
	errs := make(map[string]error)
	for _, res := range results {
		for _, ref := range res.Vulns {
			key := ref.ID + "|" + ref.Modified
			if _, done := docs[key]; done {
				continue
			}
			if _, done := errs[key]; done {
				continue
			}
			doc, err := c.fetchAdvisory(ctx, key, ref.ID)
			if err != nil {
				errs[key] = err
				continue
			}
			docs[key] = doc
		}
	}
	return docs, errs
}

// fetchAdvisory answers one advisory by id, consulting Store before making a
// request and filling it in after a successful one.
func (c *Client) fetchAdvisory(ctx context.Context, key, id string) (*vulnDoc, error) {
	if c.Store != nil {
		if payload, ok := c.Store.Get(key); ok {
			var doc vulnDoc
			if err := json.Unmarshal(payload, &doc); err == nil {
				return &doc, nil
			}
		}
	}
	body, err := c.getJSON(ctx, "/v1/vulns/"+id)
	if err != nil {
		return nil, err
	}
	var doc vulnDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode advisory %s: %w", id, err)
	}
	if c.Store != nil {
		c.Store.Put(key, body)
	}
	return &doc, nil
}

// postJSON sends payload as a JSON body and decodes the JSON response into
// out. A non-2xx response is an error; this package does not need to
// distinguish status codes the way httpx.StatusError lets a datasource do,
// since OSV offers no per-package retry story a caller could act on here.
func (c *Client) postJSON(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// getJSON fetches path and returns the raw response body, so a caller that
// also wants to cache the exact bytes (Store) does not have to re-marshal
// what it just decoded.
func (c *Client) getJSON(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	var body []byte
	if err := c.do(req, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// do performs one request and decodes its body into out. out may be *[]byte,
// which skips JSON decoding and returns the raw body.
func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return fmt.Errorf("read response from %s: %w", req.URL.Host, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d from %s", resp.StatusCode, req.URL)
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = respBody
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response from %s: %w", req.URL.Host, err)
	}
	return nil
}
