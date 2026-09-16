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
	"sort"
	"strings"
	"time"

	"github.com/ohartwig/pinup/apply"
	"github.com/ohartwig/pinup/git"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/planner"
	"github.com/ohartwig/pinup/publish"
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
	// Rebase names the branches to push again even when the rebuilt tree
	// equals what the remote holds - the dashboard's rebase and retry
	// boxes.
	Rebase map[string]bool

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
	// Every branch is built on the base and left behind; the checkout goes
	// back to where it was, so a second run over the same directory plans
	// from the tree it started with, not from the last branch pushed.
	// An empty repository (a project awaiting deletion, measured on the
	// live partition 2026-09-13) has no HEAD and nothing to return to.
	if start, err := o.Repo.Where(ctx); err == nil {
		defer func() { _ = o.Repo.Return(ctx, start) }()
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
			// whose tool found nothing to refresh. Nothing to open, and
			// the plan says why.
			hold(plan, b, model.Block{
				Reason: model.BlockNothingToRefresh, Org: model.Origin{Source: "config", Rule: model.NoRule},
				Note: "the lock file is current; the refresh changed nothing",
			})
			outcomes = append(outcomes, Outcome{Branch: b.Name, Action: "unchanged", Message: "nothing to refresh"})
			continue
		}
		footer := o.Footer
		automerge := b.Automerge
		if automerge && !hasMR {
			// The same bytes merged before and gone from the base again
			// is a revert; it does not land a second time on its own.
			// Renovate withholds automerge on the title alone, which
			// held a security bump for two days because an earlier bump
			// of the same module in other files carried the same title
			// (devops/wolfi-packages!404, 2026-09-14). The comparison
			// here is the edits themselves.
			if prior, ok := mergedBefore(ctx, o, b, plan.Updates); ok {
				automerge = false
				note := fmt.Sprintf("Automerge withheld: !%d merged exactly these edits before and the base no longer carries them; merge by hand if the revert is over.", prior)
				footer = note + "\n\n" + footer
				plan.Warnings = append(plan.Warnings, model.Warning{Stage: "publish", Msg: fmt.Sprintf("%s: automerge withheld, !%d merged the same edits before", b.Name, prior)})
			}
		}
		req := publish.Request{
			SourceBranch: b.Name, TargetBranch: o.Base, Title: b.Title,
			Description: description(b, plan.Updates, footer), Labels: union(o.Labels, b.Labels),
			Automerge: automerge, RemoveSourceBranch: true,
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
		if mr.AutomergeRefused != "" {
			// The request is open; only the merge is somebody else's. Said
			// once per run, on the branch, not as a failure that would
			// re-create nothing.
			plan.Warnings = append(plan.Warnings, model.Warning{Stage: "publish", Msg: fmt.Sprintf("%s: %s", b.Name, mr.AutomergeRefused)})
			out.Message = strings.TrimSpace(out.Message + " " + mr.AutomergeRefused)
		}
		b.Existing = &model.ExistingBranch{Name: b.Name, SHA: sha, MRIID: mr.IID, MRState: mr.State}
		outcomes = append(outcomes, out)
	}
	return outcomes, nil
}

// mergedBefore reports the newest merged request on the branch whose
// edits - the "| File | Change |" rows of its description - are exactly
// this branch's. That is a change that landed and was taken back; a bump
// that merely shares the title (the same module and version in other
// files) is not.
func mergedBefore(ctx context.Context, o Options, b *model.Branch, updates []model.Update) (int, bool) {
	history, err := o.Platform.History(ctx, o.Project, b.Name)
	if err != nil || len(history) == 0 {
		return 0, false
	}
	mine := editRows(description(b, updates, ""))
	if len(mine) == 0 {
		return 0, false
	}
	for _, m := range history {
		if m.State != "merged" {
			continue
		}
		if theirs := editRows(m.Description); len(theirs) == len(mine) && theirs == mine {
			return m.IID, true
		}
	}
	return 0, false
}

// editRows is the set of "| `file` | `old` → `new` |" rows of a
// description, sorted and joined - the fingerprint of what the branch
// changes.
func editRows(description string) string {
	var rows []string
	for _, line := range strings.Split(description, "\n") {
		// The file table's rows have two cells; the updates table's rows
		// carry a release count that moves between runs.
		if strings.HasPrefix(line, "| `") && strings.Contains(line, "` → `") && strings.Count(line, "|") == 3 {
			rows = append(rows, line)
		}
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// createWithRetry opens the merge request, retrying a 400 that says the
// source branch does not exist: measured live, GitLab answered that for a
// branch pushed a moment earlier and accepted the same request seconds
// later. Three attempts a second apart were not enough on the first live
// hour of forty repositories, eight at a time (2026-09-13, 20:33: five
// requests failed, every branch there): six attempts, doubling from two
// seconds, a minute in all, then the error stands and the next run opens
// it.
func createWithRetry(ctx context.Context, o Options, req publish.Request) (publish.MergeRequest, error) {
	var last error
	for attempt := range 6 {
		if attempt > 0 {
			if o.Sleep != nil {
				o.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
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
	// The type was decided on the planned edits; a task may have touched
	// more. Decided again on what is committed: a ci branch whose task
	// wrote outside the pipeline is a chore after all, and releases.
	if retitled := planner.CommitTypeFor(b.Title, paths); retitled != b.Title {
		b.Title = retitled
	}
	sha, committed, err := o.Repo.Commit(ctx, o.Identity, o.Signing, b.Title+"\n\n"+commitBody(b), paths...)
	if err != nil {
		return "", false, err
	}
	if !committed {
		return "", false, fmt.Errorf("the edits changed nothing against %s", base)
	}
	if existed && !o.Rebase[b.Name] {
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
// maxNotesPerUpdate and maxNotesShown cap the release sections in one
// description: a dependency thirty-five patch releases behind is a
// compare link after its ten newest, and a group of many members is a
// compare link per member beyond forty sections in all.
const (
	maxNotesPerUpdate = 10
	maxNotesShown     = 40
)

// description is the merge request's body: what moves, from where to
// where and with what notes; the bytes that change; the tasks that ran;
// what the configuration's author wrote for the reader (prBodyNotes);
// then the release notes the forge publishes for the span, each collapsed,
// and the footer. updates are the plan's; only the branch's own are read,
// and a dependency read from several places is one row.
func description(b *model.Branch, updates []model.Update, footer string) string {
	var s strings.Builder
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	var members []model.Update
	seen := map[string]bool{}
	for _, u := range updates {
		row := u.Dep.DepName + "\x00" + change(u)
		if !keys[u.Key()] || seen[row] {
			continue
		}
		seen[row] = true
		members = append(members, u)
	}
	if len(members) > 0 {
		s.WriteString("| Dependency | Update | Change | Notes |\n|---|---|---|---|\n")
		for _, u := range members {
			fmt.Fprintf(&s, "| `%s` | %s | %s | %s |\n", u.Dep.DepName, u.Type.Renovate(), change(u), noteLinks(u))
		}
		s.WriteString("\n")
	}
	s.WriteString("| File | Change |\n|---|---|\n")
	for _, e := range b.Edits {
		fmt.Fprintf(&s, "| `%s` | `%s` → `%s` |\n", e.File, e.Old, e.New)
	}
	for _, t := range b.Tasks {
		// A reader sees what ran on the branch beyond the edits - the
		// lock refresh behind a manifest change - and can rerun it.
		fmt.Fprintf(&s, "| `%s` | `%s` |\n", strings.Join(t.FileFilters, "`, `"), strings.Join(t.Command, " "))
	}
	if b.Body != "" {
		s.WriteString("\n---\n\n" + Sanitize(b.Body) + "\n")
	}
	shown := 0
	for _, u := range members {
		for i, n := range u.Notes {
			if shown == maxNotesShown || i == maxNotesPerUpdate {
				more := len(u.Notes) - i
				if u.CompareURL != "" {
					fmt.Fprintf(&s, "\n*… and %d more releases of `%s`: [compare](%s).*\n", more, u.Dep.DepName, u.CompareURL)
				} else {
					fmt.Fprintf(&s, "\n*… and %d more releases of `%s`.*\n", more, u.Dep.DepName)
				}
				break
			}
			shown++
			head := u.Dep.DepName + " " + n.Version
			if t := n.Title; t != "" && t != n.Version && t != strings.TrimPrefix(n.Version, "v") {
				head += ": " + t
			}
			// The summary is HTML: escape it, then break references in the
			// escaped text (the sanitiser's entities are HTML too).
			head = Sanitize(escapeHTML(head))
			if n.URL != "" {
				head = fmt.Sprintf("<a href=\"%s\">%s</a>", escapeHTML(n.URL), head)
			}
			body := Sanitize(strings.TrimSpace(n.Body))
			if body == "" {
				body = "*(no notes)*"
			}
			fmt.Fprintf(&s, "\n<details>\n<summary>%s</summary>\n\n%s\n\n</details>\n", head, body)
		}
	}
	if footer != "" {
		s.WriteString("\n" + footer + "\n")
	}
	return s.String()
}

// change is the "from → to" cell of an update.
func change(u model.Update) string {
	from, to := u.Dep.CurrentValue, u.NewValue
	switch {
	case u.Type == model.UpdateLockFileMaintenance:
		return "lock file refresh"
	case u.Type == model.UpdateDigest || u.Type == model.UpdatePinDigest:
		if u.Dep.CurrentDigest != "" {
			from = short(u.Dep.CurrentDigest)
		}
		to = short(u.NewDigest)
		if u.Type == model.UpdatePinDigest {
			from = u.Dep.CurrentValue
			to = u.NewValue + "@" + to
		}
	case u.Dep.LockedVersion != "" && u.Dep.LockedVersion != from:
		from += " (" + u.Dep.LockedVersion + ")"
	}
	if from == "" {
		return "`" + to + "`"
	}
	return "`" + from + "` → `" + to + "`"
}

// noteLinks is the Notes cell: the forge's compare page and the release
// count, or the source alone, or nothing.
func noteLinks(u model.Update) string {
	var parts []string
	if u.CompareURL != "" {
		parts = append(parts, "[compare]("+u.CompareURL+")")
	} else if u.Dep.SourceURL != "" {
		parts = append(parts, "[source]("+u.Dep.SourceURL+")")
	}
	switch n := len(u.Notes); {
	case n == 1:
		parts = append(parts, "1 release")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d releases", n))
	}
	if u.SecurityFix {
		parts = append(parts, "security fix")
	}
	return strings.Join(parts, ", ")
}

func short(digest string) string {
	if i := strings.Index(digest, ":"); i >= 0 && len(digest) > i+13 {
		return digest[:i+13]
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
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
		if keys[plan.Updates[i].Key()] {
			plan.Updates[i].Blocks = append(plan.Updates[i].Blocks, block)
			if plan.Updates[i].SuppressedBy == "" {
				plan.Updates[i].SuppressedBy = block.Reason
			}
		}
	}
	b.Edits = nil
	// The branch says so too: the shadow comparator buckets on the
	// branch's reason, and a limit-held branch without one read as a
	// branch pinup plans and nobody opens (measured 2026-09-15: four in
	// koh-gitops, red for the second run running).
	if b.SuppressedBy == "" {
		b.SuppressedBy = block.Reason
	}
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
