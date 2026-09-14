// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package fixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootFollowsTheSwitch(t *testing.T) {
	t.Setenv("PINUP_FIXTURES", "")
	if got := Root(t); !strings.HasSuffix(got, filepath.Join("testdata", "estate")) {
		t.Errorf("default root %q is not testdata/estate", got)
	}
	t.Setenv("PINUP_FIXTURES", "testdata/public")
	if got := Root(t); !strings.HasSuffix(got, filepath.Join("testdata", "public")) || !filepath.IsAbs(got) {
		t.Errorf("relative switch gave %q", got)
	}
	abs := t.TempDir()
	t.Setenv("PINUP_FIXTURES", abs)
	if got := Root(t); got != abs {
		t.Errorf("absolute switch gave %q, want %q", got, abs)
	}
}

func TestExpectationsComeFromTheRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "expect.json"), []byte(`{"rulesOwn": 3, "ruleVectors": 12}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PINUP_FIXTURES", root)
	e := Expect(t)
	if e.RulesOwn != 3 || e.RuleVectors != 12 {
		t.Errorf("read %+v", e)
	}
	if e.Config != "config/default.json" {
		t.Errorf("config defaulted to %q", e.Config)
	}
	if got, want := Config(t), filepath.Join(root, "config", "default.json"); got != want {
		t.Errorf("Config = %q, want %q", got, want)
	}
}

// The checked-in roots: the version CURRENT names exists in both, and the
// estate's expect.json agrees with what lies beside it - the same numbers
// the parity tests measure, so a stale pin here is caught once, not in
// every package.
func TestCheckedInRootsAreConsistent(t *testing.T) {
	t.Setenv("PINUP_FIXTURES", "")
	v := Version(t)
	for _, p := range []string{SharedCaptured(t, "versioning"), Captured(t, "resolved-options.json"), Config(t), Path(t, "golden")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if !strings.HasPrefix(v, "renovate-") {
		t.Errorf("CURRENT names %q", v)
	}
	raw, err := os.ReadFile(Captured(t, "rules", "vectors.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(strings.TrimRight(string(raw), "\n"), "\n") // header excluded
	if want := Expect(t).RuleVectors; lines != want {
		t.Errorf("vectors.ndjson carries %d vectors, expect.json pins %d", lines, want)
	}
}

func TestCorporaFindTheirTrees(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "renovate-43.288.0", "extract"), 0o755)
	os.MkdirAll(filepath.Join(root, "repos", "alpha"), 0o755)
	os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755)
	os.WriteFile(filepath.Join(root, "renovate-43.288.0", "extract", "alpha.json"), []byte(`{"npm":[{"packageFile":"package.json","deps":[{"depName":"a"}]}]}`), 0o644)
	os.WriteFile(filepath.Join(root, "renovate-43.288.0", "extract", "beta.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(root, "renovate-43.288.0", "extract", "gamma.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(root, "repos", "alpha", "package.json"), []byte(`{"name":"alpha"}`), 0o644)
	os.WriteFile(filepath.Join(root, "expect.json"), []byte(`{"corpora":{"beta":{"tree":"/nowhere/beta"},"gamma":{"tree":"elsewhere","config":"config/other.json"}}}`), 0o644)
	t.Setenv("PINUP_FIXTURES", root)

	got := map[string]Corpus{}
	for _, c := range Corpora(t) {
		got[c.Name] = c
	}
	if len(got) != 3 {
		t.Fatalf("found %d corpora, want 3", len(got))
	}
	if got["alpha"].Tree != filepath.Join(root, "repos", "alpha") {
		t.Errorf("alpha's tree = %q, want repos/alpha", got["alpha"].Tree)
	}
	if got["beta"].Tree != "" {
		t.Errorf("beta names an absent checkout and still has a tree %q", got["beta"].Tree)
	}
	if got["gamma"].Tree != filepath.Join(root, "elsewhere") || got["gamma"].Config != filepath.Join(root, "config", "other.json") {
		t.Errorf("gamma = %+v", got["gamma"])
	}
	entries := got["alpha"].Entries(t, "npm")
	if len(entries) != 1 || entries[0].PackageFile != "package.json" || string(got["alpha"].Read(t, "package.json")) != `{"name":"alpha"}` {
		t.Errorf("alpha's npm entries: %+v", entries)
	}
	if len(got["alpha"].Entries(t, "composer")) != 0 {
		t.Error("a manager the capture has no entry for yields entries")
	}
}
