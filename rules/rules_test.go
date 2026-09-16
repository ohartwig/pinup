// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package rules

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/versioning"
)

func rulesOf(t *testing.T, rs ...map[string]any) *Engine {
	t.Helper()
	raw := make([]any, 0, len(rs))
	for _, r := range rs {
		raw = append(raw, r)
	}
	e, err := Compile(raw, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The P0.13 acceptance case, read off the real config: rangeStrategy for a
// typo3/cms-* composer dependency is decided by the file's rule 33 over 32
// over 0 - after the library's rules in the resolved numbering - and
// Explain names all three with the winner last.
func TestExplainNamesEveryRuleThatWroteAKey(t *testing.T) {
	base, _ := loadVectors(t)
	rulesRaw, _ := base["packageRules"].([]any)
	eng, err := Compile(rulesRaw, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	first := len(rulesRaw) - fixture.Expect(t).RulesOwn
	res := eng.Apply(base, Subject{
		DepName: "typo3/cms-core", PackageName: "typo3/cms-core", Datasource: "packagist",
		Manager: "composer", PackageFile: "composer.json", DepType: "require", CurrentValue: "^14.0",
	})
	if got := res.Wrote["rangeStrategy"]; len(got) != 3 || got[0] != first || got[1] != first+32 || got[2] != first+33 {
		t.Fatalf("rangeStrategy written by %v, want [%d %d %d]", got, first, first+32, first+33)
	}
	if got := res.Config["rangeStrategy"]; got != "update-lockfile" {
		t.Errorf("rangeStrategy = %v, want update-lockfile from the file's rule 33", got)
	}
	explain := res.Explain("rangeStrategy")
	for _, want := range []string{fmt.Sprintf("  packageRules[%d]\n", first), fmt.Sprintf("  packageRules[%d]\n", first+32), fmt.Sprintf("> packageRules[%d]\n", first+33)} {
		if !strings.Contains(explain, want) {
			t.Errorf("explain lacks %q:\n%s", want, explain)
		}
	}
	if got := res.Explain("nonesuch"); !strings.Contains(got, "set by no rule") {
		t.Errorf("an unwritten key must say so, got %q", got)
	}
}

// The harness must be able to fail. A rule with its matcher removed fires
// for everything, and the fired-rule lists disagree on most vectors.
func TestHarnessGoesRedWhenARuleIsBroken(t *testing.T) {
	base, vs := loadVectors(t)
	rulesRaw, _ := base["packageRules"].([]any)
	broken := make([]any, len(rulesRaw))
	copy(broken, rulesRaw)
	r33 := map[string]any{}
	for k, v := range rulesRaw[33].(map[string]any) {
		if k != "matchPackageNames" {
			r33[k] = v
		}
	}
	broken[33] = r33
	eng, err := Compile(broken, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	disagree := 0
	for _, v := range vs {
		if !equalInts(eng.Apply(base, subjectOf(v.Input)).Matched, v.Matched) {
			disagree++
		}
	}
	if disagree == 0 {
		t.Fatal("a broken rule produced no disagreement; the harness is not comparing")
	}
	t.Logf("broken rule 33: %d of %d vectors disagree", disagree, len(vs))
}

func TestPatternForms(t *testing.T) {
	cases := []struct {
		list []string
		in   string
		want bool
	}{
		{[]string{"typo3/cms-*"}, "typo3/cms-core", true},
		{[]string{"typo3/cms-*"}, "TYPO3/CMS-Core", true}, // globs are case-insensitive
		{[]string{"typo3/cms-*"}, "typo3/cms", false},
		{[]string{"typo3/*", "!typo3/cms-*"}, "typo3/cms-core", false},
		{[]string{"typo3/*", "!typo3/cms-*"}, "typo3/testing-framework", true},
		{[]string{"!moselwal/**", "!koh/**"}, "vendor/x", true}, // only negations: everything else
		{[]string{"!moselwal/**", "!koh/**"}, "koh/x", false},
		{[]string{"/^typo3\\/cms-/"}, "typo3/cms-core", true},
		{[]string{"/^typo3\\/cms-/"}, "xtypo3/cms-core", false},
		{[]string{"/^TYPO3/i"}, "typo3/cms-core", true},
		{[]string{"/^TYPO3/"}, "typo3/cms-core", false}, // regex is case-sensitive without i
		{[]string{"*"}, "registry.ole-hartwig.eu/devops/ci-mirrors/trivy", true},
		{[]string{"registry.ole-hartwig.eu/**/sources"}, "registry.ole-hartwig.eu/a/b/sources", true},
		{[]string{}, "anything", true},
	}
	for _, c := range cases {
		l, err := compileList(c.list)
		if err != nil {
			t.Fatalf("%v: %v", c.list, err)
		}
		if got := l.match(c.in); got != c.want {
			t.Errorf("%v match %q = %v, want %v", c.list, c.in, got, c.want)
		}
	}
	if _, err := compileList([]string{"/a(?=b)/"}); err == nil {
		t.Error("a lookahead is not RE2 and must be refused at compile time")
	}
	if _, err := compileList([]string{"/abc/x"}); err == nil {
		t.Error("an unknown regex flag must be refused")
	}
}

func TestUnknownMatcherIsACompileError(t *testing.T) {
	_, err := Compile([]any{map[string]any{"matchNonesuch": []any{"x"}, "automerge": true}}, versioning.Registry{})
	if err == nil || !strings.Contains(err.Error(), "matchNonesuch") {
		t.Fatalf("a matcher this engine cannot evaluate must fail compilation, got %v", err)
	}
}

func TestUnsupportedJsonataIsReportedAndNeverFires(t *testing.T) {
	e := rulesOf(t, map[string]any{"matchJsonata": []any{"$substring(depName, 0, 3) = 'abc'"}, "automerge": true})
	if len(e.Warnings) != 1 {
		t.Fatalf("want one warning, got %v", e.Warnings)
	}
	res := e.Apply(map[string]any{}, Subject{DepName: "abcdef"})
	if len(res.Matched) != 0 {
		t.Errorf("an unsupported rule must not fire, fired %v", res.Matched)
	}
}

func TestSupportedJsonataShapes(t *testing.T) {
	e := rulesOf(t,
		map[string]any{"matchJsonata": []any{"isLockfileUpdate = true"}, "a": 1},
		map[string]any{"matchJsonata": []any{"$detectPlatform(sourceUrl) = 'github'"}, "b": 1},
	)
	res := e.Apply(map[string]any{}, Subject{IsLockfileUpdate: true, SourceURL: "https://github.com/x/y"})
	if !equalInts(res.Matched, []int{0, 1}) {
		t.Errorf("fired %v, want [0 1]", res.Matched)
	}
	res = e.Apply(map[string]any{}, Subject{SourceURL: "https://gitlab.com/x/y"})
	if len(res.Matched) != 0 {
		t.Errorf("fired %v, want none", res.Matched)
	}
}

func TestEnabledFalseIsASkipReasonALaterRuleCanClear(t *testing.T) {
	e := rulesOf(t,
		map[string]any{"matchPackageNames": []any{"a"}, "enabled": false},
		map[string]any{"matchPackageNames": []any{"a"}, "matchUpdateTypes": []any{"patch"}, "enabled": true},
	)
	res := e.Apply(map[string]any{"enabled": true}, Subject{PackageName: "a"})
	if res.SkipReason != SkipPackageRules {
		t.Errorf("disabled: skipReason %q", res.SkipReason)
	}
	res = e.Apply(map[string]any{"enabled": true}, Subject{PackageName: "a", UpdateType: "patch"})
	if res.SkipReason != "" {
		t.Errorf("re-enabled for patch: skipReason %q", res.SkipReason)
	}
}

func TestMatchersOverUnknownFieldsDoNotMatch(t *testing.T) {
	e := rulesOf(t,
		map[string]any{"matchUpdateTypes": []any{"major"}, "x": 1},
		map[string]any{"matchSourceUrls": []any{"https://github.com/**"}, "y": 1},
	)
	res := e.Apply(map[string]any{}, Subject{PackageName: "a"})
	if len(res.Matched) != 0 {
		t.Errorf("before lookup neither updateType nor sourceUrl is known; fired %v", res.Matched)
	}
}

func TestApplyDoesNotTouchTheBase(t *testing.T) {
	base := map[string]any{"automerge": false, "packageRules": []any{}}
	e := rulesOf(t, map[string]any{"matchPackageNames": []any{"*"}, "automerge": true, "description": "x"})
	res := e.Apply(base, Subject{PackageName: "a"})
	if base["automerge"] != false || res.Config["automerge"] != true {
		t.Errorf("base=%v res=%v", base["automerge"], res.Config["automerge"])
	}
	if _, ok := res.Config["packageRules"]; ok {
		t.Error("the resolved config must not carry packageRules")
	}
}

// prBodyDefinitions merges into the base's columns; postUpgradeTasks, like
// every other object, replaces - both measured on the rule vectors - and
// neither writes through into the base.
func TestObjectsReplaceExceptTheMeasuredMergedOnes(t *testing.T) {
	base := map[string]any{
		"prBodyDefinitions": map[string]any{"Age": "a", "Package": "p"},
		"postUpgradeTasks":  map[string]any{"commands": []any{}, "installTools": map[string]any{}},
		"packageRules":      []any{},
	}
	e := rulesOf(t, map[string]any{
		"matchPackageNames": []any{"*"},
		"prBodyDefinitions": map[string]any{"Package": "linked"},
		"postUpgradeTasks":  map[string]any{"commands": []any{"composer update"}},
	})
	res := e.Apply(base, Subject{PackageName: "a"})
	defs := res.Config["prBodyDefinitions"].(map[string]any)
	if defs["Age"] != "a" || defs["Package"] != "linked" {
		t.Errorf("prBodyDefinitions = %v, want Age kept and Package overridden", defs)
	}
	tasks := res.Config["postUpgradeTasks"].(map[string]any)
	if _, ok := tasks["installTools"]; ok {
		t.Errorf("postUpgradeTasks = %v, want the rule's object alone", tasks)
	}
	if base["prBodyDefinitions"].(map[string]any)["Package"] != "p" {
		t.Error("the base's prBodyDefinitions was written through")
	}
	if len(res.Wrote["prBodyDefinitions"]) != 1 {
		t.Errorf("Wrote[prBodyDefinitions] = %v, want the one rule", res.Wrote["prBodyDefinitions"])
	}
}

// A rule on the analyzer's label fires only once there is one: before an
// analyzer ran the subject carries no effective label, and "unknown" is
// not a value a rule can name - a rule must never fire on the absence of
// an answer. The rule is marked as one that relaxes on the analyzer's
// word, which the planner admits only with trustEffective.
func TestMatchEffectiveFiresOnlyOnALabel(t *testing.T) {
	e, err := Compile([]any{
		map[string]any{"matchDatasources": []any{"helm"}, "matchUpdateTypes": []any{"major"}, "matchEffective": []any{"patch", "minor"}, "automerge": true},
	}, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	if !e.Rules[0].UsesEffective {
		t.Error("a matchEffective rule must be marked")
	}
	base := map[string]any{"automerge": false}
	s := Subject{DepName: "redis", Datasource: "helm", UpdateType: "major"}
	if res := e.Apply(base, s); len(res.Matched) != 0 {
		t.Errorf("no label, no match: %v", res.Matched)
	}
	s.Effective = "unknown"
	if res := e.Apply(base, s); len(res.Matched) != 0 {
		t.Errorf("unknown is not a label a rule matches: %v", res.Matched)
	}
	s.Effective = "patch"
	if res := e.Apply(base, s); len(res.Matched) != 1 || res.Config["automerge"] != true {
		t.Errorf("a patch label matches: %v %v", res.Matched, res.Config["automerge"])
	}
	s.Effective = "breaking-values"
	if res := e.Apply(base, s); len(res.Matched) != 0 {
		t.Errorf("breaking-values is not in the list: %v", res.Matched)
	}
}
