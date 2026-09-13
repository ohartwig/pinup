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

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/publish"
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
	var r Result
	seenBefore := map[string]bool{}
	if prev != nil {
		for _, k := range prev.Seen {
			seenBefore[k] = true
		}
	}
	next := &State{}
	r.Plans = len(plans)
	if len(plans) == 0 {
		r.Failures = append(r.Failures, "zero plans: nothing was compared")
	}
	for _, s := range sup.Entries {
		switch {
		case s.Reason == "" || s.Owner == "" || s.Expires.IsZero():
			r.Failures = append(r.Failures, fmt.Sprintf("suppression %s/%s has no reason, owner or expiry", s.Project, s.Branch))
		case !now.Before(s.Expires):
			r.Failures = append(r.Failures, fmt.Sprintf("suppression %s/%s expired on %s", s.Project, s.Branch, s.Expires.Format("2006-01-02")))
		case s.Project == "" || s.Branch == "":
			r.Failures = append(r.Failures, "a suppression must name a project and a branch, never a bare branch pattern")
		}
	}
	used := map[string]bool{}
	onlyPinupByProject := map[string]int{}
	isControl := map[string]bool{}
	for _, c := range controls {
		isControl[c] = true
	}
	for _, p := range plans {
		if p.PinupVersion != version {
			r.Failures = append(r.Failures, fmt.Sprintf("%s: plan from pinup %s, this job runs %s; a stale artefact is not compared", p.Repo.Path, p.PinupVersion, version))
			continue
		}
		r.Deps += len(p.Deps)
		mrs, ok := open[p.Repo.Path]
		if !ok {
			r.Failures = append(r.Failures, fmt.Sprintf("%s: Renovate's side could not be read; an unreadable Renovate is not an empty Renovate", p.Repo.Path))
			continue
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
			e := Entry{Project: p.Repo.Path, Branch: name, SuppressedBy: b.SuppressedBy, Title: b.Title}
			if m, ok := theirs[name]; ok && b.SuppressedBy == model.BlockRollingMajor {
				e.Side, e.MRIID, e.Suppressed = "held_open", m.IID, "rolling-major"
				r.RollingMajor++
			} else if ok && persistingHold(b.SuppressedBy) {
				e.Side, e.MRIID, e.Suppressed = "held_open", m.IID, "persisting"
				r.Persisting++
			} else if ok && b.SuppressedBy != "" {
				e.Side, e.MRIID = "held_open", m.IID
				r.HeldOpen++
			} else if ok {
				e.Side, e.MRIID = "both", m.IID
				r.Both++
			} else if m, done := merged[name]; done && b.SuppressedBy == "" {
				// Renovate opened it and had it merged since the window
				// this comparison looks back over; the plan here was made
				// before, or cannot see the outcome - a lock refresh
				// leaves no trace a plan reads (measured: Renovate's
				// lock-file-maintenance merges at 01:2x and pinup plans
				// it again every hour of the window). The same branch,
				// one lifecycle further along.
				e.Side, e.MRIID, e.Suppressed = "both", m.IID, "merged"
				r.Merged++
			} else if b.SuppressedBy != "" {
				e.Side = "both" // held here, absent there: what a hold means
				r.Held++
			} else {
				e.Side = "only_pinup"
				switch key := suppressionFor(sup, p.Repo.Path, name, now); {
				case key != "":
					e.Suppressed = key
					used[key] = true
					r.Suppressed++
				case isControl[p.Repo.Path]:
					// The control is supposed to differ; its count is
					// checked below, exactly one.
					e.Suppressed = "control"
					r.Controls++
				default:
					k := "only_pinup|" + p.Repo.Path + "|" + name
					next.Seen = append(next.Seen, k)
					if seenBefore[k] {
						r.OnlyPinup++
					} else {
						e.Suppressed = "pending"
						r.Pending++
					}
				}
				onlyPinupByProject[p.Repo.Path]++
			}
			r.Entries = append(r.Entries, e)
		}
		for name, m := range theirs {
			if _, ok := mine[name]; ok {
				continue
			}
			e := Entry{Project: p.Repo.Path, Branch: name, Side: "only_renovate", Title: m.Title, MRIID: m.IID}
			k := "only_renovate|" + p.Repo.Path + "|" + name
			next.Seen = append(next.Seen, k)
			switch key := suppressionFor(sup, p.Repo.Path, name, now); {
			case key != "":
				// A merge request Renovate left behind - its change
				// already on main, the branch never closed - is
				// Renovate's difference, triaged like pinup's own, with
				// the same expiry. Measured: devops/images/c2patool!92
				// pins container-scanning 8.6.34, main pins 8.6.35.
				e.Suppressed = key
				used[key] = true
				r.Suppressed++
			case seenBefore[k]:
				r.OnlyRenovate++
			default:
				e.Suppressed = "pending"
				r.Pending++
			}
			r.Entries = append(r.Entries, e)
		}
	}
	sort.Slice(r.Entries, func(i, j int) bool {
		if r.Entries[i].Project != r.Entries[j].Project {
			return r.Entries[i].Project < r.Entries[j].Project
		}
		return r.Entries[i].Branch < r.Entries[j].Branch
	})
	if r.Plans > 0 && r.Deps == 0 {
		r.Failures = append(r.Failures, "zero dependencies across every plan: the runs extracted nothing")
	}
	for _, s := range sup.Entries {
		if !used[s.Project+"/"+s.Branch] && s.Kind != "fixture-control" {
			r.Failures = append(r.Failures, fmt.Sprintf("suppression %s/%s matched nothing; a dead suppression is removed, not kept", s.Project, s.Branch))
		}
	}
	for _, c := range controls {
		if onlyPinupByProject[c] != 1 {
			r.Failures = append(r.Failures, fmt.Sprintf("control %s yielded %d only-pinup entries, want exactly 1", c, onlyPinupByProject[c]))
		}
	}
	if r.OnlyRenovate > 0 {
		r.Failures = append(r.Failures, fmt.Sprintf("%d branches Renovate has open that pinup does not plan, for the second run running: a miss is an update that does not happen", r.OnlyRenovate))
	}
	if r.HeldOpen > 0 {
		r.Failures = append(r.Failures, fmt.Sprintf("%d branches Renovate has open that pinup holds; the reasons are on the entries", r.HeldOpen))
	}
	if r.OnlyPinup > 0 {
		r.Failures = append(r.Failures, fmt.Sprintf("%d branches pinup plans without a merge request, for the second run running, and without a triaged suppression", r.OnlyPinup))
	}
	sort.Strings(next.Seen)
	return r, next
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
