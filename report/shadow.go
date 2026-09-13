// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

// The shadow comparison: pinup's plans against the merge requests Renovate
// has open, joined on the branch name - legitimate because Renovate-
// compatible branch naming is a stated invariant. Every branch pinup plans
// carries suppressedBy when it is held, and Renovate's set contains nothing
// that is held, so a held branch without a merge request is agreement, not
// a difference.
//
// Six mechanisms keep this from becoming the next dead gate (docs/plan.md
// section 6.3): denominators are asserted and printed, the other side's
// readability is asserted rather than its emptiness, a permanent stale-pin
// control must yield exactly one only-pinup entry, suppressions expire and
// may not name a branch, and a plan from a different pinup version is
// refused. This file implements the comparison and those rules; the job
// that runs it has no allow_failure.

// Entry is one branch on one side or both.
type Entry struct {
	Project string `json:"project"`
	Branch  string `json:"branch"`
	// Side is "both", "only_pinup" or "only_renovate".
	Side string `json:"side"`
	// SuppressedBy is pinup's reason when it holds the branch.
	SuppressedBy model.BlockReason `json:"suppressedBy,omitempty"`
	Title        string            `json:"title,omitempty"`
	MRIID        int               `json:"mrIid,omitempty"`
	// Suppressed names the suppression that triaged this entry, if any.
	Suppressed string `json:"suppressed,omitempty"`
}

// Suppression triages one only-pinup entry as a known difference. Every
// one has a reason, an owner and an expiry; none may match by branch name
// alone, which would be a way to make any difference disappear.
type Suppression struct {
	Project string    `json:"project"`
	Branch  string    `json:"branch"`
	Kind    string    `json:"kind"` // "fixture-control", "pre-declared-improvement", "defect", "stale-renovate"
	Reason  string    `json:"reason"`
	Owner   string    `json:"owner"`
	Expires time.Time `json:"expires"`
}

// Suppressions is the file the comparator reads.
type Suppressions struct {
	Entries []Suppression `json:"suppressions"`
}

// LoadSuppressions reads the file; a missing file is no suppressions.
func LoadSuppressions(path string) (*Suppressions, error) {
	if path == "" {
		return &Suppressions{}, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Suppressions{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Suppressions
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Result is the whole comparison.
type Result struct {
	Plans        int `json:"plans"`
	Deps         int `json:"deps"`
	Both         int `json:"both"`
	OnlyPinup    int `json:"onlyPinup"`
	OnlyRenovate int `json:"onlyRenovate"`
	Held         int `json:"held"`
	// HeldOpen counts branches Renovate has open that pinup holds: the
	// other tool acted, this one would not, and the reason is on the entry.
	HeldOpen int `json:"heldOpen"`
	// RollingMajor counts the held_open entries pinup holds as a rolling
	// major: Renovate, once a human approved it on the dashboard, writes
	// the new major into a `@N` pin; pinup reports it and never will. The
	// difference is by design and pre-declared, so it is not a failure.
	RollingMajor int `json:"rollingMajor"`
	// Persisting counts the held_open entries whose hold is one Renovate
	// does not re-decide once its merge request exists: a schedule window
	// (Renovate opened it inside the window, pinup re-decides every hour),
	// a release age (pinup's first-seen rule is stricter by design), or a
	// dashboard approval (given once, on Renovate's dashboard). Each is
	// pre-declared; the runner's live mode adopts such a request rather
	// than closing it.
	Persisting int `json:"persisting"`
	// Pending counts the only_pinup and only_renovate entries seen for the
	// first time: the two tools run half an hour apart, and a difference
	// that one run later is gone was timing, not behaviour. A difference
	// fails only when the previous run saw it too.
	Pending    int `json:"pending"`
	Suppressed int `json:"suppressed"`
	// Merged counts branches pinup plans that Renovate opened and had
	// merged inside the look-back window.
	Merged   int     `json:"merged"`
	Controls int     `json:"controls"`
	Entries  []Entry `json:"entries"`
	// Failures are the reasons the comparison does not pass; empty means
	// agreement within the rules.
	Failures []string `json:"failures"`
}

// State is what one comparison leaves for the next: the differences it
// saw, so the next run can tell a persisting one from a timing one.
type State struct {
	// Seen holds "side|project|branch" of every only_pinup and
	// only_renovate entry of the last comparison.
	Seen []string `json:"seen"`
}

// LoadState reads the previous comparison's state; a missing file is an
// empty state, which makes every difference pending on the first run.
func LoadState(path string) (*State, error) {
	if path == "" {
		return &State{}, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Save writes the state for the next run.
func (st *State) Save(path string) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// persistingHold is a hold Renovate does not re-decide once its merge
// request exists; see Result.Persisting.
func persistingHold(reason model.BlockReason) bool {
	switch reason {
	case model.BlockSchedule, model.BlockMinimumReleaseAge, model.BlockDashboardApproval:
		return true
	}
	return false
}

// Compare joins plans with the open merge requests per project. controls
// names the projects that must yield exactly one only-pinup entry each.
// version is the pinup version the comparing job runs; a plan from another
// is refused. prev is the previous comparison's state; nil means none.
func Compare(plans []*model.Plan, open map[string][]publish.MergeRequest, sup *Suppressions, controls []string, version string, now time.Time, prev *State) (Result, *State) {
	c := &comparison{sup: sup, now: now, version: version, next: &State{},
		seenBefore: map[string]bool{}, used: map[string]bool{}, onlyPinupByProject: map[string]int{}, isControl: map[string]bool{}}
	if prev != nil {
		for _, k := range prev.Seen {
			c.seenBefore[k] = true
		}
	}
	for _, ctl := range controls {
		c.isControl[ctl] = true
	}
	c.r.Plans = len(plans)
	if len(plans) == 0 {
		c.fail("zero plans: nothing was compared")
	}
	c.checkSuppressions()
	for _, p := range plans {
		c.plan(p, open)
	}
	c.verdict(controls)
	sort.Strings(c.next.Seen)
	return c.r, c.next
}

// comparison is one run of the comparator: the counters as they fill,
// what the previous run saw, which suppressions were used.
type comparison struct {
	r       Result
	sup     *Suppressions
	now     time.Time
	version string
	next    *State
	// seenBefore holds last run's only_* keys: a difference seen twice
	// running is a failure, once is pending.
	seenBefore         map[string]bool
	used               map[string]bool
	onlyPinupByProject map[string]int
	isControl          map[string]bool
}

func (c *comparison) fail(format string, args ...any) {
	c.r.Failures = append(c.r.Failures, fmt.Sprintf(format, args...))
}

// checkSuppressions refuses a suppression without reason, owner and
// expiry, an expired one, and one that names no project and branch.
func (c *comparison) checkSuppressions() {
	for _, s := range c.sup.Entries {
		switch {
		case s.Reason == "" || s.Owner == "" || s.Expires.IsZero():
			c.fail("suppression %s/%s has no reason, owner or expiry", s.Project, s.Branch)
		case !c.now.Before(s.Expires):
			c.fail("suppression %s/%s expired on %s", s.Project, s.Branch, s.Expires.Format("2006-01-02"))
		case s.Project == "" || s.Branch == "":
			c.fail("a suppression must name a project and a branch, never a bare branch pattern")
		}
	}
}

// plan compares one repository's plan with Renovate's merge requests
// there: every branch of the plan against theirs, then every request of
// theirs the plan does not name.
func (c *comparison) plan(p *model.Plan, open map[string][]publish.MergeRequest) {
	if p.PinupVersion != c.version {
		c.fail("%s: plan from pinup %s, this job runs %s; a stale artefact is not compared", p.Repo.Path, p.PinupVersion, c.version)
		return
	}
	c.r.Deps += len(p.Deps)
	mrs, ok := open[p.Repo.Path]
	if !ok {
		c.fail("%s: Renovate's side could not be read; an unreadable Renovate is not an empty Renovate", p.Repo.Path)
		return
	}
	theirs := map[string]publish.MergeRequest{}
	merged := map[string]publish.MergeRequest{}
	for _, m := range mrs {
		if m.State == "merged" {
			merged[m.SourceBranch] = m
			continue
		}
		theirs[m.SourceBranch] = m
	}
	mine := map[string]model.Branch{}
	for _, b := range p.Branches {
		mine[b.Name] = b
	}
	for name, b := range mine {
		c.r.Entries = append(c.r.Entries, c.branch(p.Repo.Path, name, b, theirs, merged))
	}
	for name, m := range theirs {
		if _, ok := mine[name]; ok {
			continue
		}
		c.r.Entries = append(c.r.Entries, c.theirsOnly(p.Repo.Path, name, m))
	}
}

// branch classifies one branch of the plan.
func (c *comparison) branch(project, name string, b model.Branch, theirs, merged map[string]publish.MergeRequest) Entry {
	e := Entry{Project: project, Branch: name, SuppressedBy: b.SuppressedBy, Title: b.Title}
	m, ok := theirs[name]
	switch {
	case ok && b.SuppressedBy == model.BlockRollingMajor:
		e.Side, e.MRIID, e.Suppressed = "held_open", m.IID, "rolling-major"
		c.r.RollingMajor++
	case ok && persistingHold(b.SuppressedBy):
		e.Side, e.MRIID, e.Suppressed = "held_open", m.IID, "persisting"
		c.r.Persisting++
	case ok && b.SuppressedBy != "":
		e.Side, e.MRIID = "held_open", m.IID
		c.r.HeldOpen++
	case ok:
		e.Side, e.MRIID = "both", m.IID
		c.r.Both++
	default:
		if m, done := merged[name]; done && b.SuppressedBy == "" {
			// Renovate opened it and had it merged since the window
			// this comparison looks back over; the plan here was made
			// before, or cannot see the outcome - a lock refresh
			// leaves no trace a plan reads (measured: Renovate's
			// lock-file-maintenance merges at 01:2x and pinup plans
			// it again every hour of the window). The same branch,
			// one lifecycle further along.
			e.Side, e.MRIID, e.Suppressed = "both", m.IID, "merged"
			c.r.Merged++
			return e
		}
		if b.SuppressedBy != "" {
			e.Side = "both" // held here, absent there: what a hold means
			c.r.Held++
			return e
		}
		e.Side = "only_pinup"
		switch key := suppressionFor(c.sup, project, name, c.now); {
		case key != "":
			e.Suppressed = key
			c.used[key] = true
			c.r.Suppressed++
		case c.isControl[project]:
			// The control is supposed to differ; its count is
			// checked in the verdict, exactly one.
			e.Suppressed = "control"
			c.r.Controls++
		default:
			k := "only_pinup|" + project + "|" + name
			c.next.Seen = append(c.next.Seen, k)
			if c.seenBefore[k] {
				c.r.OnlyPinup++
			} else {
				e.Suppressed = "pending"
				c.r.Pending++
			}
		}
		c.onlyPinupByProject[project]++
	}
	return e
}

// theirsOnly classifies a merge request Renovate has open that the plan
// does not name.
func (c *comparison) theirsOnly(project, name string, m publish.MergeRequest) Entry {
	e := Entry{Project: project, Branch: name, Side: "only_renovate", Title: m.Title, MRIID: m.IID}
	k := "only_renovate|" + project + "|" + name
	c.next.Seen = append(c.next.Seen, k)
	switch key := suppressionFor(c.sup, project, name, c.now); {
	case key != "":
		// A merge request Renovate left behind - its change
		// already on main, the branch never closed - is
		// Renovate's difference, triaged like pinup's own, with
		// the same expiry. Measured: devops/images/c2patool!92
		// pins container-scanning 8.6.34, main pins 8.6.35.
		e.Suppressed = key
		c.used[key] = true
		c.r.Suppressed++
	case c.seenBefore[k]:
		c.r.OnlyRenovate++
	default:
		e.Suppressed = "pending"
		c.r.Pending++
	}
	return e
}

// verdict sorts the entries and turns the counters into failures: dead
// suppressions, a control that did not differ exactly once, and every
// difference seen for the second run running.
func (c *comparison) verdict(controls []string) {
	r := &c.r
	sort.Slice(r.Entries, func(i, j int) bool {
		if r.Entries[i].Project != r.Entries[j].Project {
			return r.Entries[i].Project < r.Entries[j].Project
		}
		return r.Entries[i].Branch < r.Entries[j].Branch
	})
	if r.Plans > 0 && r.Deps == 0 {
		c.fail("zero dependencies across every plan: the runs extracted nothing")
	}
	for _, s := range c.sup.Entries {
		if !c.used[s.Project+"/"+s.Branch] && s.Kind != "fixture-control" {
			c.fail("suppression %s/%s matched nothing; a dead suppression is removed, not kept", s.Project, s.Branch)
		}
	}
	for _, ctl := range controls {
		if c.onlyPinupByProject[ctl] != 1 {
			c.fail("control %s yielded %d only-pinup entries, want exactly 1", ctl, c.onlyPinupByProject[ctl])
		}
	}
	if r.OnlyRenovate > 0 {
		c.fail("%d branches Renovate has open that pinup does not plan, for the second run running: a miss is an update that does not happen", r.OnlyRenovate)
	}
	if r.HeldOpen > 0 {
		c.fail("%d branches Renovate has open that pinup holds; the reasons are on the entries", r.HeldOpen)
	}
	if r.OnlyPinup > 0 {
		c.fail("%d branches pinup plans without a merge request, for the second run running, and without a triaged suppression", r.OnlyPinup)
	}
}

func suppressionFor(sup *Suppressions, project, branch string, now time.Time) string {
	for _, s := range sup.Entries {
		if s.Project == project && s.Branch == branch && now.Before(s.Expires) {
			return s.Project + "/" + s.Branch
		}
	}
	return ""
}

// Summary is the one line a job log needs: matched over total, and the
// buckets.
func (r Result) Summary() string {
	total := r.Both + r.Merged + r.Held + r.HeldOpen + r.RollingMajor + r.Persisting + r.Pending + r.OnlyPinup + r.OnlyRenovate + r.Suppressed + r.Controls
	return fmt.Sprintf("shadow: %d plans, %d deps, matched %d/%d (merged %d, held %d, suppressed %d, controls %d, rolling_major %d, persisting %d, pending %d, held_open %d, only_pinup %d, only_renovate %d)",
		r.Plans, r.Deps, r.Both+r.Merged+r.Held+r.Suppressed+r.Controls+r.RollingMajor+r.Persisting, total, r.Merged, r.Held, r.Suppressed, r.Controls, r.RollingMajor, r.Persisting, r.Pending, r.HeldOpen, r.OnlyPinup, r.OnlyRenovate)
}

// Passed reports whether the comparison has no failure.
func (r Result) Passed() bool { return len(r.Failures) == 0 }

// String renders the entries, one per line.
func (r Result) String() string {
	var b strings.Builder
	for _, e := range r.Entries {
		if e.Side == "both" && e.SuppressedBy == "" {
			continue
		}
		fmt.Fprintf(&b, "%-14s %s %s", e.Side, e.Project, e.Branch)
		if e.SuppressedBy != "" {
			fmt.Fprintf(&b, " (held: %s)", e.SuppressedBy)
		}
		if e.Suppressed != "" {
			fmt.Fprintf(&b, " (%s)", e.Suppressed)
		}
		b.WriteString("\n")
	}
	return b.String()
}
