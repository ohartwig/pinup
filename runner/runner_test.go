// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/fake/platformfake"
	"git.ole-hartwig.eu/pinup/pinup/git"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/publish"
)

var (
	now     = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	testEnv = []string{
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
	}
)

const containerfile = "FROM alpine:3.20\nFROM golang:1.26\n"

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), testEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// fixture: a bare remote with a Containerfile on main, and a clone.
func fixture(t *testing.T) (remote string, repo *git.Repo) {
	t.Helper()
	if err := git.Available(); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	mustGit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	mustGit(t, root, "init", "--quiet", "--initial-branch=main", seed)
	os.WriteFile(filepath.Join(seed, "Containerfile"), []byte(containerfile), 0o644)
	mustGit(t, seed, "add", "Containerfile")
	mustGit(t, seed, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "init")
	mustGit(t, seed, "push", "--quiet", remote, "main")
	r, err := git.Clone(context.Background(), remote, filepath.Join(root, "work"), 0, testEnv)
	if err != nil {
		t.Fatal(err)
	}
	r.Env = testEnv
	return remote, r
}

func edit(old, new string) model.Edit {
	i := strings.Index(containerfile, old)
	return model.Edit{File: "Containerfile", Start: i, End: i + len(old), Old: old, New: new, Manager: "dockerfile"}
}

func plan(branches ...model.Branch) *model.Plan {
	p := &model.Plan{SchemaVersion: model.SchemaVersion, Branches: branches}
	for _, b := range branches {
		for _, k := range b.UpdateKeys {
			p.Updates = append(p.Updates, model.Update{DepKey: k, NewValue: "x"})
			p.Deps = append(p.Deps, model.Dependency{DepName: k, CustomManager: model.NoCustomManager})
		}
	}
	return p
}

func options(repo *git.Repo, pf *platformfake.Platform) Options {
	return Options{
		Repo: repo, Remote: "origin", Base: "main", Identity: git.Identity{Name: "pinup", Email: "pinup@example.invalid"},
		Platform: pf, Labels: []string{"renovate"}, Footer: "by pinup", Now: now,
	}
}

func TestCreatesThenUpdatesTheSameMergeRequest(t *testing.T) {
	remote, repo := fixture(t)
	pf := &platformfake.Platform{}
	ctx := context.Background()
	p := plan(model.Branch{
		Name: "renovate/alpine-3.x", Title: "chore(deps): update alpine docker tag to v3.21",
		UpdateKeys: []string{"Containerfile|alpine|3.20"}, Edits: []model.Edit{edit("3.20", "3.21")}, Automerge: true,
	})
	outs, err := Execute(ctx, p, options(repo, pf))
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Action != "created" || outs[0].MRIID != 1 {
		t.Fatalf("first run: %+v", outs)
	}
	if p.Branches[0].Existing == nil || p.Branches[0].Existing.MRIID != 1 {
		t.Errorf("the plan must record the merge request: %+v", p.Branches[0].Existing)
	}
	if mr := pf.MRs[0]; !mr.Automerge || mr.Labels[0] != "renovate" || !strings.Contains(mr.Description, "by pinup") || !strings.Contains(mr.Description, "`3.20` → `3.21`") {
		t.Errorf("merge request %+v", mr)
	}

	// The remote branch carries the edit.
	check := filepath.Join(filepath.Dir(repo.Dir), "check")
	mustGit(t, filepath.Dir(repo.Dir), "clone", "--quiet", "--branch", "renovate/alpine-3.x", remote, check)
	if got, _ := os.ReadFile(filepath.Join(check, "Containerfile")); string(got) != "FROM alpine:3.21\nFROM golang:1.26\n" {
		t.Errorf("remote branch content %q", got)
	}

	// Second run, same plan, fresh clone: no second merge request, no
	// new commit, the request is unchanged.
	repo2, _ := git.Clone(ctx, remote, filepath.Join(filepath.Dir(repo.Dir), "work2"), 0, testEnv)
	repo2.Env = testEnv
	p2 := plan(p.Branches[0])
	p2.Branches[0].Existing = nil
	outs, err = Execute(ctx, p2, options(repo2, pf))
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Action != "unchanged" || outs[0].MRIID != 1 || len(pf.MRs) != 1 {
		t.Fatalf("second run: %+v, MRs %d", outs, len(pf.MRs))
	}

	// A third run with a new title updates the request in place.
	repo3, _ := git.Clone(ctx, remote, filepath.Join(filepath.Dir(repo.Dir), "work3"), 0, testEnv)
	repo3.Env = testEnv
	p3 := plan(p.Branches[0])
	p3.Branches[0].Title = "chore(deps): update alpine docker tag to v3.21 (retitled)"
	outs, _ = Execute(ctx, p3, options(repo3, pf))
	if outs[0].Action != "updated" || !strings.Contains(outs[0].Message, "title") || len(pf.MRs) != 1 {
		t.Errorf("third run: %+v", outs)
	}
}

func TestHourlyLimitHoldsWithARecord(t *testing.T) {
	_, repo := fixture(t)
	pf := &platformfake.Platform{}
	p := plan(
		model.Branch{Name: "renovate/a", Title: "a", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit("3.20", "3.21")}},
		model.Branch{Name: "renovate/b", Title: "b", UpdateKeys: []string{"b"}, Edits: []model.Edit{edit("1.26", "1.27")}},
	)
	o := options(repo, pf)
	o.HourlyLimit = 1
	outs, err := Execute(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if outs[0].Action != "created" || outs[1].Action != "held" {
		t.Fatalf("outcomes %+v", outs)
	}
	held := p.Updates[1]
	if len(held.Blocks) != 1 || held.Blocks[0].Reason != model.BlockHourlyLimit || held.SuppressedBy != model.BlockHourlyLimit || held.Blocks[0].Until.IsZero() {
		t.Errorf("the held update must carry an hourlyLimit block with a thaw time: %+v", held)
	}
	if len(p.Branches[1].Edits) != 0 {
		t.Error("a held branch must carry no edits")
	}
	if len(pf.MRs) != 1 {
		t.Errorf("%d merge requests, want 1", len(pf.MRs))
	}
}

func TestAFailingBranchDoesNotStopTheOthers(t *testing.T) {
	_, repo := fixture(t)
	pf := &platformfake.Platform{CreateErr: errors.New("403 cannot create")}
	p := plan(
		model.Branch{Name: "renovate/a", Title: "a", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit("3.20", "3.21")}},
		model.Branch{Name: "renovate/b", Title: "b", UpdateKeys: []string{"b"}, Edits: []model.Edit{edit("1.26", "1.27")}},
	)
	outs, err := Execute(context.Background(), p, options(repo, pf))
	if err != nil {
		t.Fatal(err)
	}
	if outs[0].Action != "failed" || outs[1].Action != "failed" {
		t.Fatalf("outcomes %+v", outs)
	}
	if len(p.Warnings) != 2 || !strings.Contains(p.Warnings[0].Msg, "renovate/a") || !strings.Contains(p.Warnings[1].Msg, "403") {
		t.Errorf("each failure is a warning naming the branch: %+v", p.Warnings)
	}
}

// A branch somebody committed to is theirs: the run neither rebuilds it
// nor touches its merge request, and says whose it is.
func TestABranchWithForeignCommitsIsLeftAlone(t *testing.T) {
	remote, repo := fixture(t)
	pf := &platformfake.Platform{}
	ctx := context.Background()
	p := plan(model.Branch{Name: "renovate/alpine-3.x", Title: "t", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit("3.20", "3.21")}})
	if _, err := Execute(ctx, p, options(repo, pf)); err != nil {
		t.Fatal(err)
	}
	// A person pushes a commit onto the branch.
	person := filepath.Join(filepath.Dir(repo.Dir), "person")
	mustGit(t, filepath.Dir(repo.Dir), "clone", "--quiet", "--branch", "renovate/alpine-3.x", remote, person)
	os.WriteFile(filepath.Join(person, "NOTE"), []byte("mine\n"), 0o644)
	mustGit(t, person, "add", "NOTE")
	mustGit(t, person, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "a person's fix")
	mustGit(t, person, "push", "--quiet", "origin", "renovate/alpine-3.x")
	theirs := headOf(t, person)

	repo2, _ := git.Clone(ctx, remote, filepath.Join(filepath.Dir(repo.Dir), "work2"), 0, testEnv)
	repo2.Env = testEnv
	p2 := plan(p.Branches[0])
	outs, _ := Execute(ctx, p2, options(repo2, pf))
	if outs[0].Action != "failed" || !strings.Contains(outs[0].Message, "fixture@example.invalid") {
		t.Fatalf("outcome %+v", outs[0])
	}
	if got, _, _ := repo2.RemoteBranch(ctx, "origin", "renovate/alpine-3.x"); got != theirs {
		t.Error("the person's branch was rewritten")
	}
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// prConcurrentLimit counts what is already open: with two open and a limit
// of three, one more is created and the next is held with a record.
func TestConcurrentLimitCountsOpenRequests(t *testing.T) {
	_, repo := fixture(t)
	pf := &platformfake.Platform{MRs: []publish.MergeRequest{
		{IID: 1, State: "opened", SourceBranch: "renovate/old-1.x"},
		{IID: 2, State: "opened", SourceBranch: "renovate/old-2.x"},
		{IID: 3, State: "merged", SourceBranch: "renovate/gone-1.x"},
	}}
	p := plan(
		model.Branch{Name: "renovate/a", Title: "a", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit("3.20", "3.21")}},
		model.Branch{Name: "renovate/b", Title: "b", UpdateKeys: []string{"b"}, Edits: []model.Edit{edit("1.26", "1.27")}},
	)
	o := options(repo, pf)
	o.ConcurrentLimit = 3
	outs, err := Execute(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if outs[0].Action != "created" || outs[1].Action != "held" || outs[1].Message != "concurrent limit" {
		t.Fatalf("outcomes %+v", outs)
	}
	if held := p.Updates[1]; len(held.Blocks) != 1 || held.Blocks[0].Reason != model.BlockConcurrentLimit || !strings.Contains(held.Blocks[0].Note, "3 merge requests already open") {
		t.Errorf("held update %+v", held)
	}
}
