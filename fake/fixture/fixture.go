// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package fixture locates the captured behaviour the tests run against.
//
// Two roots. testdata/renovate holds what is Renovate's behaviour alone -
// the versioning tables, the preset closure, the advisory captures, the
// synthetic repositories - recorded by executing the pinned container and
// the same for everyone. The second root holds a runner configuration and
// everything captured against it: the resolution, the rule vectors, the
// extraction of repositories under it, the goldens. testdata/estate is the
// author's own estate, with its incident history and its customers' names,
// and stays out of the public repository; PINUP_FIXTURES names another root
// of the same shape - testdata/public, a synthetic twin - and the tests read
// their expectations from that root's expect.json rather than from literals.
package fixture

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Expectations are the numbers a root's configuration pins - the
// denominators that make a parity test able to fail. A recapture that moves
// one shows up as a reviewed change to this file, not as a quiet one.
type Expectations struct {
	// Config is the runner configuration, relative to the root.
	Config string `json:"config"`
	// RulesOwn is how many packageRules the configuration writes itself;
	// RulesResolved how many the resolution yields, presets included;
	// ResolvedKeys how many top-level keys the resolution has; Presets how
	// many top-level presets it extends.
	RulesOwn      int `json:"rulesOwn"`
	RulesResolved int `json:"rulesResolved"`
	ResolvedKeys  int `json:"resolvedKeys"`
	Presets       int `json:"presets"`
	// PatternEntries is the number of file-pattern entries across the
	// configuration's managers.
	PatternEntries int `json:"patternEntries"`
	// RuleVectors is the number of vectors rules/vectors.ndjson carries.
	RuleVectors int `json:"ruleVectors"`
	// CorpusDeps is the number of dependencies across extract/*.json;
	// TemplatedNames how many of them carry a templated "<project>:<name>"
	// packageName; SuggestOverMatches how many are composer `suggest`
	// descriptions read as constraints.
	CorpusDeps         int `json:"corpusDeps"`
	TemplatedNames     int `json:"templatedNames"`
	SuggestOverMatches int `json:"suggestOverMatches"`
	// Corpora says, per extraction capture, where its files are and which
	// configuration it was extracted under, where the defaults (see Corpus)
	// do not hold.
	Corpora map[string]CorpusSpec `json:"corpora"`
}

// CorpusSpec is one entry of Expectations.Corpora.
type CorpusSpec struct {
	// Tree is the directory holding the repository's files: absolute, or
	// relative to the root. A checkout that is not on this machine leaves
	// the corpus without a tree, and the tests that need one skip.
	Tree string `json:"tree"`
	// Config is the configuration the capture ran under, relative to the
	// root; empty means the root's default.
	Config string `json:"config"`
	// Commit is the checkout's commit the capture archived, when HEAD has
	// moved on since; empty means HEAD.
	Commit string `json:"commit"`
}

// Corpus is one repository's extraction capture: what the pinned Renovate
// extracted from its files, and where the files are.
type Corpus struct {
	Name string // the capture's file stem
	Path string // <root>/<Version>/extract/<Name>.json
	// Tree holds the repository's files, or is "" when they are not on this
	// machine (a private checkout the estate root names).
	Tree string
	// Config is the configuration the capture ran under.
	Config string
	// Commit is the checkout revision the capture archived; "HEAD" unless
	// expect.json says otherwise.
	Commit string
}

// Corpora lists the root's extraction captures, by name. A capture's tree
// is the one expect.json names, else <root>/repos/<name>, else the shared
// synthetic tree of that name; a tree that does not exist is "".
func Corpora(t testing.TB) []Corpus {
	t.Helper()
	dir := Captured(t, "extract")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no extraction corpus under %s: %v", dir, err)
	}
	e := Expect(t)
	var out []Corpus
	for _, ent := range entries {
		name, ok := strings.CutSuffix(ent.Name(), ".json")
		if !ok || ent.IsDir() {
			continue
		}
		c := Corpus{Name: name, Path: filepath.Join(dir, ent.Name()), Config: Config(t), Commit: "HEAD"}
		spec := e.Corpora[name]
		if spec.Config != "" {
			c.Config = Path(t, spec.Config)
		}
		if spec.Commit != "" {
			c.Commit = spec.Commit
		}
		candidates := []string{Path(t, "repos", name), Shared(t, "synthetic", name)}
		if spec.Tree != "" {
			tree := spec.Tree
			if !filepath.IsAbs(tree) {
				tree = Path(t, tree)
			}
			candidates = append([]string{tree}, candidates...)
		}
		for _, cand := range candidates {
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				c.Tree = cand
				break
			}
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		t.Fatalf("no extraction corpus under %s", dir)
	}
	return out
}

// Version is the pinned Renovate whose behaviour the captures record, as
// testdata/renovate/CURRENT names it: "renovate-43.288.0".
func Version(t testing.TB) string {
	t.Helper()
	raw, err := os.ReadFile(Shared(t, "CURRENT"))
	if err != nil {
		t.Fatalf("no capture is current: %v", err)
	}
	v := strings.TrimSpace(string(raw))
	if _, err := os.Stat(Shared(t, v)); err != nil {
		t.Fatalf("CURRENT names %q, which does not exist", v)
	}
	return v
}

// Shared is a path under testdata/renovate, the root every checkout has.
func Shared(t testing.TB, rel ...string) string {
	t.Helper()
	return filepath.Join(append([]string{moduleRoot(t), "testdata", "renovate"}, rel...)...)
}

// SharedCaptured is a path under the shared root's capture for Version.
func SharedCaptured(t testing.TB, rel ...string) string {
	t.Helper()
	return Shared(t, append([]string{Version(t)}, rel...)...)
}

// Root is the configuration-dependent root: PINUP_FIXTURES when set
// (absolute, or relative to the module root), else testdata/estate.
func Root(t testing.TB) string {
	t.Helper()
	if v := os.Getenv("PINUP_FIXTURES"); v != "" {
		if filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(moduleRoot(t), v)
	}
	return filepath.Join(moduleRoot(t), "testdata", "estate")
}

// Path is a path under Root.
func Path(t testing.TB, rel ...string) string {
	t.Helper()
	return filepath.Join(append([]string{Root(t)}, rel...)...)
}

// Config is the root's runner configuration.
func Config(t testing.TB) string {
	t.Helper()
	return Path(t, Expect(t).Config)
}

// Captured is a path under the root's capture for Version.
func Captured(t testing.TB, rel ...string) string {
	t.Helper()
	return Path(t, append([]string{Version(t)}, rel...)...)
}

// Expect reads the root's expect.json. A root without one is not a fixture
// root; the test fails rather than measuring against nothing.
func Expect(t testing.TB) Expectations {
	t.Helper()
	raw, err := os.ReadFile(Path(t, "expect.json"))
	if err != nil {
		t.Fatalf("fixture root %s carries no expect.json: %v", Root(t), err)
	}
	var e Expectations
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("expect.json: %v", err)
	}
	if e.Config == "" {
		e.Config = "config/default.json"
	}
	return e
}

// moduleRoot walks up from the working directory to go.mod.
func moduleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// Entry is one package file of a capture under one of Renovate's manager
// keys, its dependencies left as JSON for the caller to decode into the
// fields it compares.
type Entry struct {
	PackageFile string
	Deps        json.RawMessage
}

// Entries reads the capture's entries for one manager key ("composer",
// "regex", "gitlabci", ...), in the order Renovate listed them.
func (c Corpus) Entries(t testing.TB, manager string) []Entry {
	t.Helper()
	raw, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string][]struct {
		PackageFile string          `json:"packageFile"`
		Deps        json.RawMessage `json:"deps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", c.Path, err)
	}
	var out []Entry
	for _, e := range doc[manager] {
		out = append(out, Entry{PackageFile: e.PackageFile, Deps: e.Deps})
	}
	return out
}

// Read returns a package file's bytes from the capture's tree: the committed
// revision of a checkout (HEAD, or the commit expect.json names), which is
// what the capture archived and what a working tree with uncommitted changes
// is not; the file itself otherwise. A capture
// without a tree fails the test; check Tree first and skip.
func (c Corpus) Read(t testing.TB, packageFile string) []byte {
	t.Helper()
	raw, ok := c.Lookup(t, packageFile)
	if !ok {
		t.Fatalf("%s: %s is not in the tree", c.Name, packageFile)
	}
	return raw
}

// Lookup is Read for a file that may be absent: a lock file beside a
// manifest, or above it.
func (c Corpus) Lookup(t testing.TB, rel string) ([]byte, bool) {
	t.Helper()
	if c.Tree == "" {
		t.Fatalf("corpus %s has no tree on this machine", c.Name)
	}
	if _, err := os.Stat(filepath.Join(c.Tree, ".git")); err == nil {
		raw, err := exec.Command("git", "-C", c.Tree, "show", c.Commit+":"+rel).Output()
		return raw, err == nil
	}
	raw, err := os.ReadFile(filepath.Join(c.Tree, filepath.FromSlash(rel)))
	return raw, err == nil
}
