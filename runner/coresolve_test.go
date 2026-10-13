// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/model"
)

// What composer printed for the two TYPO3 branches on 2026-10-13, shortened.
const (
	typo3CoreFailure = `exit status 2: Your requirements could not be resolved to an installable set of packages.
  Problem 1
    - moselwal/sitepackage-base is locked to version 4.12.4 and an update of this package was not requested.
    - moselwal/sitepackage-base 4.12.4 requires enshrined/svg-sanitize ^0.22.0 -> found enshrined/svg-sanitize[0.22.0] but these were not loaded, because they are affected by security advisories`
	firstPartyFailure = `exit status 2: Your requirements could not be resolved to an installable set of packages.
  Problem 1
    - typo3/cms-core v14.3.7 requires enshrined/svg-sanitize ~0.22 -> found enshrined/svg-sanitize[0.22.0] but these were not loaded, because they are affected by security advisories
    - typo3/cms-tstemplate is locked to version v14.3.7 and an update of this package was not requested.`
)

func composerLock(names ...string) model.Task {
	argv := append([]string{"composer", "update"}, names...)
	argv = append(argv, "--with-all-dependencies", "--no-plugins", "--no-install", "--no-scripts", "--no-audit", "--ignore-platform-reqs")
	return model.Task{Kind: model.TaskLockRefresh, Manager: "composer", Command: argv,
		ExecutionMode: model.ExecBranch, FileFilters: []string{"composer.lock"}, AllowedBy: -1}
}

func TestCoResolveCombinesBranchesThatNameEachOther(t *testing.T) {
	core := model.Branch{Name: "pinup/typo3-core", Title: "fix(deps): update typo3 core to ^14.0", UpdateKeys: []string{"core"},
		Tasks: []model.Task{composerLock("typo3/cms-core", "typo3/cms-tstemplate")}, Automerge: true, Labels: []string{"deps"}}
	first := model.Branch{Name: "pinup/first-party-composer", Title: "chore(deps): update first-party composer packages", UpdateKeys: []string{"spb"},
		Edits: []model.Edit{{File: "composer.json", Start: 10, End: 15, Old: "4.12.4", New: "4.12.5"}},
		Tasks: []model.Task{composerLock("moselwal/sitepackage-base")}, Automerge: true}
	maint := model.Branch{Name: "pinup/lock-file-maintenance", Title: "chore(deps): refresh lock file [security]",
		Tasks: []model.Task{{Kind: model.TaskLockRefresh, Manager: "composer", Command: []string{"composer", "update", "--no-plugins"}}}}
	other := model.Branch{Name: "pinup/unrelated", Title: "chore(deps): update x", Tasks: []model.Task{composerLock("acme/x")}}

	got, warnings := coResolve([]pushFailure{
		{branch: core, err: typo3CoreFailure},
		{branch: first, err: firstPartyFailure},
		{branch: maint, err: firstPartyFailure}, // names no package: maintenance stays alone
		{branch: other, err: "exit status 2: Your requirements could not be resolved to an installable set of packages. - foo/bar"}, // names nobody's package
	})
	if len(warnings) != 0 || len(got) != 1 {
		t.Fatalf("want one combined branch, got %d (%v)", len(got), warnings)
	}
	b := got[0]
	if !strings.HasPrefix(b.Name, "pinup/co-resolve-") {
		t.Errorf("name %q", b.Name)
	}
	if b.Title != "fix(deps): update 2 co-dependent groups together" {
		t.Errorf("title %q: a fix member makes it a fix", b.Title)
	}
	cmd := strings.Join(b.Tasks[0].Command, " ")
	if !strings.HasPrefix(cmd, "composer update moselwal/sitepackage-base typo3/cms-core typo3/cms-tstemplate --with-all-dependencies --no-plugins") {
		t.Errorf("one refresh naming every package of both: %q", cmd)
	}
	if len(b.Edits) != 1 || !slices.Equal(b.UpdateKeys, []string{"spb", "core"}) || !b.Automerge {
		t.Errorf("edits %v keys %v automerge %v", b.Edits, b.UpdateKeys, b.Automerge)
	}
	if !strings.Contains(b.Body, "pinup/typo3-core") || !strings.Contains(b.Body, "pinup/first-party-composer") {
		t.Errorf("the description names the members: %q", b.Body)
	}

	// The same members in another order give the same branch, so a later
	// run updates the request this one opened.
	again, _ := coResolve([]pushFailure{{branch: first, err: firstPartyFailure}, {branch: core, err: typo3CoreFailure}})
	if len(again) != 1 || again[0].Name != b.Name {
		t.Errorf("unstable name: %q vs %v", b.Name, again)
	}
}

func TestCoResolveLeavesOverlappingEditsAndOtherFailuresAlone(t *testing.T) {
	e := model.Edit{File: "composer.json", Start: 10, End: 15, Old: "a", New: "b"}
	e2 := model.Edit{File: "composer.json", Start: 12, End: 14, Old: "a", New: "c"}
	a := model.Branch{Name: "pinup/a", Title: "chore(deps): a [security]", Edits: []model.Edit{e}, Tasks: []model.Task{composerLock("v/a")}}
	b := model.Branch{Name: "pinup/b", Title: "chore(deps): b", Edits: []model.Edit{e2}, Tasks: []model.Task{composerLock("v/b")}}
	unres := "Your requirements could not be resolved to an installable set of packages. "
	got, warnings := coResolve([]pushFailure{{branch: a, err: unres + "v/b"}, {branch: b, err: unres + "v/a"}})
	if len(got) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "same bytes") {
		t.Errorf("overlapping edits: %v %v", got, warnings)
	}
	// A failure that is not composer's resolution failure is not retried.
	got, _ = coResolve([]pushFailure{{branch: a, err: "network down v/b"}, {branch: b, err: "network down v/a"}})
	if len(got) != 0 {
		t.Errorf("a non-resolution failure was combined: %v", got)
	}
	// The security marker survives the combination.
	b.Edits = nil
	got, _ = coResolve([]pushFailure{{branch: a, err: unres + "v/b"}, {branch: b, err: unres + "v/a"}})
	if len(got) != 1 || !strings.HasSuffix(got[0].Title, "[security]") {
		t.Errorf("security marker: %v", got)
	}
}

// resolverTasks stands in for composer: a refresh fails unless it names
// every package in need, as composer does when two groups need each other.
type resolverTasks struct {
	need []string
	ran  []string
}

func (r *resolverTasks) Available([]model.Task) error { return nil }

func (r *resolverTasks) Run(_ context.Context, root string, t model.Task) error {
	r.ran = append(r.ran, strings.Join(t.Command, " "))
	for _, n := range r.need {
		if !slices.Contains(t.Command, n) {
			// composer names the package that would have to move as well
			return errors.New("exit status 2: Your requirements could not be resolved to an installable set of packages.\n - " +
				n + " is locked to version 1.0 and an update of this package was not requested.")
		}
	}
	return os.WriteFile(filepath.Join(root, "composer.lock"), []byte("{\"resolved\": true}\n"), 0o644)
}

// End to end: both branches fail on their own, the combined branch is
// pushed and opened in the same run.
func TestExecuteOpensTheCombinedBranchWhenEachAloneFails(t *testing.T) {
	_, repo := fixture(t)
	pf := &platformfake.Platform{}
	o := options(repo, pf)
	tasks := &resolverTasks{need: []string{"vendor/core", "vendor/base"}}
	o.Tasks = tasks
	p := plan(
		model.Branch{Name: "renovate/core", Title: "fix(deps): update core", UpdateKeys: []string{"Containerfile|alpine|3.20"},
			Edits: []model.Edit{edit("3.20", "3.21")}, Tasks: []model.Task{composerLock("vendor/core")}},
		model.Branch{Name: "renovate/base", Title: "chore(deps): update base", UpdateKeys: []string{"Containerfile|golang|1.26"},
			Edits: []model.Edit{edit("1.26", "1.27")}, Tasks: []model.Task{composerLock("vendor/base")}},
	)
	outs, err := Execute(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, oc := range outs {
		actions[oc.Branch] = oc.Action
	}
	if actions["renovate/core"] != "failed" || actions["renovate/base"] != "failed" {
		t.Errorf("the members alone must fail: %v", actions)
	}
	var combined string
	for name, a := range actions {
		if strings.HasPrefix(name, "renovate/co-resolve-") {
			combined = name
			if a != "created" {
				t.Errorf("combined branch %s: %s", name, a)
			}
		}
	}
	if combined == "" || len(pf.MRs) != 1 {
		t.Fatalf("no combined merge request: %v, %d MRs", actions, len(pf.MRs))
	}
	if got := tasks.ran[len(tasks.ran)-1]; !strings.Contains(got, "vendor/core") || !strings.Contains(got, "vendor/base") {
		t.Errorf("the combined refresh names both: %q", got)
	}
}
