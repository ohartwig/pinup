// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
)

// The runner's default and its fast-lane file, the shape of pinup/runner's:
// the fast lane extends the default by its alias.
const (
	testRunnerDefault = `{
  "branchPrefix": "pinup/",
  "osvVulnerabilityAlerts": true,
  "packageRules": [
    {"description": "default rule", "matchPackageNames": ["a"], "automerge": true}
  ]
}`
	testRunnerFast = `{
  "extends": ["local>pinup/runner"],
  "osvVulnerabilityAlerts": false,
  "packageRules": [
    {"description": "fast rule", "matchPackageNames": ["/.*/"], "enabled": false}
  ]
}`
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ruleDescriptions(t *testing.T, raw map[string]any) []string {
	t.Helper()
	rules, _ := raw["packageRules"].([]any)
	var out []string
	for _, r := range rules {
		m, _ := r.(map[string]any)
		switch d := m["description"].(type) {
		case string:
			out = append(out, d)
		case []any:
			if len(d) > 0 {
				s, _ := d[0].(string)
				out = append(out, s)
			}
		default:
			out = append(out, "")
		}
	}
	return out
}

func resolveForTest(t *testing.T, repoFile, repoBody, cfgPath, runnerDefault string) *config.Resolved {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if repoFile != "" {
		writeTestFile(t, filepath.Join(repo, repoFile), repoBody)
	}
	_, r, _, _, err := resolveConfig(repo, cfgPath, runnerDefault, "pinup/runner", preset.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A repository file that does not extend the runner adds to it; it does not
// replace it. Until this was fixed a two-line .pinup.yaml - one rule holding
// back doctrine/dbal, no extends - ran its repository under that rule alone:
// no branch prefix, no release age, no custom managers, no advisory check.
// The same held for every site package that opted in to
// local>pinup/runner:typo3-extension, which extends nothing itself.
func TestRepositoryFileWithoutRunnerExtendsIsLayeredOverTheRunner(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "default.json")
	writeTestFile(t, def, testRunnerDefault)

	r := resolveForTest(t, ".pinup.yaml", "packageRules:\n  - description: repo rule\n    matchPackageNames: [doctrine/dbal]\n    allowedVersions: \"<4.5\"\n", def, "")
	if got := r.Raw["branchPrefix"]; got != "pinup/" {
		t.Errorf("branchPrefix = %v, want the runner's pinup/", got)
	}
	if on, _ := r.Raw["osvVulnerabilityAlerts"].(bool); !on {
		t.Errorf("osvVulnerabilityAlerts = %v, want the runner's true", r.Raw["osvVulnerabilityAlerts"])
	}
	if got := ruleDescriptions(t, r.Raw); len(got) != 2 || got[0] != "default rule" || got[1] != "repo rule" {
		t.Errorf("rules = %q, want the runner's first and the repository's last", got)
	}
	if origins := r.Prov["/branchPrefix"]; len(origins) == 0 || origins[len(origins)-1].Source != "preset:"+runnerBasePreset {
		t.Errorf("provenance of branchPrefix = %+v, want preset:%s", origins, runnerBasePreset)
	}
}

// The repository's own keys still come last and decide.
func TestRepositoryKeysWinOverTheRunnerUnderThem(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "default.json")
	writeTestFile(t, def, testRunnerDefault)

	r := resolveForTest(t, ".pinup.json", `{"lockFileMaintenance": {"enabled": false}, "branchPrefix": "own/"}`, def, "")
	if got := r.Raw["branchPrefix"]; got != "own/" {
		t.Errorf("branchPrefix = %v, want the repository's own/", got)
	}
	lfm, _ := r.Raw["lockFileMaintenance"].(map[string]any)
	if on, _ := lfm["enabled"].(bool); on {
		t.Errorf("lockFileMaintenance = %v, want the repository's off", lfm)
	}
	if got := ruleDescriptions(t, r.Raw); len(got) != 1 || got[0] != "default rule" {
		t.Errorf("rules = %q, want the runner's", got)
	}
}

// A repository that extends the runner by its alias - every estate
// repository with a renovate.json - resolves exactly as before: the runner
// once, not twice, so rule numbers stay what the shadow comparator, the
// goldens and --explain speak in.
func TestRepositoryExtendingTheRunnerIsNotLayeredTwice(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "default.json")
	writeTestFile(t, def, testRunnerDefault)

	for _, ext := range []string{"local>devops/renovate-runner", "local>pinup/runner", "local>devops/renovate-runner:default.json"} {
		r := resolveForTest(t, "renovate.json", `{"extends": ["`+ext+`"], "packageRules": [{"description": "repo rule", "matchPackageNames": ["b"], "enabled": false}]}`, def, "")
		if got := ruleDescriptions(t, r.Raw); len(got) != 2 || got[0] != "default rule" || got[1] != "repo rule" {
			t.Errorf("%s: rules = %q, want the runner's once and the repository's", ext, got)
		}
	}
}

// The fast lane: a repository whose file does not reach the runner runs
// under release-fast.json, which brings the default with it. One that
// extends the runner resolves the alias to default.json and does not see
// release-fast.json - the documented behaviour images.json relies on.
func TestFastLaneLayersUnderAFileThatDoesNotExtendTheRunner(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "default.json")
	fast := filepath.Join(dir, "release-fast.json")
	writeTestFile(t, def, testRunnerDefault)
	writeTestFile(t, fast, testRunnerFast)

	r := resolveForTest(t, ".pinup.json", `{"packageRules": [{"description": "repo rule", "matchPackageNames": ["c"], "enabled": false}]}`, fast, def)
	if got := ruleDescriptions(t, r.Raw); len(got) != 3 || got[0] != "default rule" || got[1] != "fast rule" || got[2] != "repo rule" {
		t.Errorf("rules = %q, want default, fast, repository", got)
	}
	if on, _ := r.Raw["osvVulnerabilityAlerts"].(bool); on {
		t.Error("osvVulnerabilityAlerts on: release-fast.json's false did not apply")
	}

	r = resolveForTest(t, "renovate.json", `{"extends": ["local>devops/renovate-runner"]}`, fast, def)
	if got := ruleDescriptions(t, r.Raw); len(got) != 1 || got[0] != "default rule" {
		t.Errorf("extending the alias: rules = %q, want default.json's alone", got)
	}

	// No file: the run's configuration alone, as before.
	r = resolveForTest(t, "", "", fast, def)
	if got := ruleDescriptions(t, r.Raw); len(got) != 2 || got[0] != "default rule" || got[1] != "fast rule" {
		t.Errorf("no file: rules = %q, want default and fast", got)
	}
}

// whatif --config release-fast.json resolved the runner's alias to
// release-fast.json itself, which extends the alias: a preset cycle. run
// found the default beside the file; whatif did not look. Both use
// runnerConfig now.
func TestRunnerConfigFindsTheDefaultBesideALaneFile(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "default.json")
	fast := filepath.Join(dir, "release-fast.json")
	writeTestFile(t, def, testRunnerDefault)
	writeTestFile(t, fast, testRunnerFast)

	path, runnerDefault, cleanup, err := runnerConfig(context.Background(), nil, fast)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if path != fast || runnerDefault != def {
		t.Fatalf("runnerConfig = %q, %q; want %q, %q", path, runnerDefault, fast, def)
	}
	if _, _, _, err := runnerConfig(context.Background(), nil, "local>pinup/runner:release-fast"); err == nil {
		t.Error("a local> configuration without an instance resolved")
	}
	if _, runnerDefault, _, _ := runnerConfig(context.Background(), nil, def); runnerDefault != "" {
		t.Errorf("the default itself named a separate default: %q", runnerDefault)
	}

	// The whole path, from the configuration-less repository the cycle
	// was seen on.
	r := resolveForTest(t, "", "", path, runnerDefault)
	if got := ruleDescriptions(t, r.Raw); len(got) != 2 {
		t.Errorf("rules = %q", got)
	}
}
