// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package plugin

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/re2x"
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
	// An unlisted command is refused by name, never dropped silently.
	if _, err := Compile(PostUpgrade{Commands: []string{"make lock"}}, ups[:1], estateAllowed); err == nil || !strings.Contains(err.Error(), `"make lock"`) {
		t.Errorf("unlisted command: %v", err)
	}
}

func TestLockRefreshCommands(t *testing.T) {
	c, ok := LockRefresh("composer", "app", []string{"a/b", "c/d", "a/b"}, false)
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
	m, _ := LockRefresh("composer", "", nil, true)
	if strings.Join(m.Command, " ") != "composer update --no-plugins --no-install --no-scripts --no-audit --ignore-platform-reqs" {
		t.Errorf("maintenance: %v", m.Command)
	}
	n, ok := LockRefresh("npm", "", []string{"lodash"}, false)
	if !ok || strings.Join(n.Command, " ") != "npm install --package-lock-only --no-audit --ignore-scripts lodash" {
		t.Errorf("npm: %+v", n)
	}
	if _, ok := LockRefresh("dockerfile", "", nil, false); ok {
		t.Error("dockerfile has no lock to refresh")
	}
}

// P1d.3: a task sees exactly the allowlisted variables - never the
// platform token or the signing key, even when they are listed - and a
// hanging command is killed at the timeout and reported as such.
func TestEnvironmentIsAnAllowlistAndTimeoutsAreEnforced(t *testing.T) {
	env := map[string]string{
		"PATH": "/usr/bin", "HOME": "/h", "LANG": "C", "COMPOSER_AUTH": "{...}", "NPM_TOKEN": "n",
		"PINUP_GITLAB_TOKEN": "secret", "PINUP_SIGNING_KEY": "key", "GIT_ASKPASS": "/tmp/x", "RANDOM_VAR": "no",
	}
	r := &Runner{PassEnv: []string{"COMPOSER_AUTH", "PINUP_GITLAB_TOKEN", "GIT_ASKPASS"}, Getenv: func(k string) string { return env[k] }}
	got := strings.Join(r.Environment(), ",")
	if got != "COMPOSER_AUTH={...},HOME=/h,LANG=C,PATH=/usr/bin" {
		t.Errorf("environment = %s", got)
	}

	var seenEnv []string
	r = &Runner{Getenv: func(k string) string { return env[k] }, Exec: func(_ context.Context, c *exec.Cmd) error { seenEnv = c.Env; return nil }}
	if _, err := r.Run(context.Background(), t.TempDir(), model.Task{Command: []string{"composer", "update"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seenEnv, ",") != "HOME=/h,LANG=C,PATH=/usr/bin" {
		t.Errorf("the process got %v", seenEnv)
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
