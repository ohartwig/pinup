// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package coerced

import (
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/fake/vertest"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("semver-coerced"))
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

func TestCoercion(t *testing.T) {
	s := New()
	for _, c := range []struct {
		in            string
		maj, min, pat int
		stable        bool
	}{
		{"1", 1, 0, 0, true},
		{"1.2", 1, 2, 0, true},
		{"1.2.3", 1, 2, 3, true},
		{"v1.2.3", 1, 2, 3, true},
		{"1.2.3.4", 1, 2, 3, false},
	} {
		maj, _ := s.Major(c.in)
		min, _ := s.Minor(c.in)
		pat, _ := s.Patch(c.in)
		if maj != c.maj || min != c.min || pat != c.pat {
			t.Errorf("%q coerced to %d.%d.%d, want %d.%d.%d", c.in, maj, min, pat, c.maj, c.min, c.pat)
		}
		if got := s.IsStable(c.in); got != c.stable {
			t.Errorf("IsStable(%q) = %v, want %v", c.in, got, c.stable)
		}
	}
}
