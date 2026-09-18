// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package gitlabds implements the gitlab-tags, gitlab-releases and
// gitlab-packages datasources against a GitLab instance's REST API.
//
// Between them they serve every first-party dependency in the estate: CI
// component pins resolve through tags, first-party composer packages through
// the package registry. All three paginate with X-Next-Page, and the test
// server forces a page size of two so pagination is exercised on every run
// rather than only when a project has more than a hundred tags.
//
// The package name is a project PATH - "devops/ci-cd-components/lint-tools" -
// which the API wants URL-encoded as a single segment. The encoding is the
// one thing here that is easy to get wrong and hard to notice: an unencoded
// slash reaches a different endpoint and 404s, which reads exactly like a
// project that does not exist.
package gitlabds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// Kind selects which of the three GitLab datasources an instance serves.
type Kind string

const (
	Tags     Kind = "gitlab-tags"
	Releases Kind = "gitlab-releases"
	Packages Kind = "gitlab-packages"
)

// Datasource is one of the three, bound to a default instance.
type Datasource struct {
	kind   Kind
	client *httpx.Client
	// baseURL is used when a dependency names no registry. The estate's
	// config sets registryUrls to the instance for all three, so this is a
	// fallback rather than the normal path.
	baseURL string
}

// New returns a datasource of the given kind.
func New(kind Kind, client *httpx.Client, baseURL string) *Datasource {
	return &Datasource{kind: kind, client: client, baseURL: strings.TrimRight(baseURL, "/")}
}

func (d *Datasource) Name() string { return string(d.kind) }

// DefaultVersioning is semver for tags and releases, composer for packages -
// what the estate uses them for.
func (d *Datasource) DefaultVersioning() string {
	if d.kind == Packages {
		return "composer"
	}
	return "semver"
}

func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	base := d.baseURL
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		base = strings.TrimRight(ref.RegistryURLs[0], "/")
	}
	if base == "" {
		return nil, fmt.Errorf("%s: no registry URL for %s", d.kind, ref.PackageName)
	}
	// `https://${CI_SERVER_HOST}` is how a component include with a
	// templated host is recorded - by Renovate and by manager/gitlabci
	// alike. In the estate a rule rewrites registryUrls for every gitlab-*
	// datasource to the canonical host before lookup (rule 10 of the
	// resolved config), so this is only reached when no rule did; then
	// there is nothing to resolve the variable against, and declining is
	// better than a DNS error dressed as a registry outage.
	if strings.Contains(base, "$") {
		return nil, &lookup.DeclinedError{Reason: fmt.Sprintf(
			"registry %s is a CI variable; no rule rewrote it to a host", base)}
	}

	project, pkg := ref.PackageName, ""
	if d.kind == Packages {
		// "group/project:vendor/name" - the part after the colon is the
		// package inside that project's registry.
		if i := strings.LastIndex(ref.PackageName, ":"); i >= 0 {
			project, pkg = ref.PackageName[:i], ref.PackageName[i+1:]
		}
	}

	var endpoint string
	switch d.kind {
	case Tags:
		endpoint = fmt.Sprintf("%s/api/v4/projects/%s/repository/tags", base, url.PathEscape(project))
	case Releases:
		endpoint = fmt.Sprintf("%s/api/v4/projects/%s/releases", base, url.PathEscape(project))
	case Packages:
		endpoint = fmt.Sprintf("%s/api/v4/projects/%s/packages?package_name=%s",
			base, url.PathEscape(project), url.QueryEscape(pkg))
	default:
		return nil, fmt.Errorf("unknown gitlab datasource kind %q", d.kind)
	}

	rs := &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  string(d.kind),
		RegistryURL: base,
	}
	// Measured in the pinned container: tags and releases report the
	// project page as the source URL; the package registry reports none.
	if d.kind != Packages {
		rs.SourceURL = base + "/" + project
	}

	// Walk every page. X-Next-Page is empty on the last one; a server that
	// omitted the header entirely would end the walk after one page, which
	// is the safe direction - fewer releases rather than an endless loop.
	page := "1"
	for page != "" {
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		u := fmt.Sprintf("%s%sper_page=100&page=%s", endpoint, sep, page)
		resp, err := d.client.Get(ctx, u, httpx.ReqOptions{Accept: "application/json"})
		if err != nil {
			var se *httpx.StatusError
			if errors.As(err, &se) && se.StatusCode == 404 {
				// A 404 here means EITHER no such project OR no permission
				// to see it - GitLab does not distinguish, and neither can
				// this code. Say both, so nobody reads "not found" as "does
				// not exist".
				// Wrapped, so a caller probing for the project behind a
				// path (gods, for a module on the instance) can tell
				// this from an outage.
				return nil, fmt.Errorf("%s: 404 for %s - the project does not exist or the token cannot read it: %w",
					d.kind, project, err)
			}
			return nil, fmt.Errorf("%s: %s: %w", d.kind, project, err)
		}

		items, err := d.parse(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", d.kind, project, err)
		}
		rs.Releases = append(rs.Releases, items...)
		page = resp.Header.Get("X-Next-Page")
	}
	return rs, nil
}

// parse reads one page in the shape the kind's endpoint returns.
func (d *Datasource) parse(body []byte) ([]model.Release, error) {
	switch d.kind {
	case Tags:
		var tags []struct {
			Name   string `json:"name"`
			Commit struct {
				ID          string    `json:"id"`
				CommittedAt time.Time `json:"committed_date"`
			} `json:"commit"`
		}
		if err := json.Unmarshal(body, &tags); err != nil {
			return nil, err
		}
		out := make([]model.Release, 0, len(tags))
		for _, t := range tags {
			out = append(out, model.Release{
				Version: t.Name, Digest: t.Commit.ID, Timestamp: t.Commit.CommittedAt,
			})
		}
		return out, nil

	case Releases:
		var rels []struct {
			TagName    string    `json:"tag_name"`
			ReleasedAt time.Time `json:"released_at"`
		}
		if err := json.Unmarshal(body, &rels); err != nil {
			return nil, err
		}
		out := make([]model.Release, 0, len(rels))
		for _, r := range rels {
			out = append(out, model.Release{Version: r.TagName, Timestamp: r.ReleasedAt})
		}
		return out, nil

	case Packages:
		var pkgs []struct {
			Version   string    `json:"version"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(body, &pkgs); err != nil {
			return nil, err
		}
		out := make([]model.Release, 0, len(pkgs))
		for _, p := range pkgs {
			out = append(out, model.Release{Version: p.Version, Timestamp: p.CreatedAt})
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown kind %q", d.kind)
}

// ProjectID is exposed for diagnostics: the encoded form the API was asked
// for, so a 404 can be checked by hand.
func ProjectID(path string) string { return url.PathEscape(path) }
