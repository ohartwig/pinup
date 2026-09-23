// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/re2x"
)

// The estate's runner allowlist, as its .gitlab-ci.yml sets it.
var estateAllowed = []string{
	`^node scripts/update-pass-cli-hashes\.mjs$`,
	`^node tools/update-expected-commit\.mjs [^;&|]+$`,
	`^composer update [^;&|]+$`,
}

func upd(file, dep, cur, next string) model.Update {
	return model.Update{Dep: model.Dependency{Manager: "custom.regex", File: file, DepName: dep, CurrentValue: cur, CustomManager: 0}, NewValue: next, NewVersion: next, Type: model.UpdatePatch}
}

// P1d.2: the three measured patterns admit exactly the three measured
// commands, each recorded by the pattern that admitted it; a chaining
// character smuggled in through a depName is refused; branch mode carries
// every name in one command.
func TestCompileMatchesTheEstateAllowlist(t *testing.T) {
	branch := PostUpgrade{
		Commands:      []string{"composer update {{{depName}}} --with-all-dependencies --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs"},
		ExecutionMode: model.ExecBranch, FileFilters: []string{"composer.json", "composer.lock"},
	}
	ups := []model.Update{upd("composer.json", "moselwal/dev", "5.0.0", "5.1.0"), upd("composer.json", "koh/core", "1.0.0", "1.1.0"), upd("composer.json", "moselwal/dev", "5.0.0", "5.1.0")}
	tasks, err := Compile(branch, ups, estateAllowed)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].AllowedBy != 2 {
		t.Fatalf("branch mode: %+v", tasks)
	}
	if got := strings.Join(tasks[0].Command, " "); got != "composer update moselwal/dev koh/core --with-all-dependencies --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs" {
		t.Errorf("command = %q", got)
	}

	update := PostUpgrade{Commands: []string{"node scripts/update-pass-cli-hashes.mjs"}, ExecutionMode: model.ExecUpdate, FileFilters: []string{"**/Containerfile"}}
	tasks, err = Compile(update, ups[:2], estateAllowed)
	if err != nil || len(tasks) != 2 || tasks[0].AllowedBy != 0 || tasks[1].AllowedBy != 0 {
		t.Fatalf("update mode: %v %+v", err, tasks)
	}
	commit := PostUpgrade{Commands: []string{"node tools/update-expected-commit.mjs {{{depName}}}"}}
	if tasks, err := Compile(commit, ups[:1], estateAllowed); err != nil || len(tasks) != 1 || tasks[0].AllowedBy != 1 {
		t.Errorf("update-expected-commit: %v %+v", err, tasks)
	}

	for _, bad := range []string{"a; rm -rf /", "a && b", "a | b", "a || b"} {
		_, err := Compile(branch, []model.Update{upd("composer.json", bad, "1", "2")}, estateAllowed)
		if err == nil || !strings.Contains(err.Error(), "not on the allowedCommands list") {
			t.Errorf("depName %q was admitted: %v", bad, err)
		}
	}
	// A pattern is anchored whatever its author wrote: it admits exactly
	// what it spells out, not a longer command. The pattern has to spell
	// out the mandatory flags too - they are appended before the match, so
	// that what is checked is what runs.
	loose := []string{"composer update --no-plugins --no-scripts"}
	if tasks, err := Compile(PostUpgrade{Commands: []string{"composer update"}}, ups[:1], loose); err != nil || len(tasks) != 1 {
		t.Errorf("the exact command: %v %+v", err, tasks)
	}
	if _, err := Compile(PostUpgrade{Commands: []string{"composer update --scripts-hook=x"}}, ups[:1], loose); err == nil {
		t.Error("an unanchored pattern admitted a longer command")
	}
	// And a pattern that does NOT admit them refuses the task rather than
	// running it unhardened: the safe direction, and the error names the
	// command with the flags in it so the pattern can be corrected.
	if _, err := Compile(PostUpgrade{Commands: []string{"composer update"}}, ups[:1], []string{"composer update"}); err == nil {
		t.Error("a pattern that excludes the mandatory flags admitted the task")
	}
	// An unlisted command is refused by name, never dropped silently.
	if _, err := Compile(PostUpgrade{Commands: []string{"make lock"}}, ups[:1], estateAllowed); err == nil || !strings.Contains(err.Error(), `"make lock"`) {
		t.Errorf("unlisted command: %v", err)
	}
}

// The flags that stop a package manager running code out of the checkout
// are appended to a repository's own command, not left to the operator's
// pattern. Without them `composer update` executes scripts.pre-update-cmd
// from the checkout's composer.json - the attacker's file - as the bot.
func TestRepositoryCommandsAreHardened(t *testing.T) {
	ups := []model.Update{upd("composer.json", "a/b", "1", "2")}
	admitAnything := []string{".*"}
	for _, c := range []struct {
		name, command string
		want          string
	}{
		{"composer gains both flags", "composer update a/b",
			"composer update a/b --no-plugins --no-scripts"},
		{"a flag already there is not repeated", "composer update a/b --no-scripts",
			"composer update a/b --no-scripts --no-plugins"},
		{"npm", "npm install --package-lock-only a/b",
			"npm install --package-lock-only a/b --ignore-scripts"},
		{"yarn", "yarn install", "yarn install --ignore-scripts"},
		{"a path is still composer", "/usr/bin/composer update",
			"/usr/bin/composer update --no-plugins --no-scripts"},
		{"anything else is left alone", "node tools/x.mjs", "node tools/x.mjs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tasks, err := Compile(PostUpgrade{Commands: []string{c.command}}, ups, admitAnything)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("%v %+v", err, tasks)
			}
			if got := strings.Join(tasks[0].Command, " "); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	// The core's own refresh already carries them; hardening must not
	// double them up there either.
	c, _ := LockRefresh("composer", "", "composer.lock", []string{"a/b"}, false)
	if n := strings.Count(strings.Join(c.Command, " "), "--no-scripts"); n != 1 {
		t.Errorf("LockRefresh carries --no-scripts %d times", n)
	}
}

func TestLockRefreshCommands(t *testing.T) {
	c, ok := LockRefresh("composer", "app", "composer.lock", []string{"a/b", "c/d", "a/b"}, false)
	if !ok || strings.Join(c.Command, " ") != "composer update a/b c/d --with-all-dependencies --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs" || c.Dir != "app" || c.AllowedBy != -1 {
		t.Errorf("composer: %+v", c)
	}
	// The estate's own allowlist admits the composer refresh pinup composes.
	re, err := re2x.Compile(estateAllowed[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Allowed(strings.Join(c.Command, " "), []*re2x.Regexp{re}); err != nil {
		t.Errorf("the estate allowlist refuses pinup's own refresh: %v", err)
	}
	m, _ := LockRefresh("composer", "", "composer.lock", nil, true)
	if strings.Join(m.Command, " ") != "composer update --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs" {
		t.Errorf("maintenance: %v", m.Command)
	}
	n, ok := LockRefresh("npm", "", "package-lock.json", []string{"lodash"}, false)
	if !ok || strings.Join(n.Command, " ") != "npm install --package-lock-only --no-audit --ignore-scripts lodash" {
		t.Errorf("npm: %+v", n)
	}
	if _, ok := LockRefresh("dockerfile", "", "", nil, false); ok {
		t.Error("dockerfile has no lock to refresh")
	}
}

// P1d.3: a task sees exactly the allowlisted variables - never the
// platform token, the signing key or the job's HOME, even when they are
// listed - and a hanging command is killed at the timeout and reported as
// such.
func TestEnvironmentIsAnAllowlistAndTimeoutsAreEnforced(t *testing.T) {
	env := map[string]string{
		"PATH": "/usr/bin", "HOME": "/h", "LANG": "C", "COMPOSER_AUTH": "{...}", "NPM_TOKEN": "n",
		"PINUP_GITLAB_TOKEN": "secret", "PINUP_SIGNING_KEY": "key", "GIT_ASKPASS": "/tmp/x", "RANDOM_VAR": "no",
		"GNUPGHOME": "/h/.gnupg",
	}
	r := &Runner{PassEnv: []string{"COMPOSER_AUTH", "PINUP_GITLAB_TOKEN", "GIT_ASKPASS", "HOME", "GNUPGHOME"}, Getenv: func(k string) string { return env[k] }}
	got := strings.Join(r.Environment("/scratch"), ",")
	if got != "COMPOSER_AUTH={...},HOME=/scratch,LANG=C,PATH=/usr/bin" {
		t.Errorf("environment = %s", got)
	}

	var seenEnv []string
	r = &Runner{Getenv: func(k string) string { return env[k] }, Exec: func(_ context.Context, c *exec.Cmd) error { seenEnv = c.Env; return nil }}
	if _, err := r.Run(context.Background(), t.TempDir(), model.Task{Command: []string{"composer", "update"}}); err != nil {
		t.Fatal(err)
	}
	if len(seenEnv) != 3 || !strings.HasPrefix(seenEnv[0], "HOME=") || seenEnv[0] == "HOME=/h" || seenEnv[1] != "LANG=C" || seenEnv[2] != "PATH=/usr/bin" {
		t.Errorf("the process got %v", seenEnv)
	}
	if _, err := os.Stat(strings.TrimPrefix(seenEnv[0], "HOME=")); !os.IsNotExist(err) {
		t.Errorf("the scratch home outlived the task: %v", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("sleep")
	}
	r = &Runner{Timeout: 200 * time.Millisecond, Getenv: func(string) string { return "" }}
	_, err := r.Run(context.Background(), t.TempDir(), model.Task{Command: []string{"sleep", "5"}})
	if err == nil || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Errorf("hanging command: %v", err)
	}
	var missing *Missing
	if err := r.Available([]model.Task{{Command: []string{"no-such-tool-xyz"}}}); !errors.As(err, &missing) || missing.Tool != "no-such-tool-xyz" {
		t.Errorf("missing tool: %v", err)
	}
}

// A yarn.lock beside package.json is refreshed by yarn, not npm: install
// for a changed manifest, upgrade for a maintenance, scripts and checks
// off, the lock the only file in scope.
func TestYarnLockIsRefreshedByYarn(t *testing.T) {
	y, ok := LockRefresh("npm", "", "yarn.lock", []string{"lodash"}, false)
	if !ok || y.Command[0] != "yarn" || y.Command[1] != "install" || !strings.Contains(strings.Join(y.Command, " "), "--ignore-scripts") || y.FileFilters[0] != "yarn.lock" {
		t.Errorf("yarn refresh = %+v", y)
	}
	m, _ := LockRefresh("npm", "", "yarn.lock", nil, true)
	if m.Command[1] != "upgrade" {
		t.Errorf("yarn maintenance = %+v", m)
	}
	g, ok := LockRefresh("gomod", "", "go.sum", nil, false)
	if !ok || g.Command[0] != "go" {
		t.Errorf("gomod refresh = %+v", g)
	}
}

// A task's output is bounded and the cut is marked.
func TestTaskOutputIsBounded(t *testing.T) {
	var b limitedBuffer
	b.limit = 10
	b.Write([]byte("0123456"))
	b.Write([]byte("789abcdef"))
	if got := b.String(); got != "0123456789\n… (output truncated)" {
		t.Errorf("got %q", got)
	}
	var small limitedBuffer
	small.limit = 10
	small.Write([]byte("short"))
	if small.String() != "short" {
		t.Errorf("an output within the limit is not marked: %q", small.String())
	}
}

// The cutover's joint test (docs/plan.md §6.4, step 4): the runner's
// allowlist exactly as pinup/runner's .gitlab-ci.yml sets it - `[.]` for
// the dot, since the value crosses YAML and a variable expansion - against
// the post-upgrade tasks of the repository that has the most security
// merge requests in the estate, devops/wolfi-packages: a raised commit pin
// runs update-expected-commit and raise-epoch on its recipe, a Go module
// pin both, in update mode, each admitted by its own pattern. Thirteen
// [security] merge requests once merged green while the allowlist silently
// rejected the task; here a rejection holds the branch by name.
func TestWolfiPackagesTasksPassTheRunnerAllowlist(t *testing.T) {
	runnerAllowed := []string{
		"^node scripts/update-pass-cli-hashes[.]mjs$",
		"^node tools/update-expected-commit[.]mjs [^;&|]+$",
		"^node tools/raise-epoch[.]mjs [^;&|]+$",
		"^composer update [^;&|]+$",
		"^npm install --package-lock-only [^;&|]+$",
	}
	goPin := PostUpgrade{
		Commands:      []string{"node tools/update-expected-commit.mjs {{{packageFile}}}", "node tools/raise-epoch.mjs {{{packageFile}}}"},
		ExecutionMode: model.ExecUpdate, FileFilters: []string{"*.yaml"},
	}
	up := upd("ksops.yaml", "github.com/getsops/sops/v3", "v3.10.2", "v3.11.0")
	tasks, err := Compile(goPin, []model.Update{up}, runnerAllowed)
	if err != nil {
		t.Fatalf("the Go pin's tasks were refused: %v", err)
	}
	if len(tasks) != 2 || tasks[0].AllowedBy != 1 || tasks[1].AllowedBy != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	for i, want := range []string{"node tools/update-expected-commit.mjs ksops.yaml", "node tools/raise-epoch.mjs ksops.yaml"} {
		if got := strings.Join(tasks[i].Command, " "); got != want {
			t.Errorf("task %d = %q, want %q", i, got, want)
		}
	}
	// A recipe path a repository could not carry - one with a chaining
	// character - is refused by the pattern, not run.
	if _, err := Compile(goPin, []model.Update{upd("ksops.yaml; id", "x", "1", "2")}, runnerAllowed); err == nil {
		t.Error("a packageFile with a chaining character was admitted")
	}
}

// A .netrc the runner composes lands in the task's scratch HOME, mode
// 0600, and goes with it; the variable it came from never crosses as an
// environment variable.
func TestNetrcIsWrittenIntoTheScratchHomeOnly(t *testing.T) {
	env := map[string]string{"PATH": "/usr/bin", "PINUP_TASK_NETRC": "machine git.example.test login oauth2 password t0k"}
	var seenEnv []string
	var seenNetrc string
	var mode os.FileMode
	r := &Runner{
		PassEnv: []string{"PINUP_TASK_NETRC"}, Netrc: env["PINUP_TASK_NETRC"],
		Getenv: func(k string) string { return env[k] },
		Exec: func(_ context.Context, c *exec.Cmd) error {
			seenEnv = c.Env
			for _, e := range c.Env {
				if home, ok := strings.CutPrefix(e, "HOME="); ok {
					raw, err := os.ReadFile(filepath.Join(home, ".netrc"))
					if err != nil {
						return err
					}
					st, _ := os.Stat(filepath.Join(home, ".netrc"))
					mode = st.Mode().Perm()
					seenNetrc = string(raw)
				}
			}
			return nil
		},
	}
	if _, err := r.Run(context.Background(), t.TempDir(), model.Task{Command: []string{"go", "mod", "tidy"}}); err != nil {
		t.Fatal(err)
	}
	if seenNetrc != "machine git.example.test login oauth2 password t0k\n" || mode != 0o600 {
		t.Errorf("netrc = %q mode %v", seenNetrc, mode)
	}
	for _, e := range seenEnv {
		if strings.HasPrefix(e, "PINUP_TASK_NETRC=") {
			t.Error("the netrc crossed as a variable")
		}
	}
}

// A platform credential does not reach a task under another name. The first
// row is the estate's runner as it was until 2026-09-23: COMPOSER_AUTH and
// NPM_TOKEN built from PINUP_GITLAB_TOKEN, both on PassEnv - every name
// check passed, and every task held the estate's write token. The refusal
// names the variables and never the value, and the task never starts.
func TestAPlatformCredentialDoesNotCrossUnderAnotherName(t *testing.T) {
	const platform = "glpat-platform-write-token"
	const read = "glpat-read-only-api-token"
	for _, tc := range []struct {
		name    string
		env     map[string]string
		netrc   string
		refused string
	}{
		{"the runner until 2026-09-23", map[string]string{
			"PINUP_GITLAB_TOKEN": platform,
			"COMPOSER_AUTH":      `{"gitlab-token":{"git.example.test":"` + platform + `"}}`,
			"NPM_TOKEN":          platform,
		}, "", "COMPOSER_AUTH carries the value of PINUP_GITLAB_TOKEN"},
		{"the job token copied verbatim", map[string]string{
			"CI_JOB_TOKEN": "glcbt-64_the-job-token",
			"NPM_TOKEN":    "glcbt-64_the-job-token",
		}, "", "NPM_TOKEN carries the value of CI_JOB_TOKEN"},
		{"the signing key inside a variable", map[string]string{
			"PINUP_GPG_PRIVATE_KEY": "-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQcYBGabc\n-----END PGP PRIVATE KEY BLOCK-----",
			"COMPOSER_AUTH":         "x -----BEGIN PGP PRIVATE KEY BLOCK-----\nlQcYBGabc\n-----END PGP PRIVATE KEY BLOCK----- y",
		}, "", "COMPOSER_AUTH carries the value of PINUP_GPG_PRIVATE_KEY"},
		{"the platform token in the netrc", map[string]string{
			"PINUP_GITLAB_TOKEN": platform,
		}, "machine git.example.test login pinup-bot password " + platform, "the task's .netrc carries the value of PINUP_GITLAB_TOKEN"},
		{"the runner from 2026-09-23: a read token of its own", map[string]string{
			"PINUP_GITLAB_TOKEN": platform,
			"COMPOSER_AUTH":      `{"gitlab-token":{"git.example.test":"` + read + `"}}`,
			"NPM_TOKEN":          read,
		}, "machine git.example.test login pinup-bot password glpat-module-read", ""},
		// Below minSecret nothing is compared: "tok" is inside "stock",
		// and a refusal for that would be noise, not a finding.
		{"a value too short to be a token", map[string]string{
			"PINUP_GITLAB_TOKEN": "tok",
			"NPM_TOKEN":          "stock",
		}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.env["PATH"] = "/usr/bin"
			started := false
			r := &Runner{
				PassEnv: []string{"COMPOSER_AUTH", "NPM_TOKEN"}, Netrc: tc.netrc,
				Getenv: func(k string) string { return tc.env[k] },
				Exec:   func(context.Context, *exec.Cmd) error { started = true; return nil },
			}
			_, err := r.Run(t.Context(), t.TempDir(), model.Task{Command: []string{"composer", "update", "x"}})
			if tc.refused == "" {
				if err != nil || !started {
					t.Fatalf("a clean environment was refused: started=%t err=%v", started, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("err = %v, want it to say %q", err, tc.refused)
			}
			if started {
				t.Error("the task started anyway")
			}
			for _, secret := range Secrets {
				if v := tc.env[secret]; len(v) >= minSecret && strings.Contains(err.Error(), v) {
					t.Errorf("the refusal printed the value of %s", secret)
				}
			}
		})
	}
}

// Every name whose value is compared is also a name that never crosses: a
// secret that could still travel under its own name would make the value
// check the only line, and a check of one line is not two.
func TestSecretsAreBlockedByNameAsWell(t *testing.T) {
	for _, s := range Secrets {
		if !slices.Contains(Blocked, s) {
			t.Errorf("%s is compared by value but not blocked by name", s)
		}
	}
}

// Isolation wraps the command the task would have run, with the checkout
// and the scratch HOME kept; a refusal from it is the task's error, and the
// task never starts.
func TestATaskRunsThroughIsolation(t *testing.T) {
	root := t.TempDir()
	var kept []string
	var wrapped, cleaned, started bool
	r := &Runner{
		Getenv: func(string) string { return "" },
		Isolate: func(c *exec.Cmd, keep []string) (func(), error) {
			wrapped, kept = true, keep
			return func() { cleaned = true }, nil
		},
		Exec: func(context.Context, *exec.Cmd) error { started = true; return nil },
	}
	if _, err := r.Run(t.Context(), root, model.Task{Command: []string{"composer", "update", "x"}}); err != nil {
		t.Fatal(err)
	}
	if !wrapped || !started || !cleaned {
		t.Errorf("wrapped=%t started=%t cleaned=%t", wrapped, started, cleaned)
	}
	if len(kept) != 2 || kept[0] != root || !strings.Contains(kept[1], "pinup-task-home-") {
		t.Errorf("kept %v, want the checkout and the scratch home", kept)
	}

	started = false
	r.Isolate = func(*exec.Cmd, []string) (func(), error) { return nil, errors.New("the sandbox does not hold here") }
	_, err := r.Run(t.Context(), root, model.Task{Command: []string{"composer", "update", "x"}})
	if err == nil || !strings.Contains(err.Error(), "the sandbox does not hold here") || started {
		t.Errorf("refused isolation: err=%v started=%t", err, started)
	}
}
