// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// A configuration names the URL; the guard decides after resolution. The
// metadata endpoints, loopback, RFC 1918 and ULA are refused, a public
// address passes, and an allowed host passes whatever it resolves to.
func TestGuardRefusesPrivateAndSpecialAddresses(t *testing.T) {
	g := Guard{Allow: AllowHosts("registry.internal.example", "GIT.Example.org:443")}
	for _, tc := range []struct {
		host, addr string
		refused    bool
	}{
		{"evil.example", "127.0.0.1:80", true},
		{"evil.example", "[::1]:80", true},
		{"evil.example", "169.254.169.254:80", true},
		{"evil.example", "[fd00:ec2::254]:80", true},
		{"evil.example", "10.0.0.5:443", true},
		{"evil.example", "192.168.1.1:443", true},
		{"evil.example", "172.16.3.4:443", true},
		{"evil.example", "[fd12::1]:443", true},
		{"evil.example", "[::ffff:10.0.0.5]:443", true},
		{"evil.example", "0.0.0.0:443", true},
		{"evil.example", "[fe80::1]:443", true},
		{"evil.example", "224.0.0.1:443", true},
		{"evil.example", "93.184.216.34:443", false},
		{"evil.example", "[2606:2800:220:1:248:1893:25c8:1946]:443", false},
		{"registry.internal.example", "10.0.0.5:443", false},
		{"git.example.org", "[fd12::1]:443", false},
		{"", "127.0.0.1:80", true},
	} {
		ctx := context.WithValue(context.Background(), dialHostKey{}, tc.host)
		err := g.Control(ctx, "tcp", tc.addr, nil)
		if (err != nil) != tc.refused {
			t.Errorf("%s -> %s: refused=%v, want %v (%v)", tc.host, tc.addr, err != nil, tc.refused, err)
		}
		if err != nil && !strings.Contains(err.Error(), "PINUP_PRIVATE_HOSTS") {
			t.Errorf("%s -> %s: the refusal does not say how to allow the host: %v", tc.host, tc.addr, err)
		}
	}
	if AllowHosts("*")("anything") != true {
		t.Error("* admits every host")
	}
	for _, a := range []string{"100.64.0.1", "8.8.8.8", "2001:db8::1"} {
		if Blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s is blocked; only loopback, private, link-local and multicast are", a)
		}
	}
}

// Through a transport: a server on loopback is unreachable under a name
// the guard does not allow, and reachable under one it does.
func TestGuardPrivateOnATransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()

	refused := http.DefaultTransport.(*http.Transport).Clone()
	GuardPrivate(refused, AllowHosts())
	if _, err := (&http.Client{Transport: refused}).Get(srv.URL); err == nil || !strings.Contains(err.Error(), "private or special address") {
		t.Errorf("loopback reached through a guarded transport: %v", err)
	}

	allowed := http.DefaultTransport.(*http.Transport).Clone()
	GuardPrivate(allowed, AllowHosts("127.0.0.1", "[::1]"))
	resp, err := (&http.Client{Transport: allowed}).Get(srv.URL)
	if err != nil {
		t.Fatalf("an allowed host is refused: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Errorf("status %d", resp.StatusCode)
	}
}
