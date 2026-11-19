// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package osv

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// GitEcosystem is the name OSV asks a repository by: a C project such as
// ClamAV has no package ecosystem, and OSV keeps its advisories as GIT
// ranges over the repository with the affected tags enumerated beside them
// (OSS-Fuzz records, CVEs that reference a commit). OSV answers a
// querybatch entry {"package": {"name": <repository URL>, "ecosystem":
// "GIT"}, "version": <tag>} with the records that list the tag (measured
// 2026-11-19: clamav-1.5.4 has three OSS-Fuzz records, clamav-1.4.2 six,
// caddyserver/caddy v2.6.0 twelve CVEs).
const GitEcosystem = "GIT"

// TagHit is one record OSV lists a tag under: its id and when OSV last
// changed it. Modified is what tells "OSV says this tag is not affected"
// from "OSV has not looked since the tag was made": OSV enumerates the
// affected tags when it analyses a record, so a tag younger than the
// record's last analysis is simply absent from it.
type TagHit struct {
	ID       string
	Modified time.Time
}

// RepoURL is the repository URL OSV keys GIT ranges by, for a source URL
// a lookup recorded: https, no ".git", no trailing slash. "" for anything
// that is not an http(s) URL with a host and a path.
func RepoURL(sourceURL string) string {
	u, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	path := strings.Trim(strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git"), "/")
	if path == "" {
		return ""
	}
	return "https://" + u.Host + "/" + path
}

// TagAdvisories asks OSV, in one batch, which records list each tag of the
// repository. The answer is one slice per tag, in the order asked; an
// empty slice means OSV lists the tag under no record. An error means OSV
// could not be asked at all - a caller treats it as "nothing known", never
// as "not affected".
func (c *Client) TagAdvisories(ctx context.Context, repo string, tags []string) ([][]TagHit, error) {
	if repo == "" || len(tags) == 0 {
		return nil, nil
	}
	batch := make([]batchQuery, len(tags))
	for i, t := range tags {
		batch[i] = batchQuery{Package: pkgRef{Name: repo, Ecosystem: GitEcosystem}, Version: t}
	}
	results, err := c.queryBatch(ctx, batch)
	if err != nil {
		return nil, err
	}
	if len(results) != len(tags) {
		return nil, fmt.Errorf("osv: querybatch answered %d results for %d tags", len(results), len(tags))
	}
	out := make([][]TagHit, len(tags))
	for i, res := range results {
		for _, ref := range res.Vulns {
			hit := TagHit{ID: ref.ID}
			if t, err := time.Parse(time.RFC3339Nano, ref.Modified); err == nil {
				hit.Modified = t
			}
			out[i] = append(out[i], hit)
		}
	}
	return out, nil
}

// Record fetches one record's details - aliases, summary, severity, dates -
// through Store as Check does. Fixed is left empty: a GIT range names
// commits, and which tag carries the fix is the caller's to decide.
func (c *Client) Record(ctx context.Context, id string, modified time.Time) (Advisory, error) {
	key := id + "|" + modified.UTC().Format(time.RFC3339Nano)
	doc, err := c.fetchAdvisory(ctx, key, id)
	if err != nil {
		return Advisory{}, err
	}
	adv := Advisory{ID: doc.ID, Aliases: append([]string(nil), doc.Aliases...), Summary: doc.Summary}
	if adv.ID == "" {
		adv.ID = id
	}
	if len(doc.Severity) > 0 {
		adv.Severity = doc.Severity[0].Score
	}
	if t, err := time.Parse(time.RFC3339, doc.Published); err == nil {
		adv.Published = t
	}
	if t, err := time.Parse(time.RFC3339, doc.Modified); err == nil {
		adv.Modified = t
	}
	return adv, nil
}
