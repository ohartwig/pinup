// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/publish"
	"github.com/ohartwig/pinup/runner"
)

// One line per request, then the counts and the calls; a request that
// could not be read fails the pass, one that is not ready does not.
func TestArmWatchReportsEachRequestAndTheCounts(t *testing.T) {
	w := &platformfake.ArmWatcher{
		MRs: []publish.MergeRequest{
			{Project: "g/a", IID: 1, State: "opened", SHA: "h", Description: "x\n\n<!-- pinup:automerge direct -->\n"},
			{Project: "g/a", IID: 2, State: "opened", SHA: "h", Description: "x\n"},
		},
		Pipelines: map[string]platformfake.Pipeline{"g/a!1": {Status: "running", SHA: "h"}},
		Authors:   map[string][]string{"g/a!1": {"bot@x"}},
	}
	o := runner.ArmWatchOptions{Label: "pinup", AuthorEmail: "bot@x"}
	var out strings.Builder
	if err := armWatch(context.Background(), w, func() int64 { return 7 }, o, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  g/a!1 armed: ", "  g/a!2 skipped: no automerge marker", "2 open requests labelled pinup in scope: 1 armed, 0 merged, 0 not yet, 0 refused, 1 skipped, 0 failed; 7 API calls"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	w.MRs[0].Automerge = false
	w.ReadErr = errors.New("boom")
	if err := armWatch(context.Background(), w, func() int64 { return 0 }, o, &out); err == nil {
		t.Error("a request that could not be read did not fail the pass")
	}
}
