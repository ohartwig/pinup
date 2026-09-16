// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package dockerds implements the docker datasource against the Docker
// Registry HTTP API V2 and its token authentication extension. Both are
// public specifications (distribution/distribution and
// distribution/distribution/blob/main/docs/spec/auth/token.md); nothing here
// is derived from Renovate's implementation.
//
// The package name is a registry image reference - "alpine",
// "ghcr.io/oras-project/oras", "registry.ole-hartwig.eu/devops/ci-mirrors/alpine"
// - and Parse works out which host serves it and what the repository path is
// on that host. Docker Hub is the special case: an image with no host segment,
// or an explicit "docker.io", resolves to registry-1.docker.io, and a
// single-segment name gets the implicit "library/" namespace.
//
// # Why this package does not use httpx
//
// httpx.Client is built for static, per-host credentials attached before the
// first request. The registry protocol here is the opposite: every request
// may need a bearer token that is minted per (registry, repository) scope by
// a THIRD host (the "realm") named in a 401 challenge, cached for the life of
// the datasource, and never valid for a different repository. httpx has no
// hook for "attach this header, computed after inspecting the previous
// response, to this one request" - Client.Get takes no headers beyond the
// fixed ReqOptions. Rather than bend httpx to a shape it was not designed for,
// this package takes an http.RoundTripper directly (so tests can still use
// fake/harness) and does its own minimal, context-aware GET.
package dockerds

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// Name is the datasource name dependencies and rules refer to.
const Name = "docker"

// dockerHubRegistry is where an image with no explicit registry host lives.
// "docker.io" itself is never dialled directly - it is the human-facing
// alias, registry-1.docker.io is the API host it resolves to.
const dockerHubRegistry = "https://registry-1.docker.io"

// manifestAccept lists every manifest media type Digest is willing to accept,
// broadest (multi-arch index) first, in the order the registry HTTP API V2
// spec recommends offering them.
const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// pageSize is the number of tags requested per page. The registry may return
// fewer (or paginate internally regardless), which is exactly what the tests
// exercise.
const pageSize = 1000

// Credentials resolves HTTP basic auth for a token realm. It is called
// with the registry that issued the challenge and the realm it named, so
// the caller can bind the credential to its own registry: a registry a
// repository configuration names can otherwise name the instance's realm
// with a scope of its choosing and receive a token for it (review S5,
// 2026-09-13). A false ok means "ask anonymously", which is the normal
// case for Docker Hub and ghcr.io.
type Credentials func(registryHost string, realm *url.URL) (user, pass string, ok bool)

// The registry host is the bare host of the registry URL the lookup used
// ("registry.example.org"), the realm is the URL the challenge named.

// tokenKey is the cache identity for a bearer token: a token minted for one
// repository's scope must never be replayed against another, so the cache is
// keyed on both, not on the registry alone.
type tokenKey struct {
	registry   string
	repository string
}

// Datasource implements lookup.Datasource against the Docker Registry HTTP
// API V2.
type Datasource struct {
	hc          *http.Client
	credentials Credentials

	mu     sync.Mutex
	tokens map[tokenKey]string
}

// New returns a Datasource that dials the registry (and any token realm)
// through transport. A nil transport means http.DefaultTransport; credentials
// may be nil, meaning every realm is asked anonymously.
func New(transport http.RoundTripper, credentials Credentials) *Datasource {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Datasource{
		hc:          &http.Client{Transport: transport},
		credentials: credentials,
		tokens:      make(map[tokenKey]string),
	}
}

func (d *Datasource) Name() string { return Name }

// DefaultVersioning is "docker": tags do not follow semver, and the estate's
// versioning/docker scheme (leading numbers plus a reverse-ordered
// compatibility suffix) is what every image tag in the fixtures actually
// needs.
func (d *Datasource) DefaultVersioning() string { return "docker" }

// Parse splits an image reference into the registry that serves it and the
// repository path on that registry. It has no knowledge of a Ref's
// RegistryURLs override - that is applied by the caller, on top of this.
//
// The rule, in the order it is applied:
//
//  1. No "/" at all ("alpine"): Docker Hub, with the implicit "library/"
//     namespace official images live under.
//  2. The first segment looks like a host - it contains "." or ":", or is
//     exactly "localhost" - so it names a registry. "docker.io" is folded to
//     the real API host; every other host is used as given.
//  3. Otherwise the whole name is a Docker Hub repository that already
//     carries its own namespace ("binwiederhier/ntfy"): no host segment to
//     strip, no "library/" to add.
func Parse(name string) (registry, repository string) {
	first, rest, hasSlash := strings.Cut(name, "/")
	if !hasSlash {
		return dockerHubRegistry, "library/" + name
	}
	if looksLikeHost(first) {
		if strings.EqualFold(first, "docker.io") {
			return dockerHubRegistry, rest
		}
		return "https://" + first, rest
	}
	return dockerHubRegistry, name
}

func looksLikeHost(segment string) bool {
	return strings.ContainsAny(segment, ".:") || segment == "localhost"
}

// resolve applies a Ref's registry override, if any, on top of Parse.
func resolve(ref lookup.Ref) (registry, repository string) {
	registry, repository = Parse(ref.PackageName)
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		registry = ref.RegistryURLs[0]
	}
	return strings.TrimRight(registry, "/"), repository
}

// Releases lists every tag a repository currently has. The registry API
// carries no timestamp for a tag, so every Release's Timestamp is left zero -
// inventing one would misrepresent what is actually known, and the planner
// already has a defined fallback (TimeUnknown) for exactly this case.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	registry, repository := resolve(ref)

	tags, err := d.listTags(ctx, registry, repository)
	if err != nil {
		return nil, err
	}

	rs := &model.ReleaseSet{
		PackageName: ref.PackageName,
		Datasource:  Name,
		RegistryURL: registry,
	}
	seen := make(map[string]bool, len(tags))
	for _, t := range tags {
		if seen[t] {
			continue
		}
		seen[t] = true
		rs.Releases = append(rs.Releases, model.Release{Version: t})
	}
	return rs, nil
}

// Digest fetches the content digest of one tag. It is deliberately not
// called from Releases: a manifest request per tag would turn one lookup
// into hundreds against a repository with a long tag history. Callers ask
// for the digest of the one tag they are about to pin.
func (d *Datasource) Digest(ctx context.Context, ref lookup.Ref, tag string) (string, error) {
	registry, repository := resolve(ref)
	u := registry + "/v2/" + repository + "/manifests/" + url.PathEscape(tag)

	resp, err := d.authenticatedGet(ctx, registry, repository, u, manifestAccept)
	if err != nil {
		return "", err
	}
	if digest := resp.header.Get("Docker-Content-Digest"); digest != "" {
		return digest, nil
	}
	// Some registries omit the header on certain proxies; the content itself
	// is still addressable by its own hash.
	sum := sha256.Sum256(resp.body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Manifest returns a tag's manifest as the registry serves it - what an
// analyzer reads a chart's layers from. Not called from Releases, for the
// same reason Digest is not.
func (d *Datasource) Manifest(ctx context.Context, ref lookup.Ref, tag string) ([]byte, error) {
	registry, repository := resolve(ref)
	u := registry + "/v2/" + repository + "/manifests/" + url.PathEscape(tag)
	resp, err := d.authenticatedGet(ctx, registry, repository, u, manifestAccept)
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

// Blob returns a blob by digest, up to limit bytes; a registry that
// redirects blobs to object storage is followed, and the bearer token
// stays behind on the way (net/http drops Authorization across hosts).
func (d *Datasource) Blob(ctx context.Context, ref lookup.Ref, digest string, limit int64) ([]byte, error) {
	registry, repository := resolve(ref)
	u := registry + "/v2/" + repository + "/blobs/" + url.PathEscape(digest)
	key := tokenKey{registry: registry, repository: repository}
	resp, err := d.doGetLimit(ctx, u, "", d.tokenFor(key), limit)
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusUnauthorized {
		token, aerr := d.authenticate(ctx, registry, repository, resp.header)
		if aerr != nil {
			return nil, fmt.Errorf("docker: %s: %w", repository, aerr)
		}
		d.setToken(key, token)
		if resp, err = d.doGetLimit(ctx, u, "", token, limit); err != nil {
			return nil, err
		}
	}
	if resp.status != http.StatusOK {
		return nil, fmt.Errorf("docker: %s blob %s: status %d", repository, digest, resp.status)
	}
	return resp.body, nil
}

// listTags walks every page of the tags list, following Link: rel="next" per
// RFC 5988 until the registry stops sending one.
func (d *Datasource) listTags(ctx context.Context, registry, repository string) ([]string, error) {
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=%d", registry, repository, pageSize)

	var tags []string
	for next != "" {
		resp, err := d.authenticatedGet(ctx, registry, repository, next, "application/json")
		if err != nil {
			return nil, err
		}

		var page struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(resp.body, &page); err != nil {
			return nil, fmt.Errorf("docker: %s: decode tags/list response: %w", repository, err)
		}
		tags = append(tags, page.Tags...)

		next, err = nextPage(resp.header, registry)
		if err != nil {
			return nil, fmt.Errorf("docker: %s: %w", repository, err)
		}
	}
	return tags, nil
}

// nextPage extracts the rel="next" URL from a Link header (RFC 5988),
// resolving it against registry when the server sent a relative path - which
// real registries do, since the header describes a path on themselves.
func nextPage(header http.Header, registry string) (string, error) {
	link := header.Get("Link")
	if link == "" {
		return "", nil
	}
	target, params, found := strings.Cut(link, ";")
	if !found || !strings.Contains(params, `rel="next"`) {
		return "", nil
	}
	target = strings.TrimSpace(target)
	target = strings.TrimPrefix(target, "<")
	target = strings.TrimSuffix(target, ">")

	ref, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("bad Link header %q: %w", link, err)
	}
	base, err := url.Parse(registry)
	if err != nil {
		return "", fmt.Errorf("bad registry URL %q: %w", registry, err)
	}
	return base.ResolveReference(ref).String(), nil
}

// httpResponse is the trimmed-down shape this package needs from an
// http.Response: the body already read into memory (the underlying
// connection is released as soon as doGet returns) and the headers callers
// need (Link, Docker-Content-Digest, WWW-Authenticate).
type httpResponse struct {
	status int
	body   []byte
	header http.Header
}

// authenticatedGet performs a GET, handling the bearer-token challenge
// transparently: it attaches whatever token is cached for (registry,
// repository), and on a 401 - whether because none was cached yet or because
// a cached one turned out to be stale or wrongly scoped - it authenticates
// fresh and retries exactly once. A second 401 is reported rather than
// looped on.
func (d *Datasource) authenticatedGet(ctx context.Context, registry, repository, rawURL, accept string) (*httpResponse, error) {
	key := tokenKey{registry: registry, repository: repository}

	resp, err := d.doGet(ctx, rawURL, accept, d.tokenFor(key))
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusUnauthorized {
		token, aerr := d.authenticate(ctx, registry, repository, resp.header)
		if aerr != nil {
			return nil, fmt.Errorf("docker: %s: %w", repository, aerr)
		}
		d.setToken(key, token)

		resp, err = d.doGet(ctx, rawURL, accept, token)
		if err != nil {
			return nil, err
		}
	}
	return classify(resp, registry, repository)
}

// classify turns a final (post-retry) response into either the response
// itself or the error a caller should see. It is the one place all four
// registry error shapes the spec calls out are given a message.
func classify(resp *httpResponse, registry, repository string) (*httpResponse, error) {
	switch {
	case resp.status >= 200 && resp.status < 300:
		return resp, nil
	case resp.status == http.StatusNotFound:
		// Registries do not distinguish "does not exist" from "you may not
		// pull this" - both are a 404, and the message must say both, or a
		// reader takes it as proof of absence when it might be a
		// credentials problem.
		return nil, fmt.Errorf(
			"docker: repository %q not found at %s - it does not exist, or the credentials cannot pull it",
			repository, registry)
	case resp.status == http.StatusTooManyRequests || bytes.Contains(resp.body, []byte("TOOMANYREQUESTS")):
		return nil, fmt.Errorf("docker: %s: rate limited by %s", repository, registry)
	case resp.status == http.StatusUnauthorized:
		return nil, fmt.Errorf("docker: %s: authentication to %s failed even after obtaining a token", repository, registry)
	default:
		return nil, fmt.Errorf("docker: %s: unexpected status %d from %s", repository, resp.status, registry)
	}
}

// tokenFor returns the cached token for key, or "" if none is cached yet -
// "" is a valid argument to doGet, meaning "ask anonymously".
func (d *Datasource) tokenFor(key tokenKey) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tokens[key]
}

func (d *Datasource) setToken(key tokenKey, token string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tokens[key] = token
}

// doGet performs one GET, attaching Authorization only when token is
// non-empty, and reads the body fully before returning so the connection can
// go back to the pool.
func (d *Datasource) doGet(ctx context.Context, rawURL, accept, token string) (*httpResponse, error) {
	return d.doGetLimit(ctx, rawURL, accept, token, 4<<20)
}

// doGetLimit is doGet with the body bound to limit bytes.
func (d *Datasource) doGetLimit(ctx context.Context, rawURL, accept, token string, limit int64) (*httpResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("docker: build request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := d.hc.Do(req)
	if err != nil {
		// The stdlib error text names addresses and protocols, never our
		// headers, so it is safe to wrap directly - no token to leak here.
		return nil, fmt.Errorf("docker: request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("docker: read response from %s: %w", req.URL.Host, err)
	}
	return &httpResponse{status: resp.StatusCode, body: body, header: resp.Header.Clone()}, nil
}

// authenticate runs the token dance for one 401: parse the WWW-Authenticate
// challenge, ask the realm it names for a token scoped to repository, and
// return it. The token is never logged or wrapped into an error message.
func (d *Datasource) authenticate(ctx context.Context, registry, repository string, header http.Header) (string, error) {
	challenge := header.Get("WWW-Authenticate")
	realm, service, scope, err := parseChallenge(challenge)
	if err != nil {
		return "", err
	}
	// The scope is what a lookup needs and nothing the registry asks for:
	// pull on the repository being fetched. A challenge naming another
	// repository or push is answered with that scope anyway, and the
	// credential below is withheld for it.
	wantScope := "repository:" + repository + ":pull"
	foreignScope := scope != "" && scope != wantScope
	scope = wantScope

	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("bad realm %q: %w", realm, err)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Basic auth on the realm request only - never on the registry, only
	// over TLS, only for the scope a lookup needs, and only when the
	// caller's binding says this registry and realm are its own.
	registryHost := registry
	if ru, err := url.Parse(registry); err == nil && ru.Host != "" {
		registryHost = ru.Host
	}
	if d.credentials != nil && !foreignScope && u.Scheme == "https" {
		if user, pass, ok := d.credentials(registryHost, u); ok {
			req.SetBasicAuth(user, pass)
		}
	}

	resp, err := d.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request to %s failed: %w", u.Host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read token response from %s: %w", u.Host, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body is never included: a realm's error page can echo back
		// query parameters, and on a misconfigured server, worse.
		return "", fmt.Errorf("token request to %s failed with status %d", u.Host, resp.StatusCode)
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode token response from %s: %w", u.Host, err)
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("token response from %s carried neither token nor access_token", u.Host)
	}
	return token, nil
}

// splitChallenge splits the challenge's parameters on the commas between
// them, not on a comma inside a quoted value: scope="repository:x:pull,push"
// is one parameter, and reading it as two would drop the push a caller
// must see.
func splitChallenge(s string) []string {
	var fields []string
	start, quoted := 0, false
	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			fields = append(fields, s[start:i])
			start = i + 1
		}
	}
	return append(fields, s[start:])
}

// parseChallenge reads a "Bearer realm=\"...\",service=\"...\",scope=\"...\""
// WWW-Authenticate header per RFC 6750 / the distribution token spec.
// service and scope may be absent; realm may not.
func parseChallenge(header string) (realm, service, scope string, err error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", "", "", fmt.Errorf("unsupported WWW-Authenticate challenge: %q", header)
	}
	for _, field := range splitChallenge(header[len(prefix):]) {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		switch key {
		case "realm":
			realm = value
		case "service":
			service = value
		case "scope":
			scope = value
		}
	}
	if realm == "" {
		return "", "", "", fmt.Errorf("challenge names no realm: %q", header)
	}
	return realm, service, scope, nil
}
