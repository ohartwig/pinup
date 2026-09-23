// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package mutate is the mutation suite (H.7): twenty-six named, deterministic
// breakages of the code, one per gate, each of which the tests of its
// layer must catch - and one deliberately undetectable change, reported as
// exactly that, so the suite proves it can tell the two apart.
//
// A gate that has never been seen red is not known to gate
// (gitlab-profile/engineering/gates-that-did-not-gate.md). This is where
// every harness layer is seen red on purpose.
//
// The suite copies the tracked tree, applies one mutator, runs the tests
// it names, and expects them to fail. It is slow and runs only when
// PINUP_MUTATE=1 (CI's test:mutation job); the mutator table itself is
// checked on every run: ids contiguous, every layer at least twice, exactly
// one sentinel.
package mutate

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type mutator struct {
	ID    int
	Layer string
	Name  string
	File  string
	Old   string
	New   string
	// Tests are the packages whose tests must go red.
	Tests []string
	// Sentinel marks the one change no test can see: a comment. The suite
	// must report it as not detected, or it cannot tell red from green.
	Sentinel bool
}

var mutators = []mutator{
	// config: presets, rules, templates, globs, schedules
	{1, "config", "rules: negation ignored", "rules/pattern.go",
		"\tfor _, p := range l.negative {\n\t\tif p.matches(s) {\n\t\t\treturn false",
		"\tfor _, p := range l.negative {\n\t\tif p.matches(s) {\n\t\t\treturn true", []string{"./rules/"}, false},
	{2, "config", "rules: the first rule wins instead of the last", "rules/rules.go",
		"\tfor _, r := range e.Rules {", "\tfor _, r := range slices.Backward(e.Rules) {", []string{"./rules/"}, false},
	{3, "config", "packageRules replace instead of concatenating", "config/merge.go",
		"\t\"packageRules\": true,\n}", "}", []string{"./config/"}, false},
	{4, "config", "globstar matches nothing", "glob/glob.go",
		"func (m *Matcher) Match(name string) bool {", "func (m *Matcher) Match(name string) bool {\n\tif strings.Contains(m.pattern, \"**\") {\n\t\treturn false\n\t}", []string{"./glob/"}, false},
	{5, "config", "triple-stash escapes", "hbs/hbs.go",
		"\t\t\t\tsb.WriteString(html.EscapeString(v))", "\t\t\t\tsb.WriteString(v)", []string{"./hbs/"}, false},
	{6, "config", "schedule window inverted", "sched/natural.go",
		"\treturn m >= n.afterMin && m < n.beforeMin\n}", "\treturn m < n.afterMin || m >= n.beforeMin\n}", []string{"./sched/"}, false},
	// versioning: the captured tables
	{7, "versioning", "docker compatibility ignores the suffix", "versioning/docker/docker.go",
		"return len(tc.nums) == len(tr.nums) && tc.suffix == tr.suffix", "return len(tc.nums) == len(tr.nums)", []string{"./versioning/docker/"}, false},
	{8, "versioning", "a partial satisfies nothing", "versioning/partial/partial.go",
		"func (s *Scheme) Satisfies(version, rng string) bool {", "func (s *Scheme) Satisfies(version, rng string) bool {\n\treturn false", []string{"./versioning/partial/"}, false},
	// extract: managers against the corpus
	{9, "extract", "the pinup annotation prefix is not read", "manager/regexm/regexm.go",
		"\treturn strings.ReplaceAll(pattern, legacyPrefix, widenedGroup)", "\treturn pattern", []string{"./manager/regexm/"}, false},
	{10, "extract", ".terraform-version loses its v", "manager/tfversion/tfversion.go",
		"\t\tCurrentValue:  src[start:end],", "\t\tCurrentValue:  strings.TrimPrefix(src[start:end], \"v\"),", []string{"./manager/tfversion/"}, false},
	// lookup: cache and datasource contract
	{11, "lookup", "the cache is never fresh", "cache/cache.go",
		"\treturn payload, fresh, err\n}", "\treturn payload, false, err\n}", []string{"./cache/"}, false},
	{12, "lookup", "a declined lookup warns", "lookup/lookup.go",
		"\t\tif !errors.As(err, &declined) {", "\t\tif true {", []string{"./lookup/"}, false},
	// planner: selection, policy, naming
	{13, "planner", "majors join the minor bucket", "planner/planner.go",
		"\t\t\t\tc.majors = append(c.majors, cand)", "\t\t\t\tc.others = append(c.others, cand)", []string{"./planner/"}, false},
	{14, "planner", "deprecated releases offered", "planner/planner.go",
		"\t\tif r.Deprecated {", "\t\tif r.Deprecated && false {", []string{"./planner/"}, false},
	{15, "planner", "release age never holds", "planner/policy.go",
		"\tif age > 0 && !u.AgeWaived && u.Type != model.UpdateLockFileMaintenance && u.Type != model.UpdateDigest && u.Type != model.UpdatePinDigest {", "\tif age > 0 && false {", []string{"./planner/"}, false},
	{16, "planner", "titles keep their case", "planner/branch.go",
		"\tlower := true\n", "\tlower := false\n", []string{"./planner/"}, false},
	{17, "planner", "an unchanged value is still an update", "planner/planner.go",
		"\t\tif newValue == cur {\n\t\t\treturn model.Update{}, unchangedPrefix + target\n\t\t}", "", []string{"./cmd/pinup/"}, false},
	// delivery: apply, tasks, runner, shadow
	{18, "delivery", "a new tag lands on the old digest", "extract/edit.go",
		"\tcase hasDigest && valueChanges && !digestKnown:", "\tcase hasDigest && valueChanges && !digestKnown && false:", []string{"./extract/", "./manager/gitlabci/"}, false},
	{19, "delivery", "overlapping edits pass", "apply/apply.go",
		"\t\t\tif edits[i].Overlaps(edits[j]) {", "\t\t\tif false && edits[i].Overlaps(edits[j]) {", []string{"./apply/"}, false},
	{20, "delivery", "every command is allowed", "plugin/plugin.go",
		"\treturn -1, fmt.Errorf(\"command %q is not on the allowedCommands list\", command)", "\treturn 0, nil", []string{"./plugin/"}, false},
	{21, "delivery", "a task's result is committed whatever it touched", "runner/runner.go",
		"\tif err := apply.InScope(t, changed); err != nil {\n\t\treturn nil, err\n\t}", "", []string{"./runner/"}, false},
	{22, "delivery", "a held branch Renovate opened is not a failure", "report/shadow.go",
		"\t\tc.r.HeldOpen++", "\t\tc.r.Held++", []string{"./report/"}, false},
	// analyzer: what the effective label is read off. The layer had no
	// entry until 2026-09-22, which by this suite's own premise means it
	// was not known to gate at all.
	{23, "analyzer", "a chart's placed images are never found", "analyzer/helmchart/images.go",
		"\tif repo := str(m, \"repository\"); repo != \"\" {", "\tif repo := str(m, \"repository\"); false {", []string{"./analyzer/helmchart/"}, false},
	{24, "analyzer", "a removed values key is not breaking", "analyzer/helmchart/helmchart.go",
		"\tif len(removed) > 0 {\n\t\trisk = model.RiskBreakingValues", "\tif false {\n\t\trisk = model.RiskBreakingValues", []string{"./analyzer/helmchart/"}, false},
	{25, "planner", "a re-pushed release passes as an ordinary digest bump", "planner/planner.go",
		"\tif releasedTag(tag) {", "\tif false {", []string{"./planner/"}, false},
	// delivery again: the value filter on a task's environment. Added
	// 2026-09-23 with the filter itself - the runner had handed the
	// platform token to every task under two names the name filter did
	// not know, and a filter nobody has seen red is not known to filter.
	{26, "delivery", "a platform credential crosses under another name", "plugin/plugin.go",
		"\tif err := r.carriesSecret(); err != nil {\n\t\treturn Result{}, err\n\t}\n", "", []string{"./plugin/"}, false},
	// the one change no test can see
	{27, "sentinel", "a comment changes", "model/model.go",
		"// NoCustomManager is the CustomManager value for a built-in manager.", "// NoCustomManager is the CustomManager value for a built-in manager (unchanged).", []string{"./model/"}, true},
}

func TestMutatorTableIsWellFormed(t *testing.T) {
	ids := map[int]bool{}
	layers := map[string]int{}
	sentinels := 0
	for _, m := range mutators {
		if ids[m.ID] {
			t.Errorf("mutator %d listed twice", m.ID)
		}
		ids[m.ID] = true
		if m.Sentinel {
			sentinels++
			continue
		}
		layers[m.Layer]++
		if m.Old == "" || m.Old == m.New || len(m.Tests) == 0 {
			t.Errorf("mutator %d (%s) is not a change with tests", m.ID, m.Name)
		}
	}
	for i := 1; i <= len(mutators); i++ {
		if !ids[i] {
			t.Errorf("ids are not contiguous: %d missing", i)
		}
	}
	if sentinels != 1 {
		t.Errorf("%d sentinels, want exactly one", sentinels)
	}
	if len(mutators)-sentinels < 20 {
		t.Errorf("%d mutators, want at least 20", len(mutators)-sentinels)
	}
	for layer, n := range layers {
		if n < 2 {
			t.Errorf("layer %s has %d mutator(s); each layer is broken at least twice", layer, n)
		}
	}
	// Every mutator's Old must exist exactly once in the tree it targets,
	// or the mutator silently mutates nothing.
	root := repoRoot(t)
	for _, m := range mutators {
		src, err := os.ReadFile(filepath.Join(root, m.File))
		if err != nil {
			t.Errorf("mutator %d: %v", m.ID, err)
			continue
		}
		if n := strings.Count(string(src), m.Old); n != 1 {
			t.Errorf("mutator %d (%s): Old occurs %d times in %s, want exactly 1", m.ID, m.Name, n, m.File)
		}
	}
}

// TestEveryMutatorIsDetected copies the tree, breaks it one way at a time,
// and expects the named tests to go red - except the sentinel, which must
// stay green.
func TestEveryMutatorIsDetected(t *testing.T) {
	if os.Getenv("PINUP_MUTATE") == "" {
		t.Skip("set PINUP_MUTATE=1; the suite runs go test twenty-one times")
	}
	root := repoRoot(t)
	work := t.TempDir()
	copyTracked(t, root, work)
	var detected, missed []string
	for _, m := range mutators {
		path := filepath.Join(work, m.File)
		orig, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(orig), m.Old, m.New, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		args := append([]string{"test", "-count=1", "-v"}, m.Tests...)
		cmd := exec.Command("go", args...)
		cmd.Dir = work
		out, runErr := cmd.CombinedOutput()
		if err := os.WriteFile(path, orig, 0o644); err != nil {
			t.Fatal(err)
		}
		red := runErr != nil
		// Tests that skipped are tests that did not look: a package whose
		// every test skipped (no git on this machine, say) cannot have
		// seen the mutation, and saying "green" would be the lie this
		// suite exists to catch.
		blind := !red && !bytes.Contains(out, []byte("--- PASS")) && bytes.Contains(out, []byte("--- SKIP"))
		switch {
		case m.Sentinel && red:
			t.Errorf("sentinel %d (%s) was detected; the suite cannot tell red from green:\n%s", m.ID, m.Name, tail(out))
		case m.Sentinel:
			t.Logf("sentinel %d (%s): not detected, as it must be", m.ID, m.Name)
		case red:
			detected = append(detected, m.Name)
		case blind:
			missed = append(missed, m.Name)
			t.Errorf("mutator %d (%s, %s): the tests in %v all skipped on this machine, so nothing looked; the gate is untested here, not passed", m.ID, m.Name, m.Layer, m.Tests)
		default:
			missed = append(missed, m.Name)
			t.Errorf("mutator %d (%s, %s) was NOT detected by %v", m.ID, m.Name, m.Layer, m.Tests)
		}
	}
	sort.Strings(detected)
	t.Logf("detected %d/%d: %s", len(detected), len(mutators)-1, strings.Join(detected, "; "))
	if len(missed) > 0 {
		t.Logf("missed: %s", strings.Join(missed, "; "))
	}
}

// repoRoot is the module root: this package sits two levels below it. No
// git here - the CI image the suite runs on does not carry git.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found above %s: %v", wd, err)
	}
	return root
}

// copyTracked copies the source tree, leaving out what is never part of a
// build: the git directory, the module cache, caches and coverage output.
func copyTracked(t *testing.T, root, work string) {
	t.Helper()
	skip := map[string]bool{".git": true, ".go": true, ".pinup": true, "node_modules": true}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(work, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(work, rel), data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func tail(b []byte) string {
	s := string(b)
	if len(s) > 1500 {
		return "…" + s[len(s)-1500:]
	}
	return s
}
