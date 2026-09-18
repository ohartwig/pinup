// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package harness holds the two pieces of test plumbing that have to exist
// before any assertion logic is written, because neither can be retrofitted.
//
// The first is T: every harness layer asserts against this interface rather
// than against *testing.T directly. That is what lets the mutation suite run a
// layer's real assertions over a deliberately broken input and observe the
// failures instead of dying on them. A layer written against *testing.T cannot
// be proven to fail without rewriting it.
//
// The second is RefusingTransport, which turns "this test accidentally reached
// the network" from unlikely into impossible, and gives a dropped feature a
// positive proof: a host registered as forbidden that receives zero requests
// is evidence the feature is unreachable, not merely unimplemented.
//
// This package lives under fake/ so the convention test that keeps test
// doubles out of the binary covers it too.
package harness

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// T is the slice of *testing.T that harness assertions use.
type T interface {
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
	Helper()
}

// Recorder is a T that collects failures instead of reporting them, so a test
// can assert that an assertion failed.
type Recorder struct {
	mu     sync.Mutex
	Errors []string
	Logs   []string
}

func (r *Recorder) Helper() {}

func (r *Recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *Recorder) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Logs = append(r.Logs, fmt.Sprintf(format, args...))
}

// Failed reports whether anything was recorded.
func (r *Recorder) Failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.Errors) > 0
}

// Mentions reports whether any recorded failure contains s. The mutation suite
// uses it to check that the *right* assertion fired, not merely some assertion.
func (r *Recorder) Mentions(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.Errors {
		if strings.Contains(e, s) {
			return true
		}
	}
	return false
}

func (r *Recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.Errors, "\n")
}

// RefusingTransport routes requests to registered per-host handlers and fails
// the test on anything else.
//
// Registration is by host[:port]. A host registered with Forbid must receive
// no requests at all; Counts reports what each host actually received, because
// "zero requests" is only evidence when somebody asserts it.
type RefusingTransport struct {
	t T

	mu       sync.Mutex
	handlers map[string]http.Handler
	forbid   map[string]bool
	counts   map[string]int
	refused  []string
}

// NewRefusingTransport returns a transport that refuses every host until one
// is registered.
func NewRefusingTransport(t T) *RefusingTransport {
	return &RefusingTransport{
		t:        t,
		handlers: map[string]http.Handler{},
		forbid:   map[string]bool{},
		counts:   map[string]int{},
	}
}

// Handle registers a handler for a host. The handler speaks the real protocol;
// the fixture files it serves are data.
func (rt *RefusingTransport) Handle(host string, h http.Handler) *RefusingTransport {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.handlers[host] = h
	return rt
}

// Forbid marks a host that must receive no requests. Reaching it is a failure;
// so is asserting nothing about it, which is what MustNotHaveBeenCalled is for.
func (rt *RefusingTransport) Forbid(host string) *RefusingTransport {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.forbid[host] = true
	return rt
}

// Client returns an http.Client bound to this transport.
func (rt *RefusingTransport) Client() *http.Client {
	return &http.Client{Transport: rt}
}

// RoundTrip implements http.RoundTripper.
func (rt *RefusingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host

	rt.mu.Lock()
	rt.counts[host]++
	h, known := rt.handlers[host]
	forbidden := rt.forbid[host]
	if !known {
		rt.refused = append(rt.refused, req.URL.String())
	}
	rt.mu.Unlock()

	switch {
	case forbidden:
		rt.t.Errorf("forbidden host was contacted: %s", req.URL)
		return nil, fmt.Errorf("harness: host %q is forbidden in this test", host)
	case !known:
		// The full URL, not just the host: the point of the message is that
		// the reader can tell which call escaped without adding a print.
		rt.t.Errorf("test reached an unregistered host: %s (register it with Handle, or Forbid it deliberately)", req.URL)
		return nil, fmt.Errorf("harness: no handler for %q", host)
	}

	rec := newRecorder()
	h.ServeHTTP(rec, req)
	return rec.result(req), nil
}

// Count returns how many requests a host received.
func (rt *RefusingTransport) Count(host string) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.counts[host]
}

// MustNotHaveBeenCalled asserts a forbidden host received nothing. Calling it
// is the difference between a feature that is unreachable and one nobody
// looked for.
func (rt *RefusingTransport) MustNotHaveBeenCalled(host string) {
	rt.t.Helper()
	if n := rt.Count(host); n != 0 {
		rt.t.Errorf("host %q received %d requests; it must receive none", host, n)
		return
	}
	rt.t.Logf("host %q received 0 requests, as required", host)
}

// Refused returns the URLs that had no handler, for diagnostics.
func (rt *RefusingTransport) Refused() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := append([]string(nil), rt.refused...)
	sort.Strings(out)
	return out
}

// HostOf is a convenience for registering by URL rather than by host.
func HostOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in %q", rawURL)
	}
	return u.Host, nil
}
