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
	all, err := f.releases(ctx, kind, owner, repo, sourceURL)
	if err != nil {
		return nil, compare, err
	}
	// The compare page wants the tags as the forge spells them: a
	// manifest says 3.11.1, the tag is v3.11.1. The release list knows.
	cur, tgt := strings.TrimPrefix(current, "v"), strings.TrimPrefix(target, "v")
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
		if !v.IsVersion(ver) || !v.IsVersion(cur) || !v.IsVersion(tgt) {
			continue
		}
		if v.Compare(ver, cur) <= 0 || v.Compare(ver, tgt) > 0 {
			continue
		}
		if limit := f.MaxBody; limit > 0 && len(r.Body) > limit {
			r.Body = r.Body[:limit] + "\n\n… (truncated)"
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

func (f *Fetcher) releases(ctx context.Context, kind, owner, repo, sourceURL string) ([]model.ReleaseNote, error) {
	key := "notes\x00" + sourceURL
	if f.Cache != nil {
		if payload, fresh, err := f.Cache.GetReleases(key, f.TTL, f.Now); err == nil && fresh && len(payload) > 0 {
			var out []model.ReleaseNote
			if json.Unmarshal(payload, &out) == nil {
				return out, nil
			}
		}
	}
	var out []model.ReleaseNote
	var err error
	switch kind {
	case "github":
		out, err = f.github(ctx, owner, repo)
	case "gitlab":
		out, err = f.gitlab(ctx, owner)
	}
	if err != nil {
		return nil, err
	}
	if f.Cache != nil {
		if payload, err := json.Marshal(out); err == nil {
			_ = f.Cache.PutReleases(key, payload, f.Now)
		}
	}
	return out, nil
}

// github reads the newest hundred releases of a repository.
func (f *Fetcher) github(ctx context.Context, owner, repo string) ([]model.ReleaseNote, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", url.PathEscape(owner), url.PathEscape(repo))
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

// gitlab reads the newest hundred releases of a project on the estate's
// instance.
func (f *Fetcher) gitlab(ctx context.Context, path string) ([]model.ReleaseNote, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/releases?per_page=100", strings.TrimRight(f.GitLabURL, "/"), url.PathEscape(path))
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
