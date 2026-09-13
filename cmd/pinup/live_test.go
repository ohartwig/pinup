// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/fake/platformfake"
	"git.ole-hartwig.eu/pinup/pinup/git"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
)

// cannedDocker answers digests too, as a registry does: the runner's
// configuration pins digests, and an image without one is pinned first.
type cannedDocker struct{ cannedDS }

func (c cannedDocker) Digest(_ context.Context, _ lookup.Ref, version string) (string, error) {
	return "sha256:" + strings.Repeat(version[len(version)-1:], 64), nil
}

// The live path, end to end and without a network: a checkout with a
// remote, a canned registry, a fake platform. The first run pushes a
// branch, opens the merge request and writes the dashboard; the second
// run, with the dashboard's rebase box ticked, pushes the branch again
// and updates the request. This is the path the first live runs of
// 2026-09-13 exercised on the instance instead - the 409 after a rebase
// push and the missing cache directory were both found there.
func TestRunProjectPushesOpensAndRebasesOnRequest(t *testing.T) {
	if err := git.Available(); err != nil {
		t.Skip(err)
	}
	// runProject opens the checkout itself, so the hermetic git environment
	// goes into the process environment, not a Repo's Env.
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
		"GIT_AUTHOR_NAME": "fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
		"GIT_COMMITTER_NAME": "fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid",
	} {
		t.Setenv(k, v)
	}
	// Commit dates follow the run's clock: two runs within one second would
	// otherwise produce the same commit for the same edit.
	clock := func(at time.Time) {
		t.Setenv("GIT_AUTHOR_DATE", at.Format(time.RFC3339))
		t.Setenv("GIT_COMMITTER_DATE", at.Format(time.RFC3339))
	}
	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	mustGit(root, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	mustGit(root, "init", "--quiet", "--initial-branch=main", seed)
	os.WriteFile(filepath.Join(seed, "Containerfile"), []byte("FROM alpine:3.20\n"), 0o644)
	mustGit(seed, "add", "Containerfile")
	mustGit(seed, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "init")
	mustGit(seed, "push", "--quiet", remote, "main")
	work := filepath.Join(root, "work")
	repo, err := git.Clone(context.Background(), remote, work, git.CloneOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	pf := &platformfake.Platform{Verification: "verified"}
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC) // the 4-hourly window is open
	o := &runOptions{
		cfgPath: "../../testdata/parity/config/default.json", dryRun: false,
		platform: pf, now: at, dashboardTitle: "pinup Dashboard",
		identity:    git.Identity{Name: "fixture", Email: "fixture@example.invalid"},
		datasources: lookup.Registry{"docker": cannedDocker{cannedDS{name: "docker", scheme: "docker", releases: map[string][]string{"alpine": {"3.20", "3.21"}}}}},
	}
	var out, errw strings.Builder
	clock(o.now)
	if err := runProject(context.Background(), o, "", work, "", &out, &errw); err != nil {
		t.Fatalf("first run: %v\n%s", err, errw.String())
	}
	if len(pf.MRs) != 1 || pf.MRs[0].SourceBranch != "renovate/pin-dependencies" {
		t.Fatalf("merge requests after the first run: %+v\n%s%s", pf.MRs, out.String(), errw.String())
	}
	// An image without a digest is pinned first, as the runner's
	// configuration says; the 3.21 comes on the next run after the pin.
	if !strings.Contains(pf.MRs[0].Description, "| `alpine` | pinDigest | `3.20` → `3.20@sha256:000000000000` |") {
		t.Errorf("description:\n%s", pf.MRs[0].Description)
	}
	body := pf.IssueBodies["pinup Dashboard"]
	if !strings.Contains(body, "<!-- rebase-branch=renovate/pin-dependencies -->") || !strings.Contains(body, "(!1)") {
		t.Errorf("dashboard after the first run:\n%s", body)
	}
	head1, ok, err := repo.RemoteBranch(context.Background(), "origin", "renovate/pin-dependencies")
	if err != nil || !ok {
		t.Fatalf("the branch was not pushed: %v", err)
	}

	// Second run, nothing changed: the branch is left alone.
	o.now = at.Add(4 * time.Hour) // 20:05 Berlin, the window is open again
	clock(o.now)
	if err := runProject(context.Background(), o, "", work, "", &out, &errw); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if head2, _, _ := repo.RemoteBranch(context.Background(), "origin", "renovate/pin-dependencies"); head2 != head1 {
		t.Errorf("an unchanged branch was pushed again: %s -> %s", head1, head2)
	}

	// Third run, the rebase box ticked on the dashboard: the branch is
	// rebuilt and pushed, the request updated, the box cleared.
	pf.IssueBodies["pinup Dashboard"] = strings.Replace(body, "- [ ] <!-- rebase-branch=renovate/pin-dependencies -->", "- [x] <!-- rebase-branch=renovate/pin-dependencies -->", 1)
	o.now = at.Add(8 * time.Hour) // 00:05 Berlin
	clock(o.now)
	if err := runProject(context.Background(), o, "", work, "", &out, &errw); err != nil {
		t.Fatalf("third run: %v", err)
	}
	head3, _, _ := repo.RemoteBranch(context.Background(), "origin", "renovate/pin-dependencies")
	if head3 == head1 {
		t.Errorf("the ticked rebase did not push: %s", head3)
	}
	if len(pf.MRs) != 1 {
		t.Errorf("a rebase must update the request, not open another: %+v", pf.MRs)
	}
	if strings.Contains(pf.IssueBodies["pinup Dashboard"], "[x]") {
		t.Errorf("a ticked box must come back cleared:\n%s", pf.IssueBodies["pinup Dashboard"])
	}
	if !strings.Contains(out.String(), "created   renovate/pin-dependencies !1") || !strings.Contains(out.String(), "updated   renovate/pin-dependencies !1") {
		t.Errorf("summary lines:\n%s", out.String())
	}
}
