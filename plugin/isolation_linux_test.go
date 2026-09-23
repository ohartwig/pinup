// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/sandbox"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.Arg {
		os.Exit(sandbox.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// The whole path, with a real process: a task run through the sandbox
// writes its checkout, and a hung one is killed at the timeout - through
// the shim, which by then has become the task. A kill that stopped at the
// shim would leave the tool running and the job waiting.
func TestAnIsolatedTaskWritesItsCheckoutAndDiesAtTheTimeout(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sb := &sandbox.Sandbox{Self: self, Hide: []string{t.TempDir()}}
	if err := sb.Check(); err != nil {
		if os.Getenv("PINUP_SANDBOX_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	r := &Runner{
		Getenv:  func(k string) string { return map[string]string{"PATH": "/usr/bin:/bin"}[k] },
		Isolate: sb.Wrap,
	}

	root := t.TempDir()
	if _, err := r.Run(t.Context(), root, model.Task{Command: []string{"sh", "-c", "echo refreshed > composer.lock"}}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "composer.lock")); err != nil || string(b) != "refreshed\n" {
		t.Errorf("the task's write: %q %v", b, err)
	}

	// The bound is far from the timeout and far from the sleep: what is
	// asserted is that the kill reached the task, not how fast a loaded
	// machine starts a process. A kill that stopped at the shim would take
	// the whole 30 seconds.
	r.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err = r.Run(t.Context(), root, model.Task{Command: []string{"sleep", "30"}})
	if err == nil || !strings.Contains(err.Error(), "timed out after 300ms") {
		t.Errorf("hung task: %v", err)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("the hung task was not killed at the timeout: it took %s", took)
	}
}
