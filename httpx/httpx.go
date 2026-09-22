// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package httpx is the HTTP client every datasource in pinup uses to reach
// upstream registries and VCS hosts. It centralises three concerns that are
// easy to get wrong if left to each datasource: attaching per-host
// credentials without ever leaking them, retrying transient failures with a
// polite backoff, and bounding how many requests hit a single host at once
// so a slow registry does not get hammered by a large plan run.
package httpx

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostRule is per-host configuration, matched by host[:port] as it appears
// in the request URL (e.g. "codeberg.org" or "registry.example.com:8443").
type HostRule struct {
	// MatchHost is compared against the request URL's host, case-insensitively.
	MatchHost string

	// Token, when set, is sent as "Authorization: Bearer <token>", unless
	// HeaderName redirects it to a different header.
	Token string

	// Username and Password, when set, are sent as HTTP basic auth. Token
	// takes precedence over basic auth if both are configured.
	Username string
	Password string

	// HeaderName, when set, carries Token instead of the Authorization
	// header. Useful for hosts that expect e.g. "PRIVATE-TOKEN: <token>".
	HeaderName string

	// MaxConcurrent bounds in-flight requests to this host. 0 means a
	// sensible default (8).
	MaxConcurrent int

	// PathPattern, when set, is a regular expression the request path must
	// match for the credential to be sent; a request to the host on any
	// other path goes out anonymously. What a request's path is, a
	// repository configuration decides (registryUrls, a custom datasource);
	// binding the platform token to the paths pinup itself calls is what
	// keeps a configuration from reading the instance API with it.
	PathPattern string

	// ExposeToPlugins is carried through untouched; httpx does not act on
	// it, but callers that hand HostRule to plugin sandboxes read it back.
	ExposeToPlugins bool
}

// Options configures a Client.
type Options struct {
	// Transport is the underlying RoundTripper. nil means http.DefaultTransport.
	Transport http.RoundTripper

	// HostRules supplies per-host credentials and concurrency limits.
	HostRules []HostRule

	// MaxRetries is the number of additional attempts after the first one
	// on a retryable failure (429 or 5xx). 0 means no retries.
	MaxRetries int

	// UserAgent, when set, is sent on every request.
	UserAgent string

	// Now returns the current time. It is required: httpx never calls
	// time.Now itself, so tests can supply a fixed or stepped clock and get
	// deterministic, instant retry behaviour.
	Now func() time.Time

	// Sleep is called to wait out a backoff between retries. It is
	// required: httpx never calls time.Sleep itself, so retry tests can
	// record durations instead of actually waiting.
	Sleep func(time.Duration)

	// MaxBody bounds a response body; zero means 64 MiB - npm's full
	// package documents are the largest thing any datasource reads, and
	// a registry that streams forever must not hold a partition's memory.
	MaxBody int64
	// Timeout bounds one attempt, connection and body included; zero
	// means 60 s. A caller's context deadline still applies on top.
	Timeout time.Duration
}

const (
	defaultMaxBody = 64 << 20
	defaultTimeout = 60 * time.Second
)

// ReqOptions controls conditional request headers for a single Get call.
type ReqOptions struct {
	// ETag, when set, is sent as If-None-Match.
	ETag string
	// LastModified, when set, is sent as If-Modified-Since.
	LastModified string
	// Accept, when set, is sent as the Accept header.
	Accept string
}

// Response is the outcome of a successful (possibly conditional) request.
// "Successful" includes 304 Not Modified, which is reported via NotModified
// rather than as an error.
type Response struct {
	StatusCode   int
	Body         []byte
	ETag         string
	LastModified string
	NotModified  bool
	Header       http.Header
}

// StatusError is returned for a non-2xx, non-304 response. It carries the
// code so a caller can tell 404 from 403 from 500 with errors.As instead of
// parsing the message - a datasource needs that to say "does not exist or
// the token cannot read it" for a 404 and something different for the rest.
type StatusError struct {
	StatusCode int
	URL        string
	// Header carries the response headers: a rate-limited 403 is only
	// distinguishable from a permission 403 by X-RateLimit-Remaining.
	Header http.Header
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("httpx: unexpected status %d from %s", e.StatusCode, e.URL)
}

// Client is a shared, concurrency-safe HTTP client for datasources.
type Client struct {
	hc         *http.Client
	hostRules  map[string]HostRule
	paths      map[string]*regexp.Regexp
	maxRetries int
	userAgent  string
	now        func() time.Time
	sleep      func(time.Duration)
	maxBody    int64
	timeout    time.Duration

	mu   sync.Mutex
	sems map[string]chan struct{}
}

// defaultMaxConcurrent is used when a host has no rule, or a rule that
// leaves MaxConcurrent at its zero value.
const defaultMaxConcurrent = 8

// defaultMaxRedirects mirrors the cap net/http applies when it manages
// CheckRedirect itself; we replicate it because supplying our own
// CheckRedirect disables that built-in cap.
const defaultMaxRedirects = 10

// New builds a Client from opts. It panics if Now or Sleep is nil: both are
// load-bearing for deterministic tests, and a silent time.Now/time.Sleep
// fallback here would defeat that guarantee (and violate the house rule
// that time.Now never appears in this package's non-test code).
func New(opts Options) *Client {
	if opts.Now == nil {
		panic("httpx: Options.Now is required")
	}
	if opts.Sleep == nil {
		panic("httpx: Options.Sleep is required")
	}

	transport := opts.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	hostRules := make(map[string]HostRule, len(opts.HostRules))
	paths := make(map[string]*regexp.Regexp, len(opts.HostRules))
	for _, r := range opts.HostRules {
		hostRules[strings.ToLower(r.MatchHost)] = r
		if r.PathPattern != "" {
			// A pattern that does not compile binds the credential to
			// nothing: anonymous is the safe direction.
			paths[strings.ToLower(r.MatchHost)] = regexp.MustCompile(r.PathPattern)
		}
	}

	c := &Client{
		hostRules:  hostRules,
		paths:      paths,
		maxRetries: opts.MaxRetries,
		userAgent:  opts.UserAgent,
		now:        opts.Now,
		sleep:      opts.Sleep,
		maxBody:    cmp.Or(opts.MaxBody, int64(defaultMaxBody)),
		timeout:    cmp.Or(opts.Timeout, defaultTimeout),
		sems:       make(map[string]chan struct{}),
	}
	c.hc = &http.Client{
		Transport:     transport,
		CheckRedirect: c.checkRedirect,
	}
	return c
}

// checkRedirect drops every credential header whenever a redirect crosses
// a host boundary. net/http copies the original request's headers onto the
// redirected request before this hook runs and strips only Authorization
// and the cookie headers itself; a host rule's own header - PRIVATE-TOKEN,
// JOB-TOKEN - would otherwise be replayed against host B, which is what
// the instance's redirects to object storage would do with the platform
// token (review S3, 2026-09-13).
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= defaultMaxRedirects {
		return errors.New("httpx: stopped after too many redirects")
	}
	// Two reasons to take the credential off a redirect, and only the
	// first used to be checked.
	//
	// A different host: net/http drops Authorization itself but knows
	// nothing of a host rule's own header - PRIVATE-TOKEN, JOB-TOKEN -
	// which the instance's redirects to object storage would replay.
	//
	// The same host at a path the rule does not admit: pathAllowed ran
	// once, on the first URL, and net/http copies the initial request's
	// headers onto every hop. One 3xx from an allowed path was enough to
	// carry the token anywhere on the instance - and an instance that
	// cleans dot segments answers exactly that redirect for the traversal
	// the check above now refuses. So the rule is re-evaluated per hop.
	rule, hasRule := c.ruleFor(req.URL.Host)
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) || !hasRule || !c.pathAllowed(rule, req.URL) {
		req.Header.Del("Authorization")
		for _, r := range c.hostRules {
			if r.HeaderName != "" {
				req.Header.Del(r.HeaderName)
			}
		}
	}
	return nil
}

// ruleFor looks up the HostRule for host, trying an exact host[:port] match
// first and falling back to the bare hostname.
func (c *Client) ruleFor(host string) (HostRule, bool) {
	if r, ok := c.hostRules[strings.ToLower(host)]; ok {
		return r, true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		if r, ok := c.hostRules[strings.ToLower(h)]; ok {
			return r, true
		}
	}
	return HostRule{}, false
}

// semFor returns the concurrency gate for host, creating it on first use.
// The limit is fixed at creation time (from the matching HostRule, if any)
// since a semaphore's capacity cannot change after the fact.
func (c *Client) semFor(host string, max int) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.sems[host]; ok {
		return s
	}
	if max <= 0 {
		max = defaultMaxConcurrent
	}
	s := make(chan struct{}, max)
	c.sems[host] = s
	return s
}

// applyHostRule attaches credentials from rule to req. Token wins over
// basic auth when both are set.
func applyHostRule(req *http.Request, rule HostRule) {
	switch {
	case rule.Token != "" && rule.HeaderName != "":
		req.Header.Set(rule.HeaderName, rule.Token)
	case rule.Token != "":
		req.Header.Set("Authorization", "Bearer "+rule.Token)
	case rule.Username != "" || rule.Password != "":
		req.SetBasicAuth(rule.Username, rule.Password)
	}
}

// pathAllowed tells whether the rule's credential may go to u.
//
// Three things decide it, and the first two are the ones that were
// missing. The pattern is matched against the path as it will go ON THE
// WIRE (EscapedPath), not against the decoded form: `%2e%2e%2f` decodes to
// `../` and would otherwise be matched as a literal segment while the
// server receives traversal. And a path carrying dot segments is refused
// outright rather than cleaned, because whether `..` is resolved before or
// after the allowlist is the instance's decision, not ours - a path that
// needs cleaning to be judged is a path we decline to judge.
//
// Without this, `/api/v4/projects/1/releases/../../groups/5/variables`
// matched the estate's pattern (the `.+` spans `/`), and the platform
// token - api-scoped, so able to read CI variables - went to a URL a
// scanned repository had named in registryUrls or customDatasources.
func (c *Client) pathAllowed(rule HostRule, u *url.URL) bool {
	re, ok := c.paths[strings.ToLower(rule.MatchHost)]
	if !ok {
		return true
	}
	p := u.EscapedPath()
	if hasDotSegment(p) {
		return false
	}
	return re.MatchString(p)
}

// hasDotSegment reports whether the path contains a "." or ".." segment,
// in any encoding a server might decode before routing.
func hasDotSegment(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		switch strings.ToLower(seg) {
		case ".", "..", "%2e", "%2e%2e", ".%2e", "%2e.":
			return true
		}
	}
	return false
}

// parseRetryAfter interprets a Retry-After header value, which is either a
// number of seconds or an HTTP-date. now resolves the HTTP-date form
// relative to the injected clock rather than the wall clock.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// backoffDuration is the fallback wait between retries when the server did
// not send a usable Retry-After: a short, linearly growing delay.
func backoffDuration(attempt int) time.Duration {
	return time.Duration(attempt+1) * 200 * time.Millisecond
}

// Get fetches url, applying the matching host rule's credentials, retrying
// retryable failures with backoff, and honouring conditional headers from
// opt.
func (c *Client) Get(ctx context.Context, rawURL string, opt ReqOptions) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("httpx: invalid url: %w", err)
	}

	rule, hasRule := c.ruleFor(u.Host)
	maxConcurrent := 0
	if hasRule {
		maxConcurrent = rule.MaxConcurrent
	}
	sem := c.semFor(u.Host, maxConcurrent)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-sem }()

	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, retryable, wait, err := c.attempt(ctx, u.String(), opt, rule, hasRule)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retryable || attempt >= c.maxRetries {
			return nil, lastErr
		}
		if wait <= 0 {
			wait = backoffDuration(attempt)
		}
		c.sleep(wait)
	}
}

// attempt performs a single HTTP round trip. It reports whether the failure
// (if any) is worth retrying and, for 429/5xx responses, how long to wait
// first. Errors returned here must never include header or credential
// values — only status codes and generic descriptions — since they surface
// directly to callers.
func (c *Client) attempt(ctx context.Context, rawURL string, opt ReqOptions, rule HostRule, hasRule bool) (*Response, bool, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, 0, fmt.Errorf("httpx: build request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if opt.Accept != "" {
		req.Header.Set("Accept", opt.Accept)
	}
	if opt.ETag != "" {
		req.Header.Set("If-None-Match", opt.ETag)
	}
	if opt.LastModified != "" {
		req.Header.Set("If-Modified-Since", opt.LastModified)
	}
	if hasRule && c.pathAllowed(rule, req.URL) {
		applyHostRule(req, rule)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		// Network-level failures (dial errors, TLS errors, timeouts) are
		// treated as transient. The stdlib error text describes addresses
		// and protocols, never our request headers, so it is safe to wrap.
		return nil, true, 0, fmt.Errorf("httpx: request failed: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		drain(resp.Body)
		return &Response{
			StatusCode:   resp.StatusCode,
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			NotModified:  true,
			Header:       resp.Header.Clone(),
		}, false, 0, nil

	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
		if err != nil {
			return nil, false, 0, fmt.Errorf("httpx: read response body: %w", err)
		}
		if int64(len(body)) > c.maxBody {
			return nil, false, 0, fmt.Errorf("httpx: response from %s exceeds %d bytes", rawURL, c.maxBody)
		}
		return &Response{
			StatusCode:   resp.StatusCode,
			Body:         body,
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			Header:       resp.Header.Clone(),
		}, false, 0, nil

	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		drain(resp.Body)
		wait, _ := parseRetryAfter(resp.Header.Get("Retry-After"), c.now())
		return nil, true, wait, &StatusError{StatusCode: resp.StatusCode, URL: rawURL, Header: resp.Header.Clone()}

	default:
		drain(resp.Body)
		return nil, false, 0, &StatusError{StatusCode: resp.StatusCode, URL: rawURL, Header: resp.Header.Clone()}
	}
}

// drain discards a response body so the underlying connection can be
// reused by the transport's connection pool.
func drain(r io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 64<<10))
}

// Stream fetches rawURL and hands its body to read while it arrives, for
// the one thing a datasource downloads that no cache and no MaxBody should
// hold: a provider's zip, hashed for a lock file and discarded. The same
// credentials and user agent as Get, no conditional headers, one attempt -
// a body that fails mid-way is the caller's to retry, if it wants to. Only a
// 2xx reaches read; anything else is a StatusError.
func (c *Client) Stream(ctx context.Context, rawURL string, read func(io.Reader) error) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("httpx: invalid url: %w", err)
	}
	rule, hasRule := c.ruleFor(u.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("httpx: build request: %w", err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if hasRule && c.pathAllowed(rule, req.URL) {
		applyHostRule(req, rule)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("httpx: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		drain(resp.Body)
		return &StatusError{StatusCode: resp.StatusCode, URL: rawURL}
	}
	return read(resp.Body)
}
