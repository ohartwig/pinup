// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gittagsds

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/lookup"
)

// testEnv pins identity and disables any global/system git config, the same
// way git/git_test.go does, so these tests behave the same on every
// machine that runs them.
var testEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
	"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), testEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixtureRepo creates a repository with one commit and three tags: one
// annotated, two lightweight. It is used directly as a `file://` remote -
// git's file transport reads a working repository's refs and objects the
// same way it would a bare one, and this is what a real "public repository
// with tags" looks like from a caller's side.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "add", "f.txt")
	mustRun(t, dir, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "init")
	mustRun(t, dir, "tag", "-a", "v1.0.0", "-m", "annotated release")
	mustRun(t, dir, "tag", "v1.1.0")
	mustRun(t, dir, "tag", "v1.2.0")
	return dir
}

// refTruth returns, for every tag ref, exactly the object it points to -
// the tag object for an annotated tag, the commit for a lightweight one.
// That is precisely what `git ls-remote --tags --refs` reports (the --refs
// flag drops the peeled "^{}" lines that would otherwise also show the
// commit an annotated tag's object wraps), so it is the independent oracle
// this test compares the datasource's parsing against.
func refTruth(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := mustRun(t, dir, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/tags")
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		name, sha, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("unparsable for-each-ref line: %q", line)
		}
		got[name] = sha
	}
	return got
}

func TestThreeTagsAnnotatedAndLightweight(t *testing.T) {
	needGit(t)
	dir := fixtureRepo(t)
	truth := refTruth(t, dir)
	if len(truth) != 3 {
		t.Fatalf("fixture set up %d tags, want 3", len(truth))
	}

	ds := newLocal()
	rs, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "git-tags", PackageName: "file://" + dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Releases) != 3 {
		t.Fatalf("got %d releases, want 3: %+v", len(rs.Releases), rs.Releases)
	}

	got := map[string]string{}
	for _, r := range rs.Releases {
		if !r.Timestamp.IsZero() {
			t.Errorf("%s carries a timestamp; git ls-remote has none to give", r.Version)
		}
		got[r.Version] = r.Digest
	}
	for name, sha := range truth {
		if got[name] != sha {
			t.Errorf("release %s: digest = %q, want %q (git for-each-ref's objectname)", name, got[name], sha)
		}
	}

	// The documented surprise: the annotated tag's digest is the tag
	// OBJECT's sha, not the commit it wraps. --refs is exactly what drops
	// the peeled "^{}" line that would have given the commit instead.
	commitSHA := mustRun(t, dir, "rev-parse", "v1.0.0^{commit}")
	if got["v1.0.0"] == commitSHA {
		t.Errorf("v1.0.0's digest equals the peeled commit %s; --refs should have reported the tag object instead", commitSHA)
	}
	if got["v1.0.0"] != truth["v1.0.0"] {
		t.Errorf("v1.0.0's digest = %q, want the tag object %q", got["v1.0.0"], truth["v1.0.0"])
	}
}

func TestSourceURLStripsDotGitForHTTPS(t *testing.T) {
	needGit(t)
	// The real network call is refused by never happening: lsRemote is
	// stubbed so this test exercises only the URL bookkeeping, not a live
	// https remote.
	ds := &Datasource{allowFile: true, lsRemote: func(context.Context, string, time.Duration, string, bool) (string, error) {
		return "", nil
	}}
	rs, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "git-tags", PackageName: "https://git.ole-hartwig.eu/devops/gitops/manifests.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://git.ole-hartwig.eu/devops/gitops/manifests"; rs.SourceURL != want {
		t.Errorf("sourceUrl = %q, want %q", rs.SourceURL, want)
	}
}

func TestSourceURLIsEmptyForNonHTTPSSchemes(t *testing.T) {
	dir := t.TempDir()
	ds := &Datasource{allowFile: true, lsRemote: func(context.Context, string, time.Duration, string, bool) (string, error) {
		return "", nil
	}}
	rs, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "git-tags", PackageName: "file://" + dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rs.SourceURL != "" {
		t.Errorf("sourceUrl = %q, want empty - only measured for https://", rs.SourceURL)
	}
}

func TestANonexistentPathIsAnError(t *testing.T) {
	needGit(t)
	ds := newLocal()
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "git-tags", PackageName: "file:///no/such/path/on/this/machine",
	})
	if err == nil {
		t.Fatal("a nonexistent repository resolved")
	}
	if !strings.Contains(err.Error(), "no/such/path") {
		t.Errorf("error does not name the URL: %q", err)
	}
}

// countingRunner lets a test assert that git was never invoked, which is
// the whole point of declining before running anything.
func countingRunner(calls *int) func(context.Context, string, time.Duration, string, bool) (string, error) {
	return func(context.Context, string, time.Duration, string, bool) (string, error) {
		*calls++
		return "", nil
	}
}

func TestAnSSHURLIsDeclinedWithoutInvokingGit(t *testing.T) {
	calls := 0
	ds := &Datasource{allowFile: true, lsRemote: countingRunner(&calls)}
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "git-tags", PackageName: "ssh://git@git.ole-hartwig.eu/devops/gitops/manifests.git",
	})
	var declined *lookup.DeclinedError
	if !errors.As(err, &declined) {
		t.Fatalf("got %v (%T), want a *lookup.DeclinedError", err, err)
	}
	if calls != 0 {
		t.Errorf("git was invoked %d times; an ssh:// URL must be declined before running anything", calls)
	}
	if !strings.Contains(declined.Reason, "ssh://") {
		t.Errorf("reason does not say why: %q", declined.Reason)
	}
}

func TestARedactedURLIsDeclinedWithoutInvokingGit(t *testing.T) {
	calls := 0
	ds := &Datasource{allowFile: true, lsRemote: countingRunner(&calls)}
	_, err := ds.Releases(context.Background(), lookup.Ref{
		Datasource: "git-tags", PackageName: "https://**redacted**@git.ole-hartwig.eu/devops/gitops/manifests.git",
	})
	var declined *lookup.DeclinedError
	if !errors.As(err, &declined) {
		t.Fatalf("got %v (%T), want a *lookup.DeclinedError", err, err)
	}
	if calls != 0 {
		t.Errorf("git was invoked %d times; a redacted URL must be declined before running anything", calls)
	}
}

func TestRedactURLMasksCredentialsInText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://user:secret@git.example/x.git", "https://***@git.example/x.git"},
		{"fatal: repository 'https://user:secret@git.example/x.git' not found", "fatal: repository 'https://***@git.example/x.git' not found"},
		{"https://git.example/x.git", "https://git.example/x.git"},
		{"plain text, no url", "plain text, no url"},
	} {
		if got := redactURL(c.in); got != c.want {
			t.Errorf("redactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultVersioningIsSemver(t *testing.T) {
	ds := newLocal()
	if v := ds.DefaultVersioning(); v != "semver" {
		t.Errorf("DefaultVersioning() = %q, want semver", v)
	}
	if ds.Name() != "git-tags" {
		t.Errorf("Name() = %q, want git-tags", ds.Name())
	}
}

func TestGitRefsListsBranchesAndTagsAndAnswersDigests(t *testing.T) {
	needGit(t)
	dir := fixtureRepo(t)
	mustRun(t, dir, "branch", "release")
	head := strings.TrimSpace(mustRun(t, dir, "rev-parse", "HEAD"))

	ds := newLocalKind(Refs)
	if ds.Name() != "git-refs" {
		t.Fatalf("name = %q", ds.Name())
	}
	ref := lookup.Ref{Datasource: "git-refs", PackageName: "file://" + dir}
	rs, err := ds.Releases(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rs.Releases {
		got[r.Version] = r.Digest
	}
	for _, want := range []string{"main", "release", "v1.0.0", "v1.1.0", "v1.2.0"} {
		if _, ok := got[want]; !ok {
			t.Errorf("git-refs is missing %q; got %v", want, rs.Releases)
		}
	}
	if got["main"] != head {
		t.Errorf("main's digest = %q, want HEAD %q", got["main"], head)
	}

	digest, err := ds.Digest(context.Background(), ref, "release")
	if err != nil {
		t.Fatal(err)
	}
	if digest != head {
		t.Errorf("Digest(release) = %q, want %q", digest, head)
	}
	if _, err := ds.Digest(context.Background(), ref, "no-such-branch"); err == nil {
		t.Error("an unknown ref resolved to a digest")
	}
}

func TestGitTagsStillIgnoresBranches(t *testing.T) {
	needGit(t)
	dir := fixtureRepo(t)
	rs, err := newLocal().Releases(context.Background(), lookup.Ref{Datasource: "git-tags", PackageName: "file://" + dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs.Releases {
		if r.Version == "main" {
			t.Fatal("git-tags listed the branch main")
		}
	}
}

func newLocalKind(k Kind) *Datasource {
	d := NewKind(k)
	d.allowFile = true
	return d
}

// newLocal is the datasource over file:// fixtures, which a configuration
// can never name.
func newLocal() *Datasource {
	d := New()
	d.allowFile = true
	return d
}

// A value that is not an http(s) URL never reaches git: a leading dash
// would be an option, a path a local repository.
func TestOnlyHTTPURLsReachGit(t *testing.T) {
	for _, bad := range []string{"--upload-pack=touch /tmp/x", "/etc", "file:///tmp/x", "ext::sh -c id", "git://example.org/x"} {
		invoked := false
		d := New()
		d.lsRemote = func(context.Context, string, time.Duration, string, bool) (string, error) {
			invoked = true
			return "", nil
		}
		_, err := d.Releases(context.Background(), lookup.Ref{Datasource: "git-tags", PackageName: bad})
		var declined *lookup.DeclinedError
		if !errors.As(err, &declined) || invoked {
			t.Errorf("%q: err=%v invoked=%v", bad, err, invoked)
		}
	}
}
