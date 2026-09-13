// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"git.ole-hartwig.eu/pinup/pinup/config"
	"git.ole-hartwig.eu/pinup/pinup/config/preset"
	"git.ole-hartwig.eu/pinup/pinup/config/toyaml"
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
	err := run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--explain", "packageRules[768].enabled"}, &out, &errw)
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
	err = run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--explain", "nonesuch"}, &out, &errw)
	if err == nil {
		t.Error("an unset path must be an error, not silence")
	}

	// Against the direct resolution of the file - presets, no defaults -
	// every line the snapshot has is present here, and what is only here
	// is a builtin default.
	out.Reset()
	err = run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--diff", "../../testdata/parity/renovate-43.288.0/presets/default-resolved.json"}, &out, &errw)
	if err == nil {
		t.Error("the defaults make the resolution larger than the direct snapshot; the diff must say so")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		// The direct snapshot is pre-migration: its string descriptions
		// are lists here, its minimumReleaseAge "0" is null, and the
		// global-only executionTimeout is gone. Anything else the snapshot
		// has and this lacks is a real difference.
		if !strings.HasPrefix(line, "+ ") {
			continue
		}
		migrated := strings.Contains(line, ".description \"") ||
			strings.HasSuffix(line, `minimumReleaseAge "0"`) ||
			strings.HasPrefix(line, "+ executionTimeout ")
		if !migrated {
			t.Errorf("the snapshot has a line this resolution lacks: %s", line)
		}
	}
	// And the diff must be able to fail: the production-shape capture has
	// 1540 rules and differs.
	out.Reset()
	err = run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--diff", "../../testdata/parity/renovate-43.288.0/presets/runner-and-repo-resolved.json"}, &out, &errw)
	if err == nil || !strings.Contains(out.String(), "packageRules[1539]") {
		t.Errorf("diff against the 1540-rule capture must fail and show the extra rules: err=%v", err)
	}
}

// Every key default.json uses lands in exactly one class, and the classes
// are printed. The counts are asserted so the table cannot silently shrink.
func TestMigrateClassifiesEveryKey(t *testing.T) {
	var out strings.Builder
	if err := run([]string{"migrate", "--config", "../../testdata/parity/config/default.json", "--json"}, &out, io.Discard); err != nil {
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
	var errw strings.Builder
	projects := []string{"a", "b", "c", "d", "e", "f", "g"}
	failed := forEach(projects, 3, func(p string) error {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		seen[p]++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		if p == "c" || p == "f" {
			return errors.New("boom")
		}
		return nil
	}, &errw)
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
	if err := run([]string{"migrate", "--config", "../../testdata/parity/config/default.json", "--to", "yaml"}, &out, &errw); err != nil {
		t.Fatalf("%v: %s", err, errw.String())
	}
	if !strings.HasPrefix(out.String(), "# ") || !strings.Contains(out.String(), "\npackageRules:\n") {
		t.Errorf("unexpected output head: %q", out.String()[:200])
	}
	if err := run([]string{"migrate", "--config", "../../testdata/parity/config/default.json", "--to", "toml"}, &out, &errw); err == nil {
		t.Error("--to toml must be refused")
	}
	// The round trip the command guards: the runner's configuration,
	// converted and resolved, is the JSON resolution line for line, the
	// descriptions aside, 771 rules in order.
	src, err := os.ReadFile("../../testdata/parity/config/default.json")
	if err != nil {
		t.Fatal(err)
	}
	converted, err := toyaml.Convert(src, "default.json", toyaml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sameResolution(src, "default.json", converted); err != nil {
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
	if rules, _ := r.Raw["packageRules"].([]any); len(rules) != 771 {
		t.Errorf("the converted configuration resolves to %d rules, want 771", len(rules))
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
		src, err := os.ReadFile("../../testdata/golden/" + g + "/plan.json")
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
