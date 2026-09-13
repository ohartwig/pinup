// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package docker

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
	"github.com/ohartwig/pinup/model"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("docker"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, New(), tbl, nil)
	if res.Compared < 400 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

// The suffix order is inverted, and every one of these comes from the captured
// table. Stated separately because the rule is surprising enough that someone
// will eventually "fix" it.
func TestSuffixesOrderInReverse(t *testing.T) {
	s := New()
	for _, c := range []struct {
		a, b string
		want bool // a > b
	}{
		{"1.0.0", "1.0.0-alpha", true},
		{"1.0.0-alpha", "1.0.0", false},
		{"22-alpine3.20", "22-alpine3.21", true},
		{"22-alpine3.21", "22-alpine3.20", false},
		{"22-alpine3.21", "22-bookworm", true},
		{"22-bookworm", "22-alpine3.21", false},
	} {
		if got := s.Compare(c.a, c.b) > 0; got != c.want {
			t.Errorf("Compare(%q,%q) > 0 = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The inverted order is exactly why a suffix change must not be treated as a
// version move: following it would propose alpine3.21 -> alpine3.20.
func TestCompatibilityIsItsOwnUpdateType(t *testing.T) {
	s := New()
	for _, c := range []struct {
		from, to string
		want     model.UpdateType
		why      string
	}{
		{"22-alpine3.20", "22-alpine3.21", model.UpdateCompatibility,
			"same version, newer compatibility within one family"},
		{"22-alpine3.21", "22-bookworm", model.UpdateUnknown,
			"a family change is never offered"},
		{"22-alpine3.21", "23-alpine3.21", model.UpdateMajor,
			"the numbers decide when they differ"},
		{"1.26.0", "1.27.0", model.UpdateMinor, "ordinary minor bump"},
		{"1.27.0", "1.26.0", model.UpdateRollback, "the numbers went backwards"},
		{"22-alpine3.21", "22-alpine3.21", model.UpdateUnknown, "no change"},
	} {
		if got := s.UpdateType(c.from, c.to); got != c.want {
			t.Errorf("UpdateType(%q -> %q) = %v, want %v (%s)", c.from, c.to, got, c.want, c.why)
		}
	}
}

// A version bump must never be reported as a rollback because the suffix
// ordering pulled the other way.
func TestSuffixOrderCannotInvertAVersionBump(t *testing.T) {
	s := New()
	if got := s.UpdateType("22-alpine3.21", "23-alpine3.20"); got == model.UpdateRollback {
		t.Error("a major bump was reported as a rollback; the suffix order leaked into the classification")
	}
}

func TestRewriteKeepsTheSuffix(t *testing.T) {
	s := New()
	got, err := s.NewValue("22-alpine3.21", "23", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if got != "23-alpine3.21" {
		t.Errorf("NewValue = %q, want %q - dropping the suffix changes the image", got, "23-alpine3.21")
	}
}

func TestFamily(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"22-alpine3.21", "alpine"},
		{"22-bookworm", "bookworm"},
		{"22", ""},
		{"v0.32.2-rootless", "rootless"},
		{"latest", ""},
	} {
		if got := Family(c.in); got != c.want {
			t.Errorf("Family(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
