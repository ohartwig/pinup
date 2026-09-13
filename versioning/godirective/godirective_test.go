// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package godirective

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
	"github.com/ohartwig/pinup/versioning"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("go-mod-directive"))
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

// The rule that is easy to get wrong: a two-part directive like "1.27" is a
// caret range fixed at its own major, not a major.minor pin - it also admits
// a higher minor, which is what lets "1.28.0" satisfy "1.27".
func TestADirectiveIsACaretFloorNotAMinorPin(t *testing.T) {
	s := New()
	for _, tc := range []struct {
		version, rng string
		want         bool
	}{
		{"1.27.1", "1.27", true},
		{"1.27.10", "1.27", true},
		{"1.28.0", "1.27", true}, // measured: a higher minor still matches
		{"1.26.9", "1.27", false},
		{"2.0.0", "1.27", false},   // different major
		{"1.0.0", "1.27", false},   // below the floor
		{"1.27.0", "1.27.0", true}, // the three-part form behaves the same
	} {
		if got := s.Satisfies(tc.version, tc.rng); got != tc.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", tc.version, tc.rng, got, tc.want)
		}
	}
}

// The other rule that is easy to get wrong: bump is the only strategy that
// rewrites anything, and it drops a trailing zero patch regardless of how the
// current directive was written.
func TestBumpDropsAZeroPatchEveryOtherStrategyIsANoOp(t *testing.T) {
	s := New()
	got, err := s.NewValue("1.27", "1.1.0", versioning.StrategyBump)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.1" {
		t.Errorf("NewValue(bump, 1.1.0) = %q, want %q", got, "1.1")
	}
	got, err = s.NewValue("1.27.0", "1.28.1", versioning.StrategyBump)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.28.1" {
		t.Errorf("NewValue(bump, 1.28.1) = %q, want %q", got, "1.28.1")
	}
	for _, strategy := range []versioning.RangeStrategy{
		versioning.StrategyReplace, versioning.StrategyPin, versioning.StrategyWiden,
		versioning.StrategyUpdateLockfile, versioning.StrategyAuto,
	} {
		got, err := s.NewValue("1.27", "1.28.1", strategy)
		if err != nil {
			t.Fatalf("%s: %v", strategy, err)
		}
		if got != "1.27" {
			t.Errorf("NewValue(%s, 1.27 -> 1.28.1) = %q, want %q (unchanged)", strategy, got, "1.27")
		}
	}
}
