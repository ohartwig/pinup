// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package npm

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
)

func TestConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path(t, "npm"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, New(), tbl, rewriteGaps())
	if res.Compared < 200 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

// The captured getNewValue rows for npm are artefacts of an unrealistic probe
// grid, and are declared rather than matched.
//
// The grid paired every range with a fixed currentVersion 1.0.0 and
// newVersion 1.1.0. For "^1.2.3" that is a caret range whose lower bound is
// ABOVE the new version - a combination that does not occur, because an update
// moves a dependency forward into its range, not backwards out of it. Renovate
// answers "^1.0.0" there, which is a sensible response to a nonsensical
// question and not a rule worth reproducing. "1.x" has the same problem: the
// grid never asks it to absorb a version outside its own major.
//
// Declaring them keeps the difference visible and counted. Each entry dies the
// moment the capture carries realistic pairs, because vertest treats a
// divergence that never fires as an error - which is the point. The task is
// recorded in docs/tasks.md as a purpose-built npm rewrite capture.
func rewriteGaps() []vertest.Divergence {
	const why = "probe grid pairs this range with a newVersion outside it, " +
		"which an update never does; needs a purpose-built capture"
	var out []vertest.Divergence
	for _, inputs := range []string{
		"^1.2.3|replace", "^1.2.3|pin", "^1.2.3|widen",
		"^1.2.3|update-lockfile", "^1.2.3|auto",
		"~1.2.3|pin",
		"1.x|replace", "1.x|bump", "1.x|pin", "1.x|widen",
		"1.x|update-lockfile", "1.x|auto",
	} {
		out = append(out, vertest.Divergence{Op: "getNewValue", Inputs: inputs, Why: why})
	}
	return out
}

// In npm a partial version is a RANGE, so it is valid and never stable - the
// opposite of the go scheme, where "1.2" is a stable version.
func TestPartialsAreRangesNotVersions(t *testing.T) {
	s := New()
	for _, r := range []string{"1.2", "1", "^1.2.3", "~1.2.3", "1.x", ">=1.0.0 <2.0.0", ""} {
		if !s.IsValid(r) {
			t.Errorf("%q should be a valid range", r)
		}
		if s.IsStable(r) {
			t.Errorf("%q reported stable; a range names no release", r)
		}
	}
	if s.IsValid("1.2.3.4") {
		t.Error("1.2.3.4 was accepted; npm versions have three components")
	}
}
