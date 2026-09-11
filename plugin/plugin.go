// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package plugin compiles and runs the commands a branch needs beyond its
// byte-range edits: the lock refresh pinup asks for itself when a manifest
// with a lock file changes, and the postUpgradeTasks a configuration names.
//
// Core decides, plugins apply. A task is compiled from the plan - argv, no
// shell - checked against the allowlist on the compiled command, run with an
// environment the runner allowlisted (never the platform token, never the
// signing key), bounded by a timeout, and its result is validated against
// the file scope it was handed: one path outside the scope discards the
// whole result. This is the exec flavour of the container contract the
// spec describes (§12): the toolchain is on the job image's PATH, as the
// estate's runners have no DinD.
//
// Measured, from the estate's Renovate runner: commands are executed
// without a shell, so `a || b` hands composer a package named `||`; the
// allowlist is anchored regexes over the compiled command; in branch mode
// `{{{depName}}}` expands to every package the branch rewrote, space
// separated.
package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/hbs"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/re2x"
)

// PostUpgrade is the configuration's postUpgradeTasks object for one update.
type PostUpgrade struct {
	Commands      []string
	ExecutionMode model.ExecutionMode
	FileFilters   []string
	Origin        model.Origin
}

// LockRefresh composes the toolchain command that brings a lock file up to
// date with the manifest edits of one branch, for the managers whose lock
// pinup cannot rewrite itself. Nothing is returned for a manager without
// one. The commands are the estate's own: the composer form is the one its
// runner configuration allowlists for first-party bumps (with-all-dependencies,
// no plugins, no install, no scripts, no audit, platform requirements
// ignored - the runner image is not the deployment image); the npm form is
// what npm documents for refreshing a lock without installing.
func LockRefresh(manager, dir string, depNames []string, maintenance bool) (model.Task, bool) {
	names := unique(depNames)
	var argv []string
	switch manager {
	case "composer":
		argv = []string{"composer", "update"}
		if !maintenance {
			argv = append(argv, names...)
			argv = append(argv, "--with-all-dependencies")
		}
		argv = append(argv, "--no-plugins", "--no-install", "--no-scripts", "--no-audit", "--ignore-platform-reqs")
		return model.Task{
			Kind: model.TaskLockRefresh, Manager: manager, Dir: dir, Command: argv,
			ExecutionMode: model.ExecBranch, FileFilters: []string{"composer.lock"},
			AllowedBy: -1, Origin: model.Origin{Source: "pinup", Rule: model.NoRule},
		}, true
	case "npm":
		argv = []string{"npm", "install", "--package-lock-only", "--no-audit", "--ignore-scripts"}
		if !maintenance {
			argv = append(argv, names...)
		}
		return model.Task{
			Kind: model.TaskLockRefresh, Manager: manager, Dir: dir, Command: argv,
			ExecutionMode: model.ExecBranch, FileFilters: []string{"package-lock.json", "npm-shrinkwrap.json"},
			AllowedBy: -1, Origin: model.Origin{Source: "pinup", Rule: model.NoRule},
		}, true
	}
	return model.Task{}, false
}

// Compile renders a configuration's postUpgradeTasks for the updates of one
// branch and checks each command against the allowlist. In update mode one
// task per update is produced with that update's variables; in branch mode
// one task carries every update, depName being the space-separated names.
// A command the allowlist does not admit is an error naming it - a task
// that silently did not run is how the estate lost every first-party lock
// refresh for weeks.
func Compile(pu PostUpgrade, updates []model.Update, allowed []string) ([]model.Task, error) {
	if len(pu.Commands) == 0 || len(updates) == 0 {
		return nil, nil
	}
	patterns := make([]*re2x.Regexp, 0, len(allowed))
	for _, a := range allowed {
		re, err := re2x.Compile(a)
		if err != nil {
			return nil, fmt.Errorf("allowedCommands %q: %w", a, err)
		}
		patterns = append(patterns, re)
	}
	mode := pu.ExecutionMode
	if mode == "" {
		mode = model.ExecUpdate
	}
	var groups [][]model.Update
	if mode == model.ExecBranch {
		groups = [][]model.Update{updates}
	} else {
		for _, u := range updates {
			groups = append(groups, []model.Update{u})
		}
	}
	var out []model.Task
	for _, g := range groups {
		env := variables(g)
		dir := filepath.Dir(g[0].Dep.File)
		if dir == "." {
			dir = ""
		}
		for _, c := range pu.Commands {
			rendered, _, err := hbs.RenderString(c, env)
			if err != nil {
				return nil, fmt.Errorf("postUpgradeTasks command %q: %w", c, err)
			}
			argv := strings.Fields(rendered)
			if len(argv) == 0 {
				continue
			}
			by, err := Allowed(strings.Join(argv, " "), patterns)
			if err != nil {
				return nil, err
			}
			out = append(out, model.Task{
				Kind: model.TaskPostUpgrade, Manager: g[0].Dep.Manager, Dir: dir, Command: argv,
				ExecutionMode: mode, FileFilters: pu.FileFilters, AllowedBy: by, Origin: pu.Origin,
			})
		}
	}
	return out, nil
}

// Allowed reports which pattern admits a compiled command, or an error
// naming the command when none does. Patterns are anchored by the
// configuration that wrote them (^…$); an unanchored pattern is used as
// written, which is the author's responsibility and visible in the plan.
func Allowed(command string, patterns []*re2x.Regexp) (int, error) {
	for i, re := range patterns {
		if _, ok := re.Find(command); ok {
			return i, nil
		}
	}
	return -1, fmt.Errorf("command %q is not on the allowedCommands list", command)
}

func variables(g []model.Update) hbs.MapEnv {
	env := hbs.MapEnv{Values: map[string]string{}, Absent: map[string]bool{}}
	var names, files, newValues, currentValues []string
	for _, u := range g {
		names = append(names, u.Dep.DepName)
		files = append(files, u.Dep.File)
		newValues = append(newValues, u.NewValue)
		currentValues = append(currentValues, u.Dep.CurrentValue)
	}
	env.Values["depName"] = strings.Join(unique(names), " ")
	env.Values["packageFile"] = strings.Join(unique(files), " ")
	env.Values["newValue"] = strings.Join(newValues, " ")
	env.Values["currentValue"] = strings.Join(currentValues, " ")
	if len(g) == 1 {
		u := g[0]
		env.Values["newVersion"] = u.NewVersion
		env.Values["packageName"] = u.Dep.PackageName
		env.Values["datasource"] = u.Dep.Datasource
		env.Values["manager"] = u.Dep.Manager
		env.Values["updateType"] = u.Type.Renovate().String()
		for k, v := range env.Values {
			if v == "" {
				env.Absent[k] = true
			}
		}
	}
	return env
}

func unique(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Runner executes tasks on a checkout.
type Runner struct {
	// PassEnv names the environment variables handed to a task besides
	// the fixed baseline (PATH, HOME, LANG, LC_ALL, TMPDIR, TZ). The
	// runner decides what to expose - a registry credential for a
	// first-party package, say - and nothing else crosses: not the
	// platform token, not the signing key.
	PassEnv []string
	// Timeout bounds one task. Zero means fifteen minutes, Renovate's
	// default, which the estate's runner had to triple for its largest
	// composer repository.
	Timeout time.Duration
	// Getenv reads the process environment; nil means os.Getenv.
	Getenv func(string) string
	// LookPath resolves a command; nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Exec runs the prepared command; nil means running it. Tests inject
	// one to observe the environment without a process.
	Exec func(ctx context.Context, cmd *exec.Cmd) error
	// Now is the clock for the duration a task took; nil leaves it zero.
	Now func() time.Time
}

// Baseline is the environment every task gets, credentials excluded.
var Baseline = []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "TZ"}

// Blocked are variable names that never cross even when listed: the
// platform token and the signing material are the two things a task must
// not see.
var Blocked = []string{"PINUP_GITLAB_TOKEN", "GITLAB_TOKEN", "CI_JOB_TOKEN", "PINUP_SIGNING_KEY", "PINUP_GPG_PRIVATE_KEY", "GIT_ASKPASS"}

// Result is what one task produced.
type Result struct {
	Task     model.Task
	Duration time.Duration
	Stdout   string
	Stderr   string
}

// Missing is the error for a tool the job image does not carry: the branch
// is held pluginRequired, naming the tool, rather than pushed half-done.
type Missing struct{ Tool string }

func (m *Missing) Error() string { return "the " + m.Tool + " toolchain is not on PATH" }

// Available reports whether every task's tool is on PATH, naming the first
// that is not.
func (r *Runner) Available(tasks []model.Task) error {
	look := r.LookPath
	if look == nil {
		look = exec.LookPath
	}
	for _, t := range tasks {
		if len(t.Command) == 0 {
			continue
		}
		if _, err := look(t.Command[0]); err != nil {
			return &Missing{Tool: t.Command[0]}
		}
	}
	return nil
}

// Environment is the environment a task runs with.
func (r *Runner) Environment() []string {
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	blocked := map[string]bool{}
	for _, b := range Blocked {
		blocked[b] = true
	}
	seen := map[string]bool{}
	var env []string
	for _, name := range append(append([]string{}, Baseline...), r.PassEnv...) {
		if seen[name] || blocked[name] {
			continue
		}
		seen[name] = true
		if v := getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	sort.Strings(env)
	return env
}

// Run executes one task in the checkout at root. It reports the command's
// output; whether the files it changed are within scope is the caller's
// check, made against the repository afterwards (Scope).
func (r *Runner) Run(ctx context.Context, root string, t model.Task) (Result, error) {
	if len(t.Command) == 0 {
		return Result{}, errors.New("plugin: empty command")
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.Command[0], t.Command[1:]...)
	cmd.Dir = filepath.Join(root, t.Dir)
	cmd.Env = r.Environment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A hung tool is killed, not waited for: the timeout is the contract.
	cmd.WaitDelay = 5 * time.Second

	run := r.Exec
	if run == nil {
		run = func(_ context.Context, c *exec.Cmd) error { return c.Run() }
	}
	var started time.Time
	if r.Now != nil {
		started = r.Now()
	}
	err := run(ctx, cmd)
	res := Result{Task: t, Stdout: stdout.String(), Stderr: stderr.String()}
	if r.Now != nil {
		res.Duration = r.Now().Sub(started)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, fmt.Errorf("plugin: %s timed out after %s", strings.Join(t.Command, " "), timeout)
		}
		return res, fmt.Errorf("plugin: %s: %w: %s", strings.Join(t.Command, " "), err, strings.TrimSpace(stderr.String()))
	}
	return res, nil
}

// TaskRunner is the exec flavour for the runner's TaskRunner contract:
// Run's verdict without its output, which is folded into the error.
type TaskRunner struct{ *Runner }

// Run implements the runner's TaskRunner.
func (t TaskRunner) Run(ctx context.Context, root string, task model.Task) error {
	_, err := t.Runner.Run(ctx, root, task)
	return err
}
