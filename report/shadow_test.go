// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

func plan(repo, version string, branches ...model.Branch) *model.Plan {
	p := &model.Plan{PinupVersion: version, Repo: model.RepoRef{Path: repo}, Branches: branches,
		Deps: []model.Dependency{{DepName: "x", CustomManager: model.NoCustomManager}}}
	return p
}

// compare runs the comparison twice with the state carried over, so a
// difference counts as persisting - what the failure assertions are about.
// compareOnce is a first run, where every difference is pending.
func compare(plans []*model.Plan, open map[string][]publish.MergeRequest, sup *Suppressions, controls []string, version string, now time.Time) Result {
	_, st := Compare(plans, open, sup, controls, version, now, nil)
	r, _ := Compare(plans, open, sup, controls, version, now, st)
	return r
}

func mr(branch string, iid int) publish.MergeRequest {
	return publish.MergeRequest{IID: iid, State: "opened", SourceBranch: branch, Title: branch}
}

// Each failure mode the task names, induced once, and the agreeing case.
func TestCompareFailsOnEachDeadGateAndPassesOnAgreement(t *testing.T) {
	sup := &Suppressions{}
	// Agreement: both sides match, a held branch counts as agreement, the
	// control yields exactly one only-pinup entry.
	plans := []*model.Plan{
		plan("devops/images/a", "0.1.0", model.Branch{Name: "renovate/x-1.x"}, model.Branch{Name: "renovate/y-2.x", SuppressedBy: model.BlockSchedule}),
		plan("pinup/shadow-fixture", "0.1.0", model.Branch{Name: "renovate/ci-components"}),
	}
	open := map[string][]publish.MergeRequest{
		"devops/images/a":      {mr("renovate/x-1.x", 3)},
		"pinup/shadow-fixture": {},
	}
	r := compare(plans, open, sup, []string{"pinup/shadow-fixture"}, "0.1.0", now)
	if !r.Passed() {
		t.Fatalf("agreement must pass: %v", r.Failures)
	}
	if r.Both != 1 || r.Held != 1 || r.Controls != 1 || r.OnlyPinup != 0 || !strings.Contains(r.Summary(), "matched 3/3") {
		t.Errorf("counts: %+v %s", r, r.Summary())
	}

	// A wrong version is refused.
	if r := compare([]*model.Plan{plan("a/b", "0.0.9")}, open, sup, nil, "0.1.0", now); !failsWith(r, "stale artefact") {
		t.Errorf("version: %v", r.Failures)
	}
	// Zero plans, zero deps.
	if r := compare(nil, open, sup, nil, "0.1.0", now); !failsWith(r, "zero plans") {
		t.Errorf("zero plans: %v", r.Failures)
	}
	empty := plan("a/b", "0.1.0")
	empty.Deps = nil
	if r := compare([]*model.Plan{empty}, map[string][]publish.MergeRequest{"a/b": {}}, sup, nil, "0.1.0", now); !failsWith(r, "zero dependencies") {
		t.Errorf("zero deps: %v", r.Failures)
	}
	// Renovate's side unreadable is not empty.
	if r := compare([]*model.Plan{plan("a/b", "0.1.0")}, map[string][]publish.MergeRequest{}, sup, nil, "0.1.0", now); !failsWith(r, "unreadable Renovate") {
		t.Errorf("unreadable: %v", r.Failures)
	}
	// A miss on Renovate's side.
	if r := compare([]*model.Plan{plan("a/b", "0.1.0")}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/z-1.x", 9)}}, sup, nil, "0.1.0", now); !failsWith(r, "Renovate has open that pinup does not plan") {
		t.Errorf("only_renovate: %v", r.Failures)
	}
	// Renovate has open what pinup holds: a difference with its reason.
	heldOpen := plan("a/b", "0.1.0", model.Branch{Name: "renovate/x-1.x", SuppressedBy: model.BlockDisabled})
	if r := compare([]*model.Plan{heldOpen}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/x-1.x", 4)}}, sup, nil, "0.1.0", now); !failsWith(r, "pinup holds") || r.HeldOpen != 1 {
		t.Errorf("held_open: %v %+v", r.Failures, r)
	}
	// Renovate has open a major a human approved for a `@N` pin; pinup
	// holds it as a rolling major by design - pre-declared, not a failure,
	// and counted apart from the held_open that would be one.
	rolling := plan("a/b", "0.1.0", model.Branch{Name: "renovate/x-2.x", SuppressedBy: model.BlockRollingMajor})
	if r := compare([]*model.Plan{rolling}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/x-2.x", 5)}}, sup, nil, "0.1.0", now); !r.Passed() || r.RollingMajor != 1 || r.HeldOpen != 0 || !strings.Contains(r.Summary(), "matched 1/1") {
		t.Errorf("rolling major: %v %+v %s", r.Failures, r, r.Summary())
	}
	// The control yields nothing: the run is broken.
	if r := compare([]*model.Plan{plan("pinup/shadow-fixture", "0.1.0")}, map[string][]publish.MergeRequest{"pinup/shadow-fixture": {}}, sup, []string{"pinup/shadow-fixture"}, "0.1.0", now); !failsWith(r, "control pinup/shadow-fixture yielded 0") {
		t.Errorf("control: %v", r.Failures)
	}
}

func TestSuppressionsExpireMustMatchAndMayNotBeBare(t *testing.T) {
	p := plan("a/b", "0.1.0", model.Branch{Name: "renovate/x-1.x"})
	open := map[string][]publish.MergeRequest{"a/b": {}}
	good := &Suppressions{Entries: []Suppression{{Project: "a/b", Branch: "renovate/x-1.x", Kind: "pre-declared-improvement", Reason: "r", Owner: "o", Expires: now.Add(24 * time.Hour)}}}
	if r := compare([]*model.Plan{p}, open, good, nil, "0.1.0", now); !r.Passed() || r.Suppressed != 1 {
		t.Errorf("a triaged only-pinup entry passes: %v", r.Failures)
	}
	expired := &Suppressions{Entries: []Suppression{{Project: "a/b", Branch: "renovate/x-1.x", Reason: "r", Owner: "o", Expires: now.Add(-time.Hour)}}}
	if r := compare([]*model.Plan{p}, open, expired, nil, "0.1.0", now); !failsWith(r, "expired") {
		t.Errorf("expired: %v", r.Failures)
	}
	dead := &Suppressions{Entries: []Suppression{{Project: "a/b", Branch: "renovate/nothing", Kind: "defect", Reason: "r", Owner: "o", Expires: now.Add(time.Hour)}}}
	if r := compare([]*model.Plan{p}, open, dead, nil, "0.1.0", now); !failsWith(r, "matched nothing") {
		t.Errorf("dead: %v", r.Failures)
	}
	bare := &Suppressions{Entries: []Suppression{{Branch: "renovate/x-1.x", Reason: "r", Owner: "o", Expires: now.Add(time.Hour)}}}
	if r := compare([]*model.Plan{p}, open, bare, nil, "0.1.0", now); !failsWith(r, "never a bare branch") {
		t.Errorf("bare: %v", r.Failures)
	}
}

func failsWith(r Result, s string) bool {
	for _, f := range r.Failures {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

// Timing is not behaviour: the two tools run half an hour apart, so a
// difference seen once is pending and fails only when the next comparison
// sees it again. A hold Renovate does not re-decide once its request
// exists - a window, a release age, a dashboard approval - is a
// pre-declared agreement, not a held_open failure.
func TestDifferencesMustPersistAndPersistingHoldsAgree(t *testing.T) {
	sup := &Suppressions{}
	plans := []*model.Plan{plan("a/b", "0.1.0", model.Branch{Name: "renovate/x-1.x"})}
	open := map[string][]publish.MergeRequest{"a/b": {mr("renovate/z-1.x", 9)}}
	first, st := Compare(plans, open, sup, nil, "0.1.0", now, nil)
	if !first.Passed() || first.Pending != 2 || first.OnlyPinup != 0 || first.OnlyRenovate != 0 {
		t.Errorf("first run: %v %+v", first.Failures, first)
	}
	if len(st.Seen) != 2 {
		t.Errorf("state carries %v", st.Seen)
	}
	second, _ := Compare(plans, open, sup, nil, "0.1.0", now, st)
	if second.Passed() || second.OnlyPinup != 1 || second.OnlyRenovate != 1 || second.Pending != 0 {
		t.Errorf("second run: %v %+v", second.Failures, second)
	}
	// Gone next run: the state forgets it, and a later reappearance is
	// pending again.
	third, st3 := Compare(plans, map[string][]publish.MergeRequest{"a/b": {}}, sup, nil, "0.1.0", now, st)
	if len(st3.Seen) != 1 || third.OnlyRenovate != 0 {
		t.Errorf("third run: %+v %v", third, st3.Seen)
	}

	for _, reason := range []model.BlockReason{model.BlockSchedule, model.BlockMinimumReleaseAge, model.BlockDashboardApproval} {
		held := plan("a/b", "0.1.0", model.Branch{Name: "renovate/y-1.x", SuppressedBy: reason})
		r := compare([]*model.Plan{held}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/y-1.x", 4)}}, sup, nil, "0.1.0", now)
		if !r.Passed() || r.Persisting != 1 || r.HeldOpen != 0 {
			t.Errorf("%s: %v %+v", reason, r.Failures, r)
		}
	}
	// A hold Renovate would not have: still a failure.
	held := plan("a/b", "0.1.0", model.Branch{Name: "renovate/y-1.x", SuppressedBy: model.BlockPluginRequired})
	if r := compare([]*model.Plan{held}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/y-1.x", 4)}}, sup, nil, "0.1.0", now); r.Passed() || r.HeldOpen != 1 {
		t.Errorf("pluginRequired: %+v", r)
	}
}

// A merge request Renovate left behind - already on main, never closed -
// is triaged the way an only-pinup entry is, expiry and all.
func TestAStaleRenovateBranchCanBeTriaged(t *testing.T) {
	plans := []*model.Plan{plan("a/b", "0.1.0")}
	open := map[string][]publish.MergeRequest{"a/b": {mr("renovate/pin-dependencies", 92)}}
	sup := &Suppressions{Entries: []Suppression{{Project: "a/b", Branch: "renovate/pin-dependencies", Kind: "stale-renovate", Reason: "pins 8.6.34; main pins 8.6.35", Owner: "o", Expires: now.Add(24 * time.Hour)}}}
	_, st := Compare(plans, open, sup, nil, "0.1.0", now, nil)
	r, _ := Compare(plans, open, sup, nil, "0.1.0", now, st)
	if !r.Passed() || r.Suppressed != 1 || r.OnlyRenovate != 0 {
		t.Errorf("triaged stale branch: %v %+v", r.Failures, r)
	}
	none := &Suppressions{}
	_, st = Compare(plans, open, none, nil, "0.1.0", now, nil)
	if r, _ := Compare(plans, open, none, nil, "0.1.0", now, st); r.Passed() || r.OnlyRenovate != 1 {
		t.Errorf("untriaged stale branch must fail on the second run: %v", r.Failures)
	}
}

// A branch Renovate opened and had merged inside the look-back window is
// a match one lifecycle further along - the lock refresh pinup plans every
// hour of the window while Renovate's merged at 01:2x - and never a miss
// on Renovate's side either.
func TestAMergedRenovateBranchMatches(t *testing.T) {
	plans := []*model.Plan{plan("a/b", "0.1.0", model.Branch{Name: "renovate/lock-file-maintenance"})}
	done := mr("renovate/lock-file-maintenance", 47)
	done.State = "merged"
	open := map[string][]publish.MergeRequest{"a/b": {done, mr("renovate/x-1.x", 48)}}
	_, st := Compare(plans, open, &Suppressions{}, nil, "0.1.0", now, nil)
	r, _ := Compare(plans, open, &Suppressions{}, nil, "0.1.0", now, st)
	if r.Merged != 1 || r.OnlyPinup != 0 {
		t.Errorf("merged branch: %+v %v", r, r.Failures)
	}
	if r.OnlyRenovate != 1 {
		t.Errorf("the open x-1.x is Renovate's alone and must still count: %+v", r)
	}
	if !strings.Contains(r.Summary(), "merged 1") {
		t.Errorf("summary %q", r.Summary())
	}
}
