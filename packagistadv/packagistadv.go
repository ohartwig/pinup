// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package packagistadv reads Packagist's security-advisory API
// (https://packagist.org/apidoc#list-security-advisories), the source
// `composer audit` reads, and answers which advisories affect a composer
// package at a version and the lowest release that is no longer affected.
//
// It exists beside osv because OSV learns of PHP advisories late. Packagist
// aggregates FriendsOfPHP/security-advisories, which TYPO3's own security
// team feeds within a day or two of a TYPO3-CORE-SA; OSV and GHSA follow
// days to weeks later, measured on 2026-10-05: SA-2026-015 +3.4 days,
// SA-2026-020 +48 days, SA-2026-021 +21 days, and SA-2026-022
// (CVE-2026-77132, typo3/cms-backend) not in OSV at all four weeks after it
// was published.
//
// An advisory carries no fixed version, only the affected range as a
// composer constraint whose alternatives are separated by "|"
// (">=13.0.0,<13.4.18|>=12.0.0,<12.4.37"). The fix is derived: the lowest
// release above the queried version that the range no longer covers, or,
// without a release set, the exclusive upper bound of the alternative that
// covers the queried version when that bound is itself unaffected.
//
// # Why this package does not use httpx
//
// The same reason osv gives: it takes an http.RoundTripper directly so a
// test can hand it a fake transport, and does its own minimal request.
//
// # Layer
//
// packagistadv is L2 beside osv: it imports versioning (L1) and nothing from
// lookup, planner or datasource/*. It knows package names and versions, not
// how they were extracted.
package packagistadv

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ohartwig/pinup/versioning"
)

// DefaultBase is Packagist's public API. A test or a mirror overrides it via
// Client.Base.
const DefaultBase = "https://packagist.org"

// batch is how many package names one request carries. GET keeps the
// request cacheable and the URL of forty composer names well under any
// limit a proxy enforces.
const batch = 40

// bodyLimit caps a response read into memory: an answer for forty packages
// is a few hundred kilobytes, the whole of Packagist's list a few megabytes.
const bodyLimit = 16 << 20

// Advisory is one Packagist security advisory for one package.
type Advisory struct {
	// ID is Packagist's own id, "PKSA-...".
	ID string
	// CVE is the CVE id, "" when the advisory has none yet.
	CVE string
	// GHSA are the GitHub advisory ids among its sources.
	GHSA     []string
	Package  string
	Title    string
	Severity string
	// Affected is the composer constraint the advisory covers, its
	// alternatives separated by "|".
	Affected string
	Reported time.Time
}

// Aliases are every other id the advisory is known under: its CVE and its
// GHSA ids, in that order. OSV findings are deduplicated against these.
func (a Advisory) Aliases() []string {
	var out []string
	if a.CVE != "" {
		out = append(out, a.CVE)
	}
	return append(out, a.GHSA...)
}

// Client asks Packagist for advisories.
type Client struct {
	// Transport is the underlying http.RoundTripper. nil means
	// http.DefaultTransport.
	Transport http.RoundTripper
	// Base is the API root. "" means DefaultBase.
	Base string
}

// ForPackages answers every advisory Packagist lists for the named packages,
// keyed by package name. Names are asked in batches; a package without
// advisories is absent from the map.
func (c *Client) ForPackages(ctx context.Context, names []string) (map[string][]Advisory, error) {
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	out := map[string][]Advisory{}
	for start := 0; start < len(names); start += batch {
		q := url.Values{}
		for _, n := range names[start:min(start+batch, len(names))] {
			q.Add("packages[]", n)
		}
		if err := c.get(ctx, q, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdatedSince answers every advisory created or changed after since, across
// all of Packagist - about 1,900 a year, so the watch asks for the whole
// window rather than per package.
func (c *Client) UpdatedSince(ctx context.Context, since time.Time) (map[string][]Advisory, error) {
	out := map[string][]Advisory{}
	q := url.Values{"updatedSince": {strconv.FormatInt(since.Unix(), 10)}}
	if err := c.get(ctx, q, out); err != nil {
		return nil, err
	}
	return out, nil
}

// wireAdvisory is one advisory as Packagist writes it.
type wireAdvisory struct {
	AdvisoryID       string `json:"advisoryId"`
	PackageName      string `json:"packageName"`
	Title            string `json:"title"`
	CVE              string `json:"cve"`
	AffectedVersions string `json:"affectedVersions"`
	ReportedAt       string `json:"reportedAt"`
	Severity         string `json:"severity"`
	Sources          []struct {
		Name     string `json:"name"`
		RemoteID string `json:"remoteId"`
	} `json:"sources"`
}

func (c *Client) get(ctx context.Context, q url.Values, out map[string][]Advisory) error {
	u := strings.TrimRight(cmp.Or(c.Base, DefaultBase), "/") + "/api/security-advisories/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("packagist advisories: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: cmp.Or(c.Transport, http.DefaultTransport)}).Do(req)
	if err != nil {
		return fmt.Errorf("packagist advisories: request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return fmt.Errorf("packagist advisories: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("packagist advisories: unexpected status %d", resp.StatusCode)
	}
	// "advisories" is an object keyed by package name, and an empty
	// array when nothing matched - measured on 2026-10-05 against an
	// updatedSince window without changes.
	var doc struct {
		Advisories jsontext.Value `json:"advisories"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("packagist advisories: decode: %w", err)
	}
	if t := strings.TrimSpace(string(doc.Advisories)); t == "" || t == "[]" || t == "null" {
		return nil
	}
	var byPkg map[string][]wireAdvisory
	if err := json.Unmarshal(doc.Advisories, &byPkg); err != nil {
		return fmt.Errorf("packagist advisories: decode: %w", err)
	}
	for name, list := range byPkg {
		for _, w := range list {
			a := Advisory{ID: w.AdvisoryID, CVE: w.CVE, Package: cmp.Or(w.PackageName, name), Title: w.Title, Severity: w.Severity, Affected: w.AffectedVersions}
			// Packagist writes "2026-09-08 09:00:00", in UTC.
			if t, err := time.Parse(time.DateTime, w.ReportedAt); err == nil {
				a.Reported = t.UTC()
			}
			for _, s := range w.Sources {
				if strings.HasPrefix(s.RemoteID, "GHSA-") && !slices.Contains(a.GHSA, s.RemoteID) {
					a.GHSA = append(a.GHSA, s.RemoteID)
				}
			}
			out[name] = append(out[name], a)
		}
	}
	return nil
}

// alternatives splits an affected-versions constraint into the ranges it
// ORs together. Packagist writes a single "|"; composer itself also reads
// "||", so both are taken.
func alternatives(affected string) []string {
	var out []string
	for _, part := range strings.Split(strings.ReplaceAll(affected, "||", "|"), "|") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Affects reports whether the advisory covers version under v.
func Affects(a Advisory, version string, v versioning.Versioning) bool {
	_, ok := covering(a, version, v)
	return ok
}

func covering(a Advisory, version string, v versioning.Versioning) (string, bool) {
	for _, alt := range alternatives(a.Affected) {
		if v.Satisfies(version, alt) {
			return alt, true
		}
	}
	return "", false
}

// Fixed is the lowest release above version the advisory no longer covers.
// With a release set it is chosen from releases (prereleases only when
// version is one); without, it is the exclusive upper bound ("<X") of the
// alternative that covers version, when the advisory does not cover X too.
// "" means no fix could be named.
func Fixed(a Advisory, version string, v versioning.Versioning, releases []string) string {
	alt, ok := covering(a, version, v)
	if !ok {
		return ""
	}
	if len(releases) > 0 {
		pre := !v.IsStable(version)
		var best string
		for _, r := range releases {
			if !v.IsVersion(r) || v.Compare(r, version) <= 0 || (!pre && !v.IsStable(r)) || Affects(a, r, v) {
				continue
			}
			if best == "" || v.Compare(r, best) < 0 {
				best = r
			}
		}
		return best
	}
	for _, part := range strings.Split(alt, ",") {
		bound, isUpper := strings.CutPrefix(strings.TrimSpace(part), "<")
		if !isUpper || strings.HasPrefix(bound, "=") {
			continue
		}
		bound = strings.TrimSpace(bound)
		if v.IsVersion(bound) && !Affects(a, bound, v) {
			return bound
		}
	}
	return ""
}
