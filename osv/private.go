// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// The private feed: osv reads a private advisory feed in the OSV schema and
// answers it like OSV: the advisories of an installation's own artefacts -
// a composer package on its GitLab registry, a CI component or a project
// tagged on its instance - which no public advisory database carries.
//
// # The feed
//
// PINUP_PRIVATE_ADVISORIES names one or more sources, comma-separated:
// an https URL of an OSV export (a .zip of one record per .json file, the
// shape of OSV's own all.zip) or of a .json document (one record, an array
// of them, or {"vulns": [...]}), or a file:// directory of .json records.
// Every source is read once per process; one that cannot be read is a
// warning, never a failed run - the others still count.
//
// # Naming a package
//
// A record names a package by purl, the form a CSAF advisory's product tree
// carries, or by OSV ecosystem and name. A dependency is asked as:
//
//	packagist, gitlab-packages   pkg:composer/<vendor>/<name>   Packagist  <vendor>/<name>
//	gitlab-tags, gitlab-releases pkg:gitlab/<group>/<project>   GitLab     <group>/<project>
//	npm                          pkg:npm/<name>                 npm        <name>
//	go                           pkg:golang/<module>            Go         <module>
//	pypi                         pkg:pypi/<name>                PyPI       <name>
//
// A gitlab-packages dependency's package name is "<project>:<vendor>/<name>";
// the composer name after the colon is what the purl names. Versions are
// compared under the dependency's own versioning, with OSV's range walk.
//
// It lives in osv because it is an OSV source: the same record format and
// the same range walk, read from a feed instead of OSV's API.
package osv

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/versioning"
)

// PrivateFeed is the private advisory feed. The zero value has no sources and asks
// nothing.
type PrivateFeed struct {
	// HTTP reads https sources; the installation's client, whose host rule
	// lets a feed on the instance's package registry be read with the
	// platform token.
	HTTP *httpx.Client
	// Sources are the URLs PINUP_PRIVATE_ADVISORIES names.
	Sources []string

	once     sync.Once
	byKey    map[string][]Record
	warnings []string
}

// PrivateName is the source name the plan and the dashboard use.
const PrivateName = "private"

// PrivatePackage maps a dependency to the purl, OSV ecosystem and name the feed
// asks it by; ok is false for a datasource the feed has no naming for.
func PrivatePackage(datasource, packageName string) (purl, ecosystem, name string, ok bool) {
	switch datasource {
	case "packagist", "gitlab-packages":
		if _, n, found := strings.Cut(packageName, ":"); found {
			packageName = n
		}
		return "pkg:composer/" + packageName, "Packagist", packageName, packageName != ""
	case "gitlab-tags", "gitlab-releases":
		return "pkg:gitlab/" + packageName, "GitLab", packageName, packageName != ""
	case "npm":
		return "pkg:npm/" + strings.ReplaceAll(packageName, "@", "%40"), "npm", packageName, packageName != ""
	case "go":
		return "pkg:golang/" + packageName, "Go", packageName, packageName != ""
	case "pypi":
		return "pkg:pypi/" + strings.ToLower(packageName), "PyPI", packageName, packageName != ""
	}
	return "", "", "", false
}

// Asks reports whether the feed is asked about a dependency of datasource:
// the feed has a source and a naming for it.
func (f *PrivateFeed) Asks(datasource string) bool {
	if f == nil || len(f.Sources) == 0 {
		return false
	}
	_, _, _, ok := PrivatePackage(datasource, "x")
	return ok
}

// PrivateQuery is one dependency at one version, as the run asks the feed.
type PrivateQuery struct {
	Datasource, PackageName, Version, Versioning string
}

// Check answers, per query, the feed's advisories that affect its version,
// and the warnings of the sources that could not be read.
func (f *PrivateFeed) Check(ctx context.Context, vs versioning.Registry, queries []PrivateQuery) ([][]Advisory, []string) {
	f.once.Do(func() { f.load(ctx) })
	out := make([][]Advisory, len(queries))
	for i, q := range queries {
		purl, eco, name, ok := PrivatePackage(q.Datasource, q.PackageName)
		if !ok {
			continue
		}
		scheme, err := vs.Get(q.Versioning)
		if err != nil || !scheme.IsVersion(q.Version) {
			continue
		}
		seen := map[string]bool{}
		for _, key := range []string{"purl:" + PurlBase(purl), "eco:" + eco + "/" + name} {
			for _, r := range f.byKey[key] {
				if seen[r.ID()] {
					continue
				}
				seen[r.ID()] = true
				if a, hit := r.Evaluate(purl, eco, name, q.Version, scheme); hit {
					out[i] = append(out[i], a)
				}
			}
		}
	}
	return out, slices.Clone(f.warnings)
}

func (f *PrivateFeed) load(ctx context.Context) {
	f.byKey = map[string][]Record{}
	for _, src := range f.Sources {
		docs, err := f.read(ctx, src)
		if err != nil {
			f.warnings = append(f.warnings, fmt.Sprintf("private advisories %s: %v; planned without it", src, err))
			continue
		}
		for _, d := range docs {
			records, err := ParseRecords(d.data)
			if err != nil {
				f.warnings = append(f.warnings, fmt.Sprintf("private advisories %s: %s: %v", src, d.name, err))
				continue
			}
			for _, r := range records {
				for _, k := range slices.Compact(slices.Sorted(slices.Values(r.Packages()))) {
					f.byKey[k] = append(f.byKey[k], r)
				}
			}
		}
	}
}

type feedDoc struct {
	name string
	data []byte
}

// read returns a source's documents: the .json files of a zip or a
// directory, or the one document a .json URL serves.
func (f *PrivateFeed) read(ctx context.Context, src string) ([]feedDoc, error) {
	u, err := url.Parse(src)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file":
		entries, err := os.ReadDir(u.Path)
		if err != nil {
			return nil, err
		}
		var out []feedDoc
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(u.Path, e.Name()))
			if err != nil {
				return nil, err
			}
			out = append(out, feedDoc{e.Name(), data})
		}
		return out, nil
	case "https":
	default:
		return nil, fmt.Errorf("%q is neither an https URL nor a file:// directory", src)
	}
	if f.HTTP == nil {
		return nil, fmt.Errorf("no HTTP client")
	}
	resp, err := f.HTTP.Get(ctx, src, httpx.ReqOptions{})
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(u.Path, ".zip") {
		return []feedDoc{{filepath.Base(u.Path), resp.Body}}, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(resp.Body), int64(len(resp.Body)))
	if err != nil {
		return nil, err
	}
	var out []feedDoc
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || !strings.HasSuffix(zf.Name, ".json") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		_, err = b.ReadFrom(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, feedDoc{zf.Name, b.Bytes()})
	}
	return out, nil
}
