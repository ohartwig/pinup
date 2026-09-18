// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package pep440

import (
	"testing"

	"github.com/ohartwig/pinup/versioning"
)

// TestTheSpecificationsOrderingHolds walks the version-specifiers
// specification's own worked ordering example ("1.0.dev456 < 1.0a1 <
// 1.0a2.dev456 < ... < 1.1.dev1") plus the normalisation examples it lists.
func TestTheSpecificationsOrderingHolds(t *testing.T) {
	s := New()
	ordered := []string{
		"1.0.dev456", "1.0a1", "1.0a2.dev456", "1.0a12.dev456", "1.0a12", "1.0b1.dev456",
		"1.0b2", "1.0b2.post345.dev456", "1.0b2.post345", "1.0rc1.dev456", "1.0rc1", "1.0",
		"1.0+abc.5", "1.0+abc.7", "1.0+5", "1.0.post456.dev34", "1.0.post456", "1.1.dev1",
	}
	for i := 1; i < len(ordered); i++ {
		a, b := ordered[i-1], ordered[i]
		if !s.IsVersion(a) || !s.IsVersion(b) {
			t.Fatalf("%q or %q did not parse", a, b)
		}
		if s.Compare(a, b) >= 0 {
			t.Errorf("Compare(%q, %q) = %d, want <0", a, b, s.Compare(a, b))
		}
	}
	for _, c := range []struct{ a, b string }{
		{"1.0", "1.0.0"}, {"1.0RC1", "1.0rc1"}, {"1.0.0-rc.1", "1.0rc1"}, {"1.0c1", "1.0rc1"},
		{"1.0-pre1", "1.0rc1"}, {"1.0alpha", "1.0a0"}, {"1.0-1", "1.0.post1"}, {"1.0.r1", "1.0.post1"},
		{"1.0-dev", "1.0.dev0"}, {"v1.0", "1.0"}, {"1!1.0", "1!1.0.0"}, {"2020.01", "2020.1"},
	} {
		if !s.Equal(c.a, c.b) {
			t.Errorf("Equal(%q, %q) = false, want true (normalisation)", c.a, c.b)
		}
	}
	if s.Compare("1!0.1", "99.9") <= 0 {
		t.Error("an epoch must outrank any release")
	}
	for _, bad := range []string{"", "1.", "a1", "1.0.x", "1.0-", "1.0+", "==1.0", "1.0.0.0.rc.x"} {
		if s.IsVersion(bad) {
			t.Errorf("IsVersion(%q) = true, want false", bad)
		}
	}
	// A bare pre-release word carries an implicit 0.
	if !s.Equal("1.0.0.0.rc", "1.0.0.0rc0") {
		t.Error("1.0.0.0.rc must read as 1.0.0.0rc0")
	}
}

func TestStabilityAndComponents(t *testing.T) {
	s := New()
	for _, c := range []struct {
		v      string
		stable bool
	}{
		{"1.43.93", true}, {"6.0.3", true}, {"2.47.0", true}, {"1.0.post1", true},
		{"1.0rc1", false}, {"1.0a1", false}, {"1.0.dev3", false}, {"1.0rc1.post1", false},
	} {
		if got := s.IsStable(c.v); got != c.stable {
			t.Errorf("IsStable(%q) = %v, want %v", c.v, got, c.stable)
		}
	}
	if maj, _ := s.Major("1.43.93"); maj != 1 {
		t.Errorf("Major = %d", maj)
	}
	if min, _ := s.Minor("1.43.93"); min != 43 {
		t.Errorf("Minor = %d", min)
	}
	if pat, _ := s.Patch("1.43.93"); pat != 93 {
		t.Errorf("Patch = %d", pat)
	}
	if _, ok := s.Patch("2020.1"); ok {
		t.Error("a two-part version has no patch")
	}
}

func TestSpecifierSetsSatisfy(t *testing.T) {
	s := New()
	for _, c := range []struct {
		v, rng string
		want   bool
	}{
		{"1.43.93", "1.43.93", true}, {"1.43.93", "1.43.94", false},
		{"1.4.5", "~=1.4", true}, {"2.0", "~=1.4", false}, {"1.4.6", "~=1.4.5", true}, {"1.5", "~=1.4.5", false},
		{"1.2.9", "==1.2.*", true}, {"1.3", "==1.2.*", false}, {"1.3", "!=1.2.*", true},
		{"1.5", ">=1.0, <2.0", true}, {"2.0", ">=1.0, <2.0", false}, {"2.0rc1", ">=1.0, <2.0", false},
		{"1.0.post1", ">1.0", false}, {"1.0.1", ">1.0", true},
		{"1.0", "===1.0", true}, {"1.0.0", "===1.0", false},
		{"1.0", "~=1", false}, {"1.0", ">=", false},
	} {
		if got := s.Satisfies(c.v, c.rng); got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", c.v, c.rng, got, c.want)
		}
	}
	for _, r := range []string{">=1.0, <2.0", "==1.2.*", "~=1.4", "1.0"} {
		if !s.IsValid(r) {
			t.Errorf("IsValid(%q) = false", r)
		}
	}
	for _, r := range []string{"~=1", ">=1.*", "banana", ""} {
		if s.IsValid(r) {
			t.Errorf("IsValid(%q) = true", r)
		}
	}
}

func TestNewValueMovesWhatItCan(t *testing.T) {
	s := New()
	for _, c := range []struct {
		current, target string
		strategy        versioning.RangeStrategy
		want            string
	}{
		{"1.43.93", "1.43.94", versioning.StrategyReplace, "1.43.94"},
		{"==1.43.93", "1.43.94", versioning.StrategyReplace, "==1.43.94"},
		{">=1.0", "2.1", versioning.StrategyReplace, ">=2.1"},
		{"==1.2.*", "1.3.4", versioning.StrategyReplace, "==1.3.*"},
		{"~=1.4", "1.5.2", versioning.StrategyReplace, "~=1.5"},
		{"~=1.4.5", "1.5.2", versioning.StrategyReplace, "~=1.5.2"},
		{"<3", "2.5", versioning.StrategyReplace, "<3"},
		{">=1.0, <2.0", "1.9", versioning.StrategyPin, "==1.9"},
	} {
		got, err := s.NewValue(c.current, c.target, c.strategy)
		if err != nil {
			t.Errorf("NewValue(%q, %q, %s): %v", c.current, c.target, c.strategy, err)
			continue
		}
		if got != c.want {
			t.Errorf("NewValue(%q, %q, %s) = %q, want %q", c.current, c.target, c.strategy, got, c.want)
		}
	}
	for _, c := range []struct{ current, target string }{
		{">=1.0, <2.0", "2.1"}, {"<2", "2.1"}, {"1.0", "banana"},
	} {
		if got, err := s.NewValue(c.current, c.target, versioning.StrategyReplace); err == nil {
			t.Errorf("NewValue(%q, %q) = %q, want an error", c.current, c.target, got)
		}
	}
}
