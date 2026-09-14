// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package npm

import (
	"testing"

	"github.com/ohartwig/pinup/fake/vertest"
	"github.com/ohartwig/pinup/versioning"
)

var _ versioning.Versioning = (*Node)(nil)

func TestNodeConformsToCapturedBehaviour(t *testing.T) {
	tbl, err := vertest.Load(vertest.Path(t, "node"))
	if err != nil {
		t.Fatal(err)
	}
	res := vertest.Run(t, NewNode(), tbl, nodeRewriteGaps())
	if res.Compared < 200 {
		t.Errorf("only %d rows compared; the table was barely read", res.Compared)
	}
	if res.Mismatch != 0 {
		t.Errorf("%d rows disagree with the captured behaviour", res.Mismatch)
	}
}

// The node grid's getNewValue rows have npm's flaw (see rewriteGaps) on
// three ranges of their own - "24", "22" and "24-alpine" - paired with a
// 1.0.0 -> 1.1.0 that lies outside every one of them.
func nodeRewriteGaps() []vertest.Divergence {
	const why = "probe grid pairs this range with a newVersion outside it, " +
		"which an update never does; needs a purpose-built capture"
	var out []vertest.Divergence
	for _, inputs := range []string{
		"24|bump", "24|pin", "22|bump", "22|pin",
		"24-alpine|replace", "24-alpine|pin", "24-alpine|widen",
		"24-alpine|update-lockfile", "24-alpine|auto",
	} {
		out = append(out, vertest.Divergence{Op: "getNewValue", Inputs: inputs, Why: why})
	}
	return out
}

// An odd major is never stable, an even one from 4 on is; the docker-style
// tag the estate writes is no version at all.
func TestNodeStabilityFollowsTheLTSLines(t *testing.T) {
	s := NewNode()
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"24.8.0", true}, {"22.19.0", true}, {"23.11.0", false}, {"2.0.0", false}, {"1.0.0", false},
		{"24.8.0-rc.1", false}, {"24-alpine", false},
	} {
		if got := s.IsStable(c.in); got != c.want {
			t.Errorf("IsStable(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if s.IsValid("24-alpine") || s.IsVersion("24-alpine") {
		t.Error("24-alpine is a docker tag, not a node version")
	}
	if s.Name() != "node" {
		t.Errorf("name = %q", s.Name())
	}
}
