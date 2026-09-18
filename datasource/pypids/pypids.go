// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package pypids implements the pypi datasource: a project's releases from
// the Python Package Index's JSON API (GET /pypi/<project>/json), the
// public interface warehouse documents. Each release carries its files,
// and a file carries its upload time and whether it was yanked - the
// earliest upload is the release's timestamp, and a release whose files
// are all yanked is deprecated.
//
// A private index is named through registryUrls, as Renovate does; the
// same JSON path is asked of it, which is what devpi and the like serve.
package pypids

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// Name is the datasource's name in a configuration.
const Name = "pypi"

// defaultRegistry is the public index.
const defaultRegistry = "https://pypi.org/pypi/"

// Datasource serves releases from a PyPI-compatible JSON API.
type Datasource struct {
	client *httpx.Client
}

// New returns the datasource.
func New(client *httpx.Client) *Datasource { return &Datasource{client: client} }

func (d *Datasource) Name() string { return Name }

// DefaultVersioning is pep440, the index's own version grammar.
func (d *Datasource) DefaultVersioning() string { return "pep440" }

// separators is what PEP 503 folds into "-" when normalising a project
// name; the JSON API redirects on the unnormalised spelling, but asking
// for the canonical one saves the round trip.
var separators = regexp.MustCompile(`[-_.]+`)

// normalize is PEP 503's project-name normalisation.
func normalize(name string) string {
	return strings.ToLower(separators.ReplaceAllString(strings.TrimSpace(name), "-"))
}

// Releases fetches the project's JSON document.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	base := defaultRegistry
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		base = strings.TrimRight(ref.RegistryURLs[0], "/") + "/"
	}
	name := normalize(ref.PackageName)
	url := base + name + "/json"
	resp, err := d.client.Get(ctx, url, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("pypi: %s: %w", url, err)
	}
	var doc struct {
		Info struct {
			Name        string            `json:"name"`
			Version     string            `json:"version"`
			HomePage    string            `json:"home_page"`
			ProjectURLs map[string]string `json:"project_urls"`
		} `json:"info"`
		Releases map[string][]struct {
			UploadTime string `json:"upload_time_iso_8601"`
			Yanked     bool   `json:"yanked"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return nil, fmt.Errorf("pypi: %s: %w", url, err)
	}
	if len(doc.Releases) == 0 {
		return nil, fmt.Errorf("pypi: %s lists no releases", url)
	}
	rs := &model.ReleaseSet{PackageName: ref.PackageName, Datasource: Name, RegistryURL: strings.TrimRight(base, "/"), SourceURL: sourceURL(doc.Info.ProjectURLs, doc.Info.HomePage)}
	for ver, files := range doc.Releases {
		r := model.Release{Version: ver}
		if len(files) > 0 {
			r.Deprecated = true
			for _, f := range files {
				if !f.Yanked {
					r.Deprecated = false
				}
				if t, err := time.Parse(time.RFC3339Nano, f.UploadTime); err == nil && (r.Timestamp.IsZero() || t.Before(r.Timestamp)) {
					r.Timestamp = t.UTC()
				}
			}
		}
		rs.Releases = append(rs.Releases, r)
	}
	// The document is a map; answer in upload order so two runs record the
	// same release set.
	slices.SortFunc(rs.Releases, func(a, b model.Release) int {
		if c := a.Timestamp.Compare(b.Timestamp); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	return rs, nil
}

// sourceURL picks the repository link out of project_urls when one names
// GitHub or GitLab, else the homepage.
func sourceURL(urls map[string]string, home string) string {
	for _, key := range []string{"Source", "Source Code", "Repository", "Code", "Homepage", "Home"} {
		for k, u := range urls {
			if strings.EqualFold(k, key) && isRepo(u) {
				return u
			}
		}
	}
	for _, u := range urls {
		if isRepo(u) {
			return u
		}
	}
	return home
}

func isRepo(u string) bool {
	return strings.Contains(u, "github.com/") || strings.Contains(u, "gitlab.com/") || strings.Contains(u, "codeberg.org/")
}
