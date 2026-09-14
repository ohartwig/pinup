// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package regexm implements the custom regex manager: the one that is
// configured rather than coded.
//
// It carries most of the estate. Thirty-seven definitions exist across the
// runner config and the repository configs, against nine built-in managers, so
// this package finding the wrong thing is a bigger problem than any single
// manager being wrong.
//
// Two mechanisms do the work, and both live below this package on purpose:
//
//   - re2x tells a capture group that matched empty from one that did not
//     participate. A group that did not participate must leave its templated
//     field UNSET rather than empty, which is what lets one definition replace
//     a pair. Three pairs in the estate config exist only because the upstream
//     tool cannot express that.
//   - hbs renders the templates with a tri-state environment, so an absent
//     group propagates through a template rather than rendering as "".
//
// What is here is the manager: how matchStrings are applied, how a match
// becomes a Dependency, and where its bytes are.
package regexm

import (
	"context"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/hbs"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/re2x"
)

// Manager is the configured regex manager. One instance serves every custom
// definition; the definition arrives per call in ManagerConfig.Custom.
type Manager struct{}

// New returns the manager.
func New() *Manager { return &Manager{} }

func (*Manager) Name() string { return "custom.regex" }

// FilePatterns returns nothing: a custom manager has no defaults, only what
// its definition says. Returning a default here would make an unconfigured
// definition silently match files.
func (*Manager) FilePatterns() []string { return nil }

func (*Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Strategy names how matchStrings are combined.
const (
	StrategyAny       = "any"
	StrategyRecursive = "recursive"
	StrategyCombine   = "combination"
)

// Extract applies one custom definition to one file.
func (m *Manager) Extract(ctx context.Context, f extract.File, cfg extract.ManagerConfig) (extract.Result, error) {
	def := cfg.Custom
	if def == nil {
		return extract.Result{}, fmt.Errorf("regexm: no custom manager definition supplied for %s", f.Path)
	}
	if len(def.MatchStrings) == 0 {
		return extract.Result{}, fmt.Errorf("regexm: definition %d has no matchStrings", def.Index)
	}

	strategy := def.MatchStrategy
	if strategy == "" {
		strategy = StrategyAny
	}
	if strategy == StrategyCombine {
		// Nothing in the estate uses it, and guessing at semantics nobody has
		// exercised is how a manager quietly extracts the wrong thing.
		return extract.Result{}, fmt.Errorf(
			"regexm: definition %d uses matchStringsStrategy %q, which is not implemented", def.Index, StrategyCombine)
	}

	var res extract.Result
	src := string(f.Content)

	matches, warns, err := findMatches(src, def.MatchStrings, strategy, f.Path, def.Index)
	res.Warnings = append(res.Warnings, warns...)
	if err != nil {
		return res, err
	}

	for _, mt := range matches {
		dep, warn := m.dependency(f.Path, def, mt)
		if warn != nil {
			res.Warnings = append(res.Warnings, *warn)
			continue
		}
		res.Deps = append(res.Deps, dep)
	}
	extract.SortDeps(res.Deps)
	return res, nil
}

// Annotation prefixes. Every custom manager in the estate keys on a literal
// "renovate:" comment marker. The cutover plan keeps those lines untouched -
// a mass rename would separate annotations from the line they describe, and
// a separated annotation reports no dependency at all rather than a wrong
// one - and lets pinup read its own marker next to the old one. Widening the
// literal in the compiled pattern is what "reads both prefixes" means; the
// user's configuration is not rewritten.
const (
	legacyPrefix = "renovate:"
	widenedGroup = "(?:renovate|pinup):"
)

// WidenPrefix returns the pattern with every literal "renovate:" accepting
// "pinup:" as well. A pattern that does not mention the marker comes back
// unchanged, so a manager that never keyed on an annotation is not affected.
func WidenPrefix(pattern string) string {
	return strings.ReplaceAll(pattern, legacyPrefix, widenedGroup)
}

// span is a match plus the offset it sits at in the whole file, since a
// recursive inner match reports offsets relative to the region it searched.
type span struct {
	m      re2x.Match
	offset int
}

func findMatches(src string, patterns []string, strategy, file string, index int) ([]span, []model.Warning, error) {
	var warns []model.Warning

	switch strategy {
	case StrategyRecursive:
		// The outer pattern narrows a region; the next pattern searches only
		// inside it. That is not the same as running both over the whole file
		// - the outer one is there to stop the inner one matching text that
		// happens to look right somewhere else.
		// A region is a slice of the file plus where it starts in it, so an
		// inner match can report offsets against the whole file rather than
		// against the fragment it was found in.
		type region struct {
			text   string
			offset int
		}
		current := []region{{src, 0}}

		var out []span
		for i, p := range patterns {
			re, err := re2x.Compile(WidenPrefix(p))
			if err != nil {
				warns = append(warns, model.Warning{
					Stage: "extract", File: file,
					Msg: fmt.Sprintf("custom manager %d: pattern %d does not compile: %v", index, i, err),
				})
				return nil, warns, nil
			}
			last := i == len(patterns)-1
			var next []region
			for _, r := range current {
				for _, mt := range re.FindAll(r.text) {
					if last {
						out = append(out, span{m: mt, offset: r.offset})
						continue
					}
					s, e, ok := mt.Whole()
					if !ok {
						continue
					}
					next = append(next, region{r.text[s:e], r.offset + s})
				}
			}
			if !last {
				current = next
			}
		}
		return out, warns, nil

	default: // StrategyAny
		var out []span
		for i, p := range patterns {
			re, err := re2x.Compile(WidenPrefix(p))
			if err != nil {
				warns = append(warns, model.Warning{
					Stage: "extract", File: file,
					Msg: fmt.Sprintf("custom manager %d: pattern %d does not compile: %v", index, i, err),
				})
				continue
			}
			for _, mt := range re.FindAll(src) {
				out = append(out, span{m: mt, offset: 0})
			}
		}
		return out, warns, nil
	}
}

// dependency turns one match into a Dependency, rendering every template
// against the groups the match bound.
func (m *Manager) dependency(file string, def *model.CustomManager, s span) (model.Dependency, *model.Warning) {
	env := environment(s.m)

	render := func(what, tmpl, fromGroup string) (string, bool, error) {
		// A named group always wins over a template: the config writes
		// `(?<depName>...)` precisely so it does not have to write a template
		// too.
		if v, ok := s.m.Get(fromGroup); ok {
			return v, true, nil
		}
		if tmpl == "" {
			return "", false, nil
		}
		return renderTemplate(tmpl, env)
	}

	dep := model.Dependency{
		Manager:       "custom.regex",
		File:          file,
		CustomManager: def.Index,
	}

	for _, field := range []struct {
		what  string
		tmpl  string
		group string
		set   func(string)
	}{
		{"depName", def.DepNameTemplate, "depName", func(v string) { dep.DepName = v }},
		{"packageName", def.PackageNameTemplate, "packageName", func(v string) { dep.PackageName = v }},
		{"currentValue", def.CurrentValueTemplate, "currentValue", func(v string) { dep.CurrentValue = v }},
		{"datasource", def.DatasourceTemplate, "datasource", func(v string) { dep.Datasource = v }},
		{"versioning", def.VersioningTemplate, "versioning", func(v string) { dep.Versioning = v }},
		{"extractVersion", def.ExtractVersionTemplate, "extractVersion", func(v string) { dep.ExtractVersion = v }},
		{"depType", def.DepTypeTemplate, "depType", func(v string) { dep.DepType = v }},
	} {
		v, ok, err := render(field.what, field.tmpl, field.group)
		if err != nil {
			return model.Dependency{}, &model.Warning{
				Stage: "extract", File: file,
				Msg: fmt.Sprintf("custom manager %d: %s template: %v", def.Index, field.what, err),
			}
		}
		if ok {
			field.set(v)
		}
	}

	// registryUrl is the field the tri-state environment exists for. An
	// absent group must leave it unset rather than producing an empty URL,
	// which is what lets one definition cover both the annotated and the
	// unannotated case.
	if v, ok, err := render("registryUrl", def.RegistryURLTemplate, "registryUrl"); err != nil {
		return model.Dependency{}, &model.Warning{
			Stage: "extract", File: file,
			Msg: fmt.Sprintf("custom manager %d: registryUrl template: %v", def.Index, err),
		}
	} else if ok && v != "" {
		dep.RegistryURLs = []string{v}
	}

	if d, ok := s.m.Get("currentDigest"); ok {
		dep.CurrentDigest = d
	}

	dep.Captures = capturesOf(s.m)
	dep.Absent = absentOf(s.m)
	dep.Locus = locus(s)

	if dep.CurrentValue == "" && dep.CurrentDigest == "" {
		dep.SkipReason = "the match bound no currentValue and no currentDigest, so there is nothing to compare"
	}
	return dep, nil
}

// locus reports where the bytes this dependency owns sit in the whole file.
func locus(s span) model.Locus {
	l := model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest}
	if start, end, ok := s.m.Span("currentValue"); ok {
		l.ValueStart, l.ValueEnd = start+s.offset, end+s.offset
	}
	if start, end, ok := s.m.Span("currentDigest"); ok {
		l.DigestStart, l.DigestEnd = start+s.offset, end+s.offset
	}
	return l
}

// environment exposes the match's groups to the template renderer, keeping the
// present/absent distinction that hbs needs.
func environment(m re2x.Match) hbs.MapEnv {
	env := hbs.MapEnv{Values: map[string]string{}, Absent: map[string]bool{}}
	for _, name := range m.Names() {
		if v, ok := m.Get(name); ok {
			env.Values[name] = v
		} else {
			env.Absent[name] = true
		}
	}
	return env
}

func capturesOf(m re2x.Match) map[string]string {
	out := map[string]string{}
	for _, name := range m.Names() {
		if v, ok := m.Get(name); ok {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func absentOf(m re2x.Match) map[string]bool {
	out := map[string]bool{}
	for _, name := range m.Names() {
		if m.Absent(name) {
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func renderTemplate(tmpl string, env hbs.Env) (string, bool, error) {
	v, set, err := hbs.RenderString(tmpl, env)
	if err != nil {
		return "", false, err
	}
	return v, set, nil
}

// Edit replaces the bytes this manager recorded, refusing when they have
// moved: the value, the digest a currentDigest group bound, or both as
// value@digest - extract.EditRef, the same as the other managers. Its own
// edit replaced the value span alone, so a digest move of a reference a
// regex matched with its digest (wolfi-base:latest@sha256 in the
// image-signing templates) wrote "latest" over "latest" and the push
// found nothing changed (2026-09-14).
func (*Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	return extract.EditRef("custom.regex", f, up)
}

// Describe renders a one-line summary of a definition, for diagnostics that
// have to name which of thirty-seven definitions did something.
func Describe(def *model.CustomManager) string {
	if def == nil {
		return "custom manager <nil>"
	}
	return fmt.Sprintf("custom manager %d (%s)", def.Index, strings.Join(def.FilePatterns, ", "))
}
