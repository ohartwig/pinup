// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/publish"
)

func plan(repo, version string, branches ...model.Branch) *model.Plan {
	p := &model.Plan{PinupVersion: version, Repo: model.RepoRef{Path: repo}, Branches: branches,
		Deps: []model.Dependency{{DepName: "x", CustomManager: model.NoCustomManager}}}
	return p
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
	r := Compare(plans, open, sup, []string{"pinup/shadow-fixture"}, "0.1.0", now)
	if !r.Passed() {
		t.Fatalf("agreement must pass: %v", r.Failures)
	}
	if r.Both != 1 || r.Held != 1 || r.Controls != 1 || r.OnlyPinup != 0 || !strings.Contains(r.Summary(), "matched 3/3") {
		t.Errorf("counts: %+v %s", r, r.Summary())
	}

	// A wrong version is refused.
	if r := Compare([]*model.Plan{plan("a/b", "0.0.9")}, open, sup, nil, "0.1.0", now); !failsWith(r, "stale artefact") {
		t.Errorf("version: %v", r.Failures)
	}
	// Zero plans, zero deps.
	if r := Compare(nil, open, sup, nil, "0.1.0", now); !failsWith(r, "zero plans") {
		t.Errorf("zero plans: %v", r.Failures)
	}
	empty := plan("a/b", "0.1.0")
	empty.Deps = nil
	if r := Compare([]*model.Plan{empty}, map[string][]publish.MergeRequest{"a/b": {}}, sup, nil, "0.1.0", now); !failsWith(r, "zero dependencies") {
		t.Errorf("zero deps: %v", r.Failures)
	}
	// Renovate's side unreadable is not empty.
	if r := Compare([]*model.Plan{plan("a/b", "0.1.0")}, map[string][]publish.MergeRequest{}, sup, nil, "0.1.0", now); !failsWith(r, "unreadable Renovate") {
		t.Errorf("unreadable: %v", r.Failures)
	}
	// A miss on Renovate's side.
	if r := Compare([]*model.Plan{plan("a/b", "0.1.0")}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/z-1.x", 9)}}, sup, nil, "0.1.0", now); !failsWith(r, "Renovate has open that pinup does not plan") {
		t.Errorf("only_renovate: %v", r.Failures)
	}
	// Renovate has open what pinup holds: a difference with its reason.
	heldOpen := plan("a/b", "0.1.0", model.Branch{Name: "renovate/x-1.x", SuppressedBy: model.BlockDisabled})
	if r := Compare([]*model.Plan{heldOpen}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/x-1.x", 4)}}, sup, nil, "0.1.0", now); !failsWith(r, "pinup holds") || r.HeldOpen != 1 {
		t.Errorf("held_open: %v %+v", r.Failures, r)
	}
	// Renovate has open a major a human approved for a `@N` pin; pinup
	// holds it as a rolling major by design - pre-declared, not a failure,
	// and counted apart from the held_open that would be one.
	rolling := plan("a/b", "0.1.0", model.Branch{Name: "renovate/x-2.x", SuppressedBy: model.BlockRollingMajor})
	if r := Compare([]*model.Plan{rolling}, map[string][]publish.MergeRequest{"a/b": {mr("renovate/x-2.x", 5)}}, sup, nil, "0.1.0", now); !r.Passed() || r.RollingMajor != 1 || r.HeldOpen != 0 || !strings.Contains(r.Summary(), "matched 1/1") {
		t.Errorf("rolling major: %v %+v %s", r.Failures, r, r.Summary())
	}
	// The control yields nothing: the run is broken.
	if r := Compare([]*model.Plan{plan("pinup/shadow-fixture", "0.1.0")}, map[string][]publish.MergeRequest{"pinup/shadow-fixture": {}}, sup, []string{"pinup/shadow-fixture"}, "0.1.0", now); !failsWith(r, "control pinup/shadow-fixture yielded 0") {
		t.Errorf("control: %v", r.Failures)
	}
}

func TestSuppressionsExpireMustMatchAndMayNotBeBare(t *testing.T) {
	p := plan("a/b", "0.1.0", model.Branch{Name: "renovate/x-1.x"})
	open := map[string][]publish.MergeRequest{"a/b": {}}
	good := &Suppressions{Entries: []Suppression{{Project: "a/b", Branch: "renovate/x-1.x", Kind: "pre-declared-improvement", Reason: "r", Owner: "o", Expires: now.Add(24 * time.Hour)}}}
	if r := Compare([]*model.Plan{p}, open, good, nil, "0.1.0", now); !r.Passed() || r.Suppressed != 1 {
		t.Errorf("a triaged only-pinup entry passes: %v", r.Failures)
	}
	expired := &Suppressions{Entries: []Suppression{{Project: "a/b", Branch: "renovate/x-1.x", Reason: "r", Owner: "o", Expires: now.Add(-time.Hour)}}}
	if r := Compare([]*model.Plan{p}, open, expired, nil, "0.1.0", now); !failsWith(r, "expired") {
		t.Errorf("expired: %v", r.Failures)
	}
	dead := &Suppressions{Entries: []Suppression{{Project: "a/b", Branch: "renovate/nothing", Kind: "defect", Reason: "r", Owner: "o", Expires: now.Add(time.Hour)}}}
	if r := Compare([]*model.Plan{p}, open, dead, nil, "0.1.0", now); !failsWith(r, "matched nothing") {
		t.Errorf("dead: %v", r.Failures)
	}
	bare := &Suppressions{Entries: []Suppression{{Branch: "renovate/x-1.x", Reason: "r", Owner: "o", Expires: now.Add(time.Hour)}}}
	if r := Compare([]*model.Plan{p}, open, bare, nil, "0.1.0", now); !failsWith(r, "never a bare branch") {
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
