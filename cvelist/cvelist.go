// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package cvelist reads one CVE record from the CVE List
// (https://cveawg.mitre.org/api/cve/{id}, CVE JSON 5) and answers whether
// it is published and names a product. It exists to confirm a CVE id an
// upstream release note names: the note is written by whoever controls the
// release, the record by the CVE Numbering Authority that assigned the id.
//
// The CVE List is where a CNA publishes first. Measured 2026-11-19 on
// ClamAV 1.5.5: the release (16:09 UTC, ten CVEs in its notes) was followed
// by CVE-2026-76411's record at 16:30 UTC; OSV still had no record a day
// later, GitHub no repository advisory at all.
//
// # Layer
//
// cvelist is L2 beside osv and kev: it imports httpx (L1) and nothing that
// knows about dependencies.
package cvelist

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ohartwig/pinup/httpx"
)

// DefaultBase is the CVE Services API of the CVE Program. A test or a
// mirror overrides it via Client.Base.
const DefaultBase = "https://cveawg.mitre.org"

// TTL is how long a cached record counts as fresh. A record changes when
// its CNA amends it - a rejected id is the change that matters here - and a
// day keeps a rejection from standing longer than that.
const TTL = 24 * time.Hour

// Store caches raw records, as kev.Store does the catalog.
type Store interface {
	Get(key string, ttl time.Duration, now time.Time) (payload []byte, fresh bool, ok bool)
	Put(key string, payload []byte, now time.Time)
}

// Client reads records.
type Client struct {
	// HTTP fetches the record. Required.
	HTTP *httpx.Client
	// Base is the API root; "" means DefaultBase.
	Base string
	// Store, when set, holds records between runs.
	Store Store
	// Now is the clock the Store's freshness is judged by. With a zero
	// Now the Store is not consulted.
	Now time.Time
}

// Published is the state of a record whose id a CNA has published.
const Published = "PUBLISHED"

// Record is the part of a CVE record a plan names and a confirmation reads.
type Record struct {
	ID        string
	State     string
	Published time.Time
	// Title is the CNA's title, or the first line of its description.
	Title string
	// Severity is the CNA's CVSS vector, the newest version it gives.
	Severity string
	// text is every description, title and affected product, lower case:
	// what Mentions searches.
	text string
}

var cveID = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)

// ValidID reports whether id is spelled as a CVE id.
func ValidID(id string) bool { return cveID.MatchString(id) }

// Get reads one record. A record the API does not know is an error, the
// same as one that cannot be read: either way it confirms nothing.
func (c *Client) Get(ctx context.Context, id string) (Record, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	if !ValidID(id) {
		return Record{}, fmt.Errorf("cvelist: %q is not a CVE id", id)
	}
	now := c.Now
	store := c.Store
	if now.IsZero() {
		store = nil
	}
	key := "cve\x00" + id
	if store != nil {
		if payload, fresh, ok := c.Store.Get(key, TTL, now); ok && fresh {
			if r, err := Parse(payload); err == nil {
				return r, nil
			}
		}
	}
	base := strings.TrimRight(c.Base, "/")
	if base == "" {
		base = DefaultBase
	}
	resp, err := c.HTTP.Get(ctx, base+"/api/cve/"+id, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return Record{}, fmt.Errorf("cvelist: %s: %w", id, err)
	}
	r, err := Parse(resp.Body)
	if err != nil {
		return Record{}, fmt.Errorf("cvelist: %s: %w", id, err)
	}
	if r.ID != id {
		return Record{}, fmt.Errorf("cvelist: asked for %s, answered %s", id, r.ID)
	}
	if store != nil {
		store.Put(key, resp.Body, now)
	}
	return r, nil
}

// document is the part of CVE JSON 5 this package reads.
type document struct {
	Metadata struct {
		ID            string `json:"cveId"`
		State         string `json:"state"`
		DatePublished string `json:"datePublished"`
	} `json:"cveMetadata"`
	Containers struct {
		CNA container   `json:"cna"`
		ADP []container `json:"adp"`
	} `json:"containers"`
}

type container struct {
	Title        string `json:"title"`
	Descriptions []struct {
		Lang  string `json:"lang"`
		Value string `json:"value"`
	} `json:"descriptions"`
	Affected []struct {
		Vendor      string `json:"vendor"`
		Product     string `json:"product"`
		PackageName string `json:"packageName"`
		Repo        string `json:"repo"`
	} `json:"affected"`
	Metrics []struct {
		V40 *cvss `json:"cvssV4_0"`
		V31 *cvss `json:"cvssV3_1"`
		V30 *cvss `json:"cvssV3_0"`
	} `json:"metrics"`
}

type cvss struct {
	VectorString string `json:"vectorString"`
}

// Parse reads one CVE JSON 5 record.
func Parse(data []byte) (Record, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Record{}, err
	}
	if doc.Metadata.ID == "" {
		return Record{}, fmt.Errorf("not a CVE record: no cveMetadata.cveId")
	}
	r := Record{ID: strings.ToUpper(doc.Metadata.ID), State: doc.Metadata.State}
	if t, err := time.Parse(time.RFC3339Nano, doc.Metadata.DatePublished); err == nil {
		r.Published = t
	} else if t, err := time.Parse("2006-01-02T15:04:05.999999999", doc.Metadata.DatePublished); err == nil {
		r.Published = t
	}
	var text []string
	cna := doc.Containers.CNA
	r.Title = cna.Title
	for _, c := range append([]container{cna}, doc.Containers.ADP...) {
		text = append(text, c.Title)
		for _, d := range c.Descriptions {
			text = append(text, d.Value)
			if r.Title == "" && d.Value != "" {
				r.Title = firstSentence(d.Value)
			}
		}
		for _, a := range c.Affected {
			text = append(text, a.Vendor, a.Product, a.PackageName, a.Repo)
		}
	}
	// The newest CVSS version the CNA gives.
	for _, pick := range []func(int) *cvss{
		func(i int) *cvss { return cna.Metrics[i].V40 },
		func(i int) *cvss { return cna.Metrics[i].V31 },
		func(i int) *cvss { return cna.Metrics[i].V30 },
	} {
		for i := range cna.Metrics {
			if m := pick(i); m != nil && m.VectorString != "" && r.Severity == "" {
				r.Severity = m.VectorString
			}
		}
	}
	r.text = strings.ToLower(strings.Join(text, "\n"))
	return r, nil
}

// Mentions reports whether the record names the product: name appears in
// a description, a title or an affected entry as a word of its own, in any
// case - "clamav" in "a vulnerability in the phishcheck functionality of
// ClamAV", but not "go" in "good". An empty or one-letter name mentions
// nothing.
func (r Record) Mentions(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if len(name) < 2 {
		return false
	}
	text := r.text
	for {
		i := strings.Index(text, name)
		if i < 0 {
			return false
		}
		end := i + len(name)
		if (i == 0 || !wordByte(text[i-1])) && (end == len(text) || !wordByte(text[end])) {
			return true
		}
		text = text[i+1:]
	}
}

// firstSentence is a description's first sentence, on one line: what a
// plan's advisory table and a merge request have room for.
func firstSentence(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	return strings.TrimSpace(s)
}

func wordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z'
}

// IDs are the CVE ids a text names, upper case, each once, in order of
// first appearance.
func IDs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mention.FindAllString(text, -1) {
		id := strings.ToUpper(m)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

var mention = regexp.MustCompile(`(?i)\bCVE-[0-9]{4}-[0-9]{4,}\b`)
