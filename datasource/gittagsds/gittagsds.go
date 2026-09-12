// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package gittagsds implements the git-tags datasource: the tags of an
// arbitrary git repository, read with `git ls-remote --tags --refs`
// rather than any platform's REST API.
//
// It is the fallback the kustomize manager (and any other manager that
// records a bare repository URL rather than a platform-specific one) uses
// when a dependency names no recognisable host. That is also its limit:
// `git ls-remote` needs a URL git can dial today, from this process,
// without help. package.go's git package drives a checkout that already
// exists; nothing there runs a bare command outside a repository, and
// CLAUDE.md is explicit that nothing is to be added to it for this. So
// this package execs `git` directly, the same way `git/git.go` does
// internally, but without a Repo to run it in.
//
// Only an unauthenticated, publicly reachable repository is supported. The
// runner's job token is not available to this datasource by design - core
// decides, and handing a token to a bare `git ls-remote` invocation for a
// registry the config merely names would be a much larger credential
// exposure than the other datasources carry. A URL this process could only
// reach with a credential - ssh://, or one already redacted by whatever
// recorded it - is therefore declined rather than attempted.
package gittagsds

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// defaultTimeout bounds one `git ls-remote` invocation against a remote
// that never answers.
const defaultTimeout = 30 * time.Second

// Kind is which refs the datasource serves: tags only, or every branch
// and tag (Renovate's git-refs, where a branch is a moving reference whose
// digest is what a dependency pins).
type Kind string

const (
	Tags Kind = "git-tags"
	Refs Kind = "git-refs"
)

// Datasource is the git-tags / git-refs datasource.
type Datasource struct {
	kind Kind
	// Git is the binary to run; empty means "git" on PATH.
	Git string
	// Timeout bounds one invocation. Zero means defaultTimeout.
	Timeout time.Duration

	// lsRemote performs the actual invocation. Tests override it to count
	// calls, or to answer without a git binary at all; production code
	// always gets execLsRemote through New.
	lsRemote func(ctx context.Context, gitBin string, timeout time.Duration, repoURL string, all bool) (string, error)
}

// New returns a git-tags datasource that execs the real git binary.
func New() *Datasource {
	return &Datasource{kind: Tags, lsRemote: execLsRemote}
}

// NewKind returns a datasource of the given kind that execs the real git
// binary.
func NewKind(kind Kind) *Datasource {
	return &Datasource{kind: kind, lsRemote: execLsRemote}
}

func (d *Datasource) Name() string {
	if d.kind == "" {
		return string(Tags)
	}
	return string(d.kind)
}

// DefaultVersioning is semver: every manager that hands this datasource a
// bare repository URL - kustomize's remote bases, so far - pins a tag that
// is expected to look like one.
func (d *Datasource) DefaultVersioning() string { return "semver" }

// Releases lists a repository's tags. ref.PackageName is the repository
// URL; ref.RegistryURLs is ignored - a git remote is not addressed through
// a separate registry the way a package is.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	repoURL := ref.PackageName
	if repoURL == "" {
		return nil, fmt.Errorf("git-tags: no repository URL")
	}
	if strings.HasPrefix(repoURL, "ssh://") {
		return nil, &lookup.DeclinedError{Reason: fmt.Sprintf(
			"git-tags: %s is an ssh:// URL; only an unauthenticated public repository is supported", redactURL(repoURL))}
	}
	if strings.Contains(repoURL, "**redacted**") {
		return nil, &lookup.DeclinedError{Reason: fmt.Sprintf(
			"git-tags: %s carries a redacted credential; the runner's job token is not available to this datasource", redactURL(repoURL))}
	}

	ls := d.lsRemote
	if ls == nil {
		ls = execLsRemote
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	out, err := ls(ctx, d.Git, timeout, repoURL, d.kind == Refs)
	if err != nil {
		return nil, fmt.Errorf("%s: ls-remote %s: %w", d.Name(), redactURL(repoURL), err)
	}

	rs := &model.ReleaseSet{
		PackageName: repoURL,
		Datasource:  d.Name(),
	}
	// Measured for https:// only: the project page one gets by stripping
	// a trailing .git. Other schemes (file://, git://; ssh:// is declined
	// above) are left without a sourceUrl pending measurement against a
	// real host that serves one.
	if strings.HasPrefix(repoURL, "https://") {
		rs.SourceURL = strings.TrimSuffix(repoURL, ".git")
	}

	// `git ls-remote --tags --refs` prints "<sha>\t<ref>" per line, one per
	// tag, with peeled "^{}" lines suppressed by --refs. For an annotated
	// tag the sha is the tag OBJECT's sha, not the commit it points to -
	// the peeled commit sha is exactly what --refs drops. No timestamps:
	// ls-remote carries none, so the planner falls back to first-seen age
	// for every release this datasource returns.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		sha, refName, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		name, ok := strings.CutPrefix(refName, "refs/tags/")
		if !ok && d.kind == Refs {
			// git-refs: a branch is a release too; its digest is the
			// commit it points at now, which is what a dependency pinned
			// to a branch moves with.
			name, ok = strings.CutPrefix(refName, "refs/heads/")
		}
		if !ok {
			continue
		}
		rs.Releases = append(rs.Releases, model.Release{Version: name, Digest: sha})
	}
	return rs, nil
}

// Digest answers the commit a ref points at now - a git-refs dependency
// pinned to a branch (`master@<sha>`) is refreshed through it.
func (d *Datasource) Digest(ctx context.Context, ref lookup.Ref, version string) (string, error) {
	rs, err := d.Releases(ctx, ref)
	if err != nil {
		return "", err
	}
	for _, r := range rs.Releases {
		if r.Version == version {
			return r.Digest, nil
		}
	}
	return "", fmt.Errorf("%s: %s has no ref %q", d.Name(), redactURL(ref.PackageName), version)
}

// execLsRemote runs `git ls-remote --tags --refs repoURL` and returns its
// stdout. GIT_TERMINAL_PROMPT=0 turns a repository that needs credentials
// into an immediate failure instead of a hang; no token is ever passed, by
// design (see the package doc).
func execLsRemote(ctx context.Context, gitBin string, timeout time.Duration, repoURL string, all bool) (string, error) {
	bin := gitBin
	if bin == "" {
		bin = "git"
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"ls-remote", "--tags", "--refs", repoURL}
	if all {
		args = []string{"ls-remote", "--heads", "--tags", "--refs", repoURL}
	}
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		// git's own message may echo the URL back (a 128 for "repository
		// not found" does); redact it the same way the URL in our own
		// wrapping error is redacted, so a credential never reaches a log
		// twice with only one of the two copies scrubbed.
		return "", fmt.Errorf("%w: %s", err, redactURL(strings.TrimSpace(errb.String())))
	}
	return out.String(), nil
}

// redactURL removes anything that looks like a credential from a URL
// embedded in arbitrary text - git's own stderr echoes the URL back inside
// a sentence, not as a bare value a net/url.Parse could round-trip, so this
// scans for "scheme://user:pass@" the same way git/git.go's own (unexported,
// unreachable from here) redact does, rather than parsing.
func redactURL(s string) string {
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
