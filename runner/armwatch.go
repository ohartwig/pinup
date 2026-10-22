// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/publish"
)

// The automerge marker is the last line of a description whose request
// wants automerge: a hidden HTML comment, written by the run and read by
// ArmWatch. "direct" records automergeDirect - a request GitLab calls
// mergeable at once is merged rather than left armed.
//
// Only the last non-empty line counts, and the description's own text is
// neutralised before the marker is added: release notes and a
// configuration's body are foreign text, and a line in them that read like
// the marker must not arm a request that was never meant to merge on its
// own (a major held for review).
const (
	markerPrefix    = "<!-- pinup:automerge"
	automergeMarker = markerPrefix + " -->"
	directMarker    = markerPrefix + " direct -->"
)

// withAutomergeMarker appends the marker when the request wants automerge
// and removes any look-alike from the text before it. Without automerge
// the description carries no marker at all.
func withAutomergeMarker(description string, automerge, direct bool) string {
	description = strings.ReplaceAll(description, markerPrefix, "<!-- pinup-quoted:automerge")
	if !automerge {
		return description
	}
	marker := automergeMarker
	if direct {
		marker = directMarker
	}
	return strings.TrimRight(description, "\n") + "\n\n" + marker + "\n"
}

// automergeWanted reads the marker: whether the description's last
// non-empty line is one, and whether it asks for a direct merge.
func automergeWanted(description string) (wanted, direct bool) {
	trimmed := strings.TrimSpace(description)
	_, last, ok := strings.CutLast(trimmed, "\n")
	if !ok {
		last = trimmed
	}
	switch strings.TrimSpace(last) {
	case automergeMarker:
		return true, false
	case directMarker:
		return true, true
	}
	return false, false
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
}

// ArmResult is what the watch did with one request.
type ArmResult struct {
	Project string
	IID     int
	// Action: "armed", "merged", "refused", "not-yet", "skipped", "failed".
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
	wanted, direct := automergeWanted(mr.Description)
	switch {
	case !wanted:
		return skip("no automerge marker")
	case mr.Automerge:
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
