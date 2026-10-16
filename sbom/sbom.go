// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package sbom reads what a build shipped: the components of a CycloneDX or
// SPDX document, of a Trivy report, or of a plain module list, as names,
// versions and purl types.
//
// Layer 0: standard library only, no knowledge of datasources or advisory
// ecosystems. It answers one question - which packages, at which versions,
// of which type - and leaves what a type means to the caller. Three formats
// are read because three are what builds publish: syft and trivy write
// CycloneDX, melange and the SPDX tooling write SPDX, Trivy's own report is
// what an audit job already has on disk, and `go version -m` is what a shell
// script can produce without any of them.
package sbom

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Component is one package a manifest names.
type Component struct {
	// Type is the purl type ("golang", "npm", "apk"), "" when the
	// component carries no purl and its type is not otherwise known.
	Type string
	// Name is the package name as the ecosystem spells it: the module
	// path for Go, "@scope/name" for npm, "vendor/name" for composer.
	Name string
	// Version is as the manifest wrote it; Go modules keep their "v".
	Version string
	// PURL is the purl as written, "" when the manifest carried none.
	PURL string
}

// Formats are the manifest formats Parse reads.
var Formats = []string{"cyclonedx", "spdx", "trivy", "go-modules"}

// Parse reads a manifest in the named format. Components without a name are
// dropped; components without a version are kept with Version "" so the
// caller can count them rather than lose them. The order is the document's.
func Parse(format string, data []byte) ([]Component, error) {
	switch format {
	case "cyclonedx":
		return parseCycloneDX(data)
	case "spdx":
		return parseSPDX(data)
	case "trivy":
		return parseTrivy(data)
	case "go-modules":
		return parseGoModules(data), nil
	default:
		return nil, fmt.Errorf("sbom: unknown format %q (one of %s)", format, strings.Join(Formats, ", "))
	}
}

// ParsePURL splits a package URL into its type, name and version. The name
// is the namespace and the name joined with "/", percent-decoded, so a
// scoped npm package comes back as "@scope/name" and a Go module as its
// path. Qualifiers and subpath are dropped. An input that is not a purl
// yields ok=false.
func ParsePURL(purl string) (typ, name, version string, ok bool) {
	rest, found := strings.CutPrefix(purl, "pkg:")
	if !found {
		return "", "", "", false
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	typ, rest, found = strings.Cut(rest, "/")
	if !found || typ == "" {
		return "", "", "", false
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		version = unescape(rest[i+1:])
		rest = rest[:i]
	}
	segments := strings.Split(rest, "/")
	for i, s := range segments {
		segments[i] = unescape(s)
	}
	name = strings.Join(segments, "/")
	if name == "" {
		return "", "", "", false
	}
	return strings.ToLower(typ), name, version, true
}

func unescape(s string) string {
	u, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return u
}

// fromPURL builds a component from a purl, falling back to the name and
// version the document wrote beside it when the purl lacks them.
func fromPURL(purl, name, version string) (Component, bool) {
	c := Component{Name: name, Version: version, PURL: purl}
	if purl != "" {
		if typ, n, v, ok := ParsePURL(purl); ok {
			c.Type = typ
			if n != "" {
				c.Name = n
			}
			if v != "" && c.Version == "" {
				c.Version = v
			}
		}
	}
	if c.Name == "" {
		return Component{}, false
	}
	return c, true
}

// --- CycloneDX ------------------------------------------------------------

type cdxDocument struct {
	BOMFormat  string         `json:"bomFormat"`
	Components []cdxComponent `json:"components"`
}

type cdxComponent struct {
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Group      string         `json:"group"`
	Version    string         `json:"version"`
	PURL       string         `json:"purl"`
	Components []cdxComponent `json:"components"`
}

func parseCycloneDX(data []byte) ([]Component, error) {
	var doc cdxDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("sbom: cyclonedx: %w", err)
	}
	if doc.BOMFormat != "CycloneDX" {
		return nil, fmt.Errorf("sbom: cyclonedx: bomFormat is %q, want \"CycloneDX\"", doc.BOMFormat)
	}
	var out []Component
	var walk func([]cdxComponent)
	walk = func(cs []cdxComponent) {
		for _, c := range cs {
			name := c.Name
			if c.Group != "" && !strings.Contains(c.PURL, "/"+c.Group+"/") {
				// CycloneDX writes an npm scope or a maven group beside
				// the name; the purl, where present, carries it already.
				name = c.Group + "/" + c.Name
			}
			if comp, ok := fromPURL(c.PURL, name, c.Version); ok {
				out = append(out, comp)
			}
			walk(c.Components)
		}
	}
	walk(doc.Components)
	return out, nil
}

// --- SPDX -----------------------------------------------------------------

type spdxDocument struct {
	SPDXVersion string        `json:"spdxVersion"`
	Packages    []spdxPackage `json:"packages"`
}

type spdxPackage struct {
	Name         string `json:"name"`
	VersionInfo  string `json:"versionInfo"`
	ExternalRefs []struct {
		Category string `json:"referenceCategory"`
		Type     string `json:"referenceType"`
		Locator  string `json:"referenceLocator"`
	} `json:"externalRefs"`
}

func parseSPDX(data []byte) ([]Component, error) {
	var doc spdxDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("sbom: spdx: %w", err)
	}
	if !strings.HasPrefix(doc.SPDXVersion, "SPDX-") {
		return nil, fmt.Errorf("sbom: spdx: spdxVersion is %q, want \"SPDX-…\"", doc.SPDXVersion)
	}
	var out []Component
	for _, p := range doc.Packages {
		purl := ""
		for _, r := range p.ExternalRefs {
			if r.Type == "purl" {
				purl = r.Locator
				break
			}
		}
		if c, ok := fromPURL(purl, p.Name, p.VersionInfo); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// --- Trivy ----------------------------------------------------------------

type trivyReport struct {
	SchemaVersion int `json:"SchemaVersion"`
	Results       []struct {
		Packages []struct {
			Name       string `json:"Name"`
			Version    string `json:"Version"`
			Identifier struct {
				PURL string `json:"PURL"`
			} `json:"Identifier"`
		} `json:"Packages"`
	} `json:"Results"`
}

func parseTrivy(data []byte) ([]Component, error) {
	var rep trivyReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("sbom: trivy: %w", err)
	}
	if rep.SchemaVersion == 0 {
		return nil, fmt.Errorf("sbom: trivy: no SchemaVersion; not a Trivy report")
	}
	var out []Component
	for _, r := range rep.Results {
		for _, p := range r.Packages {
			if c, ok := fromPURL(p.Identifier.PURL, p.Name, p.Version); ok {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// --- go-modules -----------------------------------------------------------

// parseGoModules reads `path version` lines: `go list -m all` writes them
// bare, `go version -m` writes `\tdep\tpath\tversion\th1:…` and `\tmod\tpath
// \tversion\t…` for the main module; `=>` replacements name the replacement.
// Every component is of type golang. Lines without a version (the main
// module of `go list -m all`) are kept with Version "".
func parseGoModules(data []byte) []Component {
	var out []Component
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "dep", "mod":
			// `go version -m`: the kind, then path and version.
			fields = fields[1:]
		case "path", "build":
			// `go version -m` headers: the main package's path, the
			// build settings.
			continue
		}
		if strings.HasSuffix(fields[0], ":") {
			// `go version -m`: "/usr/bin/grafana: go1.27.2".
			continue
		}
		if i := indexOf(fields, "=>"); i >= 0 {
			// `old vX => new vY`: the replacement is what is built.
			fields = fields[i+1:]
		}
		if len(fields) == 0 || !strings.Contains(fields[0], ".") {
			continue
		}
		c := Component{Type: "golang", Name: fields[0]}
		if len(fields) > 1 && strings.HasPrefix(fields[1], "v") {
			c.Version = fields[1]
		}
		out = append(out, c)
	}
	return out
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}

// Lowest merges components by type and name and keeps, per name, the lowest
// version seen under a plain string comparison of the numeric parts - the
// version an advisory range is most likely to contain when one build ships
// two copies of a module (two binaries, two architectures). The result is
// sorted by type, then name.
func Lowest(cs []Component) []Component {
	byKey := map[[2]string]Component{}
	for _, c := range cs {
		k := [2]string{c.Type, c.Name}
		have, ok := byKey[k]
		if !ok || (c.Version != "" && (have.Version == "" || lessVersion(c.Version, have.Version))) {
			byKey[k] = c
		}
	}
	out := make([]Component, 0, len(byKey))
	for _, c := range byKey {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// lessVersion orders two versions by their dot-separated numeric parts,
// a leading "v" ignored; non-numeric parts fall back to string order. It is
// a tie-break for duplicates in one manifest, not a versioning scheme.
func lessVersion(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var na, nb int
		_, erra := fmt.Sscanf(pa[i], "%d", &na)
		_, errb := fmt.Sscanf(pb[i], "%d", &nb)
		if erra != nil || errb != nil {
			if pa[i] != pb[i] {
				return pa[i] < pb[i]
			}
			continue
		}
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}
