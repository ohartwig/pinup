// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/git"
	"github.com/ohartwig/pinup/model"
)

func kohLock(drop bool) model.Task {
	return model.Task{Kind: model.TaskPostUpgrade, Manager: "regex", Command: []string{"koh-lock", "Containerfile"},
		ExecutionMode: model.ExecBranch, FileFilters: []string{"**/koh-lock/**"}, DropWhenUnchanged: drop,
		Origin: model.Origin{Source: "config", Rule: 3}}
}

func TestDropUnchangedDecides(t *testing.T) {
	locked := []string{"Containerfile", "koh-lock/x86_64.lock", "koh-lock/aarch64.lock", "README.md"}
	refresh := model.Task{Kind: model.TaskLockRefresh, Command: []string{"composer", "update"}, FileFilters: []string{"composer.lock"}}
	nested := kohLock(true)
	nested.Dir = "images/base"
	nested.FileFilters = []string{"koh-lock/**"}
	for _, c := range []struct {
		name    string
		tasks   []model.Task
		changed [][]string
		tracked []string
		drop    bool
		note    string
	}{
		{name: "asked, nothing changed, lock present", tasks: []model.Task{kohLock(true)}, changed: [][]string{nil}, tracked: locked,
			drop: true, note: "koh-lock Containerfile changed none of the 2 files within **/koh-lock/**"},
		{name: "not asked", tasks: []model.Task{kohLock(false)}, changed: [][]string{nil}, tracked: locked},
		{name: "the lock moved", tasks: []model.Task{kohLock(true)}, changed: [][]string{{"koh-lock/x86_64.lock"}}, tracked: locked},
		// An image that has not adopted the lock: koh-lock writes nothing
		// and the bump flows as before.
		{name: "no file in scope", tasks: []model.Task{kohLock(true)}, changed: [][]string{nil}, tracked: []string{"Containerfile", "README.md"}},
		// Branch mode over several images: one moved lock is enough.
		{name: "one of two images moved", tasks: []model.Task{kohLock(true)}, changed: [][]string{{"b/koh-lock/x86_64.lock"}},
			tracked: []string{"a/Containerfile", "a/koh-lock/x86_64.lock", "b/Containerfile", "b/koh-lock/x86_64.lock"}},
		{name: "update mode, nothing moved anywhere", tasks: []model.Task{kohLock(true), kohLock(true)}, changed: [][]string{nil, nil},
			tracked: []string{"a/koh-lock/x86_64.lock", "b/koh-lock/x86_64.lock"}, drop: true, note: "the 2 files"},
		{name: "update mode, the second task moved its lock", tasks: []model.Task{kohLock(true), kohLock(true)}, changed: [][]string{nil, {"b/koh-lock/x86_64.lock"}},
			tracked: []string{"a/koh-lock/x86_64.lock", "b/koh-lock/x86_64.lock"}},
		// A lock refresh beside the asking task that moved its lock is a
		// change worth a merge request.
		{name: "a lock refresh beside it changed", tasks: []model.Task{refresh, kohLock(true)}, changed: [][]string{{"composer.lock"}, nil}, tracked: locked},
		{name: "a lock refresh beside it changed nothing", tasks: []model.Task{refresh, kohLock(true)}, changed: [][]string{nil, nil}, tracked: locked, drop: true},
		// The scope is read below the task's directory, as InScope reads it.
		{name: "a task in a directory", tasks: []model.Task{nested}, changed: [][]string{nil}, tracked: []string{"images/base/koh-lock/x86_64.lock"}, drop: true, note: "the 1 file within koh-lock/**"},
		{name: "a task in a directory, lock elsewhere", tasks: []model.Task{nested}, changed: [][]string{nil}, tracked: []string{"koh-lock/x86_64.lock", "images/other/koh-lock/x86_64.lock"}},
		// Not every task reported: the decision is not made on half a run.
		{name: "results missing", tasks: []model.Task{kohLock(true), kohLock(true)}, changed: [][]string{nil}, tracked: locked},
	} {
		t.Run(c.name, func(t *testing.T) {
			note, drop := dropUnchanged(c.tasks, c.changed, c.tracked)
			if drop != c.drop {
				t.Fatalf("drop = %v, want %v (%q)", drop, c.drop, note)
			}
			if c.note != "" && !strings.Contains(note, c.note) {
				t.Errorf("note %q does not say %q", note, c.note)
			}
			if !drop && note != "" {
				t.Errorf("a branch that is written carries a note: %q", note)
			}
		})
	}
}

// seed commits files onto the remote's main, as a repository that has
// adopted the lock carries them. A checkout cloned before it does not see
// them; clone after seeding.
func seed(t *testing.T, remote string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "seed")
	mustGit(t, filepath.Dir(dir), "clone", "--quiet", remote, dir)
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "seed")
	mustGit(t, dir, "push", "--quiet", "origin", "main")
}

func remoteHead(t *testing.T, remote, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", "--heads", remote, branch)
	cmd.Env = append(os.Environ(), testEnv...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	return sha
}

func clone(t *testing.T, remote, name string) *git.Repo {
	t.Helper()
	r, err := git.Clone(t.Context(), remote, filepath.Join(t.TempDir(), name), git.CloneOptions{}, testEnv)
	if err != nil {
		t.Fatal(err)
	}
	r.Env = testEnv
	return r
}

// A snapshot bump whose koh-lock leaves the image's lock as it was is not
// written: no branch, no merge request, the plan holds it with the reason.
// An image without koh-lock/ gets its bump as before; a moved lock lands
// with the edit; a failed koh-lock is a failure, not a drop; and a request
// already open for the branch is neither updated nor closed by the drop.
func TestDropWhenUnchangedHoldsABumpThatMovesNoLock(t *testing.T) {
	const lock = "alpine-base 3.20.0-r0\n"
	snapshot := func(to string) model.Branch {
		return model.Branch{
			Name: "pinup/snapshots", Title: "chore(deps): update snapshots",
			UpdateKeys: []string{"Containerfile|alpine|3.20"}, Edits: []model.Edit{edit("3.20", to)},
			Tasks: []model.Task{kohLock(true)}, Automerge: true,
		}
	}

	t.Run("not adopted yet: the bump flows", func(t *testing.T) {
		remote, repo := fixture(t)
		pf := &platformfake.Platform{}
		o := options(repo, pf)
		o.Tasks = &fakeTasks{writes: map[string]string{}}
		outs, err := Execute(t.Context(), plan(snapshot("3.21")), o)
		if err != nil {
			t.Fatal(err)
		}
		if outs[0].Action != "created" || len(pf.MRs) != 1 || remoteHead(t, remote, "pinup/snapshots") == "" {
			t.Fatalf("an image without koh-lock/ lost its bump: %+v", outs)
		}
	})

	t.Run("lock unchanged: dropped", func(t *testing.T) {
		remote, _ := fixture(t)
		seed(t, remote, map[string]string{"koh-lock/x86_64.lock": lock})
		repo := clone(t, remote, "work")
		pf := &platformfake.Platform{}
		o := options(repo, pf)
		tasks := &fakeTasks{writes: map[string]string{"koh-lock/x86_64.lock": lock}}
		o.Tasks = tasks
		p := plan(snapshot("3.21"))
		outs, err := Execute(t.Context(), p, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(outs) != 1 || outs[0].Action != "held" || tasks.ran != 1 || !strings.Contains(outs[0].Message, "koh-lock Containerfile changed none") {
			t.Fatalf("outcomes %+v, ran %d", outs, tasks.ran)
		}
		if len(pf.MRs) != 0 || remoteHead(t, remote, "pinup/snapshots") != "" {
			t.Errorf("a dropped bump was written: %d requests", len(pf.MRs))
		}
		if p.Branches[0].SuppressedBy != model.BlockScopeUnchanged {
			t.Errorf("branch suppressedBy = %q", p.Branches[0].SuppressedBy)
		}
		u := p.Updates[0]
		if len(u.Blocks) != 1 || u.Blocks[0].Reason != model.BlockScopeUnchanged || u.Blocks[0].Org.Rule != 3 || !strings.Contains(u.Blocks[0].Note, "**/koh-lock/**") {
			t.Errorf("the update does not say why: %+v", u.Blocks)
		}
		if st, _ := repo.Status(t.Context()); st != "" {
			t.Errorf("the dropped edit was left in the checkout: %q", st)
		}
	})

	t.Run("lock moved: written with the edit", func(t *testing.T) {
		remote, _ := fixture(t)
		seed(t, remote, map[string]string{"koh-lock/x86_64.lock": lock})
		repo := clone(t, remote, "work")
		pf := &platformfake.Platform{}
		o := options(repo, pf)
		o.Tasks = &fakeTasks{writes: map[string]string{"koh-lock/x86_64.lock": "alpine-base 3.21.0-r0\n"}}
		outs, err := Execute(t.Context(), plan(snapshot("3.21")), o)
		if err != nil {
			t.Fatal(err)
		}
		if outs[0].Action != "created" || len(pf.MRs) != 1 {
			t.Fatalf("a moved lock was dropped: %+v", outs)
		}
	})

	t.Run("koh-lock failed: a failure, not a drop", func(t *testing.T) {
		remote, _ := fixture(t)
		seed(t, remote, map[string]string{"koh-lock/x86_64.lock": lock})
		repo := clone(t, remote, "work")
		pf := &platformfake.Platform{}
		o := options(repo, pf)
		o.Tasks = &fakeTasks{writes: map[string]string{}, fail: errors.New("koh-lock: snapshot unreachable")}
		p := plan(snapshot("3.21"))
		outs, _ := Execute(t.Context(), p, o)
		if outs[0].Action != "failed" || p.Branches[0].SuppressedBy != model.BlockPublishFailed {
			t.Fatalf("a failed task read as %+v / %q", outs, p.Branches[0].SuppressedBy)
		}
	})

	t.Run("an open request is left as it is", func(t *testing.T) {
		remote, _ := fixture(t)
		seed(t, remote, map[string]string{"koh-lock/x86_64.lock": lock})
		repo := clone(t, remote, "work")
		pf := &platformfake.Platform{}
		o := options(repo, pf)
		o.Tasks = &fakeTasks{writes: map[string]string{"koh-lock/x86_64.lock": "alpine-base 3.21.0-r0\n"}}
		if outs, err := Execute(t.Context(), plan(snapshot("3.21")), o); err != nil || outs[0].Action != "created" {
			t.Fatalf("first snapshot: %+v %v", outs, err)
		}
		head := remoteHead(t, remote, "pinup/snapshots")
		title := pf.MRs[0].Title

		// The next snapshot moves no package of this image.
		o2 := options(clone(t, remote, "work2"), pf)
		o2.Tasks = &fakeTasks{writes: map[string]string{"koh-lock/x86_64.lock": lock}}
		o2.Prune = true
		pf.Calls = nil
		p := plan(snapshot("3.22"))
		outs, err := Execute(t.Context(), p, o2)
		if err != nil {
			t.Fatal(err)
		}
		if len(outs) != 1 || outs[0].Action != "held" || outs[0].MRIID != pf.MRs[0].IID {
			t.Fatalf("outcomes %+v", outs)
		}
		if got := remoteHead(t, remote, "pinup/snapshots"); got != head {
			t.Errorf("the open request's branch was pushed: %s -> %s", head, got)
		}
		if slices.ContainsFunc(pf.Calls, func(c string) bool { return strings.HasPrefix(c, "Update ") }) {
			t.Errorf("the open request was updated: %v", pf.Calls)
		}
		if pf.MRs[0].State != "opened" || pf.MRs[0].Title != title {
			t.Errorf("the open request was touched: %+v", pf.MRs[0])
		}
	})
}
