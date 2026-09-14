// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package preset

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/fake/fixture"
)

// The library is pinup's own and closed: every preset it names exists in
// it, every entry resolves, and the names a fixture root's configuration
// extends are all there - the preset surface the estate and the twin need.
func TestLibraryIsClosedAndResolves(t *testing.T) {
	lib := Builtin()
	if !strings.Contains(lib.Source, "pinup's own") {
		t.Errorf("the library's source must say whose it is: %q", lib.Source)
	}
	if len(lib.Presets) < 20 {
		t.Fatalf("library holds %d presets; the estate's closure needs more", len(lib.Presets))
	}
	for _, name := range lib.Names() {
		got, err := Resolve(map[string]any{"extends": []any{name}}, lib)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got.Visited) == 0 || got.Visited[0] != name {
			t.Errorf("%s: visited %v", name, got.Visited)
		}
		def := lib.Presets[name].Definition
		if d, _ := def["description"].([]any); len(d) != 1 {
			t.Errorf("%s: a library preset carries one description of its own, got %v", name, def["description"])
		}
	}
	raw, err := os.ReadFile(fixture.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Extends []string `json:"extends"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range cfg.Extends {
		if _, _, ok, _ := lib.Get(name); !ok {
			t.Errorf("the configuration extends %s, which the library does not carry", name)
		}
	}
}

// Descriptions follow the rule measured on the pinned container's resolver
// (testdata/renovate/.../presets/README.md): a nested preset with a
// description of its own drops the descriptions of children whose
// definition carries packageRules, and every other child's description is
// appended before its own; the top-level configuration keeps every
// child's regardless.
func TestDescriptionsAccumulateAsMeasured(t *testing.T) {
	src := fakeSource{
		"parent":  {"description": []any{"parent"}, "extends": []any{"rules", "plain"}},
		"rules":   {"description": []any{"rules"}, "packageRules": []any{map[string]any{"matchPackageNames": []any{"*"}, "automerge": true}}},
		"plain":   {"description": []any{"plain"}, "dependencyDashboard": true},
		"unnamed": {"extends": []any{"rules", "plain"}},
	}
	got, err := Resolve(map[string]any{"extends": []any{"parent"}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{"plain", "parent"}; !reflect.DeepEqual(got.Config["description"], want) {
		t.Errorf("a described parent: got %v, want %v", got.Config["description"], want)
	}
	got, err = Resolve(map[string]any{"extends": []any{"unnamed"}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{"rules", "plain"}; !reflect.DeepEqual(got.Config["description"], want) {
		t.Errorf("an undescribed parent: got %v, want %v", got.Config["description"], want)
	}
}

// The acceptance case: the configuration's extends resolve to the
// library's rules first and the file's own last, the preset custom
// managers before the file's, with one warning per inert preset used.
func TestResolvesTheConfiguration(t *testing.T) {
	raw, err := os.ReadFile(fixture.Config(t))
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
	own, _ := cfg["packageRules"].([]any)
	rules, _ := got.Config["packageRules"].([]any)
	if want := fixture.Expect(t).RulesResolved; len(rules) != want || len(rules) <= len(own) {
		t.Errorf("resolved %d rules, expect.json pins %d (the file's own %d come last)", len(rules), want, len(own))
	}
	if len(rules) >= len(own) && !reflect.DeepEqual(stripDescriptions(rules[len(rules)-len(own):]), stripDescriptions(own)) {
		t.Error("the file's own rules are not the last ones, verbatim")
	}
	ownCM, _ := cfg["customManagers"].([]any)
	cms, _ := got.Config["customManagers"].([]any)
	if len(cms) != len(ownCM)+2 {
		t.Errorf("%d custom managers, want the file's %d after the two the presets add", len(cms), len(ownCM))
	}
	// The three inert presets - all-badges from the file itself,
	// age-confidence-badges through config:recommended, abandonments -
	// resolve but have no effect, and each is warned about once.
	if len(got.Warnings) != 3 {
		t.Errorf("want one warning per inert preset, got %v", got.Warnings)
	}
	for _, name := range []string{"config:recommended", ":dependencyDashboard", ":semanticPrefixFixDepsChoreOthers",
		"group:monorepos", "group:recommended", "replacements:all", "workarounds:all"} {
		if !slices.Contains(got.Visited, name) {
			t.Errorf("%s was not visited", name)
		}
	}
}

func stripDescriptions(rules []any) []any {
	out := make([]any, 0, len(rules))
	for _, r := range rules {
		m, _ := r.(map[string]any)
		c := map[string]any{}
		for k, v := range m {
			if k != "description" {
				c[k] = v
			}
		}
		out = append(out, c)
	}
	return out
}

func TestInertPresetResolvesButWarnsOnce(t *testing.T) {
	got, err := Resolve(map[string]any{"extends": []any{"mergeConfidence:all-badges", "mergeConfidence:all-badges"}, "x": 1}, Builtin())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "developer.mend.io") {
		t.Errorf("warnings %v", got.Warnings)
	}
	if rules, _ := got.Config["packageRules"].([]any); len(rules) != 0 {
		t.Errorf("an inert preset resolves to nothing, got %d rules", len(rules))
	}
	if got.Config["x"] != 1 {
		t.Error("the configuration's own keys survive an inert preset")
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

// The library is what the resolution is made of: an entry with its rules
// removed resolves config:recommended to fewer rules.
func TestResolutionGoesRedWhenTheLibraryIsBroken(t *testing.T) {
	intact, err := Resolve(map[string]any{"extends": []any{"config:recommended"}}, Builtin())
	if err != nil {
		t.Fatal(err)
	}
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
	if reflect.DeepEqual(got.Config["packageRules"], intact.Config["packageRules"]) {
		t.Fatal("a broken library resolved identically; the library is not what resolves")
	}
}

// Provenance names, for every resolved key, the chain that wrote it - and
// for every rule, the preset it came from, at the index it ended up at.
func TestOriginsNameThePresetThatWroteEachKey(t *testing.T) {
	raw, err := os.ReadFile(fixture.Config(t))
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
