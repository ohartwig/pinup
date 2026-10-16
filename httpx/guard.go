// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// Guard decides whether a dial may reach a private, loopback, link-local
// or otherwise special destination. A repository configuration names URLs
// pinup fetches - registryUrls, a customDatasource, an advisory feed - and
// a transformTemplate reflects the body into the plan and the dashboard.
// Without a guard that is a request from the runner's network to whatever
// the configuration names: a cloud metadata endpoint, a sidecar, a
// neighbour on the docker bridge (AUD-007). The guard refuses those after
// DNS resolution, where a public name that resolves to a private address
// is caught as well.
//
// Allow names the hosts that may resolve to such an address anyway: the
// installation's own instance and registry when they sit on a private
// range, say. It is asked with the host the request named, as written.
type Guard struct {
	Allow func(host string) bool
}

// dialHostKey carries the host a dial was asked for into the dialer's
// control hook, which sees the resolved address alone.
type dialHostKey struct{}

// Control is a net.Dialer.ControlContext: it refuses a resolved address
// the guard blocks unless the host the dial was asked for is allowed. The
// raw connection is not touched; the decision is made on the address.
func (g Guard) Control(ctx context.Context, network, address string, _ syscall.RawConn) error {
	host, _ := ctx.Value(dialHostKey{}).(string)
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("dial %s: %w", address, err)
	}
	if !Blocked(ap.Addr()) {
		return nil
	}
	if g.Allow != nil && host != "" && g.Allow(host) {
		return nil
	}
	return fmt.Errorf("dial %s: %s resolves to %s, a private or special address pinup does not reach (PINUP_PRIVATE_HOSTS names the hosts that may)", address, hostOrAddress(host, ap.Addr()), ap.Addr())
}

func hostOrAddress(host string, a netip.Addr) string {
	if host == "" {
		return a.String()
	}
	return host
}

// Blocked reports whether a is an address no configuration-named request
// may reach: loopback, unspecified, private (RFC 1918, ULA), link-local
// (which holds the cloud metadata endpoints 169.254.169.254 and the EC2
// IPv6 one, fd00:ec2::254, under ULA), multicast, and the IPv4-mapped
// forms of any of them.
func Blocked(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast()
}

// GuardPrivate makes t refuse dials to private and special addresses,
// unless allow admits the host asked for. nil means http.DefaultTransport.
// The dial keeps preferring IPv6 (DialPreferringIPv6).
func GuardPrivate(t *http.Transport, allow func(host string) bool) {
	if t == nil {
		t, _ = http.DefaultTransport.(*http.Transport)
		if t == nil {
			return
		}
	}
	base := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, ControlContext: Guard{Allow: allow}.Control}
	t.DialContext = DialPreferringIPv6(base)
}

// AllowHosts is an Allow function for a fixed set of host names, compared
// case-insensitively and without a port; "*" admits every host.
func AllowHosts(hosts ...string) func(string) bool {
	set := map[string]bool{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if hp, _, err := net.SplitHostPort(h); err == nil {
			h = hp
		}
		set[strings.Trim(h, "[]")] = true
	}
	return func(host string) bool {
		if set["*"] {
			return true
		}
		host = strings.ToLower(host)
		if hp, _, err := net.SplitHostPort(host); err == nil {
			host = hp
		}
		return set[strings.Trim(host, "[]")]
	}
}
