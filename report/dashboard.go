// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/publish"
)

// The dashboard is one issue per repository that says what pinup holds and
// why, what it opened, and what it saw - and takes instructions back the
// way people already give them: a task-list checkbox. Ticking one edits the
// issue's description; the next run reads the description before planning,
// acts on the ticked boxes and writes the issue back with the boxes clear.
//
// The markers are the ones Renovate's dashboard carries in its description
// (`<!-- approve-branch=… -->`), a format people tick without reading, and
// one the shadow comparison can read off Renovate's issues as well. What
// pinup adds is the "Held" section: every held branch with its reason and
// its thaw time, which the plan knows and a dashboard should say.

// Checks are the boxes ticked on the dashboard since the last run.
type Checks struct {
	Approve    map[string]bool // approve-branch=<branch>: create a branch awaiting dashboard approval
	Unschedule map[string]bool // unschedule-branch=<branch>: create it outside its schedule
	ApprovePR  map[string]bool // approvePr-branch=<branch>: create it before its release age is reached
	Rebase     map[string]bool // rebase-branch=<branch>: push the branch again even if unchanged
	Retry      map[string]bool // retry-branch=<branch>: a failed branch, tried again with a fresh push
	ApproveAll bool
	CreateAll  bool
	RebaseAll  bool
}

// Lifted reports whether a hold of the given reason on the branch is lifted
// by a ticked box.
func (c Checks) Lifted(branch string, reason model.BlockReason) bool {
	switch reason {
	case model.BlockDashboardApproval:
		return c.ApproveAll || c.Approve[branch]
	case model.BlockSchedule:
		return c.CreateAll || c.Unschedule[branch]
	case model.BlockMinimumReleaseAge:
		return c.ApprovePR[branch]
	}
	return false
}

// Any reports whether anything was ticked.
func (c Checks) Any() bool {
	return c.ApproveAll || c.CreateAll || c.RebaseAll || len(c.Approve)+len(c.Unschedule)+len(c.ApprovePR)+len(c.Rebase)+len(c.Retry) > 0
}

var tickedRE = regexp.MustCompile(`(?m)^\s*- \[[xX]\] <!-- ([a-zA-Z-]+)(?:=(\S+?))? -->`)

// ParseChecks reads the ticked boxes out of a dashboard description.
func ParseChecks(description string) Checks {
	c := Checks{Approve: map[string]bool{}, Unschedule: map[string]bool{}, ApprovePR: map[string]bool{}, Rebase: map[string]bool{}, Retry: map[string]bool{}}
	for _, m := range tickedRE.FindAllStringSubmatch(description, -1) {
		switch m[1] {
		case "approve-branch":
			c.Approve[m[2]] = true
		case "unschedule-branch":
			c.Unschedule[m[2]] = true
		case "approvePr-branch":
			c.ApprovePR[m[2]] = true
		case "rebase-branch":
			c.Rebase[m[2]] = true
		case "retry-branch":
			c.Retry[m[2]] = true
		case "approve-all-pending-prs":
			c.ApproveAll = true
		case "create-all-awaiting-schedule-prs":
			c.CreateAll = true
		case "rebase-all-open-prs":
			c.RebaseAll = true
		}
	}
	return c
}

// BranchState is what the run did with one branch, as much as the dashboard
// shows: the action, the merge request and a message for failures.
type BranchState struct {
	Action  string // "created", "updated", "unchanged", "held", "failed"
	MRIID   int
	Message string
}

// Dashboard renders the issue body for a plan and what the run did with it.
// open are the merge requests under the prefix that are open now.
func Dashboard(plan *model.Plan, states map[string]BranchState, open []publish.MergeRequest, now time.Time) string {
	var b strings.Builder
	b.WriteString("This issue lists pinup updates and detected dependencies. A ticked box is read on the next run and cleared.\n\n")

	byMR := map[string]publish.MergeRequest{}
	for _, m := range open {
		byMR[m.SourceBranch] = m
	}
	updates := map[string]model.Update{}
	for _, u := range plan.Updates {
		updates[u.Key()] = u
	}
	type held struct {
		branch model.Branch
		block  model.Block
	}
	var pending, scheduled, aged, otherHeld []held
	var errored, opened, actionable []model.Branch
	for _, br := range plan.Branches {
		st, ran := states[br.Name]
		if m, ok := byMR[br.Name]; ok && br.SuppressedBy == "" {
			_ = m
			opened = append(opened, br)
			continue
		}
		if ran && st.Action == "failed" {
			errored = append(errored, br)
			continue
		}
		if br.SuppressedBy == "" {
			actionable = append(actionable, br)
			continue
		}
		blk := firstBlock(br, updates)
		switch br.SuppressedBy {
		case model.BlockDashboardApproval:
			pending = append(pending, held{br, blk})
		case model.BlockSchedule:
			scheduled = append(scheduled, held{br, blk})
		case model.BlockMinimumReleaseAge:
			aged = append(aged, held{br, blk})
		default:
			otherHeld = append(otherHeld, held{br, blk})
		}
	}

	if len(plan.Warnings) > 0 {
		b.WriteString("## Repository problems\n\n")
		n := 0
		for _, w := range plan.Warnings {
			if strings.Contains(w.Msg, "has no effect") {
				continue
			}
			n++
			if n > 20 {
				fmt.Fprintf(&b, " - … and %d more in the plan\n", len(plan.Warnings)-20)
				break
			}
			fmt.Fprintf(&b, " - ⚠️ %s%s\n", where(w), w.Msg)
		}
		b.WriteString("\n")
	}
	if len(pending) > 0 {
		b.WriteString("## Pending approval\n\nThese branches wait for approval. Tick a box to create the merge request on the next run.\n\n")
		for _, h := range pending {
			fmt.Fprintf(&b, " - [ ] <!-- approve-branch=%s -->%s\n", h.branch.Name, h.branch.Title)
		}
		b.WriteString(" - [ ] <!-- approve-all-pending-prs -->🔐 **Create all pending approval merge requests at once** 🔐\n\n")
	}
	if len(scheduled) > 0 {
		b.WriteString("## Awaiting schedule\n\nThese updates wait for their window. Tick a box to create the merge request on the next run regardless.\n\n")
		for _, h := range scheduled {
			until := ""
			if !h.block.Until.IsZero() {
				until = fmt.Sprintf(" (opens %s)", h.block.Until.UTC().Format("2006-01-02 15:04 UTC"))
			}
			fmt.Fprintf(&b, " - [ ] <!-- unschedule-branch=%s -->%s%s\n", h.branch.Name, h.branch.Title, until)
		}
		b.WriteString(" - [ ] <!-- create-all-awaiting-schedule-prs -->🔐 **Create all awaiting-schedule merge requests at once** 🔐\n\n")
	}
	if len(aged) > 0 {
		b.WriteString("## Awaiting release age\n\nThese releases are younger than the configured minimum age. Tick a box to create the merge request now.\n\n")
		for _, h := range aged {
			until := ""
			if !h.block.Until.IsZero() {
				until = fmt.Sprintf(" (old enough %s)", h.block.Until.UTC().Format("2006-01-02 15:04 UTC"))
			}
			fmt.Fprintf(&b, " - [ ] <!-- approvePr-branch=%s -->%s%s\n", h.branch.Name, h.branch.Title, until)
		}
		b.WriteString("\n")
	}
	if len(otherHeld) > 0 {
		b.WriteString("## Held\n\nThese branches are held for a reason no box lifts; the plan names it.\n\n")
		for _, h := range otherHeld {
			note := string(h.branch.SuppressedBy)
			if h.block.Note != "" {
				note += ": " + h.block.Note
			}
			fmt.Fprintf(&b, " - %s — %s\n", h.branch.Title, note)
		}
		b.WriteString("\n")
	}
	if len(errored) > 0 {
		b.WriteString("## Errored\n\nThese updates ran into an error and are retried next run. Tick a box to push the branch again from scratch.\n\n")
		for _, br := range errored {
			fmt.Fprintf(&b, " - [ ] <!-- retry-branch=%s -->%s — %s\n", br.Name, br.Title, states[br.Name].Message)
		}
		b.WriteString("\n")
	}
	if len(opened) > 0 {
		b.WriteString("## Open\n\nThese merge requests exist. Tick a box to rebase one on the next run.\n\n")
		for _, br := range opened {
			m := byMR[br.Name]
			fmt.Fprintf(&b, " - [ ] <!-- rebase-branch=%s -->[%s](!%d)\n", br.Name, br.Title, m.IID)
		}
		b.WriteString(" - [ ] <!-- rebase-all-open-prs -->**Rebase all open merge requests at once**\n\n")
	}
	if len(actionable) > 0 {
		b.WriteString("## Planned\n\nBranches this run planned and could not open here (a dry run, or a limit):\n\n")
		for _, br := range actionable {
			msg := ""
			if st, ok := states[br.Name]; ok {
				msg = " — " + st.Action
				if st.Message != "" {
					msg += ": " + st.Message
				}
			}
			fmt.Fprintf(&b, " - %s%s\n", br.Title, msg)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Detected dependencies\n\n")
	type fileDeps struct {
		manager, file string
		deps          []model.Dependency
	}
	groups := map[string]*fileDeps{}
	var order []string
	for _, d := range plan.Deps {
		k := d.Manager + "\x00" + d.File
		g, ok := groups[k]
		if !ok {
			g = &fileDeps{manager: d.Manager, file: d.File}
			groups[k] = g
			order = append(order, k)
		}
		g.deps = append(g.deps, d)
	}
	sort.Strings(order)
	byManager := map[string][]*fileDeps{}
	var managers []string
	for _, k := range order {
		g := groups[k]
		if _, ok := byManager[g.manager]; !ok {
			managers = append(managers, g.manager)
		}
		byManager[g.manager] = append(byManager[g.manager], g)
	}
	newest := map[string][]string{}
	for _, u := range plan.Updates {
		// One value once, however many entries share the key (the same
		// include read three times in one file).
		if !slices.Contains(newest[u.DepKey], u.NewValue) {
			newest[u.DepKey] = append(newest[u.DepKey], u.NewValue)
		}
	}
	for _, m := range managers {
		files := byManager[m]
		n := 0
		for _, f := range files {
			n += len(f.deps)
		}
		fmt.Fprintf(&b, "<details><summary>%s (%d)</summary>\n<blockquote>\n\n", m, n)
		for _, f := range files {
			fmt.Fprintf(&b, "<details><summary>%s (%d)</summary>\n\n", f.file, len(f.deps))
			for _, d := range f.deps {
				value := d.CurrentValue
				if d.CurrentDigest != "" && value != "" {
					value += "@" + short(d.CurrentDigest)
				} else if value == "" {
					value = short(d.CurrentDigest)
				}
				line := fmt.Sprintf(" - `%s %s`", d.DepName, value)
				if ups := newest[d.Key()]; len(ups) > 0 {
					line += " → [Updates: `" + strings.Join(ups, "`, `") + "`]"
				} else if d.SkipReason != "" && !strings.HasPrefix(d.SkipReason, "up to date") {
					line += " — " + d.SkipReason
				}
				b.WriteString(line + "\n")
			}
			b.WriteString("\n</details>\n\n")
		}
		b.WriteString("</blockquote>\n</details>\n\n")
	}
	fmt.Fprintf(&b, "---\n\n*pinup %s, %s*\n", plan.PinupVersion, now.UTC().Format("2006-01-02 15:04 UTC"))
	return b.String()
}

func firstBlock(br model.Branch, updates map[string]model.Update) model.Block {
	for _, k := range br.UpdateKeys {
		for _, blk := range updates[k].Blocks {
			if blk.Reason == br.SuppressedBy {
				return blk
			}
		}
	}
	return model.Block{Reason: br.SuppressedBy}
}

func where(w model.Warning) string {
	if w.File != "" {
		return w.File + ": "
	}
	return ""
}

func short(digest string) string {
	if i := strings.Index(digest, ":"); i >= 0 && len(digest) > i+8 {
		return digest[i+1 : i+8]
	}
	if len(digest) > 7 {
		return digest[:7]
	}
	return digest
}
