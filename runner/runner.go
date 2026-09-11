// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package runner executes a plan: for every branch that carries edits it
// checks out the branch, writes the edits, commits, pushes, and opens or
// updates the merge request.
//
// Layer 4. It receives the platform and the git checkout from wire and
// knows no implementation. Every step that fails is recorded on the plan
// as a warning against that branch and the run continues with the next
// one; a run fails as a whole only when nothing can be done at all.
//
// Order is the order a reviewer expects: the plan is complete before the
// first write, every write is to a branch never to the base, the push is
// leased on what the plan saw, and the merge request is touched last.
package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/apply"
	"git.ole-hartwig.eu/pinup/pinup/git"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/publish"
)

// Options is what one execution needs beyond the plan.
type Options struct {
	Repo     *git.Repo
	Remote   string // "origin"
	Base     string // the default branch, e.g. "main"
	Identity git.Identity
	Signing  git.Signing

	Platform publish.Platform
	Project  publish.Project

	// Labels are added to every merge request; the estate uses
	// ["renovate"] and its consumers filter on it.
	Labels []string
	// Footer is appended to every merge-request description.
	Footer string
	// HourlyLimit caps the merge requests created in one run; further
	// branches are held with Reason hourlyLimit and recorded, not dropped.
	// Zero means no cap.
	HourlyLimit int
	// ConcurrentLimit caps the merge requests open at once, counting the
	// ones already open under Prefix; further new branches are held with
	// Reason concurrentLimit. Zero means no cap.
	ConcurrentLimit int
	// Prefix is the branch prefix the open-request count looks at.
	Prefix string
	// Tasks runs a branch's tasks - lock refreshes and postUpgradeTasks -
	// on the checkout after its edits are written. nil means a branch with
	// tasks fails rather than being pushed without them.
	Tasks TaskRunner
	// Sleep waits between retries; nil means no wait (tests).
	Sleep func(time.Duration)

	Now time.Time
}

// Outcome is what happened to one branch.
type Outcome struct {
	Branch  string
	Action  string // "created", "updated", "unchanged", "held", "failed"
	MRIID   int
	SHA     string
	Message string
}

// Execute runs the plan's branches. It returns one outcome per branch and
// annotates the plan: ExistingBranch on each branch, a Block on updates of
// branches held by the hourly limit, warnings for failures.
func Execute(ctx context.Context, plan *model.Plan, o Options) ([]Outcome, error) {
	if o.Repo == nil || o.Platform == nil {
		return nil, fmt.Errorf("runner: a repository and a platform are required")
	}
	if o.Remote == "" {
		o.Remote = "origin"
	}
	var outcomes []Outcome
	created := 0
	openNow := 0
	if o.ConcurrentLimit > 0 {
		prefix := o.Prefix
		if prefix == "" {
			prefix = "renovate/"
		}
		open, err := o.Platform.OpenMergeRequests(ctx, o.Project, prefix)
		if err != nil {
			return nil, fmt.Errorf("runner: counting open merge requests: %w", err)
		}
		openNow = len(open)
	}
	for i := range plan.Branches {
		b := &plan.Branches[i]
		// A branch with neither edits nor tasks has nothing to write; a
		// lock-file maintenance branch has only tasks and still writes.
		if b.SuppressedBy != "" || (len(b.Edits) == 0 && len(b.Tasks) == 0) {
			continue
		}
		mr, hasMR, err := o.Platform.FindMergeRequest(ctx, o.Project, b.Name)
		if err != nil {
			outcomes = append(outcomes, fail(plan, b, "find merge request", err))
			continue
		}
		if !hasMR && o.ConcurrentLimit > 0 && openNow+created >= o.ConcurrentLimit {
			hold(plan, b, model.Block{
				Reason: model.BlockConcurrentLimit, Org: model.Origin{Source: "config", Rule: model.NoRule},
				Note: fmt.Sprintf("prConcurrentLimit %d: %d merge requests already open", o.ConcurrentLimit, openNow+created),
			})
			outcomes = append(outcomes, Outcome{Branch: b.Name, Action: "held", Message: "concurrent limit"})
			continue
		}
		if !hasMR && o.HourlyLimit > 0 && created >= o.HourlyLimit {
			hold(plan, b, model.Block{
				Reason: model.BlockHourlyLimit, Org: model.Origin{Source: "config", Rule: model.NoRule},
				Until: o.Now.Add(time.Hour).UTC(),
				Note:  fmt.Sprintf("prHourlyLimit %d reached in this run", o.HourlyLimit),
			})
			outcomes = append(outcomes, Outcome{Branch: b.Name, Action: "held", Message: "hourly limit"})
			continue
		}

		sha, pushed, err := pushBranch(ctx, o, b)
		if err != nil {
			outcomes = append(outcomes, fail(plan, b, "push", err))
			continue
		}
		if sha == "" && !pushed {
			// Nothing was pushed and no branch exists: a task-only branch
			// whose tool found nothing to refresh. Nothing to open.
			outcomes = append(outcomes, Outcome{Branch: b.Name, Action: "unchanged", Message: "nothing to refresh"})
			continue
		}
		req := publish.Request{
			SourceBranch: b.Name, TargetBranch: o.Base, Title: b.Title,
			Description: description(b, o.Footer), Labels: union(o.Labels, b.Labels),
			Automerge: b.Automerge, RemoveSourceBranch: true,
		}
		var out Outcome
		switch {
		case hasMR:
			updated, changed, err := o.Platform.UpdateMergeRequest(ctx, o.Project, mr.IID, req)
			if err != nil {
				outcomes = append(outcomes, fail(plan, b, "update merge request", err))
				continue
			}
			mr = updated
			action := "unchanged"
			if pushed || len(changed) > 0 {
				action = "updated"
			}
			out = Outcome{Branch: b.Name, Action: action, MRIID: mr.IID, SHA: sha, Message: fmt.Sprint(changed)}
		default:
			mr, err = createWithRetry(ctx, o, req)
			if err != nil {
				outcomes = append(outcomes, fail(plan, b, "create merge request", err))
				continue
			}
			created++
			out = Outcome{Branch: b.Name, Action: "created", MRIID: mr.IID, SHA: sha}
		}
		b.Existing = &model.ExistingBranch{Name: b.Name, SHA: sha, MRIID: mr.IID, MRState: mr.State}
		outcomes = append(outcomes, out)
	}
	return outcomes, nil
}

// createWithRetry opens the merge request, retrying a 400 that says the
// source branch does not exist: measured live, GitLab answered that for a
// branch pushed a moment earlier and accepted the same request seconds
// later. Three attempts, a second apart, then the error stands.
func createWithRetry(ctx context.Context, o Options, req publish.Request) (publish.MergeRequest, error) {
	var last error
	for attempt := range 3 {
		if attempt > 0 {
			if o.Sleep != nil {
				o.Sleep(time.Duration(attempt) * time.Second)
			}
		}
		mr, err := o.Platform.CreateMergeRequest(ctx, o.Project, req)
		if err == nil {
			return mr, nil
		}
		last = err
		if !strings.Contains(err.Error(), "does not exist") {
			break
		}
	}
	return publish.MergeRequest{}, last
}

// pushBranch rebuilds the branch from the base, writes the edits, commits,
// and pushes when the result differs from what the remote holds. It
// returns the branch head and whether anything was pushed.
//
// The branch is rebuilt rather than continued because the plan's edits are
// relative to the base: applying them on top of last run's branch would
// find the bytes already changed. A branch that carries a person's
// commits is not rebuilt - it is theirs now - and the run says so.
func pushBranch(ctx context.Context, o Options, b *model.Branch) (string, bool, error) {
	before, existed, err := o.Repo.RemoteBranch(ctx, o.Remote, b.Name)
	if err != nil {
		return "", false, err
	}
	base := o.Remote + "/" + o.Base
	if existed {
		if err := o.Repo.Fetch(ctx, o.Remote, b.Name); err != nil {
			return "", false, err
		}
		foreign, err := o.Repo.ForeignAuthors(ctx, base, o.Remote+"/"+b.Name, o.Identity)
		if err != nil {
			return "", false, err
		}
		if len(foreign) > 0 {
			return "", false, fmt.Errorf("branch carries commits by %s; leaving it to them", strings.Join(foreign, ", "))
		}
	}
	if err := o.Repo.Recreate(ctx, b.Name, base); err != nil {
		return "", false, err
	}
	if _, err := apply.WriteFiles(o.Repo.Dir, b.Edits); err != nil {
		return "", false, err
	}
	files := map[string]bool{}
	var paths []string
	for _, e := range b.Edits {
		if !files[e.File] {
			files[e.File] = true
			paths = append(paths, e.File)
		}
	}
	// Tasks run on the edited tree, one after the other; what each one
	// changes must lie within its file scope, or everything it did is
	// discarded and the branch fails by name. What survives is committed
	// together with the edits: a manifest never lands without its lock.
	for _, t := range b.Tasks {
		changed, err := o.runTask(ctx, t)
		if err != nil {
			_ = o.Repo.Discard(ctx)
			return "", false, err
		}
		for _, p := range changed {
			if !files[p] {
				files[p] = true
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		// A task-only branch whose tool found nothing to refresh: not a
		// failure, there is simply nothing to open.
		return before, false, nil
	}
	sha, committed, err := o.Repo.Commit(ctx, o.Identity, o.Signing, b.Title+"\n\n"+commitBody(b), paths...)
	if err != nil {
		return "", false, err
	}
	if !committed {
		return "", false, fmt.Errorf("the edits changed nothing against %s", base)
	}
	if existed {
		same, err := o.Repo.SameTree(ctx, sha, o.Remote+"/"+b.Name)
		if err != nil {
			return "", false, err
		}
		if same {
			// Last run's branch already holds these bytes on the current
			// base; nothing to push.
			return before, false, nil
		}
	}
	lease := ""
	if existed {
		lease = before
	}
	if err := o.Repo.Push(ctx, o.Remote, b.Name, lease); err != nil {
		return "", false, err
	}
	return sha, true, nil
}

// TaskRunner executes a branch's tasks on the checkout. wire supplies the
// exec flavour; Available answers before anything runs.
type TaskRunner interface {
	Available(tasks []model.Task) error
	Run(ctx context.Context, root string, t model.Task) error
}

// runTask executes one task and reports the paths it changed, refusing a
// change outside the task's scope. A task without a runner is a failed
// branch, not a pushed one.
func (o Options) runTask(ctx context.Context, t model.Task) ([]string, error) {
	if o.Tasks == nil {
		return nil, fmt.Errorf("no task runner for %s", strings.Join(t.Command, " "))
	}
	if err := o.Tasks.Available([]model.Task{t}); err != nil {
		return nil, err
	}
	before, err := o.Repo.Changed(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.Tasks.Run(ctx, o.Repo.Dir, t); err != nil {
		return nil, err
	}
	after, err := o.Repo.Changed(ctx)
	if err != nil {
		return nil, err
	}
	was := map[string]bool{}
	for _, p := range before {
		was[p] = true
	}
	var changed []string
	for _, p := range after {
		if !was[p] {
			changed = append(changed, p)
		}
	}
	if err := apply.InScope(t, changed); err != nil {
		return nil, err
	}
	return changed, nil
}

func commitBody(b *model.Branch) string {
	return "Refs: RENOVATE\n\nUpdate-Type: " + updateTypesOf(b)
}

func updateTypesOf(b *model.Branch) string {
	if b.GroupName != "" {
		return "group"
	}
	return "update"
}

// description is the merge-request body: what changes, why anything is
// held, and the footer.
func description(b *model.Branch, footer string) string {
	s := "| File | Change |\n|---|---|\n"
	for _, e := range b.Edits {
		s += fmt.Sprintf("| `%s` | `%s` → `%s` |\n", e.File, e.Old, e.New)
	}
	for _, t := range b.Tasks {
		// A reader sees what ran on the branch beyond the edits - the
		// lock refresh behind a manifest change - and can rerun it.
		s += fmt.Sprintf("| `%s` | `%s` |\n", strings.Join(t.FileFilters, "`, `"), strings.Join(t.Command, " "))
	}
	if footer != "" {
		s += "\n" + footer + "\n"
	}
	return s
}

func fail(plan *model.Plan, b *model.Branch, step string, err error) Outcome {
	plan.Warnings = append(plan.Warnings, model.Warning{Stage: "publish", Msg: fmt.Sprintf("%s: %s: %v", b.Name, step, err)})
	return Outcome{Branch: b.Name, Action: "failed", Message: step + ": " + err.Error()}
}

// hold records a block on every update of the branch and drops its edits,
// so the plan says the branch was held and why, and nothing writes it.
func hold(plan *model.Plan, b *model.Branch, block model.Block) {
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	for i := range plan.Updates {
		if keys[plan.Updates[i].DepKey] {
			plan.Updates[i].Blocks = append(plan.Updates[i].Blocks, block)
			if plan.Updates[i].SuppressedBy == "" {
				plan.Updates[i].SuppressedBy = block.Reason
			}
		}
	}
	b.Edits = nil
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
