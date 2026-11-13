// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"testing"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

func composerBump(name string) (*model.Branch, []model.Update) {
	u := model.Update{
		DepKey: name, NewValue: "5.10.1", Type: model.UpdatePatch,
		Dep: model.Dependency{DepName: name, CurrentValue: "5.10.0"},
	}
	b := &model.Branch{
		Name: "pinup/first-party-composer", UpdateKeys: []string{u.Key()},
		Edits: []model.Edit{{File: "composer.json", Old: "5.10.0", New: "5.10.1"}},
	}
	return b, []model.Update{u}
}

// Two packages taken through the same two versions in the same file are
// different changes: the earlier merge of one is not a revert of the
// other (moselwal-websites!1539 against !1248, 2026-11-09).
func TestAnEarlierMergeOfAnotherDependencyWithTheSameVersionsIsNotARevert(t *testing.T) {
	earlier, earlierUpdates := composerBump("moselwal/typo3-config")
	history := []publish.MergeRequest{{IID: 1248, State: "merged", Description: description(earlier, earlierUpdates, "")}}

	b, updates := composerBump("koh/frankenphp")
	if prior, ok := mergedBefore(history, b, updates); ok {
		t.Errorf("!%d of another dependency counted as the same edits", prior)
	}
}

// The same dependency through the same versions is still the revert the
// check exists for.
func TestAnEarlierMergeOfTheSameDependencyIsStillARevert(t *testing.T) {
	earlier, earlierUpdates := composerBump("koh/frankenphp")
	history := []publish.MergeRequest{{IID: 1500, State: "merged", Description: description(earlier, earlierUpdates, "by pinup")}}

	b, updates := composerBump("koh/frankenphp")
	if prior, ok := mergedBefore(history, b, updates); !ok || prior != 1500 {
		t.Errorf("mergedBefore = !%d, %v; want !1500, true", prior, ok)
	}
}

// A description from before the dependency names counted - file rows
// only, the updates table absent - does not match a branch that names
// its dependency: without the name the two cannot be shown to be the
// same change, and withholding automerge on a guess is what this fixes.
func TestAFileOnlyFingerprintDoesNotMatchANamedBranch(t *testing.T) {
	history := []publish.MergeRequest{{IID: 7, State: "merged", Description: "| File | Change |\n|---|---|\n| `composer.json` | `5.10.0` → `5.10.1` |\n"}}
	b, updates := composerBump("koh/frankenphp")
	if _, ok := mergedBefore(history, b, updates); ok {
		t.Error("a file-only description matched")
	}
}

// A lock file refresh names its command, not the bytes it wrote: every
// refresh of a repository reads the same. Matched against the refresh
// merged before, each new one had its automerge withheld
// (development/moselwal/collab-rte!248 against !245, 2026-11-12).
func TestALockFileRefreshIsNotARevertOfTheLastOne(t *testing.T) {
	u := model.Update{DepKey: "lock file", Type: model.UpdateLockFileMaintenance, Dep: model.Dependency{DepName: "lock file"}}
	b := &model.Branch{
		Name: "pinup/lock-file-maintenance", UpdateKeys: []string{u.Key()},
		Tasks: []model.Task{{FileFilters: []string{"package-lock.json"}, Command: []string{"npm", "update", "--package-lock-only"}}},
	}
	updates := []model.Update{u}
	history := []publish.MergeRequest{{IID: 245, State: "merged", Description: description(b, updates, "by pinup")}}
	if prior, ok := mergedBefore(history, b, updates); ok {
		t.Errorf("the refresh merged as !%d counted as the same edits", prior)
	}
	// Nor is a closed refresh an answer to the next one: what it would
	// write is not known to be the same.
	history[0].State = "closed"
	if prior, ok := closedBefore(history, b, updates); ok {
		t.Errorf("the refresh closed as !%d holds the next", prior)
	}
}

// The withhold is kept in the request: the note is read back while the
// edits are the same, and a changed update arms as usual.
func TestAWithholdIsReadBackFromTheOpenRequest(t *testing.T) {
	b, updates := composerBump("koh/frankenphp")
	withheld := description(b, updates, fmt.Sprintf(withheldNote, 1500)+"\n\nby pinup")
	if prior, ok := stillWithheld(withheld, b, updates); !ok || prior != 1500 {
		t.Errorf("stillWithheld = !%d, %v; want !1500, true", prior, ok)
	}
	if _, ok := stillWithheld(description(b, updates, "by pinup"), b, updates); ok {
		t.Error("a request without the note read as withheld")
	}
	moved, movedUpdates := composerBump("koh/frankenphp")
	moved.Edits[0].New = "5.10.2"
	if _, ok := stillWithheld(withheld, moved, movedUpdates); ok {
		t.Error("a changed update stayed withheld")
	}
}
