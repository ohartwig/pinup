// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

// The automerge marker is the last line of a description whose request
// wants automerge: a hidden HTML comment, written by the run and read by
// ArmWatch. "direct" records automergeDirect - a request GitLab calls
// mergeable at once is merged rather than left armed. A request whose
// merge waits for a window carries it: "tz=<zone>" and, last,
// "schedule=<expr>" with several expressions joined by " | ", so the watch
// judges the window without the configuration:
//
//	<!-- pinup:automerge direct tz=Europe/Berlin schedule=* 3-5 * * * -->
//
// Only the last non-empty line counts, and the description's own text is
// neutralised before the marker is added: release notes and a
// configuration's body are foreign text, and a line in them that read like
// the marker must not arm a request that was never meant to merge on its
// own (a major held for review). A line that starts like the marker and
// carries anything else is no marker: a request is never merged on a
// reading the watch is unsure of. A pinup before the window was recorded
// reads a windowed marker as no marker and leaves the request alone.
const (
	markerPrefix    = "<!-- pinup:automerge"
	markerSuffix    = "-->"
	automergeMarker = markerPrefix + " " + markerSuffix
	directMarker    = markerPrefix + " direct " + markerSuffix
	windowSep       = " | "
)

// withAutomergeMarker appends the marker when the request wants automerge
// and removes any look-alike from the text before it. Without automerge
// the description carries no marker at all.
func withAutomergeMarker(description string, automerge, direct bool, window model.Window) string {
	description = strings.ReplaceAll(description, markerPrefix, "<!-- pinup-quoted:automerge")
	if !automerge {
		return description
	}
	var marker strings.Builder
	marker.WriteString(markerPrefix)
	if direct {
		marker.WriteString(" direct")
	}
	if len(window.Raw) > 0 {
		fmt.Fprintf(&marker, " tz=%s schedule=%s", cmp.Or(window.Timezone, "UTC"), strings.Join(window.Raw, windowSep))
	}
	marker.WriteString(" " + markerSuffix)
	return strings.TrimRight(description, "\n") + "\n\n" + marker.String() + "\n"
}

// automergeWanted reads the marker: whether the description's last
// non-empty line is one, whether it asks for a direct merge, and the
// window the merge waits for (no Raw: none).
func automergeWanted(description string) (wanted, direct bool, window model.Window) {
	trimmed := strings.TrimSpace(description)
	_, last, ok := strings.CutLast(trimmed, "\n")
	if !ok {
		last = trimmed
	}
	body, ok := strings.CutPrefix(strings.TrimSpace(last), markerPrefix)
	if !ok {
		return false, false, model.Window{}
	}
	body, ok = strings.CutSuffix(body, markerSuffix)
	if !ok || (body != "" && body[0] != ' ') {
		return false, false, model.Window{}
	}
	body = strings.TrimSpace(body)
	if rest, ok := cutWord(body, "direct"); ok {
		direct, body = true, rest
	}
	if rest, ok := strings.CutPrefix(body, "tz="); ok {
		tz, after, _ := strings.Cut(rest, " ")
		window.Timezone, body = tz, strings.TrimSpace(after)
		exprs, ok := strings.CutPrefix(body, "schedule=")
		if !ok || tz == "" {
			return false, false, model.Window{}
		}
		for e := range strings.SplitSeq(exprs, windowSep) {
			if e = strings.TrimSpace(e); e != "" {
				window.Raw = append(window.Raw, e)
			}
		}
		if len(window.Raw) == 0 {
			return false, false, model.Window{}
		}
		body = ""
	}
	if body != "" {
		return false, false, model.Window{}
	}
	return true, direct, window
}

// cutWord cuts word off the front of s when it is a whole word.
func cutWord(s, word string) (string, bool) {
	rest, ok := strings.CutPrefix(s, word)
	if !ok || (rest != "" && rest[0] != ' ') {
		return s, false
	}
	return strings.TrimSpace(rest), true
}

// ArmWatchOptions is what one pass of the arm watch needs.
type ArmWatchOptions struct {
	// Label is the label every request of the bot carries ("pinup").
	Label string
	// Only restricts the watch to projects the runner acts on; nil is
	// every project the listing returns.
	Only *glob.Set
	// AuthorEmail is the bot's commit identity. A request with a commit by
	// anyone else was taken over by a person and is theirs.
	AuthorEmail string
	// Now reads the clock for a request whose merge waits for a window;
	// nil leaves every such request alone, since no window can be judged.
	Now func() time.Time
}

// ArmResult is what the watch did with one request.
type ArmResult struct {
	Project string
	IID     int
	// Action: "armed", "merged", "refused", "not-yet", "deferred",
	// "disarmed", "skipped", "failed". "deferred" is a request outside its
	// merge window; "disarmed" one whose arm the watch took back because
	// the window had closed.
	Action string
	Reason string
}

func (r ArmResult) String() string {
	s := fmt.Sprintf("%s!%d %s", r.Project, r.IID, r.Action)
	if r.Reason != "" {
		s += ": " + r.Reason
	}
	return s
}

// ArmWatch arms automerge on the bot's open requests that want it and are
// not armed. A run pushes and waits a minute for GitLab to create the
// pipeline it cannot arm without; when the pipeline comes later - six
// minutes and more, measured on development/moselwal/moselwal-websites!1119
// on 2026-10-22 - the request stays green and unarmed until a run that
// happens not to push. The push-triggered run does not come for a merge of
// the bot's own, and the four-hourly scan often pushes again (a moved lock
// file) and loses the same race. This pass needs no clone and no plan: the
// description says what the run decided (the marker), the platform says
// whether the moment has come.
//
// It is idempotent - a request already armed, or not ready, is left as it
// is - and every request it reads costs at most five calls, in order of
// what rules the most out first.
func ArmWatch(ctx context.Context, w publish.ArmWatcher, o ArmWatchOptions) ([]ArmResult, error) {
	open, err := w.OpenLabelled(ctx, o.Label)
	if err != nil {
		return nil, err
	}
	var out []ArmResult
	for _, mr := range open {
		if mr.Project == "" || (o.Only != nil && !o.Only.Match(mr.Project)) {
			continue
		}
		out = append(out, armOne(ctx, w, o, mr))
	}
	return out, nil
}

func armOne(ctx context.Context, w publish.ArmWatcher, o ArmWatchOptions, mr publish.MergeRequest) ArmResult {
	res := ArmResult{Project: mr.Project, IID: mr.IID, Action: "skipped"}
	skip := func(why string) ArmResult { res.Reason = why; return res }
	wanted, direct, window := automergeWanted(mr.Description)
	windowed := len(window.Raw) > 0
	if !wanted {
		return skip("no automerge marker")
	}
	if windowed {
		// Judged first and on every pass: outside the window nothing
		// else about the request matters, and an arm left on it from
		// inside the window must not merge it now.
		if o.Now == nil {
			return skip("waits for a merge window and the watch has no clock")
		}
		open, next, err := windowOpen(window, o.Now())
		switch {
		case err != nil:
			res.Action, res.Reason = "failed", "merge window: "+err.Error()
			return res
		case !open:
			res.Action, res.Reason = "deferred", deferral(window, next)
			if mr.Automerge {
				return disarm(ctx, w, mr, res, "the merge window closed")
			}
			return res
		case mr.Automerge:
			// Armed inside the window - by an older pinup, or a run
			// without the watch - the arm could outlast it. Taken back;
			// the watch merges the request itself once it is green.
			if after := disarm(ctx, w, mr, res, "an arm can outlast the merge window"); after.Action != "disarmed" {
				return after
			}
		}
	}
	switch {
	case mr.Automerge && !windowed:
		return skip("already armed")
	case mr.Draft:
		return skip("draft")
	case mr.Conflict:
		return skip("conflicted; the next run rebases it")
	case mr.SHA == "":
		return skip("no head recorded")
	}
	status, pipelineSHA, err := w.HeadPipeline(ctx, mr.Project, mr.IID)
	switch {
	case err != nil:
		res.Action, res.Reason = "failed", "head pipeline: "+err.Error()
		return res
	case status == "":
		return skip("no pipeline yet")
	case pipelineSHA != mr.SHA:
		return skip(fmt.Sprintf("the pipeline is for %s, not the head %s yet", short(pipelineSHA), short(mr.SHA)))
	case status == "failed" || status == "canceled" || status == "canceling":
		return skip("pipeline " + status)
	case windowed && status != "success":
		return skip("pipeline " + status + "; a request with a merge window is merged once its pipeline has succeeded, never armed")
	}
	authors, err := w.CommitAuthors(ctx, mr.Project, mr.IID)
	if err != nil {
		res.Action, res.Reason = "failed", "commits: "+err.Error()
		return res
	}
	if len(authors) == 0 {
		return skip("no commits")
	}
	for _, a := range authors {
		if !strings.EqualFold(a, o.AuthorEmail) {
			return skip("carries a commit by " + a + "; left to them")
		}
	}
	people, err := w.AutomergeCanceledBy(ctx, mr.Project, mr.IID)
	if err != nil {
		res.Action, res.Reason = "failed", "notes: "+err.Error()
		return res
	}
	if len(people) > 0 {
		return skip("automerge canceled by " + strings.Join(people, ", "))
	}
	if windowed {
		after, err := w.MergeNow(ctx, mr.Project, mr.IID, mr.SHA)
		switch {
		case err != nil:
			res.Action, res.Reason = "failed", "merge: "+err.Error()
		case after.State == "merged":
			res.Action, res.Reason = "merged", fmt.Sprintf("pipeline %s, inside the merge window %q (%s)", status, strings.Join(window.Raw, "; "), window.Timezone)
		case after.AutomergeRefused != "":
			res.Action, res.Reason = "refused", after.AutomergeRefused
		default:
			res.Action, res.Reason = "not-yet", "GitLab did not call it mergeable yet (pipeline "+status+")"
		}
		return res
	}
	after, err := w.ArmAutomerge(ctx, mr.Project, mr.IID, mr.SHA, direct)
	switch {
	case err != nil:
		res.Action, res.Reason = "failed", "arm: "+err.Error()
	case after.State == "merged":
		res.Action, res.Reason = "merged", "pipeline "+status
	case after.Automerge:
		mode := "merge when the pipeline succeeds"
		if direct {
			mode += ", direct"
		}
		res.Action, res.Reason = "armed", mode+", pipeline "+status
	case after.AutomergeRefused != "":
		res.Action, res.Reason = "refused", after.AutomergeRefused
	default:
		res.Action, res.Reason = "not-yet", "GitLab did not arm it yet (pipeline "+status+")"
	}
	return res
}

// disarm takes back the arm on a request whose merge waits for a window.
// An arm somebody else set is theirs and stays (the platform says so in
// AutomergeRefused); the request is reported as skipped then.
func disarm(ctx context.Context, w publish.ArmWatcher, mr publish.MergeRequest, res ArmResult, why string) ArmResult {
	after, err := w.CancelAutomerge(ctx, mr.Project, mr.IID)
	switch {
	case err != nil:
		res.Action, res.Reason = "failed", "cancel automerge: "+err.Error()
	case after.Automerge:
		res.Action, res.Reason = "skipped", strings.TrimSpace(res.Reason+"; armed, and not taken back: "+cmp.Or(after.AutomergeRefused, "the platform kept the arm"))
	default:
		res.Action = "disarmed"
		res.Reason = strings.TrimSpace(why + "; merge-when-pipeline-succeeds taken back. " + res.Reason)
	}
	return res
}
