// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package gods implements the Renovate "go" and "golang-version" datasources:
// Go modules through a module proxy, and Go toolchain releases.
//
// Nothing here is copied out of the Renovate tree; behaviour is observed
// against the real, public protocols (the Go module proxy protocol at
// proxy.golang.org, and go.dev's own release JSON) and against a pinned
// Renovate container's request log. See CLAUDE.md's licensing section for
// why that separation matters next to Renovate's AGPL-3.0 license.
//
// # go (module proxy)
//
// A module's versions come from "GET {proxy}/{module}/@v/list", where the
// module path is case-encoded per the module proxy protocol (every uppercase
// letter becomes "!" followed by its lowercase form, so
// "github.com/BurntSushi/toml" is requested as
// "github.com/!burnt!sushi/toml"). Renovate additionally probes for a
// major-version suffix: after listing a module with no "/vN" (or, for
// gopkg.in-style paths, ".vN") suffix, it tries "/v2", "/v3", ... until a
// 404; a module already carrying a suffix is probed from N+1 onwards. This
// package reproduces that probing and merges every major's versions into one
// release set.
//
// Renovate fetches the "@v/{version}.info" timestamp document for every
// version it lists - 268 requests were observed for four modules in one run.
// This package deliberately does not: it fetches ".info" only for the
// newest version of each distinct major present in the merged list (ordered
// by a small semver-ish comparison on the numeric parts; getting that
// ordering exactly right is not important, it only decides which few
// versions are worth a timestamp round trip), best-effort - a failure or a
// version this package cannot order leaves that release's Timestamp at its
// zero value. The planner's first-seen fallback covers every other version,
// the same degradation terraformds documents for registries with no
// per-version timestamp endpoint at all.
//
// A comma-separated GOPROXY list is not supported: the registry value (the
// dependency's own RegistryURLs[0], or the default proxy.golang.org) is
// treated as exactly one proxy URL, and a value containing a comma is
// rejected with an error naming that shape.
//
// SourceURL is set from a naming convention, not fetched: for a module path
// starting with "github.com/", "gitlab.com/" or "bitbucket.org/" it is
// "https://" followed by the first three path segments. This is unmeasured -
// Renovate itself resolves source URLs through go-import meta tags, which
// this package does not implement - but it covers the hosts the estate's
// dependencies actually use.
//
// # Modules on the platform's own instance
//
// A module whose path starts with the GitLab instance's host is not on any
// proxy: it is a repository there, private or not, and its versions are
// tags. Renovate resolves such a path through the go-import meta tag the
// instance serves and then asks gitlab-tags; this package asks gitlab-tags
// directly, because the platform token is bound to the API paths it needs
// (decision record dependency-bot-credential-scope) and the meta tag is
// not one of them - unauthenticated, the instance answers that request
// with the enclosing group as the repository, which is wrong (measured
// 2026-09-14). The project behind the path is found by probing: the
// longest prefix of the path that is a project the token can see; what
// follows it is the module's directory in that repository, and Go names
// the versions of a module in a directory with tags prefixed by it -
// "go/v1.5.0" for the module in go/ - so only tags under that prefix
// count, stripped of it. A major-version suffix ("/v2") is neither
// project nor directory; it selects the tags of that major.
//
// # golang-version (Go toolchain releases)
//
// Renovate's own implementation parses a source file it fetches from
// raw.githubusercontent.com/golang/website. This package deliberately does
// not reproduce that: go.dev publishes the same information as structured
// JSON at "GET {go.dev}/dl/?mode=json&include=all", and reading that is both
// simpler and less brittle than scraping a source file meant for a website
// build. A version's "go" prefix is stripped ("go1.27.1" -> "1.27.1",
// "go1.27rc3" -> "1.27rc3" verbatim); the response is not filtered by
// "stable" - the versioning scheme is what decides which releases are
// eligible for automerge, not this datasource. The response also carries a
// "files" array per release, large enough to be worth avoiding: the decode
// target below names only "version" and "stable", which is enough for
// encoding/json to skip everything else the document sends. Go toolchain
// releases carry no publish timestamp anywhere in that document, and no
// source URL - both are left unmeasured/absent, same as an unmeasured
// registry in terraformds.
package gods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/semverx"
	"github.com/ohartwig/pinup/versioning"
)

// Kind selects which of the two datasources an instance serves.
type Kind string

const (
	// Module is Renovate's "go" datasource: Go modules through a module
	// proxy.
	Module Kind = "go"
	// Toolchain is Renovate's "golang-version" datasource: Go toolchain
	// releases from go.dev.
	Toolchain Kind = "golang-version"
)

// defaultProxy is proxy.golang.org, the module proxy every Go install talks
// to unless GOPROXY says otherwise.
const defaultProxy = "https://proxy.golang.org"

// defaultGoDev is go.dev, the host that publishes the toolchain release
// JSON this package reads.
const defaultGoDev = "https://go.dev"

// Datasource is one of the two Go datasources, bound to a default registry.
type Datasource struct {
	kind            Kind
	client          *httpx.Client
	defaultRegistry string
	// instanceHost is the platform's GitLab host and instanceTags that
	// instance's tags datasource, for modules that live there; empty when
	// there is none.
	instanceHost string
	instanceTags lookup.Datasource
}

// New returns a datasource of the given kind. An empty defaultRegistry
// defaults to proxy.golang.org for Module and go.dev for Toolchain.
func New(kind Kind, client *httpx.Client, defaultRegistry string) *Datasource {
	if defaultRegistry == "" {
		switch kind {
		case Toolchain:
			defaultRegistry = defaultGoDev
		default:
			defaultRegistry = defaultProxy
		}
	}
	return &Datasource{
		kind:            kind,
		client:          client,
		defaultRegistry: strings.TrimRight(defaultRegistry, "/"),
	}
}

func (d *Datasource) Name() string { return string(d.kind) }

// WithInstance makes the Module datasource serve modules whose path starts
// with host from the tags of that GitLab instance's projects, through
// tags - its gitlab-tags datasource. baseURL is the instance's URL; its host
// is what a module path starts with.
func (d *Datasource) WithInstance(baseURL string, tags lookup.Datasource) *Datasource {
	d.instanceHost = ""
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" && tags != nil {
		d.instanceHost = u.Host
		d.instanceTags = tags
	}
	return d
}

// DefaultVersioning is "semver" for both kinds - measured: Renovate's lookup
// log shows "versioning: semver" for both a `require` line and the
// `toolchain` line.
func (d *Datasource) DefaultVersioning() string { return "semver" }

// registryFor resolves the registry origin to use for ref: its own
// RegistryURLs[0] when set, the datasource's default otherwise. Applies to
// both kinds identically; for Toolchain it just selects between go.dev and
// whatever mirror a ref names, verbatim.
func (d *Datasource) registryFor(ref lookup.Ref) string {
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		return strings.TrimRight(ref.RegistryURLs[0], "/")
	}
	return d.defaultRegistry
}

func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	origin := d.registryFor(ref)
	switch d.kind {
	case Module:
		return d.moduleReleases(ctx, ref, origin)
	case Toolchain:
		return d.toolchainReleases(ctx, ref, origin)
	default:
		return nil, fmt.Errorf("unknown gods datasource kind %q", d.kind)
	}
}

// verEntry is one line off one module path's "@v/list" - the module path is
// kept alongside the version because a version found under a probed major
// suffix ("github.com/spf13/cobra/v2") must have its ".info" document
// fetched from that same path, not the base module's.
type verEntry struct {
	modulePath string
	rel        model.Release
}

func (d *Datasource) moduleReleases(ctx context.Context, ref lookup.Ref, origin string) (*model.ReleaseSet, error) {
	if strings.Contains(origin, ",") {
		return nil, fmt.Errorf("go: %q looks like a GOPROXY-style comma-separated proxy list, which this datasource does not support - configure exactly one proxy URL", origin)
	}

	base := ref.PackageName
	if d.instanceHost != "" && strings.HasPrefix(base, d.instanceHost+"/") {
		return d.instanceReleases(ctx, ref)
	}
	entries, err := d.fetchListEntries(ctx, origin, base)
	if err != nil {
		return nil, wrapModuleNotFound(base, origin, err)
	}
	entries = append(entries, d.probeMajors(ctx, origin, base)...)
	if len(entries) == 0 {
		// A module with no tags lists nothing; "@latest" then names the
		// pseudo-version of its newest commit, and that is the one
		// release it has. Measured: golang.org/x/mobile, whose only
		// update Renovate offers is the digest move to @latest.
		latest, err := d.fetchLatest(ctx, origin, base)
		if err != nil {
			return nil, wrapModuleNotFound(base, origin, err)
		}
		if latest.Version != "" {
			entries = append(entries, verEntry{modulePath: base, rel: latest})
		}
	}

	releases, targets := dedupeAndPickNewestPerMajor(entries)
	for _, t := range targets {
		d.applyInfoTimestamp(ctx, origin, t.modulePath, &releases[t.index])
	}

	return &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  string(Module),
		RegistryURL: origin,
		Releases:    releases,
		SourceURL:   sourceURLFor(base),
	}, nil
}

// instanceReleases lists the versions of a module hosted on the platform's
// own GitLab instance: the tags of the project behind the module path,
// under the prefix Go gives a module in a subdirectory.
func (d *Datasource) instanceReleases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	path := strings.TrimPrefix(ref.PackageName, d.instanceHost+"/")
	major := 0
	if prefix, sep, n, ok := parseMajorSuffix(path); ok && sep == "/" {
		path, major = prefix, n
	}
	segments := strings.Split(path, "/")
	base := "https://" + d.instanceHost
	var tags *model.ReleaseSet
	var dir string
	// A project path has at least two segments; a single one is a group
	// or a user, never a repository.
	for i := len(segments); i >= 2; i-- {
		project := strings.Join(segments[:i], "/")
		rs, err := d.instanceTags.Releases(ctx, lookup.Ref{PackageName: project, RegistryURLs: []string{base}})
		if err != nil {
			if se, ok := errors.AsType[*httpx.StatusError](err); ok && se.StatusCode == http.StatusNotFound {
				continue
			}
			return nil, fmt.Errorf("go: %s: %w", ref.PackageName, err)
		}
		tags, dir = rs, strings.Join(segments[i:], "/")
		break
	}
	if tags == nil {
		return nil, fmt.Errorf("go: %s: no project on %s answers for that path - it does not exist or the token cannot read it", ref.PackageName, d.instanceHost)
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	var releases []model.Release
	for _, r := range tags.Releases {
		v, ok := strings.CutPrefix(r.Version, prefix)
		if !ok || !strings.HasPrefix(v, "v") || strings.Contains(v, "/") {
			continue
		}
		// Go's rule: v0 and v1 live at the bare path, every later major
		// at its own suffix. A tag of the wrong major is another module's.
		numeral, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), ".")
		m, err := strconv.Atoi(numeral)
		if err != nil || (major == 0 && m > 1) || (major > 0 && m != major) {
			continue
		}
		r.Version = v
		releases = append(releases, r)
	}
	return &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  string(Module),
		RegistryURL: base,
		Releases:    releases,
		SourceURL:   tags.SourceURL,
	}, nil
}

// fetchListEntries gets "{origin}/{case-encoded modulePath}/@v/list" and
// turns each non-empty line into one verEntry.
func (d *Datasource) fetchListEntries(ctx context.Context, origin, modulePath string) ([]verEntry, error) {
	listURL := origin + "/" + encodeModulePath(modulePath) + "/@v/list"
	resp, err := d.client.Get(ctx, listURL, httpx.ReqOptions{Accept: "text/plain"})
	if err != nil {
		return nil, err
	}
	var entries []verEntry
	for line := range strings.Lines(string(resp.Body)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entries = append(entries, verEntry{modulePath: modulePath, rel: model.Release{Version: line}})
	}
	return entries, nil
}

// fetchLatest gets "{origin}/{case-encoded modulePath}/@latest", the
// proxy's answer for the newest version it knows - for an untagged module
// a pseudo-version, whose commit becomes the release's digest.
func (d *Datasource) fetchLatest(ctx context.Context, origin, modulePath string) (model.Release, error) {
	resp, err := d.client.Get(ctx, origin+"/"+encodeModulePath(modulePath)+"/@latest", httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return model.Release{}, err
	}
	var doc struct {
		Version string    `json:"Version"`
		Time    time.Time `json:"Time"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return model.Release{}, fmt.Errorf("@latest: %w", err)
	}
	return model.Release{Version: doc.Version, Timestamp: doc.Time, Digest: versioning.GoPseudoCommit(doc.Version)}, nil
}

// majorSuffixRE matches a module path's major-version suffix: "/vN" (the
// ordinary form) or ".vN" (the gopkg.in form, e.g. "gopkg.in/yaml.v3").
var majorSuffixRE = regexp.MustCompile(`^(.*)([/.])v([2-9][0-9]*)$`)

// parseMajorSuffix reports the trailing major-version suffix of path, if it
// has one.
func parseMajorSuffix(path string) (prefix, sep string, n int, ok bool) {
	m := majorSuffixRE.FindStringSubmatch(path)
	if m == nil {
		return "", "", 0, false
	}
	num, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, false
	}
	return m[1], m[2], num, true
}

// probeMajors implements the measured behaviour: for a module path without a
// "/vN" (N >= 2) suffix, probe "/v2", "/v3", ... until a 404; for a path
// already ending in "/vN" (or, for gopkg.in, ".vN"), probe from N+1 onwards.
// The first 404 stops probing; any other error also stops it without failing
// the lookup - the base list already fetched is the answer.
func (d *Datasource) probeMajors(ctx context.Context, origin, base string) []verEntry {
	prefix, sep, start, hasSuffix := parseMajorSuffix(base)
	beginAt := 2
	if hasSuffix {
		beginAt = start + 1
	} else {
		prefix = base
		sep = "/"
	}

	var out []verEntry
	for n := beginAt; ; n++ {
		candidate := prefix + sep + "v" + strconv.Itoa(n)
		more, err := d.fetchListEntries(ctx, origin, candidate)
		if err != nil {
			// A 404 means this major does not exist; any other error means
			// probing further cannot be trusted either. Both stop here
			// without touching the error the caller sees.
			return out
		}
		out = append(out, more...)
	}
}

// timestampTarget names one release, by its index into the returned slice,
// whose ".info" document is worth fetching.
type timestampTarget struct {
	index      int
	modulePath string
}

// dedupeAndPickNewestPerMajor merges entries into a release list (a version
// string seen more than once - which should not happen across distinct
// majors, but is cheap to guard - keeps its first occurrence) and picks the
// newest release of each distinct major version for timestamp enrichment.
func dedupeAndPickNewestPerMajor(entries []verEntry) ([]model.Release, []timestampTarget) {
	releases := make([]model.Release, 0, len(entries))
	modulePathOf := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))

	type best struct {
		index   int
		version semverx.Version
		parsed  bool
	}
	bestByMajor := map[int]best{}

	for _, e := range entries {
		if seen[e.rel.Version] {
			continue
		}
		seen[e.rel.Version] = true

		idx := len(releases)
		releases = append(releases, e.rel)
		modulePathOf = append(modulePathOf, e.modulePath)

		major, ok := majorOf(e.rel.Version)
		if !ok {
			continue
		}
		parsed, pok := semverx.Parse(e.rel.Version)
		cur, exists := bestByMajor[major]
		switch {
		case !exists:
			bestByMajor[major] = best{index: idx, version: parsed, parsed: pok}
		case pok && cur.parsed && semverx.CompareVersions(parsed, cur.version) > 0:
			bestByMajor[major] = best{index: idx, version: parsed, parsed: true}
		case pok && !cur.parsed:
			bestByMajor[major] = best{index: idx, version: parsed, parsed: true}
		}
	}

	targets := make([]timestampTarget, 0, len(bestByMajor))
	for _, b := range bestByMajor {
		targets = append(targets, timestampTarget{index: b.index, modulePath: modulePathOf[b.index]})
	}
	return releases, targets
}

// majorOf reads the leading numeric component of a "vX.Y.Z..." version
// string. It reports false for anything that does not start with a
// numeric major after the "v" - such a release simply does not take part in
// the per-major timestamp selection.
func majorOf(version string) (int, bool) {
	v, ok := strings.CutPrefix(version, "v")
	if !ok {
		return 0, false
	}
	dot := strings.IndexByte(v, '.')
	if dot < 0 {
		return semverx.Numeric(v)
	}
	if dot == 0 {
		return 0, false
	}
	return semverx.Numeric(v[:dot])
}

// applyInfoTimestamp fetches "{origin}/{case-encoded modulePath}/@v/{version}.info"
// best-effort and stamps rel.Timestamp from its "Time" field. Any failure -
// transport error, non-2xx, unparsable body - leaves Timestamp at its zero
// value; the planner's first-seen fallback is the documented degradation,
// not a lookup failure.
func (d *Datasource) applyInfoTimestamp(ctx context.Context, origin, modulePath string, rel *model.Release) {
	infoURL := origin + "/" + encodeModulePath(modulePath) + "/@v/" + rel.Version + ".info"
	resp, err := d.client.Get(ctx, infoURL, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return
	}
	var doc struct {
		Time time.Time `json:"Time"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return
	}
	rel.Timestamp = doc.Time
}

// wrapModuleNotFound turns a 404 from the "@v/list" endpoint into an error
// naming the module and the proxy it was not found on. proxy.golang.org
// answers such a 404 with a body starting with "not found:"; this does not
// depend on that body, only on the status code.
func wrapModuleNotFound(module, proxy string, err error) error {
	if se, ok := errors.AsType[*httpx.StatusError](err); ok && se.StatusCode == http.StatusNotFound {
		return fmt.Errorf("go: %s: 404 from %s - the module does not exist at that proxy", module, proxy)
	}
	return fmt.Errorf("go: %s: %w", module, err)
}

// encodeModulePath applies the Go module proxy protocol's case encoding:
// every uppercase letter becomes "!" followed by its lowercase form, so a
// case-sensitive module path survives a case-insensitive filesystem or
// storage layer underneath the proxy.
func encodeModulePath(path string) string {
	var b strings.Builder
	b.Grow(len(path) + 8)
	for _, r := range path {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sourceURLFor applies the one measured naming convention: for a module path
// hosted on github.com, gitlab.com or bitbucket.org, the source is
// "https://" followed by the first three path segments. Unmeasured beyond
// that - Renovate itself resolves this through go-import meta tags - but it
// covers the hosts that matter in this estate's corpus.
func sourceURLFor(modulePath string) string {
	for _, host := range [...]string{"github.com/", "gitlab.com/", "bitbucket.org/"} {
		if !strings.HasPrefix(modulePath, host) {
			continue
		}
		parts := strings.SplitN(modulePath, "/", 4)
		if len(parts) >= 3 {
			return "https://" + strings.Join(parts[:3], "/")
		}
	}
	return ""
}

// goDevRelease is deliberately narrow: go.dev's release JSON carries a large
// "files" array per entry that this datasource has no use for, and naming
// only the two fields it does use is what keeps encoding/json from ever
// decoding that array.
type goDevRelease struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

func (d *Datasource) toolchainReleases(ctx context.Context, ref lookup.Ref, origin string) (*model.ReleaseSet, error) {
	if ref.PackageName != "go" {
		return nil, fmt.Errorf("golang-version: unexpected package name %q, want %q", ref.PackageName, "go")
	}

	listURL := origin + "/dl/?mode=json&include=all"
	resp, err := d.client.Get(ctx, listURL, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("golang-version: %w", err)
	}
	var docs []goDevRelease
	if err := json.Unmarshal(resp.Body, &docs); err != nil {
		return nil, fmt.Errorf("golang-version: decode %s: %w", listURL, err)
	}

	releases := make([]model.Release, 0, len(docs))
	for _, rel := range docs {
		releases = append(releases, model.Release{Version: strings.TrimPrefix(rel.Version, "go")})
	}

	return &model.ReleaseSet{
		PackageName: "go",
		Datasource:  string(Toolchain),
		RegistryURL: origin,
		Releases:    releases,
	}, nil
}
