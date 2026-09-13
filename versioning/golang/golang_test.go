// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package golang

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path("go"))
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

// A pseudo-version names an untagged commit. Offering one as an upgrade over a
// real release is the mistake this test exists to prevent - and pinup updates
// its own go.mod, so it would be pinup's own mistake.
func TestPseudoVersionsAreUnstable(t *testing.T) {
	s := New()
	const pseudo = "v0.0.0-20260101000000-abcdef123456"
	if !s.IsValid(pseudo) {
		t.Error("a pseudo-version should be valid")
	}
	if s.IsStable(pseudo) {
		t.Error("a pseudo-version reported stable; it names a commit, not a release")
	}
	if s.IsStable("1.2.3.4") {
		t.Error("a four-component version reported stable")
	}
	// +incompatible marks a module without a /v2 path, not a prerelease.
	if !s.IsStable("v2.0.0+incompatible") {
		t.Error("+incompatible cost stability; it marks a module path, not a prerelease")
	}
	// Partial versions are stable here, unlike in semver.
	for _, v := range []string{"1", "1.2", "v1.27.0"} {
		if !s.IsStable(v) {
			t.Errorf("%q should be stable in the go scheme", v)
		}
	}
}

// The v prefix is NOT this package's business. Measured: rewriting "v1.27.0"
// yields a bare "1.1.0". A go.mod reference does need the v, but restoring it
// is manager/gomod's job - a versioning scheme that knew about reference
// syntax would give the manager a second place to look when a prefix went
// missing.
//
// This assertion started life the other way round, asserting the prefix
// survived, until the captured table said otherwise.
func TestTheVPrefixBelongsToTheManagerNotTheScheme(t *testing.T) {
	got, err := New().NewValue("v1.26.0", "1.27.0", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.27.0" {
		t.Errorf("NewValue = %q, want %q", got, "1.27.0")
	}
}
