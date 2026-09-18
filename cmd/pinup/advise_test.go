// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/advise"
	"github.com/ohartwig/pinup/fake/fixture"
)

type adviceJSON struct {
	File     string           `json:"file"`
	Skipped  []string         `json:"skipped"`
	Counts   map[string]int   `json:"counts"`
	Findings []advise.Finding `json:"findings"`
	Fix      *struct {
		Applied []advise.Applied `json:"applied"`
		Skipped []advise.Skipped `json:"skipped"`
		Wrote   string           `json:"wrote"`
	} `json:"fix"`
}

func adviseJSON(t *testing.T, args ...string) (adviceJSON, error) {
	t.Helper()
	var out, errw strings.Builder
	err := run(append([]string{"advise", "--json"}, args...), &out, &errw)
	var r adviceJSON
	if out.Len() > 0 {
		if jerr := json.Unmarshal([]byte(out.String()), &r); jerr != nil {
			t.Fatalf("output is not the report: %v\n%s", jerr, out.String())
		}
	}
	return r, err
}

// The runner's configuration through the command: findings, no errors,
// the plan checks skipped without --plan and run with one.
func TestAdviseReportsTheRunnerConfiguration(t *testing.T) {
	r, err := adviseJSON(t, "--config", fixture.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Counts["error"] != 0 || r.Counts["warn"] == 0 || r.Counts["info"] == 0 {
		t.Errorf("counts = %v", r.Counts)
	}
	if len(r.Skipped) == 0 {
		t.Error("without --plan the plan checks are skipped, and the report says so")
	}
	plan := fixture.Path(t, "golden", "estate", "plan.json")
	r, err = adviseJSON(t, "--config", fixture.Config(t), "--plan", plan, "--strict")
	if err != nil {
		t.Fatalf("--strict fails a configuration without errors: %v", err)
	}
	if len(r.Skipped) != 0 {
		t.Errorf("with --plan nothing is skipped: %v", r.Skipped)
	}
	found := false
	for _, f := range r.Findings {
		if f.ID == "plan/datasource-failing" {
			found = true
		}
	}
	if !found {
		t.Error("the golden plan's failing datasource is not reported")
	}
	if _, err := adviseJSON(t, "--config", fixture.Config(t), "--plan", "nonesuch/plan.json"); err == nil || !strings.Contains(err.Error(), "nonesuch/plan.json") {
		t.Errorf("a missing plan must fail naming the file, got %v", err)
	}
}

func TestAdviseStrictFailsOnAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "renovate.json")
	os.WriteFile(path, []byte(`{"automerge": true}`), 0o644)
	if _, err := adviseJSON(t, "--config", path); err != nil {
		t.Errorf("without --strict an error finding is reported, not failed: %v", err)
	}
	_, err := adviseJSON(t, "--config", path, "--strict")
	if err == nil || !strings.Contains(err.Error(), "1 errors") {
		t.Errorf("--strict must fail on the automerge error, got %v", err)
	}
	var out, errw strings.Builder
	if err := run([]string{"advise", "--config", path}, &out, &errw); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"error (1)", "sec/automerge-major", "fix:  set /major", "from: builtin"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, out.String())
		}
	}
}

// --fix is a dry run until --out or --write says where; the input is never
// touched by --out, and --write keeps the file's mode.
func TestAdviseFixWritesOnlyWhereTold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "renovate.jsonc")
	src := "// mine\n{\n  \"extends\": [\"config:recommended\"],\n  \"schedule\": [\"at any time\"], // always\n  \"nonesuchKey\": 1\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := adviseJSON(t, "--config", path, "--fix")
	if err != nil {
		t.Fatal(err)
	}
	if r.Fix == nil || len(r.Fix.Applied) < 2 || r.Fix.Wrote != "" {
		t.Fatalf("dry run: %+v", r.Fix)
	}
	if got, _ := os.ReadFile(path); string(got) != src {
		t.Fatal("a dry run rewrote the file")
	}

	outPath := filepath.Join(dir, "fixed.jsonc")
	r, err = adviseJSON(t, "--config", path, "--fix", "--out", outPath)
	if err != nil {
		t.Fatal(err)
	}
	if r.Fix.Wrote != outPath {
		t.Errorf("wrote = %q", r.Fix.Wrote)
	}
	if got, _ := os.ReadFile(path); string(got) != src {
		t.Error("--out rewrote the input")
	}
	fixed, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(fixed), "// mine\n") || strings.Contains(string(fixed), "nonesuchKey") || strings.Contains(string(fixed), "at any time") {
		t.Errorf("fixed file:\n%s", fixed)
	}

	if _, err := adviseJSON(t, "--config", path, "--fix", "--out", outPath, "--write"); err == nil {
		t.Error("--out and --write together must be refused")
	}
	if _, err := adviseJSON(t, "--config", path, "--write"); err == nil {
		t.Error("--write without --fix must be refused")
	}

	r, err = adviseJSON(t, "--config", path, "--fix", "--write")
	if err != nil {
		t.Fatal(err)
	}
	if r.Fix.Wrote != path {
		t.Errorf("wrote = %q", r.Fix.Wrote)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != string(fixed) {
		t.Error("--write and --out disagree")
	}
	// The fixed file has nothing left to fix.
	r, err = adviseJSON(t, "--config", path, "--fix")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Fix.Applied) != 0 {
		t.Errorf("fixes left after fixing: %+v", r.Fix.Applied)
	}
}

// YAML is reported, not rewritten, in this version.
func TestAdviseFixLeavesYAMLToTheReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".pinup.yaml")
	os.WriteFile(path, []byte("schedule:\n  - at any time\n"), 0o644)
	r, err := adviseJSON(t, "--config", path, "--fix", "--write")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Fix.Applied) != 0 || len(r.Fix.Skipped) == 0 || !strings.Contains(r.Fix.Skipped[0].Reason, "by hand") {
		t.Errorf("fix report = %+v", r.Fix)
	}
	if got, _ := os.ReadFile(path); string(got) != "schedule:\n  - at any time\n" {
		t.Error("the YAML file was rewritten")
	}
}
