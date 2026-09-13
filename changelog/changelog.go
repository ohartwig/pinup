// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package changelog fetches the release notes between two versions of a
// dependency from its source repository - GitHub releases and GitLab
// releases, through the source URL the lookup records - so a merge request
// can say what it brings. No third-party service is contacted; a source
// this package does not know yields a compare link and nothing else.
package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// Fetcher reads releases from the forges it knows. GitLab is the estate's
// own instance; anything under github.com is GitHub.
type Fetcher struct {
	Client    *httpx.Client
	GitLabURL string // e.g. https://git.ole-hartwig.eu
	// Cache remembers a source's releases for TTL; nil means no cache.
	Cache Cache
	TTL   time.Duration
	Now   time.Time
	// MaxBody caps a note's body in the plan; 0 keeps it whole.
	MaxBody int
}

// Cache is the slice of the lookup cache this package uses.
type Cache interface {
	GetReleases(key string, ttl time.Duration, now time.Time) ([]byte, bool, error)
	PutReleases(key string, payload []byte, now time.Time) error
}

// Notes returns the releases of the source repository whose versions lie
// above current and up to target under the scheme, newest first, and the
// compare URL between the two. An unknown source yields the compare URL
// alone where one can be formed, and nothing otherwise.
func (f *Fetcher) Notes(ctx context.Context, sourceURL string, v versioning.Versioning, current, target string) ([]model.ReleaseNote, string, error) {
	kind, owner, repo := classify(sourceURL, f.GitLabURL)
	compare := compareURL(kind, sourceURL, current, target)
	if kind == "" {
		return nil, compare, nil
	}
	cur, tgt := strings.TrimPrefix(current, "v"), strings.TrimPrefix(target, "v")
	if !v.IsVersion(cur) || !v.IsVersion(tgt) {
		return nil, compare, nil
	}
	all, err := f.releases(ctx, kind, owner, repo, sourceURL, v, cur)
	if err != nil {
		return nil, compare, err
	}
	// The compare page wants the tags as the forge spells them: a
	// manifest says 3.11.1, the tag is v3.11.1. The release list knows.
	curTag, tgtTag := current, target
	for _, r := range all {
		switch strings.TrimPrefix(r.Version, "v") {
		case cur:
			curTag = r.Version
		case tgt:
			tgtTag = r.Version
		}
	}
	compare = compareURL(kind, sourceURL, curTag, tgtTag)
	var out []model.ReleaseNote
	for _, r := range all {
		ver := strings.TrimPrefix(r.Version, "v")
		if !v.IsVersion(ver) {
			continue
		}
		if v.Compare(ver, cur) <= 0 || v.Compare(ver, tgt) > 0 {
			continue
		}
		if limit := f.MaxBody; limit > 0 && len(r.Body) > limit {
			r.Body = truncate(r.Body, limit) + "\n\n… (truncated)"
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return v.Compare(strings.TrimPrefix(out[i].Version, "v"), strings.TrimPrefix(out[j].Version, "v")) > 0
	})
	return out, compare, nil
}

// classify names the forge behind a source URL.
func classify(sourceURL, gitlabURL string) (kind, owner, repo string) {
	u, err := url.Parse(sourceURL)
	if err != nil || u.Host == "" {
		return "", "", ""
	}
	path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	switch {
	case u.Host == "github.com":
		parts := strings.SplitN(path, "/", 3)
		if len(parts) < 2 {
			return "", "", ""
		}
		return "github", parts[0], parts[1]
	case gitlabURL != "" && strings.EqualFold(u.Host, hostOf(gitlabURL)):
		if path == "" {
			return "", "", ""
		}
		return "gitlab", path, ""
	}
	return "", "", ""
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// compareURL is the forge's diff page between two tags, "" for a source
// that is not a forge this package knows.
func compareURL(kind, sourceURL, current, target string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(sourceURL, "/"), ".git")
	switch kind {
	case "github":
		return fmt.Sprintf("%s/compare/%s...%s", base, current, target)
	case "gitlab":
		return fmt.Sprintf("%s/-/compare/%s...%s", base, current, target)
	}
	return ""
}

// maxPages bounds one source's release list: ten pages of a hundred. A
// project releasing several times a day for years is a compare link
// beyond that.
const maxPages = 10

// stored is what the cache holds for one source: the releases read so
// far, newest first, and whether the list reached the source's end.
type stored struct {
	Releases []model.ReleaseNote `json:"releases"`
	Complete bool                `json:"complete"`
}

// covers reports whether the list reaches below current under v - every
// release in the span is then in it - or has no further page.
func (st stored) covers(v versioning.Versioning, current string) bool {
	if st.Complete {
		return true
	}
	for _, r := range st.Releases {
		if ver := strings.TrimPrefix(r.Version, "v"); v.IsVersion(ver) && v.Compare(ver, current) <= 0 {
			return true
		}
	}
	return false
}

// releases reads the source's releases newest first, page by page, until
// one older than current is in hand: a project on 2.5.x whose consumer
// pins the 1.x line has its 1.63 notes deep in the list (measured:
// composed-default-pipelines, 2026-09-13). The cache serves a list that
// covers the span; a shorter one is read again.
func (f *Fetcher) releases(ctx context.Context, kind, owner, repo, sourceURL string, v versioning.Versioning, current string) ([]model.ReleaseNote, error) {
	key := "notes\x00" + sourceURL
	if f.Cache != nil {
		if payload, fresh, err := f.Cache.GetReleases(key, f.TTL, f.Now); err == nil && fresh && len(payload) > 0 {
			var st stored
			if json.Unmarshal(payload, &st) == nil && st.covers(v, current) {
				return st.Releases, nil
			}
		}
	}
	var st stored
	for page := 1; page <= maxPages; page++ {
		var batch []model.ReleaseNote
		var err error
		switch kind {
		case "github":
			batch, err = f.github(ctx, owner, repo, page)
		case "gitlab":
			batch, err = f.gitlab(ctx, owner, page)
		}
		if err != nil {
			return nil, err
		}
		st.Releases = append(st.Releases, batch...)
		if len(batch) < pageSize {
			st.Complete = true
		}
		if st.covers(v, current) {
			break
		}
	}
	if f.Cache != nil {
		if payload, err := json.Marshal(st); err == nil {
			_ = f.Cache.PutReleases(key, payload, f.Now)
		}
	}
	return st.Releases, nil
}

const pageSize = 100

// truncate cuts s to at most limit bytes on a line boundary when one lies
// in the second half of the budget, else on a rune boundary - never inside
// a UTF-8 sequence, which encoding/json would turn into U+FFFD.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	if nl := strings.LastIndexByte(s[:limit], '\n'); nl > limit/2 {
		return s[:nl]
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// github reads one page of a repository's releases, newest first.
func (f *Fetcher) github(ctx context.Context, owner, repo string, page int) ([]model.ReleaseNote, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=%d&page=%d", url.PathEscape(owner), url.PathEscape(repo), pageSize, page)
	resp, err := f.Client.Get(ctx, u, httpx.ReqOptions{Accept: "application/vnd.github+json"})
	if err != nil {
		return nil, fmt.Errorf("changelog: github %s/%s: %w", owner, repo, err)
	}
	var items []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
	}
	if err := json.Unmarshal(resp.Body, &items); err != nil {
		return nil, fmt.Errorf("changelog: github %s/%s: %w", owner, repo, err)
	}
	var out []model.ReleaseNote
	for _, it := range items {
		if it.Draft {
			continue
		}
		out = append(out, model.ReleaseNote{Version: it.TagName, Title: it.Name, Body: it.Body, URL: it.HTMLURL, Published: it.PublishedAt})
	}
	return out, nil
}

// gitlab reads one page of a project's releases on the estate's instance,
// newest first.
func (f *Fetcher) gitlab(ctx context.Context, path string, page int) ([]model.ReleaseNote, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/releases?per_page=%d&page=%d", strings.TrimRight(f.GitLabURL, "/"), url.PathEscape(path), pageSize, page)
	resp, err := f.Client.Get(ctx, u, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("changelog: gitlab %s: %w", path, err)
	}
	var items []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Description string    `json:"description"`
		ReleasedAt  time.Time `json:"released_at"`
		Links       struct {
			Self string `json:"self"`
		} `json:"_links"`
	}
	if err := json.Unmarshal(resp.Body, &items); err != nil {
		return nil, fmt.Errorf("changelog: gitlab %s: %w", path, err)
	}
	var out []model.ReleaseNote
	for _, it := range items {
		out = append(out, model.ReleaseNote{Version: it.TagName, Title: it.Name, Body: it.Description, URL: it.Links.Self, Published: it.ReleasedAt})
	}
	return out, nil
}
