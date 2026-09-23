// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type fakeInfo struct{ dir bool }

func (f fakeInfo) Name() string       { return "" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return map[bool]fs.FileMode{true: fs.ModeDir}[f.dir] }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

// A job's filesystem as the estate's runner has it: the checkouts and the
// imported key under /tmp, the caches under the project directory.
func jobFS(p string) (fs.FileInfo, error) {
	switch p {
	case "/tmp", "/tmp/pinup-run-a", "/tmp/pinup-run-a/repo", "/tmp/pinup-run-a/repo/secrets",
		"/tmp/pinup-task-home-1", "/tmp/tmp.gnupg", "/home/job", "/home/job/.local/bin",
		"/builds/pinup/runner/.pinup/composer", "/usr/bin":
		return fakeInfo{dir: true}, nil
	case "/builds/pinup/runner/.pinup/cache.db", "/tmp/ssh-x/agent.1":
		return fakeInfo{}, nil
	}
	return nil, fs.ErrNotExist
}

func render(ops []op) string {
	var b strings.Builder
	for _, o := range ops {
		switch {
		case o.Keep && o.Src != o.Path:
			b.WriteString("repo " + o.Path + "\n")
		case o.Keep:
			b.WriteString("keep " + o.Path + "\n")
		case o.Src == os.DevNull:
			b.WriteString("null " + o.Path + "\n")
		default:
			b.WriteString("hide " + o.Path + "\n")
		}
	}
	return b.String()
}

// The order is the design: shallowest first, a hide before a keep at one
// depth. A checkout kept under the hidden /tmp comes back; a directory
// hidden under the kept checkout is hidden again after it; the caches the
// environment names stay where they are, because nothing covers them.
func TestPlanOrdersHidesAndKeepsByDepth(t *testing.T) {
	for _, tc := range []struct {
		name           string
		hide, keep     []string
		handed, caches []string
		want, errHas   string
	}{
		{name: "the estate's runner",
			hide:   []string{"/tmp", "/home/job", "/tmp/tmp.gnupg", "/tmp/ssh-x/agent.1", "/builds/pinup/runner/.pinup/cache.db"},
			keep:   []string{"/tmp/pinup-run-a/repo", "/tmp/pinup-task-home-1"},
			handed: []string{"/usr/bin", "/tmp", "/builds/pinup/runner/.pinup/composer", "/tmp/pinup-task-home-1"},
			// The key and the agent socket are listed although /tmp
			// covers them already; the shim skips a path an earlier
			// stand-in has made disappear. Listing them keeps them
			// hidden when TMPDIR points somewhere else.
			want: "hide /tmp\n" +
				"hide /home/job\nhide /tmp/tmp.gnupg\nkeep /tmp/pinup-task-home-1\n" +
				"null /tmp/ssh-x/agent.1\nkeep /tmp/pinup-run-a/repo\n" +
				"null /builds/pinup/runner/.pinup/cache.db\n"},
		{name: "hidden inside a kept path is hidden again",
			hide: []string{"/tmp", "/tmp/pinup-run-a/repo/secrets"},
			keep: []string{"/tmp/pinup-run-a/repo"},
			want: "hide /tmp\nkeep /tmp/pinup-run-a/repo\nhide /tmp/pinup-run-a/repo/secrets\n"},
		{name: "a tool the environment names under a hidden HOME stays usable",
			hide:   []string{"/home/job"},
			handed: []string{"/home/job/.local/bin"},
			want:   "hide /home/job\nkeep /home/job/.local/bin\n"},
		{name: "the root is never hidden, a relative or missing path is skipped",
			hide: []string{"/", "tmp", "/nowhere", "/tmp"},
			want: "hide /tmp\n"},
		{name: "a cache the task is handed becomes its repository's own",
			hide:   []string{"/tmp"},
			keep:   []string{"/tmp/pinup-run-a/repo"},
			handed: []string{"/builds/pinup/runner/.pinup/composer", "/usr/bin"},
			caches: []string{"/builds/pinup/runner/.pinup/composer"},
			want:   "hide /tmp\nkeep /tmp/pinup-run-a/repo\nrepo /builds/pinup/runner/.pinup/composer\n"},
		{name: "a cache under the hidden HOME is given back per repository",
			hide:   []string{"/home/job"},
			caches: []string{"/home/job/.cache/composer"},
			want:   "hide /home/job\nrepo /home/job/.cache/composer\n"},
		// A tool directory inside a cache would reach past the cover into
		// the shared cache the cover is there to keep out of reach.
		{name: "nothing handed inside a cache is kept",
			hide:   []string{"/home/job"},
			handed: []string{"/home/job/.local/bin"},
			caches: []string{"/home/job/.local"},
			want:   "hide /home/job\nrepo /home/job/.local\n"},
		{name: "a cache cannot contain the checkout",
			keep:   []string{"/tmp/pinup-run-a/repo"},
			caches: []string{"/tmp/pinup-run-a"},
			errHas: "cannot be a cache per repository: the task runs in it"},
		{name: "the checkout cannot be the hidden directory itself",
			hide:   []string{"/tmp/pinup-run-a/repo"},
			keep:   []string{"/tmp/pinup-run-a/repo"},
			errHas: "cannot hide /tmp/pinup-run-a/repo: the task runs in it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, err := plan(tc.hide, tc.keep, tc.handed, tc.caches, "group/repo", jobFS)
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := render(ops); got != tc.want {
				t.Errorf("plan:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// A tool that was not found stays not found: Wrap leaves the command alone
// and Start reports the missing tool, not a sandbox error.
func TestAMissingToolIsReportedNotWrapped(t *testing.T) {
	cmd := exec.Command("pinup-no-such-tool-xyz")
	cleanup, err := (&Sandbox{Self: "/proc/self/exe"}).Wrap(cmd, nil)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("start: %v, want not found", err)
	}
}

// Two repositories never get the same cache, and the two branches of one
// always do: the key is the repository, not the task.
func TestACacheBelongsToOneRepository(t *testing.T) {
	src := func(key string) string {
		ops, err := plan(nil, nil, nil, []string{"/builds/cache/composer"}, key, jobFS)
		if err != nil || len(ops) != 1 {
			t.Fatalf("plan: %v %v", ops, err)
		}
		return ops[0].Src
	}
	a, again, b := src("development/moselwal/a"), src("development/moselwal/a"), src("development/moselwal/b")
	if a != again {
		t.Errorf("one repository, two caches: %s, %s", a, again)
	}
	if a == b {
		t.Errorf("two repositories, one cache: %s", a)
	}
	if !strings.HasPrefix(a, "/builds/cache/composer/.pinup-repos/") {
		t.Errorf("the repository's cache is not beneath the shared one: %s", a)
	}
}
