// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package composer

import (
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/fake/vertest"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("composer"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, New(), tbl, nil)
	if res.Compared < 300 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

// The rewriting rules differ per strategy, which no other scheme here does.
// These come from the captured table.
func TestRewritingKeepsShapePerStrategy(t *testing.T) {
	s := New()
	for _, c := range []struct {
		current  string
		strategy versioning.RangeStrategy
		want     string
	}{
		{"^8.5", versioning.StrategyReplace, "^1.1"},
		{"^8.5", versioning.StrategyBump, "^1.1.0"},
		{"^8.5", versioning.StrategyPin, "^1.1"},
		{"^8.5", versioning.StrategyWiden, "^8.5 || ^1.1"},
		{"^8.5", versioning.StrategyUpdateLockfile, "^1.1"},
		{"^8.5", versioning.StrategyAuto, "^1.1"},
		{"~0.9", versioning.StrategyReplace, "~1.0"},
		{"~0.9", versioning.StrategyBump, "~1.0"},
		{"~0.9", versioning.StrategyWiden, "~0.9 || ~1.0"},
		{"^13.4", versioning.StrategyReplace, "^1.1"},
	} {
		got, err := s.NewValue(c.current, "1.1.0", c.strategy)
		if err != nil {
			t.Errorf("NewValue(%q, %s): %v", c.current, c.strategy, err)
			continue
		}
		if got != c.want {
			t.Errorf("NewValue(%q -> 1.1.0, %s) = %q, want %q", c.current, c.strategy, got, c.want)
		}
	}
}

// A constraint is valid but never stable, because it does not name a release.
func TestConstraintsAreValidButNotStable(t *testing.T) {
	s := New()
	for _, r := range []string{"^8.5", "~0.9", "^5.0", "1.0.*", ">=1.0 <2.0", "13.4.*"} {
		if !s.IsValid(r) {
			t.Errorf("%q should be a valid constraint", r)
		}
		if s.IsStable(r) {
			t.Errorf("%q reported stable; a range does not name a release", r)
		}
	}
	for _, v := range []string{"1.0.0", "1.2", "1", "v1.2.3"} {
		if !s.IsStable(v) {
			t.Errorf("%q should be stable", v)
		}
	}
}

// Composer's stability flags order below a plain release, which is what keeps
// a TYPO3 release candidate from being offered as an upgrade.
//
// Note where "1.0.0-RC1" sits: BELOW dev, not between beta and stable. An
// earlier version of this test put it where intuition says it belongs and the
// captured behaviour disagreed. Composer matches its flags lowercase, so an
// uppercase "RC1" is not a flag at all - it is an unrecognised suffix, and
// those rank below everything.
func TestStabilityFlagsOrder(t *testing.T) {
	s := New()
	ordered := []string{"1.0.0-RC1", "1.0.0-dev", "1.0.0-alpha", "1.0.0-beta2", "1.0.0-rc.1", "1.0.0"}
	for i := 0; i+1 < len(ordered); i++ {
		if s.Compare(ordered[i], ordered[i+1]) >= 0 {
			t.Errorf("%q should sort before %q", ordered[i], ordered[i+1])
		}
	}
	for _, f := range []string{"1.0.0-alpha", "1.0.0-beta2", "1.0.0-RC1", "1.0.0-rc.1"} {
		if s.IsStable(f) {
			t.Errorf("%q reported stable", f)
		}
	}
}

// Four components is not a composer version, unlike apk and docker where it
// is. Each scheme draws this line differently.
func TestFourComponentsIsNotAVersion(t *testing.T) {
	if New().IsValid("1.2.3.4") {
		t.Error("1.2.3.4 was accepted; composer versions have at most three components")
	}
}

// The estate's real constraints, from the extraction corpus.
func TestRealEstateConstraints(t *testing.T) {
	s := New()
	for _, c := range []struct {
		version, rng string
		want         bool
	}{
		{"0.9.1", "~0.9", true},
		// ~0.9 means >=0.9 <1.0 in composer - tilde on two components frees
		// the minor - so 0.10.0 IS in range. This assertion said false until
		// the implementation disagreed and composer's own documentation
		// settled it.
		{"0.10.0", "~0.9", true},
		{"1.0.0", "~0.9", false},
		{"13.4.2", "^13.4", true},
		{"14.0.0", "^13.4", false},
		{"5.1.0", "^5.0", true},
		{"6.0.0", "^5.0", false},
		{"0.6.2", "^0.3 || ^0.4 || ^0.5 || ^0.6", true},
		{"0.7.0", "^0.3 || ^0.4 || ^0.5 || ^0.6", false},
	} {
		if got := s.Satisfies(c.version, c.rng); got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", c.version, c.rng, got, c.want)
		}
	}
}
