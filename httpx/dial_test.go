// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"net"
	"testing"
	"time"
)

// The dial prefers IPv6 and falls back to IPv4: a listener on ::1 is
// reached over IPv6 when the host has it, a listener on 127.0.0.1 alone is
// reached over IPv4 after the IPv6 attempt failed at once.
func TestDialPrefersIPv6ThenFallsBack(t *testing.T) {
	dial := DialPreferringIPv6(&net.Dialer{Timeout: 5 * time.Second})

	l4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l4.Close()
	go accept(l4)
	start := time.Now()
	c, err := dial(context.Background(), "tcp", "localhost:"+port(l4))
	if err != nil {
		t.Fatalf("IPv4 fallback: %v", err)
	}
	c.Close()
	if d := time.Since(start); d > ipv6First {
		t.Errorf("the IPv4 fallback waited %v; an IPv6 attempt with nothing listening must fail at once", d)
	}

	l6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback here")
	}
	defer l6.Close()
	go accept(l6)
	c, err = dial(context.Background(), "tcp", "localhost:"+port(l6))
	if err != nil {
		t.Fatalf("IPv6: %v", err)
	}
	defer c.Close()
	if ip := c.RemoteAddr().(*net.TCPAddr).IP; ip.To4() != nil {
		t.Errorf("connected over %s; IPv6 was available", ip)
	}
}

func accept(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

func port(l net.Listener) string {
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}
