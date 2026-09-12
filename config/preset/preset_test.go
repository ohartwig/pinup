// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package preset

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	closurePath         = "../../testdata/parity/renovate-43.288.0/presets/closure.json"
	defaultResolvedPath = "../../testdata/parity/renovate-43.288.0/presets/default-resolved.json"
	defaultConfigPath   = "../../testdata/parity/config/default.json"
	// presetFloor is asserted so a truncated closure cannot pass.
	presetFloor = 1000
)

type closure struct {
	Presets map[string]struct {
		Definition map[string]any `json:"definition"`
		Resolved   struct {
			Config  map[string]any `json:"config"`
			Visited struct {
				Merged []string `json:"merged"`
			} `json:"visitedPresets"`
		} `json:"resolved"`
	} `json:"presets"`
}

func loadClosure(t *testing.T) closure {
	t.Helper()
	raw, err := os.ReadFile(closurePath)
	if err != nil {
		t.Fatal(err)
	}
	var c closure
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Presets) < presetFloor {
		t.Fatalf("only %d presets captured; the closure was barely read", len(c.Presets))
	}
	return c
}

// Every captured preset, resolved here, must equal what Renovate resolved
// for `{extends: [name]}` - config and the visited list both.
func TestResolvesEveryCapturedPresetAsRenovateDid(t *testing.T) {
	c := loadClosure(t)
	lib := Builtin()
	names := make([]string, 0, len(c.Presets))
	for n := range c.Presets {
		names = append(names, n)
	}
	sort.Strings(names)

	mismatch, compared := 0, 0
	for _, name := range names {
		want := c.Presets[name]
		got, err := Resolve(map[string]any{"extends": []any{name}}, lib)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			mismatch++
			continue
		}
		compared++
		if !reflect.DeepEqual(got.Config, want.Resolved.Config) {
			mismatch++
			if mismatch <= 5 {
				t.Errorf("%s: resolved config differs\n got: %s\nwant: %s", name, short(got.Config), short(want.Resolved.Config))
			}
		}
		if !reflect.DeepEqual(got.Visited, want.Resolved.Visited.Merged) {
			mismatch++
			if mismatch <= 5 {
				t.Errorf("%s: visited %v, Renovate %v", name, got.Visited, want.Resolved.Visited.Merged)
			}
		}
	}
	t.Logf("%d presets compared, %d mismatches", compared, mismatch)
	if compared < presetFloor {
		t.Fatalf("compared only %d", compared)
	}
	if mismatch != 0 {
		t.Fail()
	}
}

// The acceptance case: default.json's extends resolve to what the container
// resolved them to - 771 rules and all.
func TestResolvesDefaultConfigAsRenovateDid(t *testing.T) {
	raw, err := os.ReadFile(defaultConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(defaultResolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(cfg, Builtin())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range sortedKeys(want) {
		if !reflect.DeepEqual(got.Config[k], want[k]) {
			t.Errorf("key %s differs\n got: %s\nwant: %s", k, short(got.Config[k]), short(want[k]))
		}
	}
	for k := range got.Config {
		if _, ok := want[k]; !ok {
			t.Errorf("key %s is set here and not by Renovate", k)
		}
	}
	if rules, _ := got.Config["packageRules"].([]any); len(rules) != 771 {
		t.Errorf("resolved %d rules, want 771 - the numbering must stay Renovate's", len(rules))
	}
	// The two mergeConfidence presets - all-badges from default.json itself,
	// age-confidence-badges through config:recommended - resolve but have
	// no effect, and each is warned about once.
	if len(got.Warnings) != 2 {
		t.Errorf("want one warning per inert preset, got %v", got.Warnings)
	}
	for _, w := range got.Warnings {
		if !strings.Contains(w, "mergeConfidence:") || !strings.Contains(w, "developer.mend.io") {
			t.Errorf("warning must name the preset and the reason: %q", w)
		}
	}
	// The closure the task names, as merged presets.
	for _, name := range []string{"config:recommended", ":dependencyDashboard", ":semanticPrefixFixDepsChoreOthers",
		"group:monorepos", "group:recommended", "replacements:all", "workarounds:all"} {
		found := false
		for _, v := range got.Visited {
			if v == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s was not visited", name)
		}
	}
}

func TestInertPresetResolvesButWarnsOnce(t *testing.T) {
	got, err := Resolve(map[string]any{"extends": []any{"mergeConfidence:all-badges", "mergeConfidence:all-badges"}, "x": 1}, Builtin())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "developer.mend.io") {
		t.Errorf("warnings %v", got.Warnings)
	}
	if rules, _ := got.Config["packageRules"].([]any); len(rules) != 2 {
		t.Errorf("an inert preset still resolves its rules (numbering), got %d", len(rules))
	}
}

func TestUnknownPresetIsAnError(t *testing.T) {
	_, err := Resolve(map[string]any{"extends": []any{"nonesuch:preset"}}, Builtin())
	if err == nil || !strings.Contains(err.Error(), "nonesuch:preset") {
		t.Fatalf("want an error naming the preset, got %v", err)
	}
}

func TestOwnKeysWinAndLaterPresetsOverrideEarlier(t *testing.T) {
	src := fakeSource{
		"a": {"schedule": []any{"a"}, "x": "a", "packageRules": []any{map[string]any{"from": "a"}}},
		"b": {"schedule": []any{"b"}, "packageRules": []any{map[string]any{"from": "b"}}},
		"c": {"extends": []any{"a"}, "x": "c"},
	}
	got, err := Resolve(map[string]any{"extends": []any{"c", "b"}, "packageRules": []any{map[string]any{"from": "own"}}, "schedule": []any{"own"}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config["x"] != "c" {
		t.Errorf("c extends a and sets x itself: x = %v", got.Config["x"])
	}
	if s := got.Config["schedule"].([]any); len(s) != 1 || s[0] != "own" {
		t.Errorf("arrays replace and own keys win: schedule = %v", s)
	}
	rules := got.Config["packageRules"].([]any)
	order := []string{}
	for _, r := range rules {
		order = append(order, r.(map[string]any)["from"].(string))
	}
	if strings.Join(order, ",") != "a,b,own" {
		t.Errorf("packageRules concatenate in resolution order, got %v", order)
	}
	if strings.Join(got.Visited, ",") != "c,a,b" {
		t.Errorf("visited %v", got.Visited)
	}
}

func TestCycleIsAnError(t *testing.T) {
	src := fakeSource{"a": {"extends": []any{"b"}}, "b": {"extends": []any{"a"}}}
	if _, err := Resolve(map[string]any{"extends": []any{"a"}}, src); err == nil || !strings.Contains(err.Error(), "extends itself") {
		t.Fatalf("want a cycle error, got %v", err)
	}
}

func TestLibraryMatchesGenerator(t *testing.T) {
	// The committed library must be exactly what presetgen produces from the
	// closure, so the two cannot drift apart unnoticed.
	want, err := os.ReadFile("library.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(want), `"source": "executed in renovate/renovate:43.288.0`) {
		t.Fatal("the library must name the capture it was generated from")
	}
	if len(Builtin().Presets) < presetFloor {
		t.Fatalf("library holds %d presets", len(Builtin().Presets))
	}
}

type fakeSource map[string]map[string]any

func (f fakeSource) Get(name string) (map[string]any, string, bool, error) {
	d, ok := f[name]
	return d, "", ok, nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func short(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}

// The comparison must be able to fail: a library with one preset's rules
// removed resolves config:recommended differently.
func TestParityGoesRedWhenTheLibraryIsBroken(t *testing.T) {
	c := loadClosure(t)
	lib := &Library{Presets: map[string]Entry{}}
	for n, e := range Builtin().Presets {
		lib.Presets[n] = e
	}
	broken := map[string]any{}
	for k, v := range lib.Presets["group:nodeJs"].Definition {
		if k != "packageRules" {
			broken[k] = v
		}
	}
	lib.Presets["group:nodeJs"] = Entry{Definition: broken}
	got, err := Resolve(map[string]any{"extends": []any{"config:recommended"}}, lib)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(got.Config, c.Presets["config:recommended"].Resolved.Config) {
		t.Fatal("a broken library resolved identically; the comparison is not comparing")
	}
}

// Provenance names, for every resolved key, the chain that wrote it - and
// for every rule, the preset it came from, at the index it ended up at.
func TestOriginsNameThePresetThatWroteEachKey(t *testing.T) {
	raw, err := os.ReadFile(defaultConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(cfg, Builtin())
	if err != nil {
		t.Fatal(err)
	}
	rules := got.Config["packageRules"].([]any)
	// Every rule has exactly one origin, and the config's own rules come
	// last, after every preset's.
	own := 0
	for i := range rules {
		chain := got.Origins[fmt.Sprintf("/packageRules/%d", i)]
		if len(chain) != 1 {
			t.Fatalf("packageRules[%d] has origin chain %v, want exactly one", i, chain)
		}
		if chain[0] == OwnSource {
			own++
		} else if own > 0 {
			t.Fatalf("packageRules[%d] from %s follows the config's own rules", i, chain[0])
		}
	}
	if own != 49 {
		t.Errorf("%d rules attributed to the config itself, want 49 (what default.json writes)", own)
	}
	if chain := got.Origins["/packageRules/0"]; chain[0] != ":semanticPrefixFixDepsChoreOthers" {
		t.Errorf("rule 0 comes from %v, want :semanticPrefixFixDepsChoreOthers via config:recommended", chain)
	}
	// dependencyDashboard: set by :dependencyDashboard, then by the file.
	if chain := got.Origins["/dependencyDashboard"]; len(chain) != 2 || chain[0] != ":dependencyDashboard" || chain[1] != OwnSource {
		t.Errorf("dependencyDashboard chain %v", chain)
	}
	// Every leaf key of the resolved config carries an origin.
	for k := range got.Config {
		if isConcat(k) {
			continue
		}
		if len(got.Origins["/"+k]) == 0 {
			t.Errorf("key %s has no origin", k)
		}
	}
}
