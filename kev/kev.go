// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package kev reads CISA's Known Exploited Vulnerabilities catalog
// (https://www.cisa.gov/known-exploited-vulnerabilities-catalog) and answers
// whether a CVE is in it. An advisory whose CVE is listed there is being
// exploited, not merely possible to exploit, and the run plans its fix before
// everything else (handbook I-237; OWASP DevSecOps Guideline 2-7-4, 3-3-4).
//
// The catalog is one JSON document of a few megabytes, published by CISA and
// changed a few times a week. It is read with a plain GET through httpx and
// kept in the cache for a day; a run whose cached copy is older and that
// cannot reach CISA uses the stale copy and says so, because a day-old list
// of exploited CVEs is still a list of exploited CVEs.
//
// # Layer
//
// kev is L2 beside osv: it imports httpx (L1) and model-free types of its
// own. It knows nothing about dependencies, only CVE ids.
package kev

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/ohartwig/pinup/httpx"
)

// DefaultURL is CISA's published feed. A test or a mirror overrides it via
// Client.URL.
const DefaultURL = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"

// TTL is how long a cached catalog counts as fresh. CISA adds entries a few
// times a week; a day keeps the feed's load at one request per cache and
// day while a new entry reaches the plans within a day.
const TTL = 24 * time.Hour

// cacheKey is the one key the catalog lives under in a Store.
const cacheKey = "kev\x00catalog"

// Store caches the raw catalog. Get reports whether a payload exists and
// whether it is younger than ttl at now.
type Store interface {
	Get(key string, ttl time.Duration, now time.Time) (payload []byte, fresh bool, ok bool)
	Put(key string, payload []byte, now time.Time)
}

// Entry is one catalog record, the fields a plan and a merge request name.
type Entry struct {
	CVE  string
	Name string
	// DateAdded is when CISA listed the CVE; DueDate the remediation date
	// CISA sets for US federal agencies, kept as the catalog writes them
	// (YYYY-MM-DD).
	DateAdded string
	DueDate   string
	// Ransomware is the catalog's knownRansomwareCampaignUse: "Known" or
	// "Unknown".
	Ransomware string
}

// Catalog is the catalog by CVE id, upper case.
type Catalog map[string]Entry

// Match returns the first catalog entry among ids - an advisory's id and
// its aliases, in any case - and whether there is one. Ids that are not
// CVEs (GHSA-…, GO-…) never match: the catalog lists CVEs only.
func (c Catalog) Match(ids ...string) (Entry, bool) {
	for _, id := range ids {
		if e, ok := c[strings.ToUpper(strings.TrimSpace(id))]; ok {
			return e, true
		}
	}
	return Entry{}, false
}

// Client loads the catalog.
type Client struct {
	// HTTP fetches the feed. Required.
	HTTP *httpx.Client
	// URL is the feed; "" means DefaultURL.
	URL string
	// Store, when set, holds the catalog between runs. nil means every
	// Load fetches.
	Store Store
}

// Load returns the catalog: the cached one when it is fresh at now, else a
// fetched one, else - when the fetch fails - a stale cached one together
// with a warning. Only a failed fetch with nothing cached is an error.
func (c *Client) Load(ctx context.Context, now time.Time) (Catalog, []string, error) {
	var stale []byte
	if c.Store != nil {
		if payload, fresh, ok := c.Store.Get(cacheKey, TTL, now); ok {
			if cat, err := Parse(payload); err == nil {
				if fresh {
					return cat, nil, nil
				}
				stale = payload
			}
		}
	}
	url := c.URL
	if url == "" {
		url = DefaultURL
	}
	resp, err := c.HTTP.Get(ctx, url, httpx.ReqOptions{Accept: "application/json"})
	if err == nil {
		var cat Catalog
		if cat, err = Parse(resp.Body); err == nil {
			if c.Store != nil {
				c.Store.Put(cacheKey, resp.Body, now)
			}
			return cat, nil, nil
		}
	}
	if stale != nil {
		cat, _ := Parse(stale)
		return cat, []string{fmt.Sprintf("kev: %v; using the cached catalog older than %s", err, TTL)}, nil
	}
	return nil, nil, fmt.Errorf("kev: %w", err)
}

// feed is the part of CISA's document this package reads.
type feed struct {
	CatalogVersion  string `json:"catalogVersion"`
	Vulnerabilities []struct {
		CVEID      string `json:"cveID"`
		Name       string `json:"vulnerabilityName"`
		DateAdded  string `json:"dateAdded"`
		DueDate    string `json:"dueDate"`
		Ransomware string `json:"knownRansomwareCampaignUse"`
	} `json:"vulnerabilities"`
}

// Parse reads a catalog document. A document without a single CVE is
// refused: an empty catalog would silently drop every exploited mark, and
// that is never what CISA publishes.
func Parse(payload []byte) (Catalog, error) {
	var f feed
	if err := json.Unmarshal(payload, &f); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	cat := make(Catalog, len(f.Vulnerabilities))
	for _, v := range f.Vulnerabilities {
		id := strings.ToUpper(strings.TrimSpace(v.CVEID))
		if !strings.HasPrefix(id, "CVE-") {
			continue
		}
		cat[id] = Entry{CVE: id, Name: v.Name, DateAdded: v.DateAdded, DueDate: v.DueDate, Ransomware: v.Ransomware}
	}
	if len(cat) == 0 {
		return nil, fmt.Errorf("catalog %q lists no CVE", f.CatalogVersion)
	}
	return cat, nil
}
