// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package partial

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("semver-partial"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, New(), tbl, nil)
	if res.Compared < 250 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

// The rule that is easy to get wrong, stated as its own test because eight
// corpus vectors and the rolling-major notification depend on it.
func TestAPartialIsAPatternNotAVersion(t *testing.T) {
	s := New()
	if !s.Satisfies("1.10.17", "1") {
		t.Error(`"1.10.17" should satisfy "1"`)
	}
	if s.Satisfies("1.22", "1") {
		t.Error(`"1.22" must NOT satisfy "1": a two-part value is not a version here`)
	}
	if _, ok := s.Major("1.22"); ok {
		t.Error(`"1.22" reported a major; a partial has no components`)
	}
	if s.IsStable("1") {
		t.Error(`"1" reported stable; a pattern does not name a release`)
	}
	if !s.IsValid("1") {
		t.Error(`"1" should be valid as a constraint`)
	}
}

// A bare-major pin must stay a bare-major pin. Widening it is the automatic
// adoption the estate's rolling-tag decision forbids.
func TestRewritingKeepsTheShapeOfThePin(t *testing.T) {
	s := New()
	got, err := s.NewValue("1", "1.10.18", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1" {
		t.Errorf("NewValue(1 -> 1.10.18) = %q, want %q", got, "1")
	}
	got, err = s.NewValue("1.10.17", "1.10.18", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.10.18" {
		t.Errorf("NewValue(1.10.17 -> 1.10.18) = %q, want %q", got, "1.10.18")
	}
}
