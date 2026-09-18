// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"net"
	"net/http"
	"time"
)

// ipv6First is how long the IPv6 attempt may take before IPv4 is tried
// instead. A host that answers over IPv6 does so in milliseconds; two
// seconds is the budget for one that has an AAAA record and no working
// path to it.
const ipv6First = 2 * time.Second

// DialPreferringIPv6 returns a dial function that tries IPv6 before IPv4.
//
// Go orders addresses by RFC 6724 and reads no gai.conf, and RFC 6724
// ranks a private IPv4 above a unique-local IPv6 for a global destination:
// in a job container on a dual-stack runner (a public IPv4 on the host, a
// ULA on the docker bridge) every Go client - pinup, cosign, containerd -
// leaves over IPv4, from an address the estate's own firewall does not
// know as its own, while git and curl next to it read gai.conf and leave
// over IPv6 from the VPC range that is whitelisted. Measured 2026-09-15:
// the estate's CrowdSec banned the runner workers' IPv4 addresses for
// probing, on pinup's own 404s.
//
// So: IPv6 first, with a bounded attempt, then IPv4. A host without an
// AAAA record fails the first attempt at once and costs nothing; an
// IPv6-only host never reaches the second.
func DialPreferringIPv6(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if base == nil {
		base = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" {
			return base.DialContext(ctx, network, addr)
		}
		v6ctx, cancel := context.WithTimeout(ctx, ipv6First)
		conn, err := base.DialContext(v6ctx, "tcp6", addr)
		cancel()
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return base.DialContext(ctx, "tcp4", addr)
	}
}

// PreferIPv6 makes t dial IPv6 first; nil means http.DefaultTransport.
func PreferIPv6(t *http.Transport) {
	if t == nil {
		t, _ = http.DefaultTransport.(*http.Transport)
		if t == nil {
			return
		}
	}
	t.DialContext = DialPreferringIPv6(nil)
}

// WithUserAgent returns a RoundTripper that names pinup on every request
// that carries no User-Agent of its own: the registry, platform and
// advisory clients build on http.DefaultTransport and said
// "Go-http-client/2.0" to the estate's access log (measured 2026-09-15),
// which is no name to be whitelisted or blamed by.
func WithUserAgent(rt http.RoundTripper, ua string) http.RoundTripper {
	return userAgentTransport{rt: rt, ua: ua}
}

type userAgentTransport struct {
	rt http.RoundTripper
	ua string
}

func (t userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.ua)
	}
	return t.rt.RoundTrip(req)
}
