// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package hashicorp

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("hashicorp"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, New(), tbl, nil)
	if res.Compared < 200 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

// Terraform's pessimistic operator, which the 22 corpus vectors all use.
func TestPessimisticOperator(t *testing.T) {
	s := New()
	for _, c := range []struct {
		version, rng string
		want         bool
	}{
		{"3.1.0", "~> 3.1", true},
		{"3.9.9", "~> 3.1", true},
		{"4.0.0", "~> 3.1", false},
		{"3.0.0", "~> 3.1", false},
		{"3.1.5", "~> 3.1.4", true},
		{"3.2.0", "~> 3.1.4", false},
		{"1.5.0", ">= 1.0, < 2.0", true},
		{"2.0.0", ">= 1.0, < 2.0", false},
		{"0.9.0", ">= 1.0, < 2.0", false},
	} {
		if got := s.Satisfies(c.version, c.rng); got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", c.version, c.rng, got, c.want)
		}
	}
}

func TestConstraintsAreValidButNotStable(t *testing.T) {
	s := New()
	for _, r := range []string{"1.2", "1", "~> 3.1", ">= 1.0, < 2.0"} {
		if !s.IsValid(r) {
			t.Errorf("%q should be a valid constraint", r)
		}
		if s.IsStable(r) {
			t.Errorf("%q reported stable; a constraint names no release", r)
		}
	}
	for _, bad := range []string{"1.2.3.4", ""} {
		if s.IsValid(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}
