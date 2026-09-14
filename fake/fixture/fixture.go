// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

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
	// CorpusDeps is the number of dependencies across extract/*.json.
	CorpusDeps int `json:"corpusDeps"`
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
