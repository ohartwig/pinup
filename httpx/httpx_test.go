// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostRuleAppliesCredentials(t *testing.T) {
	const token = "line-up-token-abc123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name       string
		withRule   bool
		wantStatus int
		wantErr    bool
	}{
		{name: "no rule configured", withRule: false, wantErr: true},
		{name: "matching rule sends bearer token", withRule: true, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rules []HostRule
			if tc.withRule {
				rules = []HostRule{{MatchHost: hostOf(srv.URL), Token: token}}
			}
			client := New(Options{
				HostRules: rules,
				Now:       time.Now,
				Sleep:     func(time.Duration) {},
			})

			resp, err := client.Get(t.Context(), srv.URL, ReqOptions{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Get() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && resp.StatusCode != tc.wantStatus {
				t.Errorf("StatusCode = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

// TestRetryBehaviour covers the three retry-policy requirements together:
// 429 with Retry-After is honoured, 5xx is retried up to MaxRetries, and
// non-429 4xx is never retried. The injected Sleep never actually waits, so
// this test asserts wall-clock time stays well under what real backoffs
// would cost.
func TestRetryBehaviour(t *testing.T) {
	for _, tc := range []struct {
		name         string
		maxRetries   int
		statuses     []int // status per call; the last entry repeats if exhausted
		retryAfter   string
		wantAttempts int32
		wantErr      bool
		checkSleeps  func(t *testing.T, sleeps []time.Duration)
	}{
		{
			name:         "429 with Retry-After then success",
			maxRetries:   1,
			statuses:     []int{http.StatusTooManyRequests, http.StatusOK},
			retryAfter:   "1",
			wantAttempts: 2,
			checkSleeps: func(t *testing.T, sleeps []time.Duration) {
				t.Helper()
				if len(sleeps) != 1 || sleeps[0] != time.Second {
					t.Errorf("sleeps = %v, want [1s]", sleeps)
				}
			},
		},
		{
			name:         "three 500s then success",
			maxRetries:   3,
			statuses:     []int{http.StatusInternalServerError, http.StatusInternalServerError, http.StatusInternalServerError, http.StatusOK},
			wantAttempts: 4,
		},
		{
			name:         "404 is not retried",
			maxRetries:   3,
			statuses:     []int{http.StatusNotFound},
			wantAttempts: 1,
			wantErr:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(attempts.Add(1)) - 1
				if n >= len(tc.statuses) {
					n = len(tc.statuses) - 1
				}
				status := tc.statuses[n]
				if status == http.StatusTooManyRequests && tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()

			var mu sync.Mutex
			var sleeps []time.Duration
			client := New(Options{
				MaxRetries: tc.maxRetries,
				Now:        time.Now,
				Sleep: func(d time.Duration) {
					mu.Lock()
					sleeps = append(sleeps, d)
					mu.Unlock()
				},
			})

			start := time.Now()
			resp, err := client.Get(t.Context(), srv.URL, ReqOptions{})
			elapsed := time.Since(start)

			// A real 1s Retry-After plus growing 5xx backoffs would take
			// seconds; an injected Sleep that never actually sleeps keeps
			// this well under that.
			if elapsed > 500*time.Millisecond {
				t.Errorf("elapsed = %v, want well under 500ms (Sleep must not really sleep)", elapsed)
			}

			if (err != nil) != tc.wantErr {
				t.Fatalf("Get() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if wantSleeps := int(tc.wantAttempts) - 1; len(sleeps) != wantSleeps {
				t.Errorf("len(sleeps) = %d, want %d", len(sleeps), wantSleeps)
			}
			if !tc.wantErr && resp.StatusCode != http.StatusOK {
				t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
			}
			if tc.checkSleeps != nil {
				tc.checkSleeps(t, sleeps)
			}
		})
	}
}

// TestConditionalRequestETag exercises the If-None-Match / 304 round trip:
// the first fetch returns a body and an ETag, the second fetch echoes that
// ETag and gets back an empty, NotModified response.
func TestConditionalRequestETag(t *testing.T) {
	const etag = `"v1"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()

	client := New(Options{Now: time.Now, Sleep: func(time.Duration) {}})

	first, err := client.Get(t.Context(), srv.URL, ReqOptions{})
	if err != nil {
		t.Fatalf("first Get() error = %v", err)
	}
	if first.NotModified {
		t.Fatal("first response reported NotModified")
	}
	if string(first.Body) != "hello" || first.ETag != etag {
		t.Fatalf("first response = %q/%q, want hello/%s", first.Body, first.ETag, etag)
	}

	second, err := client.Get(t.Context(), srv.URL, ReqOptions{ETag: first.ETag})
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if !second.NotModified {
		t.Error("second response did not report NotModified")
	}
	if len(second.Body) != 0 {
		t.Errorf("second response body = %q, want empty", second.Body)
	}
}

// TestRedirectDropsAuthorizationCrossHost proves the security-critical
// behaviour: a token configured for host A must not follow a redirect to
// host B.
func TestRedirectDropsAuthorizationCrossHost(t *testing.T) {
	const token = "hostA-only-token"

	var sawAuthOnB atomic.Bool
	hostB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuthOnB.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer hostB.Close()

	var sawAuthOnA atomic.Bool
	hostA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+token {
			sawAuthOnA.Store(true)
		}
		http.Redirect(w, r, hostB.URL, http.StatusFound)
	}))
	defer hostA.Close()

	client := New(Options{
		HostRules: []HostRule{{MatchHost: hostOf(hostA.URL), Token: token}},
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
	})

	resp, err := client.Get(t.Context(), hostA.URL, ReqOptions{})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if !sawAuthOnA.Load() {
		t.Error("host A never received the Authorization header (test setup problem)")
	}
	if sawAuthOnB.Load() {
		t.Error("host B received an Authorization header carried over from host A")
	}
}

// The same for a rule with a header of its own: PRIVATE-TOKEN is not one
// of the headers net/http strips, and the instance redirects artifacts and
// packages to object storage. A cross-host redirect must lose it.
func TestRedirectDropsRuleHeaderCrossHost(t *testing.T) {
	const token = "glpat-hostA-only"
	var sawOnB atomic.Bool
	hostB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			sawOnB.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer hostB.Close()
	var sawOnA atomic.Bool
	hostA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == token {
			sawOnA.Store(true)
		}
		http.Redirect(w, r, hostB.URL, http.StatusFound)
	}))
	defer hostA.Close()
	client := New(Options{
		HostRules: []HostRule{{MatchHost: hostOf(hostA.URL), Token: token, HeaderName: "PRIVATE-TOKEN"}},
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
	})
	if _, err := client.Get(t.Context(), hostA.URL, ReqOptions{}); err != nil {
		t.Fatal(err)
	}
	if !sawOnA.Load() {
		t.Error("host A never received PRIVATE-TOKEN (test setup problem)")
	}
	if sawOnB.Load() {
		t.Error("host B received the PRIVATE-TOKEN carried over from host A")
	}
}

// TestErrorNeverLeaksToken is the hard requirement from CLAUDE.md: whatever
// a failure's error text says, it must never contain the configured token.
func TestErrorNeverLeaksToken(t *testing.T) {
	const token = "do-not-leak-this-secret-9f8e7d"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(Options{
		HostRules: []HostRule{{MatchHost: hostOf(srv.URL), Token: token}},
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
	})

	_, err := client.Get(t.Context(), srv.URL, ReqOptions{})
	if err == nil {
		t.Fatal("Get() error = nil, want an error for 403")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error text leaks the configured token: %q", err.Error())
	}
}

// TestPerHostConcurrencyLimit asserts the semaphore actually bounds
// concurrency: with MaxConcurrent=2 and six simultaneous callers, the
// handler must never observe more than two requests in flight at once.
func TestPerHostConcurrencyLimit(t *testing.T) {
	const limit = 2
	const callers = 6

	var mu sync.Mutex
	current, peak := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current++
		if current > peak {
			peak = current
		}
		mu.Unlock()

		time.Sleep(30 * time.Millisecond) // hold the slot long enough for overlap to show up

		mu.Lock()
		current--
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New(Options{
		HostRules: []HostRule{{MatchHost: hostOf(srv.URL), MaxConcurrent: limit}},
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
	})

	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			if _, err := client.Get(t.Context(), srv.URL, ReqOptions{}); err != nil {
				t.Errorf("Get() error = %v", err)
			}
		})
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > limit {
		t.Errorf("peak concurrent requests = %d, want <= %d", peak, limit)
	}
}

// hostOf extracts the host[:port] portion of a URL, matching how
// HostRule.MatchHost is expected to be configured.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host
}

// A body over the limit is an error, not a partition's memory; an attempt
// that stalls is cut at the timeout and retried like any transient
// failure.
func TestBodyLimitAndAttemptTimeout(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 2048))
	}))
	defer big.Close()
	client := New(Options{MaxBody: 1024, Now: time.Now, Sleep: func(time.Duration) {}})
	if _, err := client.Get(t.Context(), big.URL, ReqOptions{}); err == nil || !strings.Contains(err.Error(), "exceeds 1024 bytes") {
		t.Errorf("oversized body: %v", err)
	}

	release := make(chan struct{})
	defer close(release)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	var waits int
	client = New(Options{Timeout: 50 * time.Millisecond, MaxRetries: 1, Now: time.Now, Sleep: func(time.Duration) { waits++ }})
	start := time.Now()
	_, err := client.Get(t.Context(), slow.URL, ReqOptions{})
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("stalled attempt: %v", err)
	}
	if waits != 1 {
		t.Errorf("a stalled attempt is transient and retried once: %d waits", waits)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("the timeout did not cut the attempt")
	}
}

// A rule with a path pattern sends its credential only on matching paths:
// the platform token reaches the API endpoints pinup calls and not a path
// a repository configuration names.
func TestPathPatternBindsTheCredential(t *testing.T) {
	var got sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.URL.Path, r.Header.Get("PRIVATE-TOKEN"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := New(Options{
		HostRules: []HostRule{{MatchHost: hostOf(srv.URL), Token: "glpat-x", HeaderName: "PRIVATE-TOKEN",
			PathPattern: `^/api/v4/projects/.+/(releases|packages)(/|$)`}},
		Now: time.Now, Sleep: func(time.Duration) {},
	})
	for _, path := range []string{"/api/v4/projects/devops%2Fx/releases", "/api/v4/projects/1/packages/npm/x", "/api/v4/groups/1/variables", "/api/v4/projects/1/variables"} {
		if _, err := client.Get(t.Context(), srv.URL+path, ReqOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(path, want string) {
		v, _ := got.Load(path)
		if v != want {
			t.Errorf("%s: token %q, want %q", path, v, want)
		}
	}
	check("/api/v4/projects/devops/x/releases", "glpat-x")
	check("/api/v4/projects/1/packages/npm/x", "glpat-x")
	check("/api/v4/groups/1/variables", "")
	check("/api/v4/projects/1/variables", "")
}
