// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package apk

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path(t, "apk"))
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

// The revision is why this scheme exists: a Wolfi rebuild bumps -rN without
// touching the upstream version, and missing it means missing a CVE fix.
func TestRevisionsOrder(t *testing.T) {
	s := New()
	ordered := []string{"1.2.3", "1.2.3-r1", "1.2.3-r4", "1.2.3-r5", "1.2.4"}
	for i := 0; i+1 < len(ordered); i++ {
		if s.Compare(ordered[i], ordered[i+1]) >= 0 {
			t.Errorf("%q should sort before %q", ordered[i], ordered[i+1])
		}
	}
	if rev, ok := Revision("1.27.0-r1"); !ok || rev != 1 {
		t.Errorf("Revision(1.27.0-r1) = (%d,%v), want (1,true)", rev, ok)
	}
	if _, ok := Revision("1.27.0"); ok {
		t.Error("Revision reported a revision on a value that carries none")
	}
}

// Only -rN may follow the numbers. A prerelease suffix is not ignored, it
// invalidates the value - which is what keeps a semver prerelease from being
// read as an apk package.
func TestOnlyRevisionSuffixesAreAccepted(t *testing.T) {
	s := New()
	for _, bad := range []string{"1.0.0-alpha", "1.0.0-rc.1", "1.0.0-r", "1.0.0-rx", "1.0.0-r-1"} {
		if s.IsValid(bad) {
			t.Errorf("%q was accepted; only an -rN revision may follow the numbers", bad)
		}
	}
	for _, good := range []string{"1.0.0", "1.0.0-r0", "1.2", "1", "1.2.3.4", "v1.2.3", "8.5.10-r0"} {
		if !s.IsValid(good) {
			t.Errorf("%q was rejected", good)
		}
	}
}

// Wolfi versions its packages with apk's underscore suffixes. python-3.13
// 3.13.15_git20260912-r0 fixed a HIGH that 3.13.15-r8 carried, and pinup
// never offered it while this scheme rejected the value (2026-09-27).
func TestUnderscoreSuffixesOrder(t *testing.T) {
	s := New()
	ordered := []string{
		"1.0_alpha", "1.0_alpha2", "1.0_beta", "1.0_pre1", "1.0_rc1", "1.0_rc2",
		"1.0", "1.0-r3", "1.0_cvs", "1.0_svn", "1.0_git20260101", "1.0_git20260912",
		"1.0_hg", "1.0_p1", "1.0_p2", "1.0_p2-r1", "1.0a", "1.0b_rc1", "1.0b", "1.0.1",
	}
	for i := 0; i+1 < len(ordered); i++ {
		if !s.IsValid(ordered[i]) {
			t.Errorf("%q was rejected", ordered[i])
		}
		if s.Compare(ordered[i], ordered[i+1]) >= 0 || s.Compare(ordered[i+1], ordered[i]) <= 0 {
			t.Errorf("%q should sort before %q", ordered[i], ordered[i+1])
		}
	}
	if s.Compare("3.13.15-r8", "3.13.15_git20260912-r0") >= 0 {
		t.Error("the Wolfi python-3.13 fix does not sort after the build it replaces")
	}
	for _, v := range []string{"1.0_rc1", "2.0_beta3-r1", "1.0_alpha"} {
		if s.IsStable(v) {
			t.Errorf("%q is a prerelease and was called stable", v)
		}
	}
	for _, v := range []string{"3.13.15_git20260912-r0", "9.9_p1-r2", "1.0_cvs", "1.0a"} {
		if !s.IsStable(v) {
			t.Errorf("%q is a release and was called unstable", v)
		}
	}
	for _, bad := range []string{"1.0_", "1.0_foo", "1.0_rc_", "1.0__p1", "_p1", "1.0_p1x", "1.0A", "1.0ab"} {
		if s.IsValid(bad) {
			t.Errorf("%q was accepted; it is not an apk version", bad)
		}
	}
}
