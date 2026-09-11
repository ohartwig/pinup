// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package git drives a repository through the git binary.
//
// Layer 1. Nothing here parses .git on its own: every operation is a git
// invocation with explicit arguments, no shell, and the output read back.
// That is deliberate twice over. Signing is git's, so `git commit -S`
// produces exactly the signature the platform verifies today - the reason
// GPG is not reimplemented. And the branch and push semantics are git's,
// so what a person would do at the prompt and what pinup does are the same
// commands.
//
// The identity check is the one thing added: a commit is refused before
// it is made when the author's email is not one the signing key vouches
// for, because a signed commit whose signature the platform cannot tie to
// its author shows as unverified without saying why.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Identity is who commits.
type Identity struct {
	Name  string
	Email string
}

// Signing configures `git commit -S`.
type Signing struct {
	// Format is "ssh" or "openpgp". Empty means unsigned.
	Format string
	// Key is the signing key: for ssh the path of the public key (or the
	// literal key), for openpgp the key id or fingerprint.
	Key string
	// AllowedSigners is the ssh allowed-signers file mapping principals
	// (emails) to keys; required for ssh so the identity check and
	// verification have something to check against.
	AllowedSigners string
}

// Repo is a checkout.
type Repo struct {
	Dir string
	// Git is the binary to run; empty means "git" on PATH.
	Git string
	// Env is added to every invocation. GIT_* variables for tests, a
	// GIT_ASKPASS for pushes.
	Env []string
}

// ErrNoGit is returned when the git binary cannot be found.
var ErrNoGit = errors.New("git: no git binary on PATH")

// Available reports whether git can be run at all.
func Available() error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrNoGit
	}
	return nil
}

// Open returns a Repo for an existing checkout.
func Open(dir string) (*Repo, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return nil, fmt.Errorf("git: %s is not a checkout: %w", dir, err)
	}
	return &Repo{Dir: dir}, nil
}

// Clone clones url into dir. depth 0 means a full clone.
func Clone(ctx context.Context, url, dir string, depth int, env []string) (*Repo, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	args := []string{"clone", "--quiet"}
	if depth > 0 {
		args = append(args, "--depth", fmt.Sprint(depth), "--no-single-branch")
	}
	args = append(args, "--", url, dir)
	r := &Repo{Dir: filepath.Dir(dir), Env: env}
	if _, err := r.run(ctx, args...); err != nil {
		return nil, err
	}
	r.Dir = dir
	return r, nil
}

// run executes git in the repository and returns stdout. stderr is folded
// into the error, with anything that looks like a credential removed.
func (r *Repo) run(ctx context.Context, args ...string) (string, error) {
	return r.runWith(ctx, nil, args...)
}

func (r *Repo) runWith(ctx context.Context, env []string, args ...string) (string, error) {
	bin := r.Git
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.Dir
	cmd.Env = append(append(os.Environ(), r.Env...), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, redact(strings.TrimSpace(errb.String())))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// redact removes anything that looks like a token in a URL.
func redact(s string) string {
	for {
		i := strings.Index(s, "://")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], '@')
		k := strings.IndexAny(s[i+3:], "/ \n")
		if j < 0 || (k >= 0 && j > k+3) {
			return s
		}
		s = s[:i+3] + "***" + s[i+j:]
		if next := strings.Index(s[i+3:], "://"); next < 0 {
			return s
		}
	}
}

// Head returns the current commit.
func (r *Repo) Head(ctx context.Context) (string, error) {
	return r.run(ctx, "rev-parse", "HEAD")
}

// CurrentBranch returns the checked-out branch name.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	return r.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
}

// RemoteBranch returns the commit a remote branch points at, or ok=false
// when the remote has no such branch. It asks the remote, not the local
// tracking ref, so the answer is current.
func (r *Repo) RemoteBranch(ctx context.Context, remote, branch string) (sha string, ok bool, err error) {
	out, err := r.run(ctx, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", false, err
	}
	if out == "" {
		return "", false, nil
	}
	return strings.Fields(out)[0], true, nil
}

// Fetch updates the remote-tracking refs for the named branches.
func (r *Repo) Fetch(ctx context.Context, remote string, branches ...string) error {
	args := []string{"fetch", "--quiet", remote}
	for _, b := range branches {
		args = append(args, "refs/heads/"+b+":refs/remotes/"+remote+"/"+b)
	}
	_, err := r.run(ctx, args...)
	return err
}

// Checkout switches to a branch, creating it from start when it does not
// exist locally. start is a commit or ref.
func (r *Repo) Checkout(ctx context.Context, branch, start string) error {
	if _, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = r.run(ctx, "checkout", "--quiet", branch)
		return err
	}
	_, err := r.run(ctx, "checkout", "--quiet", "-b", branch, start)
	return err
}

// Adopt makes branch the checked-out branch, taking the remote's version
// when it exists and rebasing it onto base - so a branch a person has
// commits on is continued, not replaced - and creating it from base when
// it does not. It reports whether the remote had one.
func (r *Repo) Adopt(ctx context.Context, remote, branch, base string) (existed bool, err error) {
	_, existed, err = r.RemoteBranch(ctx, remote, branch)
	if err != nil {
		return false, err
	}
	if !existed {
		if _, err := r.run(ctx, "checkout", "--quiet", "-B", branch, base); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := r.Fetch(ctx, remote, branch); err != nil {
		return true, err
	}
	if _, err := r.run(ctx, "checkout", "--quiet", "-B", branch, "refs/remotes/"+remote+"/"+branch); err != nil {
		return true, err
	}
	if _, err := r.run(ctx, "rebase", "--quiet", base); err != nil {
		// A rebase that stops has left the tree mid-way; put it back and
		// report, rather than leave a checkout nobody can reason about.
		_, _ = r.run(ctx, "rebase", "--abort")
		return true, fmt.Errorf("git: rebasing %s onto %s conflicts: %w", branch, base, err)
	}
	return true, nil
}

// Recreate makes branch the checked-out branch starting fresh from start,
// discarding any local branch of that name. The runner rebuilds its
// branches from the base on every run - the plan's edits are relative to
// the base - and compares the result with what the remote holds.
func (r *Repo) Recreate(ctx context.Context, branch, start string) error {
	_, err := r.run(ctx, "checkout", "--quiet", "-B", branch, start)
	return err
}

// ForeignAuthors lists the author emails of commits on branch that are not
// on base and were not made by who. A branch a person has committed to is
// theirs now; the runner leaves it alone and says so.
func (r *Repo) ForeignAuthors(ctx context.Context, base, branch string, who Identity) ([]string, error) {
	out, err := r.run(ctx, "log", "--format=%ae", base+".."+branch)
	if err != nil {
		return nil, err
	}
	var foreign []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		email := strings.TrimSpace(line)
		if email == "" || strings.EqualFold(email, who.Email) || seen[email] {
			continue
		}
		seen[email] = true
		foreign = append(foreign, email)
	}
	return foreign, nil
}

// SameTree reports whether two commits have identical trees.
func (r *Repo) SameTree(ctx context.Context, a, b string) (bool, error) {
	ta, err := r.run(ctx, "rev-parse", a+"^{tree}")
	if err != nil {
		return false, err
	}
	tb, err := r.run(ctx, "rev-parse", b+"^{tree}")
	if err != nil {
		return false, err
	}
	return ta == tb, nil
}

// Status returns the porcelain status, empty when the tree is clean.
func (r *Repo) Status(ctx context.Context) (string, error) {
	return r.run(ctx, "status", "--porcelain")
}

// Commit stages paths and commits them as who, signed as sign says. It
// returns the new commit, or ok=false when there was nothing to commit.
//
// The identity check runs first: the author's email must be a principal
// the signing key vouches for. A mismatch is an error naming both.
func (r *Repo) Commit(ctx context.Context, who Identity, sign Signing, message string, paths ...string) (sha string, ok bool, err error) {
	if err := CheckIdentity(ctx, who, sign); err != nil {
		return "", false, err
	}
	if len(paths) == 0 {
		return "", false, errors.New("git: nothing to stage")
	}
	if _, err := r.run(ctx, append([]string{"add", "--"}, paths...)...); err != nil {
		return "", false, err
	}
	staged, err := r.run(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return "", false, err
	}
	if staged == "" {
		return "", false, nil
	}
	// The identity goes in through the environment, which git reads
	// before any config - so a GIT_AUTHOR_EMAIL set by the surrounding
	// job cannot make the commit somebody else's.
	env := []string{
		"GIT_AUTHOR_NAME=" + who.Name, "GIT_AUTHOR_EMAIL=" + who.Email,
		"GIT_COMMITTER_NAME=" + who.Name, "GIT_COMMITTER_EMAIL=" + who.Email,
	}
	var args []string
	switch sign.Format {
	case "":
		args = append(args, "-c", "commit.gpgsign=false")
	case "ssh":
		args = append(args, "-c", "gpg.format=ssh", "-c", "user.signingkey="+sign.Key)
		if sign.AllowedSigners != "" {
			args = append(args, "-c", "gpg.ssh.allowedSignersFile="+sign.AllowedSigners)
		}
	case "openpgp":
		args = append(args, "-c", "gpg.format=openpgp", "-c", "user.signingkey="+sign.Key)
	default:
		return "", false, fmt.Errorf("git: unknown signing format %q", sign.Format)
	}
	args = append(args, "commit", "--quiet", "-m", message)
	if sign.Format != "" {
		args = append(args, "-S")
	}
	if _, err := r.runWith(ctx, env, args...); err != nil {
		return "", false, err
	}
	sha, err = r.Head(ctx)
	return sha, true, err
}

// VerifyHead checks the signature of HEAD with git's own verifier and
// returns git's verdict line. For ssh this needs the allowed-signers file.
func (r *Repo) VerifyHead(ctx context.Context, sign Signing) (string, error) {
	args := []string{}
	if sign.Format == "ssh" && sign.AllowedSigners != "" {
		args = append(args, "-c", "gpg.ssh.allowedSignersFile="+sign.AllowedSigners)
	}
	args = append(args, "verify-commit", "--raw", "HEAD")
	bin := r.Git
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), r.Env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git verify-commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Push pushes branch to remote. When expect is non-empty the push is
// force-with-lease against it: it succeeds only if the remote branch still
// points at expect, so a branch somebody moved since the plan was made is
// left alone with an error rather than overwritten. An empty expect pushes
// without force, which fails on any non-fast-forward.
func (r *Repo) Push(ctx context.Context, remote, branch, expect string) error {
	args := []string{"push", "--quiet"}
	if expect != "" {
		args = append(args, "--force-with-lease=refs/heads/"+branch+":"+expect)
	}
	args = append(args, remote, "refs/heads/"+branch+":refs/heads/"+branch)
	_, err := r.run(ctx, args...)
	if err != nil && strings.Contains(err.Error(), "stale info") {
		return fmt.Errorf("git: refusing to push %s: the remote branch moved since the plan was made", branch)
	}
	if err != nil && strings.Contains(err.Error(), "rejected") {
		return fmt.Errorf("git: push of %s rejected by the remote: %w", branch, err)
	}
	return err
}

// CheckIdentity verifies that the signing key vouches for who. For ssh the
// allowed-signers file must carry a line whose principal is who.Email and
// whose key matches; for openpgp the key's user ids must contain the
// email. Unsigned commits pass.
func CheckIdentity(ctx context.Context, who Identity, sign Signing) error {
	switch sign.Format {
	case "":
		return nil
	case "ssh":
		if sign.AllowedSigners == "" {
			return fmt.Errorf("git: ssh signing needs an allowed-signers file to tie %s to the key", who.Email)
		}
		raw, err := os.ReadFile(sign.AllowedSigners)
		if err != nil {
			return fmt.Errorf("git: allowed signers: %w", err)
		}
		pub, err := publicKey(sign.Key)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || strings.HasPrefix(fields[0], "#") {
				continue
			}
			principals := strings.Split(fields[0], ",")
			// principals [options] keytype base64 [comment]: find the key
			// type, whatever options precede it or comment follows.
			var lineKey string
			for i := 1; i+1 < len(fields); i++ {
				if strings.HasPrefix(fields[i], "ssh-") || strings.HasPrefix(fields[i], "sk-") {
					lineKey = fields[i] + " " + fields[i+1]
					break
				}
			}
			if lineKey != pub {
				continue
			}
			for _, p := range principals {
				if strings.EqualFold(p, who.Email) {
					return nil
				}
			}
			return fmt.Errorf("git: the signing key vouches for %s, not for the author %s; a commit would show as unverified",
				fields[0], who.Email)
		}
		return fmt.Errorf("git: the signing key is not in %s; a commit by %s would show as unverified", sign.AllowedSigners, who.Email)
	case "openpgp":
		out, err := exec.CommandContext(ctx, "gpg", "--batch", "--with-colons", "--list-keys", sign.Key).Output()
		if err != nil {
			return fmt.Errorf("git: gpg key %s: %w", sign.Key, err)
		}
		var uids []string
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "uid:") {
				fields := strings.Split(line, ":")
				if len(fields) > 9 {
					uids = append(uids, fields[9])
				}
			}
		}
		for _, u := range uids {
			if strings.Contains(strings.ToLower(u), "<"+strings.ToLower(who.Email)+">") {
				return nil
			}
		}
		return fmt.Errorf("git: gpg key %s carries the user ids %q, none of which is the author %s; a commit would show as unverified",
			sign.Key, uids, who.Email)
	}
	return fmt.Errorf("git: unknown signing format %q", sign.Format)
}

// publicKey returns "type body" for an ssh key given as a file path or as
// the literal key.
func publicKey(key string) (string, error) {
	text := key
	if !strings.HasPrefix(key, "ssh-") && !strings.HasPrefix(key, "sk-") {
		raw, err := os.ReadFile(key)
		if err != nil {
			return "", fmt.Errorf("git: signing key: %w", err)
		}
		text = string(raw)
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "", fmt.Errorf("git: %q is not an ssh public key", key)
	}
	return fields[0] + " " + fields[1], nil
}
