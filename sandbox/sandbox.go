// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package sandbox runs a task where it cannot reach what the job holds.
//
// A task - a lock refresh, or a `node scripts/<file>.mjs` out of the scanned
// repository - runs under the job's uid. Without this package it can read
// the parent's /proc/<pid>/environ (the platform token), open any file the
// uid owns (the imported signing key, the gpg-agent socket beside it), attach
// to the parent with ptrace where yama allows it, and write into the
// checkouts of the other repositories the job is working on at the same time
// - moments before pinup commits them with its own signature.
//
// The shape follows what was measured on the estate's executor on
// 2026-09-22: the job runs as an unprivileged uid with no capabilities, so a
// task cannot be dropped to another uid; an unprivileged user namespace can
// be created, and it denies environ and ptrace across its boundary, but not
// a file the same uid owns. Hiding files takes a mount inside the namespace
// before the task starts, and Go cannot run code in a child between clone and
// exec. So pinup re-executes itself as a shim (Arg) in a new user and mount
// namespace. The shim binds an empty directory over every path the task must
// not see (/dev/null over a file), binds back the paths the task was handed -
// its checkout, its scratch HOME, the caches its environment names - drops
// every capability, sets no_new_privs, and only then executes the task.
//
// That the kernel allows this is a property of the executor (no seccomp
// profile), not a promise. Check runs the sandbox on itself and names the
// door that stayed open; a caller that requires isolation refuses tasks when
// it fails, instead of running them as if it had held.
package sandbox

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Arg, as the first argument, makes the binary the shim instead of itself.
// The binary has to look for it before anything else runs: the shim becomes
// the task, and nothing of pinup's own start-up belongs in that process.
const Arg = "__task-sandbox"

// ErrUnsupported is what a platform without user namespaces answers.
var ErrUnsupported = errors.New("task isolation needs Linux user namespaces")

// Sandbox describes what a task must not see.
type Sandbox struct {
	// Self is the binary that answers Arg: pinup itself, or a test binary.
	Self string
	// Hide are the paths a task must not see: the job's HOME and GNUPGHOME,
	// the agent sockets, the temporary directory the other checkouts live
	// in, pinup's own state files. A path that does not exist when the task
	// starts is skipped; a directory is covered by an empty one, anything
	// else by /dev/null.
	Hide []string
}

// spec is what the shim is told, as its second argument.
type spec struct {
	Ops   []op   `json:"ops"`
	Check *check `json:"check,omitempty"`
	// Inner marks the check after the shim's exec: inside the namespace,
	// without privileges, with nothing left to set up.
	Inner bool `json:"inner,omitzero"`
}

// op is one bind mount. Src is bound over Path: an empty directory or
// /dev/null for a hidden path, the path itself for a kept one - opened by
// the shim before anything is hidden, because afterwards it may be
// unreachable by name.
type op struct {
	Path string `json:"path"`
	Src  string `json:"src"`
	Keep bool   `json:"keep,omitzero"`
}

// check is the shim's self-test in place of a task. It runs after the same
// exec a task goes through, so it sees exactly what a task would.
type check struct {
	// Environ is the parent's environment file; it must not be readable.
	Environ string `json:"environ"`
	// Visible is a file under a kept path; it must read Want.
	Visible string `json:"visible"`
	Want    string `json:"want"`
	// Absent were entries of hidden directories; none may exist.
	Absent []string `json:"absent,omitempty"`
	// Empty were hidden files; each must read as nothing.
	Empty []string `json:"empty,omitempty"`
}

// plan orders what the shim does. A hide covers a path with a stand-in; a
// keep binds a path the task was handed back into a hidden tree. Applied
// shallowest first, hides before keeps at one depth: a checkout kept under a
// hidden temporary directory comes back, and a hidden directory under a kept
// one is hidden again after it.
//
// keep is what the task cannot run without - its checkout, its scratch HOME;
// one that equals a hidden path is a contradiction and an error. handed are
// the directories named in the task's environment: kept when they lie inside
// a hidden tree, dropped when they would uncover one (TMPDIR names the very
// directory that is hidden).
func plan(hide, keep, handed []string, stat func(string) (fs.FileInfo, error)) ([]op, error) {
	var ops []op
	hidden := map[string]bool{}
	for _, p := range hide {
		if !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		// The root is never hidden: covering it covers the task's tools,
		// and there is nothing under it that a narrower path does not name.
		if p == "/" || hidden[p] {
			continue
		}
		fi, err := stat(p)
		if err != nil {
			continue
		}
		hidden[p] = true
		src := "" // an empty directory, made per task by Wrap
		if !fi.IsDir() {
			src = os.DevNull
		}
		ops = append(ops, op{Path: p, Src: src})
	}
	under := func(p string) bool {
		for h := range hidden {
			if strings.HasPrefix(p, h+"/") {
				return true
			}
		}
		return false
	}
	kept := map[string]bool{}
	add := func(p string, must bool) error {
		if !filepath.IsAbs(p) {
			return nil
		}
		p = filepath.Clean(p)
		switch {
		case kept[p]:
			return nil
		case hidden[p] && must:
			return fmt.Errorf("sandbox: cannot hide %s: the task runs in it", p)
		case hidden[p], !under(p):
			return nil
		}
		fi, err := stat(p)
		switch {
		case err != nil && must:
			return fmt.Errorf("sandbox: %s: %w", p, err)
		case err != nil, !fi.IsDir():
			return nil
		}
		kept[p] = true
		ops = append(ops, op{Path: p, Src: p, Keep: true})
		return nil
	}
	for _, p := range keep {
		if err := add(p, true); err != nil {
			return nil, err
		}
	}
	for _, p := range handed {
		if err := add(p, false); err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(ops, func(a, b op) int {
		if d := strings.Count(a.Path, "/") - strings.Count(b.Path, "/"); d != 0 {
			return d
		}
		switch {
		case a.Keep == b.Keep:
			return strings.Compare(a.Path, b.Path)
		case a.Keep:
			return 1
		}
		return -1
	})
	return ops, nil
}

// handedPaths are the directories the task's environment names: PATH and
// its entries, the caches (COMPOSER_HOME, GOMODCACHE, npm_config_cache), its
// scratch HOME. A value may be a list, as PATH is.
func handedPaths(env []string) []string {
	var out []string
	for _, kv := range env {
		_, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for p := range strings.SplitSeq(v, string(os.PathListSeparator)) {
			if filepath.IsAbs(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// Wrap rewrites cmd so it runs through the shim, keeping the given paths -
// the checkout, the scratch HOME - visible. The returned cleanup removes the
// per-task stand-ins, and with them whatever the task wrote into its private
// temporary directory; call it after the command has finished.
func (s *Sandbox) Wrap(cmd *exec.Cmd, keep []string) (cleanup func(), err error) {
	return s.prepare(cmd, keep, nil)
}

func (s *Sandbox) prepare(cmd *exec.Cmd, keep []string, c *check) (cleanup func(), err error) {
	cleanup = func() {}
	if cmd.Err != nil {
		// The tool was not found; Start reports that, and there is
		// nothing to isolate.
		return cleanup, nil
	}
	attr, err := sysProcAttr()
	if err != nil {
		return cleanup, err
	}
	ops, err := plan(s.Hide, keep, handedPaths(cmd.Env), os.Stat)
	if err != nil {
		return cleanup, err
	}
	// Under the temporary directory, which is itself hidden: the shim
	// reaches the stand-ins through descriptors it opens before hiding.
	work, err := os.MkdirTemp("", "pinup-sandbox-")
	if err != nil {
		return cleanup, fmt.Errorf("sandbox: %w", err)
	}
	cleanup = func() { os.RemoveAll(work) }
	for i := range ops {
		if ops[i].Src != "" {
			continue
		}
		if ops[i].Src, err = os.MkdirTemp(work, "empty-"); err != nil {
			cleanup()
			return func() {}, fmt.Errorf("sandbox: %w", err)
		}
	}
	raw, err := json.Marshal(spec{Ops: ops, Check: c})
	if err != nil {
		cleanup()
		return func() {}, fmt.Errorf("sandbox: %w", err)
	}
	cmd.Args = append([]string{s.Self, Arg, string(raw), cmd.Path}, cmd.Args[1:]...)
	cmd.Path = s.Self
	cmd.SysProcAttr = attr
	return cleanup, nil
}

// Check runs the sandbox on itself, as a task would run, and returns an
// error naming every door that stayed open: the parent's environment
// readable, an entry of a hidden directory still there, a capability kept.
// It also proves the other half - a kept path stays visible - since a
// sandbox that hides everything would pass the first half and break every
// task.
func (s *Sandbox) Check() error {
	if _, err := sysProcAttr(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "pinup-sandbox-check-")
	if err != nil {
		return fmt.Errorf("sandbox check: %w", err)
	}
	defer os.RemoveAll(dir)
	// dir is hidden and dir/kept kept: the shape of a checkout under a
	// hidden temporary directory. dir/canary must be gone.
	kept := filepath.Join(dir, "kept")
	if err := os.Mkdir(kept, 0o700); err != nil {
		return fmt.Errorf("sandbox check: %w", err)
	}
	const want = "a kept path stays visible\n"
	c := &check{Environ: fmt.Sprintf("/proc/%d/environ", os.Getpid()), Visible: filepath.Join(kept, "visible"), Want: want}
	if err := os.WriteFile(c.Visible, []byte(want), 0o600); err != nil {
		return fmt.Errorf("sandbox check: %w", err)
	}
	canary := filepath.Join(dir, "canary")
	if err := os.WriteFile(canary, []byte("hidden\n"), 0o600); err != nil {
		return fmt.Errorf("sandbox check: %w", err)
	}
	probe := &Sandbox{Self: s.Self, Hide: append(slices.Clone(s.Hide), dir)}
	c.Absent, c.Empty = expectations(probe.Hide, []string{kept})

	cmd := exec.Command(s.Self)
	cmd.Env = []string{}
	cleanup, err := probe.prepare(cmd, []string{kept}, c)
	if err != nil {
		return err
	}
	defer cleanup()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox check: %w: %s", err, strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", "; "))
	}
	return nil
}

// expectations lists what the check must not find: every entry of a hidden
// directory except those leading to a kept path, and every hidden file. At
// most a few dozen entries per directory - enough to see that a stand-in is
// in place, without turning the check into a listing of /tmp.
func expectations(hide, keep []string) (absent, empty []string) {
	const perDir = 32
	leadsToKept := func(p string) bool {
		for _, k := range keep {
			if k == p || strings.HasPrefix(k, p+"/") {
				return true
			}
		}
		return false
	}
	for _, h := range hide {
		if !filepath.IsAbs(h) || filepath.Clean(h) == "/" {
			continue
		}
		h = filepath.Clean(h)
		fi, err := os.Stat(h)
		switch {
		case err != nil:
			continue
		case !fi.IsDir():
			empty = append(empty, h)
			continue
		}
		entries, err := os.ReadDir(h)
		if err != nil {
			continue
		}
		n := 0
		for _, e := range entries {
			p := filepath.Join(h, e.Name())
			if leadsToKept(p) || n == perDir {
				continue
			}
			absent = append(absent, p)
			n++
		}
	}
	return absent, empty
}

// Main runs the shim: args[0] is Arg, args[1] the spec, the rest the task.
// It returns only when the shim failed, with the exit status to use; on
// success the process has become the task.
func Main(args []string) int {
	if len(args) < 2 || args[0] != Arg {
		fmt.Fprintln(os.Stderr, "pinup sandbox: not called as the shim")
		return 125
	}
	var sp spec
	if err := json.Unmarshal([]byte(args[1]), &sp); err != nil {
		fmt.Fprintln(os.Stderr, "pinup sandbox: "+err.Error())
		return 125
	}
	if sp.Check == nil && len(args) < 3 {
		fmt.Fprintln(os.Stderr, "pinup sandbox: no task to run")
		return 125
	}
	code, err := shim(sp, args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pinup sandbox: "+err.Error())
	}
	return code
}
