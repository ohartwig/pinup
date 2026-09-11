// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package npmds implements the npm datasource against the npm Registry API
// (github.com/npm/registry/blob/main/docs/REGISTRY-API.md), a public
// specification. Nothing here is copied out of the Renovate tree; behaviour
// is implemented from the documented contract itself. See CLAUDE.md's
// licensing section for why that distinction matters in this repository.
//
// The registry serves two document shapes at "GET {registry}/{name}": an
// abbreviated one (requested with Accept: application/vnd.npm.install-v1+json)
// that carries "versions" and "dist-tags" but no "time", and the full one
// (Accept: application/json) that carries everything, including "time". This
// datasource always requests the full document - one request per package,
// simpler than requesting the abbreviated form first and falling back only
// when "time" is missing, and the full document is a superset of what the
// abbreviated one carries anyway.
//
// GitLab's npm registry (used for the estate's own @scoped packages) answers
// the same document shape at the same path convention; nothing here is
// GitLab-specific.
package npmds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// Name is the datasource name dependencies and rules refer to.
const Name = "npm"

// defaultRegistry is used when a dependency names no registry of its own.
const defaultRegistry = "https://registry.npmjs.org"

// Datasource implements lookup.Datasource against the npm Registry API.
type Datasource struct {
	client *httpx.Client
}

// New returns a Datasource that fetches through client. Per-host credentials
// (for a private, scoped registry such as a GitLab package registry) are the
// caller's concern, supplied to client as an httpx.HostRule; this package
// sends no authentication of its own.
func New(client *httpx.Client) *Datasource {
	return &Datasource{client: client}
}

func (d *Datasource) Name() string { return Name }

// DefaultVersioning is "npm": the scheme every package on the registry
// declares its versions and ranges under.
func (d *Datasource) DefaultVersioning() string { return "npm" }

// registryDoc is the subset of the registry document this datasource reads,
// present in both the abbreviated and full document shapes except for Time,
// which only the full document carries.
type registryDoc struct {
	Versions map[string]struct {
		Deprecated string `json:"deprecated"`
	} `json:"versions"`
	// Time maps a version to its RFC 3339 publish time, plus "created" and
	// "modified" keys this datasource has no use for and ignores.
	Time       map[string]string `json:"time"`
	DistTags   map[string]string `json:"dist-tags"`
	Repository repositoryField   `json:"repository"`
}

// repositoryField reads npm's "repository" key, which the ecosystem writes
// as either a bare string or a {"url": "..."} object.
type repositoryField struct {
	URL string
}

func (r *repositoryField) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		r.URL = s
		return nil
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	r.URL = obj.URL
	return nil
}

func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	base := defaultRegistry
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		base = ref.RegistryURLs[0]
	}
	base = strings.TrimRight(base, "/")

	// A scoped name's "/" is percent-encoded so the whole name is one path
	// segment ("@moselwal%2Fdev"), exactly as the registry API requires;
	// url.PathEscape does this (and leaves an unscoped name unchanged).
	u := base + "/" + url.PathEscape(ref.PackageName)

	resp, err := d.client.Get(ctx, u, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, wrapError(ref.PackageName, base, err)
	}

	var doc registryDoc
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return nil, fmt.Errorf("npm: %s: decode response from %s: %w", ref.PackageName, base, err)
	}

	rs := &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  Name,
		RegistryURL: base,
		SourceURL:   cleanSourceURL(doc.Repository.URL),
	}

	// Versions come from a JSON object, which carries no order; sorted
	// output keeps a lookup's result deterministic run to run. Any ordering
	// that matters for update selection (semver order, say) is the
	// versioning scheme's job, downstream of this datasource.
	versions := make([]string, 0, len(doc.Versions))
	for v := range doc.Versions {
		versions = append(versions, v)
	}
	sort.Strings(versions)

	for _, v := range versions {
		rel := model.Release{Version: v}
		if ts, ok := doc.Time[v]; ok {
			if t, terr := time.Parse(time.RFC3339, ts); terr == nil {
				rel.Timestamp = t
			}
		}
		if doc.Versions[v].Deprecated != "" {
			rel.Deprecated = true
		}
		rs.Releases = append(rs.Releases, rel)
	}
	return rs, nil
}

// cleanSourceURL strips the "git+" prefix and ".git" suffix npm's
// "repository.url" convention carries, e.g.
// "git+https://github.com/juliangarnier/anime.git" ->
// "https://github.com/juliangarnier/anime".
func cleanSourceURL(u string) string {
	u = strings.TrimPrefix(u, "git+")
	u = strings.TrimSuffix(u, ".git")
	return u
}

// wrapError turns a lookup failure into a message that names what actually
// went wrong, for the three cases the estate's rules key on: a package this
// token cannot read (or that does not exist), a registry the token cannot
// read at all, and the registry's rate limit.
func wrapError(name, registry string, err error) error {
	if se, ok := errors.AsType[*httpx.StatusError](err); ok {
		switch se.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf(
				"npm: %s: 404 from %s - the package does not exist on that registry, or the token cannot read it",
				name, registry)
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf(
				"npm: %s: %d from %s - the token cannot read this registry",
				name, se.StatusCode, registry)
		case http.StatusTooManyRequests:
			return fmt.Errorf("npm: %s: rate limited (429) by %s", name, registry)
		}
	}
	return fmt.Errorf("npm: %s: %w", name, err)
}
