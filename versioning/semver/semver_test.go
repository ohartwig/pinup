// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package semver

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
)

// The captured behaviour table is the specification. This test is the whole
// acceptance check for the scheme.
func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path(t, "semver"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, New(), tbl, nil)
	// About half the table is skipped rather than compared, and legitimately:
	// those are rows where Renovate itself threw (getMajor on "1.2", say).
	// An exception is not an answer, so it is not a specification either -
	// isValid already covers the same ground. The floor guards against the
	// case that matters: a run that compared almost nothing and passed.
	if res.Compared < 250 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

// A few properties the table cannot express, because it is a list of answers
// rather than a statement about them.
func TestOrderingIsATotalOrderHere(t *testing.T) {
	s := New()
	versions := []string{"1.0.0", "1.0.1", "1.1.0", "2.0.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-rc.1"}
	for _, a := range versions {
		for _, b := range versions {
			ab, ba := s.Compare(a, b), s.Compare(b, a)
			if ab != -ba {
				t.Errorf("Compare(%q,%q)=%d but Compare(%q,%q)=%d - not antisymmetric", a, b, ab, b, a, ba)
			}
			for _, c := range versions {
				if s.Compare(a, b) < 0 && s.Compare(b, c) < 0 && s.Compare(a, c) >= 0 {
					t.Errorf("%q < %q < %q but not %q < %q - not transitive", a, b, c, a, c)
				}
			}
		}
	}
}

func TestPrereleaseOrdering(t *testing.T) {
	s := New()
	// From the specification, and exercised by the corpus through TYPO3 and
	// composer prereleases.
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta",
		"1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
	}
	for i := 0; i+1 < len(ordered); i++ {
		if s.Compare(ordered[i], ordered[i+1]) >= 0 {
			t.Errorf("%q should sort before %q", ordered[i], ordered[i+1])
		}
	}
}

func TestVPrefixIsPreservedOnRewrite(t *testing.T) {
	s := New()
	// Not in the captured table, and decided here: writing "1.1.0" where the
	// file said "v1.0.0" would produce a reference that names no tag.
	got, err := s.NewValue("v1.0.0", "1.1.0", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.1.0" {
		t.Errorf("NewValue(v1.0.0 -> 1.1.0) = %q, want %q", got, "v1.1.0")
	}
	got, err = s.NewValue("1.0.0", "1.1.0", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.1.0" {
		t.Errorf("NewValue(1.0.0 -> 1.1.0) = %q, want %q", got, "1.1.0")
	}
}

func TestLeadingZeroIsRejected(t *testing.T) {
	if New().IsValid("01.2.3") {
		t.Error("01.2.3 was accepted; the specification forbids a leading zero")
	}
}
