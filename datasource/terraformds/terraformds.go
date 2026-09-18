// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package terraformds implements the terraform-provider and terraform-module
// datasources against the Terraform Registry Protocol
// (developer.hashicorp.com/terraform/internals/module-registry-protocol,
// developer.hashicorp.com/terraform/internals/provider-registry-protocol), a
// public specification both registry.terraform.io and registry.opentofu.org
// implement. Nothing here is copied out of the Renovate tree; behaviour is
// implemented from the documented, versioned contract itself and cross
// checked against curl and against a pinned Renovate container. See
// CLAUDE.md's licensing section for why that distinction matters in this
// repository.
//
// Both kinds share the same shape: discover the registry's real API paths
// from its service-discovery document, fetch the versions list, and,
// best-effort, enrich each release with a publish timestamp and the package
// with a source URL. What differs between the two kinds is only the package
// name shape ("namespace/name" for a provider, "namespace/name/provider" for
// a module) and the path segment the protocol adds for a module
// ("{namespace}/{name}/{provider}/versions" instead of
// "{namespace}/{name}/versions") - not enough to justify two types, the same
// reasoning githubds and gitlabds already apply to their own kinds.
//
// Timestamps and source URLs are measured, not guessed at, and they diverge
// by registry:
//
//   - registry.opentofu.org publishes a "discovered" timestamp on the
//     versions endpoint itself, but that is when OpenTofu's mirror first saw
//     the release, not when the upstream author published it - measured on
//     cloudflare/cloudflare 2.9.0: "discovered" reads 2026-04-21 while
//     Renovate's own releaseTimestamp for that version is 2020-07-29T22:33:53Z.
//     That real timestamp comes from OpenTofu's separate docs API
//     (api.opentofu.org), which this package fetches best-effort per package.
//     The source URL for registry.opentofu.org is not fetched at all: it
//     follows a fixed naming convention the registry enforces on every
//     provider and module it mirrors.
//   - Every other registry (registry.terraform.io included) publishes no
//     per-version timestamp anywhere this package could measure, so releases
//     from those registries carry a zero Timestamp and the planner falls
//     back to first-seen age. Their source URL comes from the plain package
//     metadata endpoint's "source" field, fetched best-effort.
//
// The module source-URL convention
// ("https://github.com/{namespace}/terraform-{provider}-{name}") is measured
// on exactly one example (terraform-aws-modules/vpc/aws); it is applied
// uniformly rather than special-cased because the provider convention it
// mirrors is measured on two.
package terraformds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// Kind selects which of the two Terraform Registry Protocol datasources an
// instance serves.
type Kind string

const (
	Provider Kind = "terraform-provider"
	Module   Kind = "terraform-module"
)

// openTofuHost is the one registry this package knows a second, richer API
// for. Every other host - including registry.terraform.io - is served
// through the plain protocol endpoints only.
const openTofuHost = "registry.opentofu.org"

// openTofuDocsBase is OpenTofu's documentation API, which carries the
// upstream publish timestamp the registry's own "discovered" field does not.
// It is a fixed, separate host from the registry itself, so requests to it
// are registered on the refusing transport independently in tests.
const openTofuDocsBase = "https://api.opentofu.org"

// fallbackProvidersPath and fallbackModulesPath are the documented defaults
// (developer.hashicorp.com/terraform/internals/provider-registry-protocol),
// used whenever service discovery cannot be trusted.
const (
	fallbackProvidersPath = "/v1/providers"
	fallbackModulesPath   = "/v1/modules"
)

// discoveryPaths is what one registry's "GET /.well-known/terraform.json"
// resolved to, cached per origin so a run does one discovery per host.
// Both fields are already absolute ("https://host/v1/providers", not
// "/v1/providers"), with no trailing slash, so every caller joins them the
// same way regardless of whether the registry answered with a relative or
// an absolute path.
type discoveryPaths struct {
	providersBase string
	modulesBase   string
}

// Datasource is one of the two Terraform Registry Protocol datasources,
// bound to a default registry.
type Datasource struct {
	kind            Kind
	client          *httpx.Client
	defaultRegistry string

	mu          sync.Mutex
	discoveries map[string]discoveryPaths
}

// New returns a datasource of the given kind. An empty defaultRegistry
// defaults to registry.terraform.io; a dependency's own RegistryURLs[0]
// overrides it at lookup time - the estate always sets that to
// registry.opentofu.org, since every provider in koh-infra is mirrored
// there rather than resolved against HashiCorp's registry directly.
func New(kind Kind, client *httpx.Client, defaultRegistry string) *Datasource {
	if defaultRegistry == "" {
		defaultRegistry = "https://registry.terraform.io"
	}
	return &Datasource{
		kind:            kind,
		client:          client,
		defaultRegistry: strings.TrimRight(defaultRegistry, "/"),
		discoveries:     map[string]discoveryPaths{},
	}
}

func (d *Datasource) Name() string { return string(d.kind) }

// DefaultVersioning is "hashicorp" for both kinds - the versioning scheme
// every provider and module constraint in the estate is written against.
func (d *Datasource) DefaultVersioning() string { return "hashicorp" }

// registryFor resolves the registry origin to use for ref: its own
// RegistryURLs[0] when set, the datasource's default otherwise.
func (d *Datasource) registryFor(ref lookup.Ref) string {
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		return strings.TrimRight(ref.RegistryURLs[0], "/")
	}
	return d.defaultRegistry
}

func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	origin := d.registryFor(ref)
	switch d.kind {
	case Provider:
		return d.providerReleases(ctx, ref, origin)
	case Module:
		return d.moduleReleases(ctx, ref, origin)
	default:
		return nil, fmt.Errorf("unknown terraform datasource kind %q", d.kind)
	}
}

func (d *Datasource) providerReleases(ctx context.Context, ref lookup.Ref, origin string) (*model.ReleaseSet, error) {
	namespace, name, err := splitProviderName(ref.PackageName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.kind, err)
	}

	providersBase, _ := d.discover(ctx, origin)
	versionsURL := providersBase + "/" + namespace + "/" + name + "/versions"

	releases, err := fetchVersions(ctx, d.client, versionsURL, providerVersionsDoc{})
	if err != nil {
		return nil, wrapNotFound(d.kind, ref.PackageName, origin, err)
	}

	rs := &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  string(d.kind),
		RegistryURL: origin,
		Releases:    releases,
	}

	if strings.EqualFold(hostOf(origin), openTofuHost) {
		// Measured convention, not fetched: registry.opentofu.org names every
		// mirrored provider's source repository this way.
		rs.SourceURL = fmt.Sprintf("https://github.com/%s/terraform-provider-%s", namespace, name)
		docsURL := fmt.Sprintf("%s/registry/docs/providers/%s/%s/index.json", openTofuDocsBase, namespace, name)
		applyOpenTofuTimestamps(ctx, d.client, docsURL, rs.Releases)
	} else {
		// Not measured for registry.terraform.io (or any other host): no
		// per-version timestamp endpoint is known, so releases keep a zero
		// Timestamp and the planner falls back to first-seen age.
		metadataURL := providersBase + "/" + namespace + "/" + name
		rs.SourceURL = fetchSourceURL(ctx, d.client, metadataURL)
	}
	return rs, nil
}

func (d *Datasource) moduleReleases(ctx context.Context, ref lookup.Ref, origin string) (*model.ReleaseSet, error) {
	namespace, name, provider, err := splitModuleName(ref.PackageName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.kind, err)
	}

	_, modulesBase := d.discover(ctx, origin)
	versionsURL := modulesBase + "/" + namespace + "/" + name + "/" + provider + "/versions"

	releases, err := fetchVersions(ctx, d.client, versionsURL, moduleVersionsDoc{})
	if err != nil {
		return nil, wrapNotFound(d.kind, ref.PackageName, origin, err)
	}

	rs := &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  string(d.kind),
		RegistryURL: origin,
		Releases:    releases,
	}

	if strings.EqualFold(hostOf(origin), openTofuHost) {
		// Measured on exactly one example
		// (terraform-aws-modules/vpc/aws -> .../terraform-aws-vpc); see the
		// package comment.
		rs.SourceURL = fmt.Sprintf("https://github.com/%s/terraform-%s-%s", namespace, provider, name)
		docsURL := fmt.Sprintf("%s/registry/docs/modules/%s/%s/%s/index.json", openTofuDocsBase, namespace, name, provider)
		applyOpenTofuTimestamps(ctx, d.client, docsURL, rs.Releases)
	} else {
		metadataURL := modulesBase + "/" + namespace + "/" + name + "/" + provider
		rs.SourceURL = fetchSourceURL(ctx, d.client, metadataURL)
	}
	return rs, nil
}

// discover resolves origin's service-discovery document
// ("GET {origin}/.well-known/terraform.json") to absolute API path
// prefixes, caching a successful result for the lifetime of the Datasource.
//
// A failure (non-2xx, or a body that does not parse) is never cached: it
// falls back to the documented default paths for this call only, so a
// transient hiccup on one lookup does not poison every later one against
// the same host for the rest of the run.
func (d *Datasource) discover(ctx context.Context, origin string) (providersBase, modulesBase string) {
	d.mu.Lock()
	if disc, ok := d.discoveries[origin]; ok {
		d.mu.Unlock()
		return disc.providersBase, disc.modulesBase
	}
	d.mu.Unlock()

	providersBase = origin + fallbackProvidersPath
	modulesBase = origin + fallbackModulesPath

	resp, err := d.client.Get(ctx, origin+"/.well-known/terraform.json", httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return providersBase, modulesBase
	}

	var doc struct {
		ProvidersV1 string `json:"providers.v1"`
		ModulesV1   string `json:"modules.v1"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return providersBase, modulesBase
	}

	if p := resolvePath(origin, doc.ProvidersV1); p != "" {
		providersBase = p
	}
	if m := resolvePath(origin, doc.ModulesV1); m != "" {
		modulesBase = m
	}

	d.mu.Lock()
	d.discoveries[origin] = discoveryPaths{providersBase: providersBase, modulesBase: modulesBase}
	d.mu.Unlock()
	return providersBase, modulesBase
}

// resolvePath turns a service-discovery path into an absolute, no-trailing-
// slash base URL. A value starting with "/" is relative to origin, as the
// protocol allows; anything already absolute is used verbatim.
func resolvePath(origin, path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return strings.TrimSuffix(path, "/")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return origin + strings.TrimSuffix(path, "/")
}

// hostOf returns the host component of an absolute URL, or "" if it cannot
// be parsed - which just means the openTofuHost comparison below fails, the
// safe direction (falls back to the un-enriched path).
func hostOf(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return ""
	}
	rest := rawURL[i+3:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// providerVersionsDoc is "GET {providers.v1}{namespace}/{name}/versions".
type providerVersionsDoc struct {
	Versions []struct {
		Version string `json:"version"`
	} `json:"versions"`
}

func (d providerVersionsDoc) releases() []model.Release {
	out := make([]model.Release, 0, len(d.Versions))
	for _, v := range d.Versions {
		out = append(out, model.Release{Version: v.Version})
	}
	return out
}

// moduleVersionsDoc is
// "GET {modules.v1}{namespace}/{name}/{provider}/versions". Renovate reads
// modules[0].versions - the registry protocol accommodates more than one
// module entry per response for reasons this datasource has no use for, and
// no vector in the estate's corpus exercises more than one.
type moduleVersionsDoc struct {
	Modules []struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	} `json:"modules"`
}

func (d moduleVersionsDoc) releases() []model.Release {
	if len(d.Modules) == 0 {
		return nil
	}
	items := d.Modules[0].Versions
	out := make([]model.Release, 0, len(items))
	for _, v := range items {
		out = append(out, model.Release{Version: v.Version})
	}
	return out
}

// versionsDoc is implemented by both response shapes, so fetchVersions can
// be shared: the only difference between the provider and module endpoints
// is how their JSON nests the version list.
type versionsDoc interface {
	releases() []model.Release
}

// fetchVersions gets versionsURL and decodes it as shape, returning
// releases in the order the registry sent them - not sorted, because
// ordering across registries is the planner's job, not a datasource's (see
// gitlabds and githubds, which apply the same rule).
func fetchVersions[T versionsDoc](ctx context.Context, client *httpx.Client, versionsURL string, shape T) ([]model.Release, error) {
	resp, err := client.Get(ctx, versionsURL, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(resp.Body, &shape); err != nil {
		return nil, fmt.Errorf("decode versions from %s: %w", versionsURL, err)
	}
	return shape.releases(), nil
}

// wrapNotFound turns a 404 from the versions endpoint into an error naming
// the package and the registry it was not found in - never an empty
// ReleaseSet, which would read as "this package has no releases" rather
// than "this package does not exist here".
func wrapNotFound(kind Kind, packageName, registry string, err error) error {
	if se, ok := errors.AsType[*httpx.StatusError](err); ok && se.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %s: 404 from %s - the package does not exist on that registry",
			kind, packageName, registry)
	}
	return fmt.Errorf("%s: %s: %w", kind, packageName, err)
}

// applyOpenTofuTimestamps fetches docsURL best-effort and stamps a matching
// Timestamp onto each release whose version appears in the document's
// "versions[].id" (with its leading "v" stripped). Any failure - transport
// error, non-2xx, unparsable body - leaves every Timestamp at its zero value
// and returns nothing to report: the planner's first-seen fallback is the
// documented degradation, not a lookup failure.
func applyOpenTofuTimestamps(ctx context.Context, client *httpx.Client, docsURL string, releases []model.Release) {
	resp, err := client.Get(ctx, docsURL, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return
	}
	var doc struct {
		Versions []struct {
			ID        string    `json:"id"`
			Published time.Time `json:"published"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return
	}

	published := make(map[string]time.Time, len(doc.Versions))
	for _, v := range doc.Versions {
		published[strings.TrimPrefix(v.ID, "v")] = v.Published
	}
	for i := range releases {
		if ts, ok := published[releases[i].Version]; ok {
			releases[i].Timestamp = ts
		}
	}
}

// fetchSourceURL fetches metadataURL best-effort and returns its "source"
// field, or "" on any failure - the same best-effort degradation as
// applyOpenTofuTimestamps, for the registries that carry a source URL on
// the plain metadata endpoint instead of a richer docs API.
func fetchSourceURL(ctx context.Context, client *httpx.Client, metadataURL string) string {
	resp, err := client.Get(ctx, metadataURL, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return ""
	}
	var doc struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return ""
	}
	return doc.Source
}

// splitProviderName reduces PackageName to (namespace, name) for the
// terraform-provider datasource.
func splitProviderName(name string) (namespace, provName string, err error) {
	parts := strings.Split(name, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf(`%q is not "namespace/name" for a terraform provider`, name)
	}
	return parts[0], parts[1], nil
}

// splitModuleName reduces PackageName to (namespace, name, provider) for
// the terraform-module datasource. A fourth, leading "host/" segment -
// which Terraform itself accepts to name a non-default module registry - is
// deliberately not supported here: this datasource already resolves the
// registry from RegistryURLs, and a caller that also encoded it into
// PackageName gets an error naming the expected shape rather than a
// datasource that silently ignores one of the two.
func splitModuleName(name string) (namespace, modName, provider string, err error) {
	parts := strings.Split(name, "/")
	if len(parts) == 4 && parts[0] != "" && parts[1] != "" && parts[2] != "" && parts[3] != "" {
		return "", "", "", fmt.Errorf(
			`%q carries a leading host segment ("host/namespace/name/provider"), which the terraform-module datasource does not support - use "namespace/name/provider" and set the registry via RegistryURLs instead`, name)
	}
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf(`%q is not "namespace/name/provider" for a terraform module`, name)
	}
	return parts[0], parts[1], parts[2], nil
}
