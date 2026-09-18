// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package versioning

import "testing"

func TestGoPseudoCommit(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"v0.0.0-20260821190718-4776eadac327", "4776eadac327"},
		{"v0.0.0-20260908204917-8b95e45f8d3e", "8b95e45f8d3e"},
		{"v1.2.3-pre.0.20260908204917-8b95e45f8d3e", "8b95e45f8d3e"},
		{"v1.2.4-0.20260908204917-8b95e45f8d3e", "8b95e45f8d3e"},
		{"v1.2.3", ""},
		{"v0.0.0-20260908204917-8B95E45F8D3E", ""},
		{"v0.0.0-2026090820491-8b95e45f8d3e", ""},
		{"1.0.0-20260908204917-8b95e45f8d3e", ""},
	} {
		if got := GoPseudoCommit(c.in); got != c.want {
			t.Errorf("GoPseudoCommit(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
