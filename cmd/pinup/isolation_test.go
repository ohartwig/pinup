// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/sandbox"
)

// The test binary answers the shim's argument as pinup does, so a sandbox
// started from a test here has a shim to start.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.Arg {
		os.Exit(sandbox.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// Tasks are isolated unless the runner switches isolation off BY NAME, and
// then it says so. Where the sandbox cannot hold - off Linux, or under a
// kernel or seccomp profile that refuses it - the task is refused with the
// reason, never run as if it had held.
func TestTasksAreIsolatedUnlessSwitchedOffByName(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	var warn bytes.Buffer
	if iso := taskIsolation(env(map[string]string{"PINUP_TASK_ISOLATION": "off"}), &warn); iso != nil {
		t.Error("PINUP_TASK_ISOLATION=off still isolates")
	}
	if !strings.Contains(warn.String(), "tasks run unisolated") {
		t.Errorf("switching isolation off went unsaid: %q", warn.String())
	}

	iso := taskIsolation(env(nil), io.Discard)
	if iso == nil {
		t.Fatal("tasks run unisolated by default")
	}
	if taskRunner(env(nil), iso).Isolate == nil {
		t.Fatal("the task runner dropped the isolation it was handed")
	}
	cmd := exec.Command("true")
	cleanup, err := iso(cmd, []string{t.TempDir()})
	defer cleanup()
	switch {
	case runtime.GOOS != "linux":
		if err == nil || !strings.Contains(err.Error(), "does not hold here, so no task runs") {
			t.Errorf("off Linux a task must be refused, got %v", err)
		}
	case err == nil:
		if cmd.Args[1] != sandbox.Arg {
			t.Errorf("the task was not wrapped: %v", cmd.Args)
		}
	case os.Getenv("PINUP_SANDBOX_REQUIRED") != "":
		t.Fatalf("PINUP_SANDBOX_REQUIRED, and the sandbox did not hold: %v", err)
	default:
		t.Logf("no sandbox on this machine, refused as it must be: %v", err)
	}
}

// What a task must not see is everything the job holds: the temporary
// directory with the other checkouts and the imported key, the job's HOME,
// the key's directory and the agent sockets wherever they are, and pinup's
// own state.
func TestTaskHideCoversWhatTheJobHolds(t *testing.T) {
	got := taskHide(func(k string) string {
		return map[string]string{"HOME": "/home/job", "GNUPGHOME": "/srv/gnupg", "SSH_AUTH_SOCK": "/run/ssh/agent"}[k]
	}, []string{".pinup/cache.db", ".pinup/consumers.json", ""})
	cache, _ := filepath.Abs(".pinup/cache.db")
	index, _ := filepath.Abs(".pinup/consumers.json")
	for _, want := range []string{os.TempDir(), "/home/job", "/srv/gnupg", "/run/ssh/agent", cache, index} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is not hidden; hide = %v", want, got)
		}
	}
}
