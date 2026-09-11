// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package semverx

import "testing"

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in            string
		ok            bool
		maj, min, pat int
		pre           int
		vPrefix       bool
	}{
		{"1.2.3", true, 1, 2, 3, 0, false},
		{"v1.2.3", true, 1, 2, 3, 0, true},
		{"0.0.0", true, 0, 0, 0, 0, false},
		{"1.2.3-alpha.1", true, 1, 2, 3, 2, false},
		{"1.2.3+build", true, 1, 2, 3, 0, false},
		{"1.2.3-rc.1+build", true, 1, 2, 3, 2, false},
		{"1.2", false, 0, 0, 0, 0, false},
		{"1", false, 0, 0, 0, 0, false},
		{"1.2.3.4", false, 0, 0, 0, 0, false},
		{"01.2.3", false, 0, 0, 0, 0, false},
		{"1.2.3-", false, 0, 0, 0, 0, false},
		{"1.2.3+", false, 0, 0, 0, 0, false},
		{"1.2.3-a..b", false, 0, 0, 0, 0, false},
		{"", false, 0, 0, 0, 0, false},
		{"latest", false, 0, 0, 0, 0, false},
	} {
		v, ok := Parse(c.in)
		if ok != c.ok {
			t.Errorf("Parse(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if v.Major != c.maj || v.Minor != c.min || v.Patch != c.pat {
			t.Errorf("Parse(%q) = %d.%d.%d, want %d.%d.%d", c.in, v.Major, v.Minor, v.Patch, c.maj, c.min, c.pat)
		}
		if len(v.Pre) != c.pre {
			t.Errorf("Parse(%q) has %d prerelease identifiers, want %d", c.in, len(v.Pre), c.pre)
		}
		if v.HadVPrefix != c.vPrefix {
			t.Errorf("Parse(%q) v-prefix = %v, want %v", c.in, v.HadVPrefix, c.vPrefix)
		}
	}
}

func TestStringRoundTrips(t *testing.T) {
	for _, s := range []string{"1.2.3", "1.2.3-alpha.1", "1.2.3+build", "1.2.3-rc.1+b"} {
		v, ok := Parse(s)
		if !ok {
			t.Fatalf("Parse(%q) failed", s)
		}
		if got := v.String(); got != s {
			t.Errorf("String() = %q, want %q", got, s)
		}
	}
}

func TestOrdering(t *testing.T) {
	// Straight from the specification's own example ordering.
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1",
		"1.1.0", "2.0.0",
	}
	for i := 0; i+1 < len(ordered); i++ {
		a, _ := Parse(ordered[i])
		b, _ := Parse(ordered[i+1])
		if CompareVersions(a, b) >= 0 {
			t.Errorf("%q should sort before %q", ordered[i], ordered[i+1])
		}
	}
	// Build metadata does not participate.
	x, _ := Parse("1.2.3+a")
	y, _ := Parse("1.2.3+b")
	if CompareVersions(x, y) != 0 {
		t.Error("build metadata affected the ordering; the specification says it must not")
	}
}

func TestNumeric(t *testing.T) {
	for _, c := range []struct {
		in string
		n  int
		ok bool
	}{
		{"0", 0, true}, {"7", 7, true}, {"10", 10, true},
		{"01", 0, false}, {"", 0, false}, {"a", 0, false}, {"-1", 0, false},
	} {
		n, ok := Numeric(c.in)
		if ok != c.ok || (ok && n != c.n) {
			t.Errorf("Numeric(%q) = (%d,%v), want (%d,%v)", c.in, n, ok, c.n, c.ok)
		}
	}
}
