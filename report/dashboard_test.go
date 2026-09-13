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

// Ticked boxes are read by their markers, unticked ones are not, and the
// all-at-once boxes lift a whole section.
func TestParseChecksReadsTickedBoxesOnly(t *testing.T) {
	body := `## Pending approval

 - [x] <!-- approve-branch=renovate/major-symfony -->fix(deps): update symfony packages to v8
 - [ ] <!-- approve-branch=renovate/major-laravel -->chore(deps): update laravel to v12
 - [ ] <!-- approve-all-pending-prs -->all

## Awaiting schedule

 - [X] <!-- unschedule-branch=renovate/lock-file-maintenance -->refresh
 - [x] <!-- create-all-awaiting-schedule-prs -->all

## Open

 - [x] <!-- rebase-branch=renovate/ci-components -->[update ci components](!12)
 - [x] <!-- retry-branch=renovate/npm -->update npm
`
	c := ParseChecks(body)
	if !c.Approve["renovate/major-symfony"] || c.Approve["renovate/major-laravel"] || c.ApproveAll {
		t.Errorf("approve: %+v", c)
	}
	if !c.Unschedule["renovate/lock-file-maintenance"] || !c.CreateAll {
		t.Errorf("schedule: %+v", c)
	}
	if !c.Rebase["renovate/ci-components"] || !c.Retry["renovate/npm"] || c.RebaseAll {
		t.Errorf("rebase: %+v", c)
	}
	if !c.Lifted("renovate/major-symfony", model.BlockDashboardApproval) || c.Lifted("renovate/major-laravel", model.BlockDashboardApproval) {
		t.Error("Lifted must follow the approve box")
	}
	if !c.Lifted("renovate/anything", model.BlockSchedule) {
		t.Error("create-all lifts every schedule hold")
	}
	if c.Lifted("renovate/major-symfony", model.BlockMinimumReleaseAge) {
		t.Error("an approval does not lift a release age")
	}
	if !c.Any() || ParseChecks("nothing ticked").Any() {
		t.Error("Any")
	}
}

// The dashboard says what is held and why, with a box where a box can lift
// it, what is open with a rebase box, what failed with a retry box, and
// every dependency seen - and never a box that is already ticked.
func TestDashboardSectionsAndBoxes(t *testing.T) {
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	dep := model.Dependency{Manager: "composer", File: "composer.json", DepName: "symfony/console", CurrentValue: "^7.4", Datasource: "packagist", CustomManager: model.NoCustomManager}
	dep.Locus = model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest}
	n := 0
	mk := func(name string, typ model.UpdateType, blocks ...model.Block) (model.Update, model.Branch) {
		n++
		// Distinct targets, so each update has a key of its own.
		u := model.Update{DepKey: dep.Key(), Dep: dep, NewValue: "^8." + string(rune('0'+n)), NewVersion: "8.0.0", Type: typ, Blocks: blocks, TimeSource: model.TimeUnknown}
		if len(blocks) > 0 {
			u.SuppressedBy = blocks[0].Reason
		}
		b := model.Branch{Name: name, Title: "chore(deps): update " + name, UpdateKeys: []string{u.Key()}, SuppressedBy: u.SuppressedBy}
		return u, b
	}
	u1, b1 := mk("renovate/major-symfony", model.UpdateMajor, model.Block{Reason: model.BlockDashboardApproval})
	u2, b2 := mk("renovate/lock-file-maintenance", model.UpdateLockFileMaintenance, model.Block{Reason: model.BlockSchedule, Until: now.Add(3 * time.Hour)})
	u3, b3 := mk("renovate/npm", model.UpdateMinor, model.Block{Reason: model.BlockMinimumReleaseAge, Until: now.Add(48 * time.Hour)})
	u4, b4 := mk("renovate/ci-components", model.UpdateMinor)
	u5, b5 := mk("renovate/failed", model.UpdateMinor)
	u6, b6 := mk("renovate/tasked", model.UpdateMinor, model.Block{Reason: model.BlockTaskRefused, Note: "command \"x\" is not on the allowedCommands list"})
	plan := &model.Plan{
		PinupVersion: "test", Repo: model.RepoRef{Path: "a/b"},
		Deps: []model.Dependency{dep},
		// u4 twice: the same include read from two places shares a key
		// and is listed once (measured: lint-tools in pinup's own file).
		Updates:  []model.Update{u1, u2, u3, u4, u5, u6, u4},
		Branches: []model.Branch{b1, b2, b3, b4, b5, b6},
		Warnings: []model.Warning{{Stage: "lookup", Msg: "x could not be reached"}},
	}
	states := map[string]BranchState{
		"renovate/ci-components": {Action: "created", MRIID: 12},
		"renovate/failed":        {Action: "failed", Message: "push: rejected"},
	}
	open := []publish.MergeRequest{{IID: 12, SourceBranch: "renovate/ci-components", State: "opened"}}
	body := Dashboard(plan, states, open, now)
	for _, want := range []string{
		"## Repository problems", "x could not be reached",
		"## Pending approval", "- [ ] <!-- approve-branch=renovate/major-symfony -->", "approve-all-pending-prs",
		"## Awaiting schedule", "- [ ] <!-- unschedule-branch=renovate/lock-file-maintenance -->", "(opens 2026-09-13 11:00 UTC)",
		"## Awaiting release age", "- [ ] <!-- approvePr-branch=renovate/npm -->", "old enough 2026-09-15",
		"## Held", "taskRefused: command \"x\"",
		"## Errored", "- [ ] <!-- retry-branch=renovate/failed -->", "push: rejected",
		"## Open", "- [ ] <!-- rebase-branch=renovate/ci-components -->[chore(deps): update renovate/ci-components](!12)",
		"## Detected dependencies", "<details><summary>composer (1)</summary>", "`symfony/console ^7.4` → [Updates: `^8.1`, `^8.2`, `^8.3`, `^8.4`, `^8.5`, `^8.6`]",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "[x]") {
		t.Error("a rendered dashboard has no ticked box")
	}
	if ParseChecks(body).Any() {
		t.Error("a fresh dashboard reads as nothing ticked")
	}
}
