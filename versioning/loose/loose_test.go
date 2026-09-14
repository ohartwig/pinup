// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package loose

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path(t, "loose"))
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

// The estate uses loose for the bare-major component pins, so this is the case
// that matters in practice.
func TestBareMajorsOrder(t *testing.T) {
	s := New()
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1", "2", -1}, {"3", "2", 1}, {"1", "1", 0},
		{"1.33.59", "1.33.64", -1},
	} {
		if got := s.Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
