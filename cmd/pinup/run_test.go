// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/config/toyaml"
	"github.com/ohartwig/pinup/fake/fixture"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunDispatches(t *testing.T) {
	for _, c := range []struct {
		args    []string
		wantErr bool
		wantOut string
	}{
		{[]string{"version"}, false, "dev"},
		{[]string{}, false, "usage: pinup"},
		{[]string{"nonesuch"}, true, ""},
		{[]string{"whatif"}, true, ""},
	} {
		var out, errw bytes.Buffer
		err := run(c.args, &out, &errw)
		if (err != nil) != c.wantErr {
			t.Errorf("run(%v): err = %v, want error %v", c.args, err, c.wantErr)
		}
		if c.wantOut != "" && !strings.Contains(out.String(), c.wantOut) {
			t.Errorf("run(%v): output %q does not contain %q", c.args, out.String(), c.wantOut)
		}
	}
}

// TestEveryCommandIsListed keeps `usage` from drifting away from the dispatch
// table - a command that dispatches but is not listed is undiscoverable.
func TestEveryCommandIsListed(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	for _, c := range commands() {
		if !strings.Contains(out.String(), c.name) {
			t.Errorf("command %q is not listed in usage", c.name)
		}
	}
}

func TestPrintConfigExplainAndDiff(t *testing.T) {
	var out, errw strings.Builder
	first := fixture.Expect(t).RulesResolved - fixture.Expect(t).RulesOwn
	err := run([]string{"print-config", "--config", fixture.Config(t),
		"--explain", fmt.Sprintf("packageRules[%d].enabled", first+46)}, &out, &errw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "> file:") || !strings.Contains(out.String(), "default.json") {
		t.Errorf("explain must name the file as the winner:\n%s", out.String())
	}
	if !strings.Contains(errw.String(), "mergeConfidence:all-badges") {
		t.Errorf("the inert-preset warning must reach stderr, got %q", errw.String())
	}

	out.Reset()
	err = run([]string{"print-config", "--config", fixture.Config(t),
		"--explain", "nonesuch"}, &out, &errw)
	if err == nil {
		t.Error("an unset path must be an error, not silence")
	}

	// Against the captured options - every option the container resolved,
	// without its rules, custom managers and descriptions - the diff must
	// fail, and what it lists as only here is exactly those three keys;
	// what is only in the capture describes the capture run (hostRules,
	// the repository, its branch list), which config's parity test drops
	// by name.
	out.Reset()
	err = run([]string{"print-config", "--config", fixture.Config(t),
		"--diff", fixture.Captured(t, "resolved-options.json")}, &out, &errw)
	if err == nil {
		t.Error("the rules and custom managers make the resolution larger than the options capture; the diff must say so")
	}
	extra, other := 0, 0
	for _, line := range strings.Split(out.String(), "\n") {
		switch {
		case strings.HasPrefix(line, "- packageRules["), strings.HasPrefix(line, "- customManagers["), strings.HasPrefix(line, "- description["):
			extra++
		case strings.HasPrefix(line, "- "):
			other++
			if other <= 5 {
				t.Errorf("a line differs beyond the rules and managers: %s", line)
			}
		}
	}
	if extra == 0 {
		t.Error("the diff listed no rule or manager lines; it compared nothing")
	}
}

// Every key default.json uses lands in exactly one class, and the classes
// are printed. The counts are asserted so the table cannot silently shrink.
func TestMigrateClassifiesEveryKey(t *testing.T) {
	var out strings.Builder
	if err := run([]string{"migrate", "--config", fixture.Config(t), "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Supported   []string `json:"supported"`
		Partial     []string `json:"partial"`
		Unsupported []string `json:"x-unsupported"`
		Managers    []string `json:"managersNotImplemented"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatal(err)
	}
	all := len(got.Supported) + len(got.Partial) + len(got.Unsupported)
	if all < 40 {
		t.Errorf("only %d keys classified", all)
	}
	seen := map[string]int{}
	for _, list := range [][]string{got.Supported, got.Partial, got.Unsupported} {
		for _, k := range list {
			seen[k]++
		}
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s classified %d times", k, n)
		}
	}
	for _, want := range []string{"extends", "packageRules", "packageRules[].matchPackageNames"} {
		if seen[want] == 0 {
			t.Errorf("%s not classified", want)
		}
	}
	// What is built is supported; what is read and ignored by design is
	// named, not dropped.
	for _, want := range []string{"lockFileMaintenance", "packageRules[].postUpgradeTasks", "vulnerabilityAlerts", "osvVulnerabilityAlerts", "dependencyDashboard", "packageRules[].fetchChangeLogs"} {
		if !contains(got.Supported, want) {
			t.Errorf("%s is built and must be supported: %v", want, got.Supported)
		}
	}
	// The runner's file uses nothing pinup does not read - every key it
	// carries is built. TestMigrateNamesWhatIsNotBuilt proves the class
	// can still be reached.
	if len(got.Unsupported) != 0 {
		t.Errorf("the runner configuration uses keys pinup does not read: %v", got.Unsupported)
	}
	// gitlabci-include is read by the gitlabci manager: nothing is missing
	// for the runner's list.
	if len(got.Managers) != 0 {
		t.Errorf("managers reported missing: %v", got.Managers)
	}
}

// A key nothing reads is named, not dropped: at the top level and inside
// a rule.
func TestMigrateNamesWhatIsNotBuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "renovate.json")
	if err := os.WriteFile(path, []byte(`{"extends":["config:recommended"],"fooBar":true,"packageRules":[{"matchCategories":["go"],"enabled":false}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := run([]string{"migrate", "--config", path, "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Unsupported []string `json:"x-unsupported"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fooBar", "packageRules[].matchCategories"} {
		if !contains(got.Unsupported, want) {
			t.Errorf("%s not named as unsupported: %v", want, got.Unsupported)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Every project runs once, failures are counted and named, and no more
// than the requested number run at the same time.
func TestForEachBoundsConcurrencyAndCountsFailures(t *testing.T) {
	var mu sync.Mutex
	running, peak := 0, 0
	seen := map[string]int{}
	var out, errw strings.Builder
	projects := []string{"a", "b", "c", "d", "e", "f", "g"}
	failed := forEach(projects, 3, func(p string, pout, perr io.Writer) error {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		seen[p]++
		mu.Unlock()
		// Two lines per project, written apart in time: they must come
		// out together, never interleaved with another project's.
		fmt.Fprintf(pout, "%s: first\n", p)
		time.Sleep(5 * time.Millisecond)
		fmt.Fprintf(pout, "%s: second\n", p)
		mu.Lock()
		running--
		mu.Unlock()
		if p == "c" || p == "f" {
			return errors.New("boom")
		}
		return nil
	}, &out, &errw)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2*len(projects) {
		t.Fatalf("%d output lines, want %d", len(lines), 2*len(projects))
	}
	for i := 0; i < len(lines); i += 2 {
		if lines[i][:1] != lines[i+1][:1] || !strings.HasSuffix(lines[i], "first") || !strings.HasSuffix(lines[i+1], "second") {
			t.Errorf("output interleaved: %q / %q", lines[i], lines[i+1])
		}
	}
	if failed != 2 {
		t.Errorf("failed = %d, want 2", failed)
	}
	if peak > 3 || peak < 2 {
		t.Errorf("peak concurrency = %d, want between 2 and 3", peak)
	}
	for _, p := range projects {
		if seen[p] != 1 {
			t.Errorf("%s ran %d times", p, seen[p])
		}
	}
	if !strings.Contains(errw.String(), "c: boom") || !strings.Contains(errw.String(), "f: boom") {
		t.Errorf("failures not named: %q", errw.String())
	}
	if got := repositoryConcurrency(func(string) string { return "" }); got != 4 {
		t.Errorf("default concurrency = %d, want 4", got)
	}
	if got := repositoryConcurrency(func(k string) string { return map[string]string{"PINUP_REPOSITORY_CONCURRENCY": "8"}[k] }); got != 8 {
		t.Errorf("configured concurrency = %d, want 8", got)
	}
}

// pinup migrate --to yaml rewrites a configuration and refuses to hand over
// a conversion that loads differently.
func TestMigrateToYAML(t *testing.T) {
	var out, errw bytes.Buffer
	if err := run([]string{"migrate", "--config", fixture.Config(t), "--to", "yaml"}, &out, &errw); err != nil {
		t.Fatalf("%v: %s", err, errw.String())
	}
	if !strings.HasPrefix(out.String(), "# ") || !strings.Contains(out.String(), "\npackageRules:\n") {
		t.Errorf("unexpected output head: %q", out.String()[:200])
	}
	if err := run([]string{"migrate", "--config", fixture.Config(t), "--to", "toml"}, &out, &errw); err == nil {
		t.Error("--to toml must be refused")
	}
	// The round trip the command guards: the runner's configuration,
	// converted and resolved, is the JSON resolution line for line, the
	// descriptions aside, every rule in order.
	src, err := os.ReadFile(fixture.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	converted, err := toyaml.Convert(src, "default.json", toyaml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sameResolution(src, "default.json", converted, preset.Builtin()); err != nil {
		t.Fatal(err)
	}
	layer, err := config.Parse(converted, "default.pinup.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := config.ResolveLayer(layer, preset.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	if rules, _ := r.Raw["packageRules"].([]any); len(rules) != fixture.Expect(t).RulesResolved {
		t.Errorf("the converted configuration resolves to %d rules, want %d", len(rules), fixture.Expect(t).RulesResolved)
	}
	if strings.Contains(string(converted), "description:") {
		t.Error("a description key survived; it is a comment now")
	}
}

// notify estate renders the overview from plan files and refuses a scan
// that saw too little.
func TestNotifyEstateFromPlans(t *testing.T) {
	dir := t.TempDir()
	for _, g := range []string{"estate", "lock"} {
		src, err := os.ReadFile(fixture.Path(t, "golden", g, "plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, g+".json"), src, 0o644)
	}
	var out, errw bytes.Buffer
	outPath := filepath.Join(dir, "estate.md")
	if err := run([]string{"notify", "estate", "--plans", filepath.Join(dir, "*.json"), "--dry-run", "--out", outPath}, &out, &errw); err != nil {
		t.Fatalf("%v: %s", err, errw.String())
	}
	body, _ := os.ReadFile(outPath)
	if !strings.Contains(string(body), "| Repositories | Dependencies |") || !strings.Contains(string(body), "<details><summary>") {
		t.Errorf("overview: %s", body)
	}
	if !strings.Contains(errw.String(), "2 plans, 2 repositories") {
		t.Errorf("stderr: %s", errw.String())
	}
	if err := run([]string{"notify", "estate", "--plans", filepath.Join(dir, "*.json"), "--dry-run", "--min-refs", "10000"}, &out, &errw); err == nil {
		t.Error("a scan with too few dependencies must fail")
	}
}

// A repository file that extends the runner: migrate resolves it through
// --runner as a run would, renames the extends entry it is told to,
// drops the Renovate schema line, and refuses when the result would
// resolve differently.
func TestMigrateRewritesARepositoryFileAgainstTheRunner(t *testing.T) {
	// --runner names a path here; the project the aliases answer for
	// comes from the environment, as in the runner's own job.
	t.Setenv("PINUP_RUNNER_PROJECT", "pinup/runner")
	dir := t.TempDir()
	src := "{\n  \"$schema\": \"https://docs.renovatebot.com/renovate-schema.json\",\n  // the runner's file, by its old name\n  \"extends\": [\"local>devops/renovate-runner\"],\n  \"packageRules\": [{\"matchDepNames\": [\"alpine\"], \"automerge\": true}]\n}\n"
	path := filepath.Join(dir, "renovate.json")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without a runner the extends is unknown, and that is an error, not
	// a silent drop.
	if err := run([]string{"migrate", "--config", path, "--json"}, io.Discard, io.Discard); err == nil {
		t.Fatal("a local> extends without --runner must fail")
	}
	var out strings.Builder
	if err := run([]string{"migrate", "--config", path, "--runner", fixture.Config(t), "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"packageRules.automerge"`) && !strings.Contains(out.String(), "automerge") {
		t.Errorf("the repository's rule is not classified: %s", out.String())
	}
	yamlPath := filepath.Join(dir, ".pinup.yaml")
	if err := run([]string{"migrate", "--config", path, "--runner", fixture.Config(t), "--to", "yaml",
		"--extends", "local>devops/renovate-runner=local>pinup/runner", "--out", yamlPath}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "$schema") || strings.Contains(string(got), "devops/renovate-runner") || !strings.Contains(string(got), "local>pinup/runner") {
		t.Errorf("rewritten file:\n%s", got)
	}
	// Resolved through the same runner, both files are one document
	// (the extends entry and the schema aside).
	sources := runnerSources(mustLoad(t, fixture.Config(t)), "pinup/runner", nil)
	before, _, err := config.ResolveFile(path, sources)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := config.ResolveFile(yamlPath, sources)
	if err != nil {
		t.Fatal(err)
	}
	flat := func(r *config.Resolved) map[string]string {
		m := map[string]string{}
		for _, l := range config.Flatten(r.Raw) {
			if l.Path == "$schema" || strings.HasPrefix(l.Path, "extends") || strings.Contains(l.Path, "description") {
				continue
			}
			m[l.Path] = l.Value
		}
		return m
	}
	if b, a := flat(before), flat(after); len(b) != len(a) || len(b) == 0 {
		t.Errorf("resolutions differ in size: %d before, %d after", len(b), len(a))
	} else {
		for k, v := range b {
			if a[k] != v {
				t.Errorf("%s: %q became %q", k, v, a[k])
			}
		}
	}
	// A rename that renames nothing is a typo.
	if err := run([]string{"migrate", "--config", path, "--runner", fixture.Config(t), "--to", "yaml",
		"--extends", "local>nobody=local>pinup/runner"}, io.Discard, io.Discard); err == nil {
		t.Error("an extends entry that is not in the file must be refused")
	}
	// A rename to a name the chain does not answer changes the resolution
	// and is refused by the guard.
	if err := run([]string{"migrate", "--config", path, "--runner", fixture.Config(t), "--to", "yaml",
		"--extends", "local>devops/renovate-runner=local>somewhere/else"}, io.Discard, io.Discard); err == nil {
		t.Error("a rename to an unknown preset must be refused")
	}
}

func mustLoad(t *testing.T, path string) map[string]any {
	t.Helper()
	l, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return l.Raw
}
