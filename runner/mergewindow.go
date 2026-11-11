// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/sched"
)

// A merge window is the schedule an automerge waits for
// (model.Branch.MergeWindow). The schedule gates the creation of a
// request; a request that exists is kept current outside it, and its merge
// waits for the window - judged at the moment of merging, by every path
// that merges: the run arming a request, and the arm watch merging one.
//
// Merge-when-pipeline-succeeds cannot be bounded by a window: armed at
// 05:50 for a pipeline that finishes at 06:10, GitLab merges at 06:10.
// Where the arm watch runs (GitLab), a windowed request is therefore never
// armed: the watch merges it outright once its pipeline has succeeded and
// the window is open, and takes back an arm it finds on it. The cost is up
// to one watch interval (ten minutes) between a green pipeline and the
// merge, and a pipeline that turns green in the last minutes of a window
// waits for the next one. Without the watch (GitHub) the run arms the
// request inside the window and takes the arm back on its first pass
// outside it - the platform's auto-merge is the only merge there is.
//
// Measured on devops/koh-gitops!8807 (2026-11-11): a mariadb bump under
// the night window "* 3-5 * * *" (Europe/Berlin) opened at 05:18, armed,
// its pipeline failed; the four-hourly run rebased it outside the window at 08:56, the
// new pipeline passed at 09:07, and the arm watch merged it directly at
// 09:13 - every tenant's database restarted in the daytime.

// windowOpen judges the window at now: whether it is open, and when it
// next opens if not. A window without expressions is always open.
func windowOpen(w model.Window, now time.Time) (bool, time.Time, error) {
	if len(w.Raw) == 0 {
		return true, time.Time{}, nil
	}
	s, err := sched.Parse(w.Raw, w.Timezone)
	if err != nil {
		return false, time.Time{}, err
	}
	if s.IsActive(now) {
		return true, time.Time{}, nil
	}
	return false, s.NextOpen(now), nil
}

// armDecision says whether the run arms automerge on a request that wants
// it, and the note it logs when the merge is deferred. watch is
// Options.MergeWatch.
func armDecision(w model.Window, now time.Time, watch bool) (bool, string, error) {
	if len(w.Raw) == 0 {
		return true, "", nil
	}
	open, next, err := windowOpen(w, now)
	switch {
	case err != nil:
		return false, "", err
	case !open:
		return false, deferral(w, next), nil
	case watch:
		// Open, and the arm watch merges it once green; an arm now
		// could outlast the window.
		return false, "", nil
	}
	return true, "", nil
}

// deferral is the line said when a merge waits for its window.
func deferral(w model.Window, next time.Time) string {
	s := fmt.Sprintf("automerge deferred: outside the merge window %q (%s)", strings.Join(w.Raw, "; "), w.Timezone)
	if next.IsZero() {
		return s + "; it does not open within a year"
	}
	if loc, err := time.LoadLocation(w.Timezone); err == nil {
		next = next.In(loc)
	}
	return s + "; next opens " + next.Format("2006-01-02 15:04 MST")
}

// clock is the time at the moment of merging.
func (o Options) clock() time.Time {
	if o.Clock != nil {
		return o.Clock()
	}
	return o.Now
}
