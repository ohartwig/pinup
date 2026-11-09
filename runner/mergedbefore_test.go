// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
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
