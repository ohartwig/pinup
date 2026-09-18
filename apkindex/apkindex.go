// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package apkindex parses Alpine/Wolfi APKINDEX.tar.gz files and answers
// which package versions are available.
//
// An APKINDEX.tar.gz is a gzip stream wrapping a tar archive with one member
// named APKINDEX: a text file of blank-line-separated records, each a set of
// "<letter>:<value>" lines. Only P: (package name) and V: (version) matter
// here; every other letter is ignored.
//
// This package replaces the Node sidecar scripts/wolfi-apk-index.mjs and
// reproduces its semantics: union across mirrors for one architecture,
// intersection across architectures so a version only offered on one
// architecture is never proposed for a multi-arch build, and a SAFE_NAME
// filter on package names.
package apkindex

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// safeName mirrors SAFE_NAME from wolfi-apk-index.mjs: a package name must
// start with an alphanumeric and contain only alphanumerics plus ._+-.
// Anything else is a name apk's own repository tooling would never produce,
// and letting it through would make Union/Intersect trust attacker-shaped
// input from a mirror.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// tarMember is the name of the text file inside an APKINDEX.tar.gz.
const tarMember = "APKINDEX"

// Index is a parsed APKINDEX: package name to the set of versions it offers.
type Index struct {
	versions map[string]map[string]struct{}
}

// Parse reads an APKINDEX.tar.gz gzip stream and builds an Index.
//
// An index that parses to zero packages is returned as an error, not as an
// empty Index. An empty index and a package that genuinely has no versions
// look identical to a caller that only gets "no versions back" - that
// ambiguity is exactly the bug this package must not reproduce, so it fails
// loudly here instead of failing lookups silently later.
func Parse(r io.Reader) (*Index, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("apkindex: opening gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var text []byte
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("apkindex: reading tar stream: %w", err)
		}
		if hdr.Name != tarMember {
			continue
		}
		text, err = io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("apkindex: reading %s member: %w", tarMember, err)
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("apkindex: no %s member in the tarball", tarMember)
	}

	idx := &Index{versions: parseRecords(string(text))}
	if len(idx.versions) == 0 {
		return nil, errors.New("apkindex: parsed to zero packages - refusing to return an empty index rather than failing lookups silently")
	}
	return idx, nil
}

// parseRecords reads blank-line-separated P:/V: records, exactly as
// wolfi-apk-index.mjs's parseIndex does, and drops any package name that
// fails the SAFE_NAME filter.
func parseRecords(text string) map[string]map[string]struct{} {
	versions := make(map[string]map[string]struct{})
	add := func(name, version string) {
		if !safeName.MatchString(name) {
			return
		}
		set, ok := versions[name]
		if !ok {
			set = make(map[string]struct{})
			versions[name] = set
		}
		set[version] = struct{}{}
	}

	var name, version string
	for line := range strings.SplitSeq(text, "\n") {
		switch {
		case strings.HasPrefix(line, "P:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "P:"))
		case strings.HasPrefix(line, "V:"):
			version = strings.TrimSpace(strings.TrimPrefix(line, "V:"))
		case line == "":
			if name != "" && version != "" {
				add(name, version)
			}
			name, version = "", ""
		}
	}
	// The final record has no trailing blank line to close it.
	if name != "" && version != "" {
		add(name, version)
	}
	return versions
}

// Versions returns the known versions of pkg, sorted, or nil if pkg is
// unknown to this index.
func (i *Index) Versions(pkg string) []string {
	set, ok := i.versions[pkg]
	if !ok {
		return nil
	}
	return slices.Sorted(maps.Keys(set))
}

// Packages returns every package name in the index, sorted.
func (i *Index) Packages() []string {
	return slices.Sorted(maps.Keys(i.versions))
}

// Len returns the number of packages in the index.
func (i *Index) Len() int {
	return len(i.versions)
}

// Union merges indexes from different mirrors for the same architecture: a
// version is installable if at least one mirror carries it, which is how apk
// itself resolves a single architecture across /etc/apk/repositories.
func Union(indexes ...*Index) *Index {
	out := &Index{versions: make(map[string]map[string]struct{})}
	for _, idx := range indexes {
		if idx == nil {
			continue
		}
		for name, set := range idx.versions {
			dst, ok := out.versions[name]
			if !ok {
				dst = make(map[string]struct{})
				out.versions[name] = dst
			}
			maps.Copy(dst, set)
		}
	}
	return out
}

// Intersect keeps only the package versions present in every index: used
// across architectures, so a version that exists on x86_64 but not aarch64 is
// not offered for a multi-arch build.
func Intersect(indexes ...*Index) *Index {
	out := &Index{versions: make(map[string]map[string]struct{})}
	if len(indexes) == 0 {
		return out
	}
	first, rest := indexes[0], indexes[1:]
	if first == nil {
		return out
	}
	for name, set := range first.versions {
		common := make(map[string]struct{})
		for version := range set {
			inAll := true
			for _, idx := range rest {
				if idx == nil {
					inAll = false
					break
				}
				if _, ok := idx.versions[name][version]; !ok {
					inAll = false
					break
				}
			}
			if inAll {
				common[version] = struct{}{}
			}
		}
		if len(common) > 0 {
			out.versions[name] = common
		}
	}
	return out
}
