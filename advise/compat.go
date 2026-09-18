// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/re2x"
	"github.com/ohartwig/pinup/sched"
)

// The compat checks say what this version of pinup does with the
// configuration: what it does not read, what it cannot evaluate, what it
// rewrote, and what it would refuse.
var compatChecks = []Check{
	{ID: "compat/preset-inert", Run: presetInert},
	{ID: "compat/migrated", Run: migrated},
	{ID: "compat/rule-not-evaluable", Run: ruleNotEvaluable},
	{ID: "compat/rules-not-compilable", Run: rulesNotCompilable},
	{ID: "compat/key-unsupported", Run: keyUnsupported},
	{ID: "compat/key-partial", Run: keyPartial},
	{ID: "compat/manager-unknown", Run: managerUnknown},
	{ID: "compat/datasource-unknown", Run: datasourceUnknown},
	{ID: "compat/schedule-invalid", Run: scheduleInvalid},
	{ID: "compat/regex-not-re2", Run: regexNotRE2},
}

var inertName = regexp.MustCompile(`^preset "([^"]+)"`)

// presetInert passes the resolver's inert-preset warnings through, pointed
// at the extends entry when the file names the preset itself.
func presetInert(in *Input) []Finding {
	var out []Finding
	own := stringsOf(in.Layer.Raw["extends"])
	for _, w := range in.PresetWarnings {
		f := Finding{ID: "compat/preset-inert", Category: Compat, Severity: Warn, Pointer: "/extends", Frame: FrameFile, Msg: w}
		f.Origin = in.fileOrigin("/extends", model.NoRule)
		if m := inertName.FindStringSubmatch(w); m != nil {
			if i := slices.Index(own, m[1]); i >= 0 {
				f.Pointer = ptr("extends", i)
				f.Origin = in.fileOrigin(f.Pointer, model.NoRule)
				f.Fix = &Fix{Pointer: f.Pointer, Op: OpRemove}
			}
		}
		out = append(out, f)
	}
	return out
}

// migrated passes the migration notes through, for what the file itself
// wrote: a preset's migrations are the preset's business. A "0" release
// age the run reads as null can be written as null; the other notes have
// no fix.
func migrated(in *Input) []Finding {
	var out []Finding
	for _, note := range in.Resolved.Migrations {
		pointer, _, _ := strings.Cut(note, ": ")
		if !strings.HasPrefix(pointer, "/") {
			pointer = "/" + strings.Fields(note)[0]
		}
		fp, own := in.filePointer(pointer)
		if !own {
			continue
		}
		f := Finding{ID: "compat/migrated", Category: Compat, Severity: Info, Pointer: pointer, Frame: FrameResolved, Origin: in.originAt(pointer), Msg: note}
		if strings.Contains(note, `"0" migrated to null`) {
			f.Fix = &Fix{Pointer: fp, Op: OpSet, Value: nil}
		}
		out = append(out, f)
	}
	return out
}

var ruleIndexIn = regexp.MustCompile(`^packageRules\[(\d+)\]`)

// ruleNotEvaluable passes the engine's warnings through: a rule carrying a
// matcher the engine does not evaluate never fires.
func ruleNotEvaluable(in *Input) []Finding {
	if in.Engine == nil {
		return nil
	}
	var out []Finding
	for _, w := range in.Engine.Warnings {
		f := Finding{ID: "compat/rule-not-evaluable", Category: Compat, Severity: Warn, Pointer: "/packageRules", Frame: FrameResolved, Msg: w}
		if m := ruleIndexIn.FindStringSubmatch(w); m != nil {
			n, _ := strconv.Atoi(m[1])
			f.Pointer = ptr("packageRules", n)
			if i, ok := in.ownIndex("packageRules", n); ok {
				f.Msg += fmt.Sprintf(" (the file's packageRules[%d])", i)
			}
		}
		f.Origin = in.originAt(f.Pointer)
		out = append(out, f)
	}
	return out
}

// rulesNotCompilable is the one finding that stands for a run that would
// not start: a matcher the engine has never heard of.
func rulesNotCompilable(in *Input) []Finding {
	if in.RuleError == "" {
		return nil
	}
	return []Finding{{ID: "compat/rules-not-compilable", Category: Compat, Severity: Error, Pointer: "/packageRules", Frame: FrameResolved, Origin: in.originAt("/packageRules"), Msg: in.RuleError}}
}

// keyUnsupported names the file's keys nothing in pinup reads. Removing one
// changes nothing the run does; the resolved document still carries the
// key, so the fix declares it.
func keyUnsupported(in *Input) []Finding {
	return keysOfClass(in, "compat/key-unsupported", Warn, func(k string) bool {
		cls, ok := KeySupport[k]
		return !ok || cls == Unsupported
	}, "`%s` is read by nothing in pinup", true)
}

// keyPartial names the file's keys pinup honours in part.
func keyPartial(in *Input) []Finding {
	return keysOfClass(in, "compat/key-partial", Info, func(k string) bool {
		return KeySupport[k] == Partial
	}, "`%s` is honoured partially: see docs/configuration.md", false)
}

func keysOfClass(in *Input, id string, sev Severity, want func(string) bool, format string, fix bool) []Finding {
	var out []Finding
	emit := func(pointer, key string, rule int) {
		f := Finding{ID: id, Category: Compat, Severity: sev, Pointer: pointer, Frame: FrameFile, Origin: in.fileOrigin(pointer, rule), Msg: fmt.Sprintf(format, key)}
		if fix {
			f.Fix = &Fix{Pointer: pointer, Op: OpRemove, Changes: []string{pointer}}
		}
		out = append(out, f)
	}
	for _, k := range slices.Sorted(maps.Keys(in.Layer.Raw)) {
		if want(k) {
			emit(ptr(k), k, model.NoRule)
		}
	}
	for i, rule := range in.ownRules() {
		for _, k := range slices.Sorted(maps.Keys(rule)) {
			if want(k) {
				emit(ptr("packageRules", i, k), k, in.base("packageRules")+i)
			}
		}
	}
	return out
}

// managerUnknown names enabled managers nothing implements: discovery for
// them finds nothing, every run.
func managerUnknown(in *Input) []Finding {
	if in.Covers == nil {
		return nil
	}
	var out []Finding
	own := stringsOf(in.Layer.Raw["enabledManagers"])
	for _, m := range in.Decoded.EnabledManagers {
		if m == "custom.regex" || in.Covers(m) {
			continue
		}
		f := Finding{ID: "compat/manager-unknown", Category: Compat, Severity: Warn, Pointer: "/enabledManagers", Frame: FrameResolved, Origin: in.originAt("/enabledManagers"),
			Msg: fmt.Sprintf("manager `%s` is enabled but not implemented; discovery for it finds nothing", m)}
		if j := slices.Index(own, m); j >= 0 {
			f.Fix = &Fix{Pointer: ptr("enabledManagers", j), Op: OpRemove, Changes: []string{"/enabledManagers"}}
		}
		out = append(out, f)
	}
	return out
}

// datasourceUnknown names datasources no lookup serves: a rule matching on
// one never fires, a custom manager naming one never resolves.
func datasourceUnknown(in *Input) []Finding {
	if in.Datasources == nil {
		return nil
	}
	known := func(name string) bool {
		if in.Datasources[name] {
			return true
		}
		if rest, ok := strings.CutPrefix(name, "custom."); ok {
			_, defined := in.Decoded.CustomDatasources[rest]
			return defined
		}
		return false
	}
	var out []Finding
	// The file's rules only: a library preset naming an ecosystem pinup
	// does not serve is the library's business, and nothing the file's
	// author can act on.
	for i, rule := range in.ownRules() {
		for j, ds := range stringsOf(rule["matchDatasources"]) {
			if known(ds) {
				continue
			}
			p := ptr("packageRules", i, "matchDatasources", j)
			out = append(out, Finding{ID: "compat/datasource-unknown", Category: Compat, Severity: Warn, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, in.base("packageRules")+i),
				Msg: fmt.Sprintf("datasource `%s` is not implemented; the rule never matches", ds)})
		}
	}
	for i, cm := range in.Decoded.CustomManagers {
		ds := cm.DatasourceTemplate
		if ds == "" || strings.Contains(ds, "{{") || known(ds) {
			continue
		}
		p := ptr("customManagers", i, "datasourceTemplate")
		out = append(out, Finding{ID: "compat/datasource-unknown", Category: Compat, Severity: Warn, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p),
			Msg: fmt.Sprintf("datasource `%s` is not implemented; nothing this manager finds can be looked up", ds)})
	}
	return out
}

// scheduleInvalid parses every schedule the run would: the repository's,
// the lock-file maintenance's, the vulnerability alerts', each rule's.
func scheduleInvalid(in *Input) []Finding {
	tz, _ := in.Resolved.Raw["timezone"].(string)
	var out []Finding
	check := func(pointer string, exprs []string) {
		if len(exprs) == 0 {
			return
		}
		_, err := sched.Parse(exprs, tz)
		if err == nil {
			return
		}
		p := pointer
		if strings.HasPrefix(err.Error(), "timezone ") {
			p = "/timezone"
		}
		out = append(out, Finding{ID: "compat/schedule-invalid", Category: Compat, Severity: Error, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p), Msg: err.Error()})
	}
	check("/schedule", in.Decoded.Schedule)
	for _, k := range []string{"lockFileMaintenance", "vulnerabilityAlerts"} {
		if obj, ok := in.Resolved.Raw[k].(map[string]any); ok {
			check(ptr(k, "schedule"), stringsOf(obj["schedule"]))
		}
	}
	for i, rule := range in.resolvedRules() {
		check(ptr("packageRules", i, "schedule"), stringsOf(rule["schedule"]))
	}
	return out
}

// regexNotRE2 is the lint the spec asked for (tech-spec §0.2): every regex
// the configuration carries must be RE2, because Go's regexp is. A pattern
// with a lookaround or a backreference would fail at run time, or match
// something else.
func regexNotRE2(in *Input) []Finding {
	var out []Finding
	check := func(pointer, expr string) {
		if err := re2x.CheckRE2(expr); err != nil {
			out = append(out, Finding{ID: "compat/regex-not-re2", Category: Compat, Severity: Error, Pointer: pointer, Frame: FrameResolved, Origin: in.originAt(pointer),
				Msg: fmt.Sprintf("pattern is not RE2: %v", err)})
			return
		}
		if _, err := re2x.Compile(expr); err != nil {
			out = append(out, Finding{ID: "compat/regex-not-re2", Category: Compat, Severity: Error, Pointer: pointer, Frame: FrameResolved, Origin: in.originAt(pointer),
				Msg: fmt.Sprintf("pattern does not compile: %v", err)})
		}
	}
	// A /…/ pattern, with Renovate's one flag stripped; a glob is not a regex.
	slashed := func(pointer, raw string) {
		body := strings.TrimPrefix(raw, "!")
		if len(body) < 2 || !strings.HasPrefix(body, "/") || strings.LastIndex(body, "/") == 0 {
			return
		}
		end := strings.LastIndex(body, "/")
		check(pointer, body[1:end])
	}
	for i, cm := range in.Decoded.CustomManagers {
		for j, m := range cm.MatchStrings {
			check(ptr("customManagers", i, "matchStrings", j), m)
		}
		for j, p := range cm.FilePatterns {
			slashed(ptr("customManagers", i, "managerFilePatterns", j), p)
		}
	}
	for name, patterns := range in.Decoded.FilePatterns {
		if strings.HasPrefix(name, "custom.regex#") {
			continue
		}
		for j, p := range patterns {
			slashed(ptr(name, "managerFilePatterns", j), p)
		}
	}
	for i, rule := range in.resolvedRules() {
		for _, k := range matcherKeys(rule) {
			for j, p := range stringsOf(rule[k]) {
				slashed(ptr("packageRules", i, k, j), p)
			}
		}
		if ev, ok := rule["extractVersion"].(string); ok {
			check(ptr("packageRules", i, "extractVersion"), ev)
		}
	}
	return out
}

// filePointer translates a resolved pointer into the file's frame: 1:1 for
// most keys, shifted by the presets' contribution for a concat key. The
// second result is false when the file does not own the element.
func (in *Input) filePointer(resolved string) (string, bool) {
	segs := strings.Split(strings.TrimPrefix(resolved, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return "", false
	}
	if concatKeys[segs[0]] && len(segs) > 1 {
		n, err := strconv.Atoi(segs[1])
		if err != nil {
			return "", false
		}
		i, ok := in.ownIndex(segs[0], n)
		if !ok {
			return "", false
		}
		segs[1] = strconv.Itoa(i)
	} else if _, ok := in.Layer.Raw[unescape(segs[0])]; !ok {
		return "", false
	}
	return "/" + strings.Join(segs, "/"), true
}

func unescape(seg string) string {
	return strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
}
