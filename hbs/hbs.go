// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package hbs renders the subset of Handlebars the estate's configuration
// uses. Measured across default.json and the repository-level configs, that
// subset is closed:
//
//	{{name}}                       interpolation, HTML-escaped
//	{{{name}}}                     interpolation, raw
//	{{#if name}}…{{else}}…{{/if}}  truthiness
//	{{#if (equals name 'lit')}}…   equality, nested three deep in practice
//
// and nothing else. A construct outside it is a located error, never a silent
// empty render - a subset that renders the wrong thing quietly is worse than
// one that refuses.
//
// The environment is tri-state: a name is present with a value, present but
// empty, or absent. That distinction is the point of this package rather than
// a detail of it. A regex capture group that did not participate in the match
// is absent, and a template that interpolates an absent name yields *unset*,
// not the empty string - so `registryUrlTemplate: "{{{registryUrl}}}"` simply
// does not set the key when the annotation omitted it.
//
// That is what lets one manager replace the pairs Renovate needs: three pairs
// in the estate config exist only because Renovate cannot express "this group
// may be absent".
package hbs

import (
	"fmt"
	"html"
	"strings"
)

// Env resolves names. Absent and present-but-empty are different answers.
type Env interface {
	Lookup(name string) (value string, present bool)
}

// MapEnv is an Env backed by a map, with an explicit absent set so a caller
// can model "matched, but empty".
type MapEnv struct {
	Values map[string]string
	// Absent overrides Values: a name listed here is absent even if Values
	// carries it.
	Absent map[string]bool
}

func (m MapEnv) Lookup(name string) (string, bool) {
	if m.Absent[name] {
		return "", false
	}
	v, ok := m.Values[name]
	return v, ok
}

// Error is a parse or render failure, located in the template.
type Error struct {
	Template string
	Offset   int
	Token    string
	Msg      string
}

func (e *Error) Error() string {
	return fmt.Sprintf("template %q: at offset %d, near %q: %s", e.Template, e.Offset, e.Token, e.Msg)
}

// Template is a parsed template.
type Template struct {
	raw   string
	nodes []node
}

// Raw returns the template as written.
func (t *Template) Raw() string { return t.raw }

type node interface{ isNode() }

type textNode struct{ text string }

type interpNode struct {
	name   string
	raw    bool // {{{ }}} - no HTML escaping
	offset int
}

type ifNode struct {
	cond      condition
	then, els []node
	offset    int
}

func (textNode) isNode()   {}
func (interpNode) isNode() {}
func (ifNode) isNode()     {}

// condition is either a bare name (truthiness) or an equality against a
// literal. Those are the only two forms measured.
type condition struct {
	name string
	// equals is set for `(equals name 'literal')`.
	isEquals bool
	literal  string
}

// Result is what rendering produced.
type Result struct {
	// Value is the rendered text.
	Value string
	// Set is false when the template interpolated a name that was absent. The
	// caller must then leave the field unset rather than assigning "".
	Set bool
}

// Parse compiles a template.
func Parse(src string) (*Template, error) {
	p := &parser{src: src}
	nodes, err := p.parseNodes("")
	if err != nil {
		return nil, err
	}
	if p.pos < len(src) {
		return nil, &Error{src, p.pos, src[p.pos:], "unexpected trailing input"}
	}
	return &Template{raw: src, nodes: nodes}, nil
}

// MustParse is Parse for package-level template constants.
func MustParse(src string) *Template {
	t, err := Parse(src)
	if err != nil {
		panic(err)
	}
	return t
}

// Render evaluates a template.
func (t *Template) Render(env Env) (Result, error) {
	var sb strings.Builder
	set := true
	if err := renderNodes(t.nodes, env, &sb, &set, t.raw); err != nil {
		return Result{}, err
	}
	return Result{Value: sb.String(), Set: set}, nil
}

// RenderString is the common case: render, and report whether the field should
// be assigned at all.
func RenderString(src string, env Env) (string, bool, error) {
	t, err := Parse(src)
	if err != nil {
		return "", false, err
	}
	r, err := t.Render(env)
	if err != nil {
		return "", false, err
	}
	return r.Value, r.Set, nil
}

func renderNodes(nodes []node, env Env, sb *strings.Builder, set *bool, raw string) error {
	for _, n := range nodes {
		switch n := n.(type) {
		case textNode:
			sb.WriteString(n.text)
		case interpNode:
			v, present := env.Lookup(n.name)
			if !present {
				// The load-bearing line: interpolating an absent name unsets
				// the whole field. Absence consumed by a condition does not -
				// see evalCondition, which never touches `set`.
				*set = false
				continue
			}
			if n.raw {
				sb.WriteString(v)
			} else {
				sb.WriteString(html.EscapeString(v))
			}
		case ifNode:
			branch := n.els
			if evalCondition(n.cond, env) {
				branch = n.then
			}
			if err := renderNodes(branch, env, sb, set, raw); err != nil {
				return err
			}
		}
	}
	return nil
}

func evalCondition(c condition, env Env) bool {
	v, present := env.Lookup(c.name)
	if c.isEquals {
		return present && v == c.literal
	}
	// Handlebars truthiness: absent, empty and "false" are falsy.
	return present && v != "" && v != "false"
}
