// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package preset resolves `extends` into a flat configuration.
//
// Layer 1. The library it ships is the observed definition of every preset in
// the transitive closure of the estate's configuration, recorded by running
// the pinned Renovate container's resolver (tools/capture/presets.sh) and
// generated into library.json by tools/presetgen - rebuilt once, from
// behaviour, not embedded from the Renovate tree. Presets pinup deliberately
// does not carry are marked dropped: resolving one yields nothing and a
// warning that says why, so a configuration that names one is not silently
// weaker than it reads.
//
// Resolution order is Renovate's, checked against 1085 captured presets: a
// preset's own extends resolve before its body, presets listed later override
// earlier ones, and the configuration's own keys win over everything it
// extends. packageRules concatenate, description accumulates, everything
// else replaces. A rule inside packageRules may itself extend presets; those
// resolve into the rule.
package preset

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed library.json
var libraryJSON []byte

// Entry is one library preset.
type Entry struct {
	Definition map[string]any `json:"definition,omitempty"`
	Dropped    string         `json:"dropped,omitempty"`
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
	// Get returns the preset definition. ok is false when the source does
	// not know the name; an error means the source knows it and could not
	// deliver it, which fails resolution.
	Get(name string) (def map[string]any, dropped string, ok bool, err error)
}

// Get implements Source over the library.
func (l *Library) Get(name string) (map[string]any, string, bool, error) {
	e, ok := l.Presets[name]
	if !ok {
		return nil, "", false, nil
	}
	return e.Definition, e.Dropped, true, nil
}

// Result is a resolved configuration with what resolution saw on the way.
type Result struct {
	Config map[string]any
	// Visited lists every preset merged, in the order it was merged.
	Visited []string
	// Warnings names dropped presets and the reasons.
	Warnings []string
}

// Resolve expands config's extends, recursively, against src. config is not
// modified.
//
// An unknown preset is an error, not a warning: a configuration that extends
// something that cannot be found would otherwise run with less than it says,
// which for an ignore list or a disable rule means updates nobody asked for.
func Resolve(config map[string]any, src Source) (*Result, error) {
	r := &Result{}
	cfg, err := r.resolve(config, src, nil)
	if err != nil {
		return nil, err
	}
	r.Config = cfg
	return r, nil
}

func (r *Result) resolve(config map[string]any, src Source, stack []string) (map[string]any, error) {
	acc := map[string]any{}
	_, ownDescription := config["description"]
	nested := len(stack) > 0
	names, err := extendsOf(config)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		for _, s := range stack {
			if s == name {
				return nil, fmt.Errorf("preset %q extends itself through %s", name, strings.Join(append(stack, name), " > "))
			}
		}
		def, dropped, ok, err := src.Get(name)
		if err != nil {
			return nil, fmt.Errorf("preset %q: %w", name, err)
		}
		if !ok {
			return nil, fmt.Errorf("preset %q is not known", name)
		}
		r.Visited = append(r.Visited, name)
		if dropped != "" {
			r.warnOnce(fmt.Sprintf("preset %q is dropped: %s", name, dropped))
			continue
		}
		resolved, err := r.resolve(def, src, append(stack, name))
		if err != nil {
			return nil, err
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
		acc = merge(acc, resolved)
	}

	own := map[string]any{}
	for k, v := range config {
		if k == "extends" {
			continue
		}
		own[k] = v
	}
	out := merge(acc, own)

	// Rule-level extends: group:<x>Monorepo is one rule extending
	// monorepo:<x>, and the matchers arrive from the extended preset.
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
			resolvedRule, err := r.resolve(obj, src, stack)
			if err != nil {
				return nil, err
			}
			flat = append(flat, resolvedRule)
		}
		out["packageRules"] = flat
	}
	return out, nil
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

// merge writes child over parent into a new map. packageRules,
// customManagers and description concatenate - measured: workarounds:all
// resolves to its own description only because its children carry theirs
// inside packageRules, not because a parent's description wins. Everything
// else, arrays and objects included, is replaced by the child's value.
func merge(parent, child map[string]any) map[string]any {
	out := make(map[string]any, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		switch {
		case k == "packageRules" || k == "customManagers" || k == "description":
			out[k] = concat(out[k], v)
		default:
			out[k] = v
		}
	}
	return out
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
