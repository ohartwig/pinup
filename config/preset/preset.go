// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package preset resolves `extends` into a flat configuration.
//
// Layer 1. The library it ships, presets.json, is pinup's own: written for
// the behaviour the configurations it serves need, under the names
// Renovate uses, and checked by effect against the rule vectors the pinned
// container recorded rather than against Renovate's preset data, which is
// AGPL and not carried (README.md in this directory). Presets whose
// behaviour pinup deliberately does not carry are inert: they resolve to
// nothing, and resolving one warns with the reason, so a configuration that
// names one is not silently weaker than it reads.
//
// Resolution order is Renovate's, measured on the pinned container's
// resolver: a preset's own extends resolve before its body, presets listed
// later override earlier ones, and the configuration's own keys win over
// everything it extends. packageRules concatenate, description accumulates,
// everything else replaces. A rule inside packageRules may itself extend
// presets; those resolve into the rule.
package preset

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed presets.json
var libraryJSON []byte

// Entry is one library preset.
type Entry struct {
	Definition map[string]any `json:"definition,omitempty"`
	// Inert, when set, is the warning to raise when the preset is used:
	// its keys resolve but have no effect in pinup.
	Inert string `json:"inert,omitempty"`
}

// Library maps preset names to their definitions.
type Library struct {
	Source  string           `json:"source"`
	Presets map[string]Entry `json:"presets"`
}

// Builtin returns the shipped library.
func Builtin() *Library {
	lib, err := ParseLibrary(libraryJSON)
	if err != nil {
		panic("preset: embedded library does not parse: " + err.Error())
	}
	return lib
}

// ParseLibrary reads a library document.
func ParseLibrary(b []byte) (*Library, error) {
	var lib Library
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&lib); err != nil {
		return nil, err
	}
	return &lib, nil
}

// Names returns the library's preset names, sorted.
func (l *Library) Names() []string {
	out := make([]string, 0, len(l.Presets))
	for n := range l.Presets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Source fetches a preset by name. The builtin library answers internal
// names; `local>` and other remote forms are answered by a Source the caller
// composes in front of it.
type Source interface {
	// Get returns the preset definition and, for an inert preset, the
	// warning to raise. ok is false when the source does not know the name;
	// an error means the source knows it and could not deliver it, which
	// fails resolution.
	Get(name string) (def map[string]any, inert string, ok bool, err error)
}

// Get implements Source over the library.
func (l *Library) Get(name string) (map[string]any, string, bool, error) {
	e, ok := l.Presets[name]
	if !ok {
		return nil, "", false, nil
	}
	return e.Definition, e.Inert, true, nil
}

// Result is a resolved configuration with what resolution saw on the way.
type Result struct {
	Config map[string]any
	// Visited lists every preset merged, in the order it was merged.
	Visited []string
	// Warnings names inert presets that were used, and the reasons.
	Warnings []string
	// Origins maps an RFC 6901 pointer - "/schedule", "/packageRules/12" -
	// to the chain of sources that wrote it, winner last. A preset is named
	// by its name; the configuration's own keys by OwnSource.
	Origins map[string][]string
}

// OwnSource is the origin recorded for keys the configuration sets itself.
const OwnSource = "config"

// Resolve expands config's extends, recursively, against src. config is not
// modified.
//
// An unknown preset is an error, not a warning: a configuration that extends
// something that cannot be found would otherwise run with less than it says,
// which for an ignore list or a disable rule means updates nobody asked for.
func Resolve(config map[string]any, src Source) (*Result, error) {
	r := &Result{}
	cfg, prov, err := r.resolve(config, src, nil, OwnSource)
	if err != nil {
		return nil, err
	}
	r.Config = cfg
	r.Origins = prov
	return r, nil
}

// provenance maps pointers to the chain of sources that wrote them, in the
// frame of one resolved document. Merging shifts the indices of concat keys,
// so a child's provenance is remapped into the parent's frame as it merges.
type provenance map[string][]string

// resolve expands one configuration or preset. owner names it in provenance.
func (r *Result) resolve(config map[string]any, src Source, stack []string, owner string) (map[string]any, provenance, error) {
	acc, accProv := map[string]any{}, provenance{}
	_, ownDescription := config["description"]
	nested := len(stack) > 0
	names, err := extendsOf(config)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range names {
		for _, s := range stack {
			if s == name {
				return nil, nil, fmt.Errorf("preset %q extends itself through %s", name, strings.Join(append(stack, name), " > "))
			}
		}
		def, inert, ok, err := src.Get(name)
		if err != nil {
			return nil, nil, fmt.Errorf("preset %q: %w", name, err)
		}
		if !ok {
			return nil, nil, fmt.Errorf("preset %q is not known", name)
		}
		r.Visited = append(r.Visited, name)
		if inert != "" {
			r.warnOnce(fmt.Sprintf("preset %q has no effect: %s", name, inert))
		}
		resolved, resolvedProv, err := r.resolve(def, src, append(stack, name), name)
		if err != nil {
			return nil, nil, err
		}
		// Measured over every parent/child pair in the captured closure
		// (152 pairs, no exception): a nested preset that has a description
		// of its own drops the descriptions of children whose definition
		// carries packageRules. workarounds:all therefore describes itself
		// only, while config:recommended - no description of its own -
		// lists its children's. The top-level configuration keeps every
		// child's description, its own or not.
		if _, hasRules := def["packageRules"]; nested && ownDescription && hasRules {
			delete(resolved, "description")
		}
		acc, accProv = merge(acc, accProv, resolved, resolvedProv)
	}

	own, ownProv := map[string]any{}, provenance{}
	for k, v := range config {
		if k == "extends" {
			continue
		}
		own[k] = v
		if list, ok := v.([]any); ok && isConcat(k) {
			for i := range list {
				ownProv[fmt.Sprintf("/%s/%d", k, i)] = []string{owner}
			}
		} else {
			ownProv["/"+k] = []string{owner}
		}
	}
	out, outProv := merge(acc, accProv, own, ownProv)

	// Rule-level extends: group:<x>Monorepo is one rule extending
	// monorepo:<x>, and the matchers arrive from the extended preset. The
	// resolved rule keeps the provenance of the rule that named them.
	if rules, ok := out["packageRules"].([]any); ok {
		flat := make([]any, 0, len(rules))
		for _, rule := range rules {
			obj, ok := rule.(map[string]any)
			if !ok {
				flat = append(flat, rule)
				continue
			}
			if _, has := obj["extends"]; !has {
				flat = append(flat, obj)
				continue
			}
			resolvedRule, _, err := r.resolve(obj, src, stack, owner)
			if err != nil {
				return nil, nil, err
			}
			flat = append(flat, resolvedRule)
		}
		out["packageRules"] = flat
	}
	return out, outProv, nil
}

func extendsOf(config map[string]any) ([]string, error) {
	raw, ok := config["extends"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch x := raw.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("extends contains a non-string entry %v", e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("extends is %T, want a list of strings", raw)
}

// isConcat names the keys that concatenate instead of replacing.
// Measured: workarounds:all resolves to its own description only because
// its children carry theirs inside packageRules, not because a parent's
// description wins.
func isConcat(k string) bool {
	return k == "packageRules" || k == "customManagers" || k == "description"
}

// merge writes child over parent into a new map, and child's provenance
// over parent's in the merged frame. Concat keys append and their pointers
// shift by the parent's length; everything else - arrays and objects
// included - is replaced by the child's value, and the chain grows.
func merge(parent map[string]any, parentProv provenance, child map[string]any, childProv provenance) (map[string]any, provenance) {
	out := make(map[string]any, len(parent)+len(child))
	prov := make(provenance, len(parentProv)+len(childProv))
	for k, v := range parent {
		out[k] = v
	}
	for p, chain := range parentProv {
		prov[p] = append([]string(nil), chain...)
	}
	for k, v := range child {
		if isConcat(k) {
			base := 0
			if existing, ok := out[k].([]any); ok {
				base = len(existing)
			}
			out[k] = concat(out[k], v)
			n := len(out[k].([]any)) - base
			for i := range n {
				from := fmt.Sprintf("/%s/%d", k, i)
				to := fmt.Sprintf("/%s/%d", k, base+i)
				prov[to] = append([]string(nil), childProv[from]...)
			}
			continue
		}
		out[k] = v
		prov["/"+k] = append(prov["/"+k], childProv["/"+k]...)
	}
	return out, prov
}

func (r *Result) warnOnce(w string) {
	for _, have := range r.Warnings {
		if have == w {
			return
		}
	}
	r.Warnings = append(r.Warnings, w)
}

func concat(existing, add any) any {
	var out []any
	switch x := existing.(type) {
	case []any:
		out = append(out, x...)
	case string:
		out = append(out, x)
	}
	switch x := add.(type) {
	case []any:
		out = append(out, x...)
	case nil:
	default:
		out = append(out, x)
	}
	return out
}
