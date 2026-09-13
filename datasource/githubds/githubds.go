// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package githubds implements the github-releases and github-tags
// datasources against the GitHub REST API.
//
// The REST API is a public specification (docs.github.com/rest), not
// Renovate internals: nothing here is copied out of the Renovate tree, only
// observed against the documented, versioned contract GitHub itself
// publishes. See CLAUDE.md's licensing section for why that distinction
// matters in this repository.
//
// Both kinds share one endpoint shape - "GET /repos/{owner}/{repo}/<thing>",
// paginated via the RFC 5988 Link header rather than GitLab's X-Next-Page -
// so one type serves both, the same way gitlabds serves three GitLab
// datasources from one type.
package githubds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// Kind selects which of the two GitHub datasources an instance serves.
type Kind string

const (
	Releases Kind = "github-releases"
	Tags     Kind = "github-tags"
)

// defaultAPIBase is api.github.com's REST endpoint. GitHub Enterprise
// Server instances publish the same API shape under their own host, which
// is why it is overridable per dependency via registryUrls rather than
// baked in.
const defaultAPIBase = "https://api.github.com"

// Datasource is one of the two, bound to a default API host.
type Datasource struct {
	kind   Kind
	client *httpx.Client
	// apiBase is used when a dependency names no registry. The estate's
	// config leaves registryUrls unset for github.com dependencies, so this
	// is the normal path here (unlike gitlabds, where the instance is
	// always named explicitly).
	apiBase string
}

// New returns a datasource of the given kind. An empty apiBase defaults to
// api.github.com; a dependency's own RegistryURLs[0], when set, overrides
// it at lookup time for GitHub Enterprise.
func New(kind Kind, client *httpx.Client, apiBase string) *Datasource {
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	return &Datasource{kind: kind, client: client, apiBase: strings.TrimRight(apiBase, "/")}
}

func (d *Datasource) Name() string { return string(d.kind) }

// DefaultVersioning is semver for both kinds - what every github.com
// release or tag in the estate is versioned with.
func (d *Datasource) DefaultVersioning() string { return "semver" }

func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	owner, repo, err := splitOwnerRepo(ref.PackageName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.kind, err)
	}

	base := d.apiBase
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		base = strings.TrimRight(ref.RegistryURLs[0], "/")
	}

	var segment string
	switch d.kind {
	case Releases:
		segment = "releases"
	case Tags:
		segment = "tags"
	default:
		return nil, fmt.Errorf("unknown github datasource kind %q", d.kind)
	}

	rs := &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  string(d.kind),
		RegistryURL: base,
		SourceURL:   fmt.Sprintf("https://github.com/%s/%s", owner, repo),
	}

	// Walk every page. The Link header carries an absolute URL for rel=next,
	// so nothing here needs to know the query string shape past the first
	// request. No Link header, or none of its relations is "next", ends the
	// walk - the safe direction: fewer releases rather than an endless loop.
	next := fmt.Sprintf("%s/repos/%s/%s/%s?per_page=100", base, owner, repo, segment)
	for next != "" {
		resp, err := d.client.Get(ctx, next, httpx.ReqOptions{Accept: "application/vnd.github+json"})
		if err != nil {
			return nil, wrapError(d.kind, owner, repo, err)
		}

		var items []model.Release
		switch d.kind {
		case Releases:
			items, err = parseReleases(resp.Body)
		case Tags:
			items, err = parseTags(resp.Body)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %s/%s: %w", d.kind, owner, repo, err)
		}
		rs.Releases = append(rs.Releases, items...)
		next = nextLink(resp.Header)
	}
	return rs, nil
}

// parseReleases reads one page of "GET /repos/{owner}/{repo}/releases".
// Drafts are dropped here rather than left for a later stage: a draft names
// no committed tag yet, so it is not a release candidate for anything
// downstream. Prereleases are kept - stability is the versioning scheme's
// call, not this datasource's.
func parseReleases(body []byte) ([]model.Release, error) {
	var items []struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	out := make([]model.Release, 0, len(items))
	for _, it := range items {
		if it.Draft {
			continue
		}
		// published_at is JSON null for a draft (already skipped above) and
		// otherwise always present, but json.Unmarshal leaves a null time
		// field at its zero value rather than erroring, so a genuinely
		// absent timestamp degrades to model.Release's documented zero-time
		// convention instead of failing the whole page.
		out = append(out, model.Release{Version: it.TagName, Timestamp: it.PublishedAt})
	}
	return out, nil
}

// parseTags reads one page of "GET /repos/{owner}/{repo}/tags". The tags
// endpoint carries no timestamp at all - only a name and a commit SHA this
// datasource has no use for.
func parseTags(body []byte) ([]model.Release, error) {
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	out := make([]model.Release, 0, len(items))
	for _, it := range items {
		out = append(out, model.Release{Version: it.Name})
	}
	return out, nil
}

// nextLink extracts the rel="next" target from an RFC 5988 Link header,
// e.g. `<https://api.github.com/...&page=2>; rel="next", <...>; rel="last"`.
// The URL is already absolute, so callers just re-request it.
func nextLink(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		urlPart, params, ok := strings.Cut(part, ";")
		if !ok {
			continue
		}
		urlPart = strings.TrimSpace(urlPart)
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			if p == `rel="next"` || p == "rel=next" {
				return urlPart[1 : len(urlPart)-1]
			}
		}
	}
	return ""
}

// splitOwnerRepo reduces PackageName to (owner, repo). Both the bare
// "owner/repo" form and a full "https://github.com/owner/repo" URL are
// accepted, since config sources disagree on which one they carry; every
// caller downstream of this function only ever sees the bare form.
func splitOwnerRepo(name string) (owner, repo string, err error) {
	s := name
	if strings.Contains(s, "://") {
		u, perr := url.Parse(s)
		if perr != nil {
			return "", "", fmt.Errorf("%q is not a valid github.com URL: %w", name, perr)
		}
		s = u.Path
	}
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")

	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf(`%q is not "owner/repo" (or a https://github.com/owner/repo URL)`, name)
	}
	return parts[0], parts[1], nil
}

// wrapError turns a lookup failure into a message that names what actually
// went wrong, for the two cases the estate's rules key on: a repository this
// token cannot see, and GitHub's rate limit. GitHub signals the primary
// limit as a 403 with X-RateLimit-Remaining: 0 and the secondary limit as a
// 429; both name when the limit clears in X-RateLimit-Reset (epoch seconds).
func wrapError(kind Kind, owner, repo string, err error) error {
	if se, ok := errors.AsType[*httpx.StatusError](err); ok {
		switch se.StatusCode {
		case http.StatusNotFound:
			// A 404 here means EITHER no such repository OR no permission
			// to see it - GitHub does not distinguish, and neither can this
			// code. Say both, so nobody reads "not found" as "does not
			// exist".
			return fmt.Errorf("%s: 404 for %s/%s - the repository does not exist or the token cannot read it",
				kind, owner, repo)
		case http.StatusTooManyRequests:
			return fmt.Errorf("%s: %s/%s: GitHub rate limit exhausted (429)%s", kind, owner, repo, resetNote(se.Header))
		case http.StatusForbidden:
			if se.Header.Get("X-RateLimit-Remaining") == "0" {
				return fmt.Errorf("%s: %s/%s: GitHub rate limit exhausted (403)%s", kind, owner, repo, resetNote(se.Header))
			}
		}
	}
	return fmt.Errorf("%s: %s/%s: %w", kind, owner, repo, err)
}

// resetNote renders X-RateLimit-Reset as a time, when present.
func resetNote(h http.Header) string {
	var epoch int64
	if _, err := fmt.Sscanf(h.Get("X-RateLimit-Reset"), "%d", &epoch); err != nil || epoch <= 0 {
		return ""
	}
	return ", resets at " + time.Unix(epoch, 0).UTC().Format(time.RFC3339)
}
