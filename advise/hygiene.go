// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package advise

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/sched"
)

// The hygiene checks find what the file says twice, or says for nothing:
// a value a preset already sets, a rule nothing can reach, a preset
// extended twice. None of them changes what the run does; they make the
// file say what it means.
var hygieneChecks = []Check{
	{ID: "hygiene/redundant-inherited", Run: redundantInherited},
	{ID: "hygiene/redundant-default", Run: redundantDefault},
	{ID: "hygiene/null-clears-nothing", Run: nullClearsNothing},
	{ID: "hygiene/schedule-anytime-explicit", Run: scheduleAnytimeExplicit},
	{ID: "hygiene/extends-duplicate", Run: extendsDuplicate},
	{ID: "hygiene/extends-transitive", Run: extendsTransitive},
	{ID: "hygiene/duplicate-entry", Run: duplicateEntry},
	{ID: "hygiene/rule-no-matchers", Run: ruleNoMatchers},
	{ID: "hygiene/rule-no-effect", Run: ruleNoEffect},
	{ID: "hygiene/rule-duplicate-matchers", Run: ruleDuplicateMatchers},
	{ID: "hygiene/custom-manager-duplicate", Run: customManagerDuplicate},
}

// structural keys are never redundant by value: they compose rather than set.
var structural = map[string]bool{"extends": true, "description": true, "packageRules": true, "customManagers": true, "$schema": true}

// concatChanged is what a fix that drops or adds one preset declares: the
// arrays presets concatenate are the ones whose resolution moves.
var concatChanged = []string{"/packageRules", "/customManagers", "/description"}

// redundantInherited finds top-level keys the file repeats from its presets.
func redundantInherited(in *Input) []Finding {
	var out []Finding
	for _, k := range slices.Sorted(maps.Keys(in.Layer.Raw)) {
		if structural[k] {
			continue
		}
		inherited, ok := in.Inherited[k]
		if !ok || !reflect.DeepEqual(in.Layer.Raw[k], inherited) {
			continue
		}
		p := ptr(k)
		repeats := "a preset"
		if chain := in.Resolved.Prov[p]; len(chain) >= 2 {
			repeats = chain[len(chain)-2].Source
		}
		out = append(out, Finding{ID: "hygiene/redundant-inherited", Category: Hygiene, Severity: Info, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, model.NoRule),
			Msg: fmt.Sprintf("`%s` repeats what %s already sets", k, repeats), Fix: &Fix{Pointer: p, Op: OpRemove}})
	}
	return out
}

// redundantDefault finds top-level keys the file sets to the builtin default
// while no preset sets them.
func redundantDefault(in *Input) []Finding {
	defaults := config.Defaults()
	var out []Finding
	for _, k := range slices.Sorted(maps.Keys(in.Layer.Raw)) {
		if structural[k] || in.Layer.Raw[k] == nil {
			continue
		}
		if _, inherited := in.Inherited[k]; inherited {
			continue
		}
		def, ok := defaults[k]
		if !ok || !reflect.DeepEqual(in.Layer.Raw[k], def) {
			continue
		}
		p := ptr(k)
		out = append(out, Finding{ID: "hygiene/redundant-default", Category: Hygiene, Severity: Info, Pointer: p, Frame: FrameFile, Origin: model.Origin{Source: config.DefaultsSource, Pointer: p, Rule: model.NoRule},
			Msg: fmt.Sprintf("`%s` is the builtin default", k), Fix: &Fix{Pointer: p, Op: OpRemove}})
	}
	return out
}

// nullClearsNothing finds a null that clears a key no preset sets.
func nullClearsNothing(in *Input) []Finding {
	defaults := config.Defaults()
	var out []Finding
	for _, k := range slices.Sorted(maps.Keys(in.Layer.Raw)) {
		if in.Layer.Raw[k] != nil || structural[k] {
			continue
		}
		if _, inherited := in.Inherited[k]; inherited {
			continue
		}
		if def, ok := defaults[k]; ok && def != nil {
			continue
		}
		p := ptr(k)
		out = append(out, Finding{ID: "hygiene/null-clears-nothing", Category: Hygiene, Severity: Warn, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, model.NoRule),
			Msg: fmt.Sprintf("`%s: null` clears nothing; no preset sets it", k), Fix: &Fix{Pointer: p, Op: OpRemove, Changes: []string{p}}})
	}
	return out
}

// scheduleAnytimeExplicit finds a schedule that spells out "no schedule".
// When the presets set a real window, the file is opening it on purpose and
// the finding says so without a fix.
func scheduleAnytimeExplicit(in *Input) []Finding {
	own, ok := in.Layer.Raw["schedule"]
	if !ok {
		return nil
	}
	exprs := stringsOf(own)
	if len(exprs) == 0 {
		return nil
	}
	for _, e := range exprs {
		if !sched.IsAnyTime(strings.TrimSpace(e)) {
			return nil
		}
	}
	inherited, hasInherited := in.Inherited["schedule"]
	if hasInherited && reflect.DeepEqual(own, inherited) {
		return nil // redundant-inherited says it
	}
	f := Finding{ID: "hygiene/schedule-anytime-explicit", Category: Hygiene, Severity: Info, Pointer: "/schedule", Frame: FrameFile, Origin: in.fileOrigin("/schedule", model.NoRule),
		Msg: "an explicit `at any time` equals no schedule", Fix: &Fix{Pointer: "/schedule", Op: OpRemove, Changes: []string{"/schedule"}}}
	if hasInherited {
		if list := stringsOf(inherited); len(list) > 0 && !sched.IsAnyTime(list[0]) {
			f.Msg = fmt.Sprintf("`at any time` opens the inherited schedule %s; say so in a description if that is meant", canonical(inherited))
			f.Fix = nil
		}
	}
	return []Finding{f}
}

// extendsDuplicate finds a preset named twice in the file's extends.
func extendsDuplicate(in *Input) []Finding {
	ext := stringsOf(in.Layer.Raw["extends"])
	var out []Finding
	seen := map[string]bool{}
	for j, name := range ext {
		if seen[name] {
			p := ptr("extends", j)
			out = append(out, Finding{ID: "hygiene/extends-duplicate", Category: Hygiene, Severity: Warn, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, model.NoRule),
				Msg: fmt.Sprintf("`%s` is extended twice; its rules are concatenated twice", name), Fix: &Fix{Pointer: p, Op: OpRemove, Changes: concatChanged}})
		}
		seen[name] = true
	}
	return out
}

// extendsTransitive finds a preset the file names that another of its
// entries already brings in.
func extendsTransitive(in *Input) []Finding {
	ext := stringsOf(in.Layer.Raw["extends"])
	var out []Finding
	for i, name := range ext {
		for _, other := range ext {
			if other == name || !slices.Contains(in.Reached[other], name) {
				continue
			}
			p := ptr("extends", i)
			out = append(out, Finding{ID: "hygiene/extends-transitive", Category: Hygiene, Severity: Info, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, model.NoRule),
				Msg: fmt.Sprintf("`%s` is already reached through `%s`; listing it again only reorders what it sets", name, other), Fix: &Fix{Pointer: p, Op: OpRemove, Changes: concatChanged}})
			break
		}
	}
	return out
}

// listKeys are the top-level lists whose entries are a set.
var listKeys = []string{"ignorePaths", "ignoreDeps", "enabledManagers", "labels", "addLabels", "allowedCommands"}

// duplicateEntry finds a string listed twice in a set-like list.
func duplicateEntry(in *Input) []Finding {
	var out []Finding
	report := func(base string, list []string, rule int) {
		if j := firstDuplicate(list); j >= 0 {
			p := fmt.Sprintf("%s/%d", base, j)
			out = append(out, Finding{ID: "hygiene/duplicate-entry", Category: Hygiene, Severity: Info, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, rule),
				Msg: fmt.Sprintf("`%s` appears twice in %s", list[j], base), Fix: &Fix{Pointer: p, Op: OpRemove, Changes: []string{base}}})
		}
	}
	for _, k := range listKeys {
		if list, ok := in.Layer.Raw[k].([]any); ok {
			report(ptr(k), stringsOf(list), model.NoRule)
		}
	}
	for i, rule := range in.ownRules() {
		for _, k := range matcherKeys(rule) {
			if list, ok := rule[k].([]any); ok {
				report(ptr("packageRules", i, k), stringsOf(list), in.base("packageRules")+i)
			}
		}
	}
	return out
}

// ruleNoMatchers finds a rule that applies to every dependency.
func ruleNoMatchers(in *Input) []Finding {
	var out []Finding
	for i, rule := range in.ownRules() {
		if len(matcherKeys(rule)) > 0 {
			continue
		}
		p := ptr("packageRules", i)
		out = append(out, Finding{ID: "hygiene/rule-no-matchers", Category: Hygiene, Severity: Warn, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, in.base("packageRules")+i),
			Msg: fmt.Sprintf("packageRules[%d] matches every dependency; its keys belong at the top level", i)})
	}
	return out
}

// ruleNoEffect finds a rule that matches and sets nothing.
func ruleNoEffect(in *Input) []Finding {
	var out []Finding
	for i, rule := range in.ownRules() {
		sets := slices.DeleteFunc(applyKeys(rule), func(k string) bool { return k == "description" })
		if len(sets) > 0 {
			continue
		}
		p := ptr("packageRules", i)
		out = append(out, Finding{ID: "hygiene/rule-no-effect", Category: Hygiene, Severity: Warn, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, in.base("packageRules")+i),
			Msg: fmt.Sprintf("packageRules[%d] matches but sets nothing", i), Fix: &Fix{Pointer: p, Op: OpRemove, Changes: []string{"/packageRules"}}})
	}
	return out
}

// ruleDuplicateMatchers finds two rules selecting the same dependencies. The
// later one overrides the earlier per key, so an earlier rule whose keys the
// later one all sets again is shadowed; otherwise the two could be one.
func ruleDuplicateMatchers(in *Input) []Finding {
	rules := in.ownRules()
	var out []Finding
	for i, a := range rules {
		for j := i + 1; j < len(rules); j++ {
			b := rules[j]
			if canonical(matchers(a)) != canonical(matchers(b)) {
				continue
			}
			p := ptr("packageRules", i)
			f := Finding{ID: "hygiene/rule-duplicate-matchers", Category: Hygiene, Severity: Info, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, in.base("packageRules")+i)}
			setsA := slices.DeleteFunc(applyKeys(a), func(k string) bool { return k == "description" })
			setsB := applyKeys(b)
			if len(setsA) > 0 && slices.ContainsFunc(setsA, func(k string) bool { return !slices.Contains(setsB, k) }) {
				f.Msg = fmt.Sprintf("packageRules[%d] and packageRules[%d] select the same dependencies; they could be one rule", i, j)
			} else {
				f.Msg = fmt.Sprintf("packageRules[%d] is shadowed by packageRules[%d], which selects the same dependencies and sets every key again", i, j)
				f.Fix = &Fix{Pointer: p, Op: OpRemove, Changes: []string{"/packageRules"}}
			}
			out = append(out, f)
			break
		}
	}
	return out
}

func matchers(rule map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range matcherKeys(rule) {
		out[k] = rule[k]
	}
	return out
}

// customManagerDuplicate finds two identical custom managers: every file
// they match is extracted twice.
func customManagerDuplicate(in *Input) []Finding {
	list, _ := in.Layer.Raw["customManagers"].([]any)
	var out []Finding
	for i := range list {
		for j := i + 1; j < len(list); j++ {
			if canonical(list[i]) != canonical(list[j]) {
				continue
			}
			p := ptr("customManagers", j)
			out = append(out, Finding{ID: "hygiene/custom-manager-duplicate", Category: Hygiene, Severity: Warn, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, model.NoRule),
				Msg: fmt.Sprintf("customManagers[%d] duplicates customManagers[%d]; every file it matches is extracted twice", j, i), Fix: &Fix{Pointer: p, Op: OpRemove, Changes: []string{"/customManagers"}}})
			break
		}
	}
	return out
}
