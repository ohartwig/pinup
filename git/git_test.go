// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests drive real git against local bare repositories: a bare "remote"
// and two clones, one playing pinup and one playing a person who pushes in
// between. Nothing leaves the machine.

var testEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
	"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
}

func needGit(t *testing.T) {
	t.Helper()
	if err := Available(); err != nil {
		t.Skip(err)
	}
}

// fixture creates a bare remote with one commit on main and returns its
// path and a clone.
func fixture(t *testing.T) (remote string, clone *Repo) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	mustRun(t, root, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	mustRun(t, root, "init", "--quiet", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "Containerfile"), []byte("FROM alpine:3.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, seed, "add", "Containerfile")
	mustRun(t, seed, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "init")
	mustRun(t, seed, "push", "--quiet", remote, "main")
	c, err := Clone(ctx, remote, filepath.Join(root, "pinup"), 0, testEnv)
	if err != nil {
		t.Fatal(err)
	}
	c.Env = testEnv
	return remote, c
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), testEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestCommitAndPushANewBranch(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote, r := fixture(t)
	existed, err := r.Adopt(ctx, "origin", "renovate/alpine-3.x", "origin/main")
	if err != nil || existed {
		t.Fatalf("adopt: existed=%v err=%v", existed, err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "Containerfile"), []byte("FROM alpine:3.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, ok, err := r.Commit(ctx, Identity{"pinup", "pinup@example.invalid"}, Signing{}, "chore(deps): update alpine docker tag to v3.21", "Containerfile")
	if err != nil || !ok {
		t.Fatalf("commit: ok=%v err=%v", ok, err)
	}
	if err := r.Push(ctx, "origin", "renovate/alpine-3.x", ""); err != nil {
		t.Fatal(err)
	}
	got, ok, err := r.RemoteBranch(ctx, "origin", "renovate/alpine-3.x")
	if err != nil || !ok || got != sha {
		t.Errorf("remote branch = %q ok=%v err=%v, want %q", got, ok, err, sha)
	}
	// Nothing changed: no commit.
	if _, ok, err := r.Commit(ctx, Identity{"pinup", "pinup@example.invalid"}, Signing{}, "again", "Containerfile"); err != nil || ok {
		t.Errorf("a second commit with nothing staged: ok=%v err=%v", ok, err)
	}
	_ = remote
}

// The P1c.2 acceptance: an existing renovate branch is adopted and rebased
// onto the moved base, not duplicated; and a push is refused when the
// remote branch moved since the plan.
func TestAdoptRebasesAndForceWithLeaseRefusesAMovedRemote(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote, r := fixture(t)

	// A previous run pushed renovate/alpine-3.x with one commit.
	if _, err := r.Adopt(ctx, "origin", "renovate/alpine-3.x", "origin/main"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.Dir, "Containerfile"), []byte("FROM alpine:3.21\n"), 0o644)
	first, _, err := r.Commit(ctx, Identity{"pinup", "pinup@example.invalid"}, Signing{}, "first", "Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, "origin", "renovate/alpine-3.x", ""); err != nil {
		t.Fatal(err)
	}

	// Meanwhile main moved: a person merged something else.
	person := filepath.Join(filepath.Dir(r.Dir), "person")
	mustRun(t, filepath.Dir(r.Dir), "clone", "--quiet", remote, person)
	os.WriteFile(filepath.Join(person, "README"), []byte("hello\n"), 0o644)
	mustRun(t, person, "add", "README")
	mustRun(t, person, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "readme")
	mustRun(t, person, "push", "--quiet", "origin", "main")

	// The next run, in a fresh clone: adopt the existing branch, rebased
	// onto the new main, and add a commit.
	next, err := Clone(ctx, remote, filepath.Join(filepath.Dir(r.Dir), "next"), 0, testEnv)
	if err != nil {
		t.Fatal(err)
	}
	next.Env = testEnv
	existed, err := next.Adopt(ctx, "origin", "renovate/alpine-3.x", "origin/main")
	if err != nil || !existed {
		t.Fatalf("adopt existing: existed=%v err=%v", existed, err)
	}
	if _, err := os.Stat(filepath.Join(next.Dir, "README")); err != nil {
		t.Error("the adopted branch was not rebased onto the moved main")
	}
	if got, _ := os.ReadFile(filepath.Join(next.Dir, "Containerfile")); string(got) != "FROM alpine:3.21\n" {
		t.Errorf("the adopted branch lost its commit: %q", got)
	}
	os.WriteFile(filepath.Join(next.Dir, "Containerfile"), []byte("FROM alpine:3.22\n"), 0o644)
	if _, _, err := next.Commit(ctx, Identity{"pinup", "pinup@example.invalid"}, Signing{}, "second", "Containerfile"); err != nil {
		t.Fatal(err)
	}
	// The rebase rewrote history, so the push must be forced - with a
	// lease on what the remote held when the run started.
	if err := next.Push(ctx, "origin", "renovate/alpine-3.x", first); err != nil {
		t.Fatalf("force-with-lease against the expected sha: %v", err)
	}

	// A third run plans against the branch, but somebody pushes to it in
	// between: the lease is stale and the push must be refused.
	third, _ := Clone(ctx, remote, filepath.Join(filepath.Dir(r.Dir), "third"), 0, testEnv)
	third.Env = testEnv
	if _, err := third.Adopt(ctx, "origin", "renovate/alpine-3.x", "origin/main"); err != nil {
		t.Fatal(err)
	}
	planned, _ := third.Head(ctx)
	mustRun(t, person, "fetch", "--quiet", "origin")
	mustRun(t, person, "checkout", "--quiet", "-B", "renovate/alpine-3.x", "origin/renovate/alpine-3.x")
	os.WriteFile(filepath.Join(person, "NOTE"), []byte("mine\n"), 0o644)
	mustRun(t, person, "add", "NOTE")
	mustRun(t, person, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "a person's commit")
	mustRun(t, person, "push", "--quiet", "origin", "renovate/alpine-3.x")

	os.WriteFile(filepath.Join(third.Dir, "Containerfile"), []byte("FROM alpine:3.23\n"), 0o644)
	third.Commit(ctx, Identity{"pinup", "pinup@example.invalid"}, Signing{}, "third", "Containerfile")
	err = third.Push(ctx, "origin", "renovate/alpine-3.x", planned)
	if err == nil || !strings.Contains(err.Error(), "moved since the plan") {
		t.Fatalf("a moved remote must refuse the push, got %v", err)
	}
	if got, _, _ := third.RemoteBranch(ctx, "origin", "renovate/alpine-3.x"); got == planned {
		t.Error("the remote was overwritten")
	}
}

// The P1c.1 acceptance, the half that needs no platform: a mismatch between
// the author and the key's principal is refused by name before any commit
// is made, and a matching one signs a commit git itself verifies.
func TestSSHSigningIdentityCheckAndVerify(t *testing.T) {
	needGit(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	ctx := context.Background()
	_, r := fixture(t)
	keyDir := t.TempDir()
	key := filepath.Join(keyDir, "id")
	mustRunPlain(t, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "pinup signing", "-f", key)
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keyDir, "allowed_signers")
	os.WriteFile(signers, []byte("pinup@example.invalid namespaces=\"git\" "+string(pub)), 0o644)
	sign := Signing{Format: "ssh", Key: key + ".pub", AllowedSigners: signers}
	// The private key must sit next to the public one for git to sign.

	err := CheckIdentity(ctx, Identity{"someone", "someone-else@example.invalid"}, sign)
	if err == nil || !strings.Contains(err.Error(), "pinup@example.invalid") || !strings.Contains(err.Error(), "someone-else@example.invalid") {
		t.Fatalf("a mismatched author must be refused naming both: %v", err)
	}
	if err := CheckIdentity(ctx, Identity{"pinup", "pinup@example.invalid"}, sign); err != nil {
		t.Fatalf("the matching author: %v", err)
	}

	r.Adopt(ctx, "origin", "renovate/x", "origin/main")
	os.WriteFile(filepath.Join(r.Dir, "Containerfile"), []byte("FROM alpine:3.21\n"), 0o644)
	if _, _, err := r.Commit(ctx, Identity{"pinup", "pinup@example.invalid"}, sign, "signed", "Containerfile"); err != nil {
		t.Fatal(err)
	}
	verdict, err := r.VerifyHead(ctx, sign)
	if err != nil || !strings.Contains(verdict, "Good") {
		t.Errorf("git must verify its own signature: %q %v", verdict, err)
	}
	// The wrong author never gets as far as a commit.
	before, _ := r.Head(ctx)
	os.WriteFile(filepath.Join(r.Dir, "Containerfile"), []byte("FROM alpine:3.22\n"), 0o644)
	if _, _, err := r.Commit(ctx, Identity{"x", "someone-else@example.invalid"}, sign, "refused", "Containerfile"); err == nil {
		t.Fatal("a commit with a mismatched author was made")
	}
	if after, _ := r.Head(ctx); after != before {
		t.Error("HEAD moved although the commit was refused")
	}
}

func mustRunPlain(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"fatal: unable to access 'https://oauth2:glpat-secret@git.example.org/x.git/'": "fatal: unable to access 'https://***@git.example.org/x.git/'",
		"https://git.example.org/x.git": "https://git.example.org/x.git",
		"no url here":                   "no url here",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}
