// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// The test binary is its own shim, as pinup is: sandboxed tasks below are
// this binary again, re-entered through Arg.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Arg {
		os.Exit(Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func self(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// usable asks the kernel for a user namespace and nothing else. Where it
// refuses, the tests below cannot say anything and skip - unless
// PINUP_SANDBOX_REQUIRED is set, as CI sets it: a sandbox test that skips in
// the pipeline is a gate that does not gate.
func usable(t *testing.T) {
	t.Helper()
	attr, err := sysProcAttr()
	if err == nil {
		cmd := exec.Command(self(t), "-test.run=^$")
		cmd.SysProcAttr = attr
		err = cmd.Run()
	}
	if err == nil {
		return
	}
	if os.Getenv("PINUP_SANDBOX_REQUIRED") != "" {
		t.Fatalf("PINUP_SANDBOX_REQUIRED, and the kernel refused a user namespace: %v", err)
	}
	t.Skipf("no user namespace here: %v", err)
}

// A job's secrets as the estate's runner lays them out, under one root the
// test can hide without touching the machine's own /tmp.
type job struct {
	root, home, gnupg, tmp, checkout, other, state string
}

func newJob(t *testing.T) job {
	t.Helper()
	root := t.TempDir()
	j := job{
		root:     root,
		home:     filepath.Join(root, "home"),
		gnupg:    filepath.Join(root, "gnupg"),
		tmp:      filepath.Join(root, "tmp"),
		checkout: filepath.Join(root, "tmp", "pinup-run-a", "repo"),
		other:    filepath.Join(root, "tmp", "pinup-run-b", "repo"),
		state:    filepath.Join(root, "state", "cache.db"),
	}
	for p, content := range map[string]string{
		filepath.Join(j.home, ".netrc"):                           "machine x password platform-token\n",
		filepath.Join(j.gnupg, "private-keys-v1.d", "key.key"):    "signing key\n",
		filepath.Join(j.checkout, "composer.json"):                "{}\n",
		filepath.Join(j.other, "composer.json"):                   "{\"other\": true}\n",
		j.state:                                                   "first-seen records\n",
		filepath.Join(j.tmp, "tmp.gnupg", "S.gpg-agent.sockfile"): "agent\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return j
}

func (j job) sandbox(t *testing.T) *Sandbox {
	return &Sandbox{Self: self(t), Hide: []string{j.home, j.gnupg, j.tmp, j.state}}
}

// The check pinup runs before its first task holds on this machine: the
// parent's environment unreadable, every hidden entry gone, the kept path
// there, no capability left, no_new_privs set.
func TestCheckHolds(t *testing.T) {
	usable(t)
	j := newJob(t)
	if err := j.sandbox(t).Check(); err != nil {
		t.Fatal(err)
	}
}

// The check can fail. Run where nothing is isolated - in this process - it
// must see every door it is meant to see; a check that reports nothing here
// would report nothing anywhere, and its "held" above would mean nothing.
func TestTheCheckSeesAnOpenDoor(t *testing.T) {
	j := newJob(t)
	absent, empty := expectations([]string{j.home, j.state}, nil)
	got := strings.Join(openDoors(check{
		Environ: fmt.Sprintf("/proc/%d/environ", os.Getpid()),
		Visible: filepath.Join(j.root, "missing"),
		Absent:  absent,
		Empty:   empty,
	}), "\n")
	for _, want := range []string{
		"open: the parent's environment is readable",
		"broken: a kept path is not visible",
		"open: " + filepath.Join(j.home, ".netrc") + " is visible",
		"open: " + j.state + " is readable",
		"open: no_new_privs is not set",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the unisolated check did not report %q; it said:\n%s", want, got)
		}
	}
}

// TestHelperTask is the task the tests below run inside the sandbox: this
// binary again, doing what PINUP_SANDBOX_TASK lists and printing what
// happened. Outside that role it does nothing.
func TestHelperTask(t *testing.T) {
	actions := os.Getenv("PINUP_SANDBOX_TASK")
	if actions == "" {
		return
	}
	for a := range strings.SplitSeq(actions, ";") {
		verb, path, _ := strings.Cut(a, ":")
		var err error
		switch verb {
		case "read":
			_, err = os.ReadFile(path)
		case "write":
			err = os.WriteFile(path, []byte("written by the task\n"), 0o600)
		case "mkdir":
			err = os.MkdirAll(path, 0o700)
		case "umount":
			err = syscall.Unmount(path, syscall.MNT_DETACH)
		case "mount":
			err = syscall.Mount("tmpfs", path, "tmpfs", 0, "")
		}
		result := "ok"
		if err != nil {
			result = "denied"
		}
		fmt.Printf("%s:%s=%s\n", verb, path, result)
	}
}

// A task inside the sandbox reaches its checkout, reads and writes it, and
// what it writes lands in the job's checkout. It reaches nothing else: not
// the other repository's checkout beside it, not the job's HOME, not the
// key, not pinup's state. Its /tmp is its own and gone afterwards, and it
// cannot take any of this apart again.
func TestATaskSeesItsCheckoutAndNothingElse(t *testing.T) {
	usable(t)
	j := newJob(t)
	private := filepath.Join(j.tmp, "left-behind")
	want := []string{
		"read:" + filepath.Join(j.checkout, "composer.json") + "=ok",
		"write:" + filepath.Join(j.checkout, "composer.lock") + "=ok",
		"read:" + filepath.Join(j.other, "composer.json") + "=denied",
		"write:" + filepath.Join(j.other, "composer.json") + "=denied",
		"read:" + filepath.Join(j.home, ".netrc") + "=denied",
		"read:" + filepath.Join(j.gnupg, "private-keys-v1.d", "key.key") + "=denied",
		"read:" + filepath.Join(j.tmp, "tmp.gnupg", "S.gpg-agent.sockfile") + "=denied",
		"read:" + j.state + "=ok", // /dev/null: readable, and empty - see below
		"write:" + private + "=ok",
		"umount:" + j.tmp + "=denied",
		"mount:" + j.checkout + "=denied",
	}
	var actions []string
	for _, w := range want {
		a, _, _ := strings.Cut(w, "=")
		actions = append(actions, a)
	}
	cmd := exec.Command(self(t), "-test.run=^TestHelperTask$")
	cmd.Dir = j.checkout
	cmd.Env = []string{"PATH=/usr/bin:/bin", "PINUP_SANDBOX_TASK=" + strings.Join(actions, ";")}
	cleanup, err := j.sandbox(t).Wrap(cmd, []string{j.checkout})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	cleanup()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, w := range want {
		if !slices.Contains(lines, w) {
			t.Errorf("want %s; the task reported:\n%s", w, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(j.checkout, "composer.lock")); err != nil || string(b) != "written by the task\n" {
		t.Errorf("the task's write did not reach the checkout: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(j.other, "composer.json")); string(b) != "{\"other\": true}\n" {
		t.Errorf("the other checkout changed: %q", b)
	}
	if b, _ := os.ReadFile(j.state); string(b) != "first-seen records\n" {
		t.Errorf("pinup's state changed: %q", b)
	}
	if _, err := os.Stat(private); err == nil {
		t.Error("the task's private /tmp leaked into the job's")
	}
}

// The cache a task is handed is its repository's. Repository A writes into
// its COMPOSER_HOME - the poisoned metadata of the review's attack - and
// repository B, handed the very same path in the same job, does not see
// it; a second branch of A does. The shared directory itself gains nothing
// but the per-repository directories beneath it.
func TestACacheIsNotSharedBetweenRepositories(t *testing.T) {
	usable(t)
	j := newJob(t)
	shared := filepath.Join(j.root, "builds", ".pinup", "composer")
	poison := filepath.Join(shared, "repo", "https---repo.packagist.org", "provider-typo3~cms-core.json")

	task := func(repo, actions string) string {
		t.Helper()
		cmd := exec.Command(self(t), "-test.run=^TestHelperTask$")
		cmd.Dir = j.checkout
		cmd.Env = []string{"PATH=/usr/bin:/bin", "COMPOSER_HOME=" + shared, "PINUP_SANDBOX_TASK=" + actions}
		sb := j.sandbox(t)
		sb.Caches, sb.Repo = []string{"COMPOSER_HOME"}, repo
		cleanup, err := sb.Wrap(cmd, []string{j.checkout})
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v:\n%s", err, out)
		}
		return string(out)
	}
	if err := os.MkdirAll(filepath.Dir(poison), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := task("development/a", "mkdir:"+filepath.Dir(poison)+";write:"+poison); !strings.Contains(got, "write:"+poison+"=ok") {
		t.Fatalf("repository A could not write its own cache:\n%s", got)
	}
	if got := task("development/b", "read:"+poison); !strings.Contains(got, "read:"+poison+"=denied") {
		t.Errorf("repository B read what A wrote into the cache:\n%s", got)
	}
	if got := task("development/a", "read:"+poison); !strings.Contains(got, "read:"+poison+"=ok") {
		t.Errorf("a second branch of A lost A's cache:\n%s", got)
	}
	if _, err := os.Stat(poison); err == nil {
		t.Error("the write landed in the shared cache, where every repository reads")
	}
	entries, _ := os.ReadDir(shared)
	for _, e := range entries {
		if e.Name() != ".pinup-repos" && e.Name() != "repo" {
			t.Errorf("the shared cache gained %s", e.Name())
		}
	}
}
