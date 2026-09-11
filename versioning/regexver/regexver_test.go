// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package regexver

import (
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/fake/vertest"
)

// The estate's one literal regex versioning, on the bash package.
const alpinePattern = `^alpine(?<major>[0-9]+)\.(?<minor>[0-9]+)`

func configured(t *testing.T) *Scheme {
	t.Helper()
	v, err := New().WithConfig(alpinePattern)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*Scheme)
}

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("regex-alpine"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, configured(t), tbl, nil)
	if res.Compared < 100 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

func TestAlpineTagsBecomeVersions(t *testing.T) {
	s := configured(t)
	for _, c := range []struct {
		in       string
		ok       bool
		maj, min int
	}{
		{"alpine3.21", true, 3, 21},
		{"alpine3.20", true, 3, 20},
		{"alpine3.9", true, 3, 9},
		{"alpine4.0", true, 4, 0},
		{"alpine3", false, 0, 0},
		{"alpine", false, 0, 0},
		{"3.21", false, 0, 0},
		{"bookworm", false, 0, 0},
	} {
		if got := s.IsValid(c.in); got != c.ok {
			t.Errorf("IsValid(%q) = %v, want %v", c.in, got, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		maj, _ := s.Major(c.in)
		min, _ := s.Minor(c.in)
		if maj != c.maj || min != c.min {
			t.Errorf("%q = %d.%d, want %d.%d", c.in, maj, min, c.maj, c.min)
		}
	}
	// 3.9 before 3.20 - the whole reason a regex scheme is needed here rather
	// than a string comparison.
	if s.Compare("alpine3.9", "alpine3.20") >= 0 {
		t.Error("alpine3.9 should sort below alpine3.20; the groups are numbers, not text")
	}
	if s.Compare("alpine4.0", "alpine3.21") <= 0 {
		t.Error("alpine4.0 should outrank alpine3.21")
	}
}

func TestConfigurationIsRefusedWhenUseless(t *testing.T) {
	if _, err := New().WithConfig(""); err == nil {
		t.Error("an empty pattern was accepted")
	}
	if _, err := New().WithConfig(`^alpine[0-9]+`); err == nil {
		t.Error("a pattern with no named groups was accepted; nothing would become a version component")
	}
	if _, err := New().WithConfig(`^alpine(?<major>[0-9]+`); err == nil {
		t.Error("an uncompilable pattern was accepted")
	}
}

// A compatibility group separates values that are not comparable - the same
// separation docker draws by hand.
func TestCompatibilityGroupsDoNotCompare(t *testing.T) {
	v, err := New().WithConfig(`^(?<compatibility>[a-z]+)(?<major>[0-9]+)\.(?<minor>[0-9]+)$`)
	if err != nil {
		t.Fatal(err)
	}
	s := v.(*Scheme)
	if s.Compare("alpine3.21", "debian3.21") != 0 {
		t.Error("values with different compatibility groups should not compare")
	}
	if s.Equal("alpine3.21", "debian3.21") {
		t.Error("values with different compatibility groups are not equal")
	}
	if s.Compare("alpine3.21", "alpine3.20") <= 0 {
		t.Error("within one compatibility group the numbers still order")
	}
}
