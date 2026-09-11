// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package jsonata evaluates the subset of JSONata that `customDatasources`
// transform templates use.
//
// Measured across the estate, that subset is two expressions:
//
//	{ "releases": versions }
//	{ "releases": [ { "version": passCliVersions.version } ] }
//
// So: object construction, array construction, dotted paths, string literals,
// and mapping a path over an array. Nothing else - no functions, no
// predicates, no operators.
//
// A construct outside the subset is a located error naming the offset and the
// token, never a silent empty result. A transform that quietly produced no
// releases would look exactly like a package with no releases, which is the
// failure shape this project keeps meeting.
package jsonata

import (
	"fmt"
	"strings"
)

// Error is a parse or evaluation failure, located in the expression.
type Error struct {
	Expr   string
	Offset int
	Token  string
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("jsonata %q: at offset %d, near %q: %s", e.Expr, e.Offset, e.Token, e.Msg)
}

// Transformer evaluates one expression against a decoded JSON document.
type Transformer struct {
	raw  string
	node node
}

// Raw returns the expression as written.
func (t *Transformer) Raw() string { return t.raw }

type node interface{ eval(env any) (any, error) }

// pathNode is a dotted path like `versions` or `passCliVersions.version`.
//
// Applied to an array, it maps: `passCliVersions.version` over an array of
// objects yields an array of the version fields. That is JSONata's sequence
// semantics, and it is the only reason the protonpass expression works.
type pathNode struct{ parts []string }

func (p pathNode) eval(env any) (any, error) {
	cur := env
	for _, part := range p.parts {
		next, err := step(cur, part)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

func step(cur any, field string) (any, error) {
	switch v := cur.(type) {
	case map[string]any:
		got, ok := v[field]
		if !ok {
			return nil, nil
		}
		return got, nil
	case []any:
		var out []any
		for _, e := range v {
			got, err := step(e, field)
			if err != nil {
				return nil, err
			}
			if got != nil {
				out = append(out, got)
			}
		}
		return out, nil
	case nil:
		return nil, nil
	default:
		return nil, nil
	}
}

type literalNode struct{ v any }

func (l literalNode) eval(any) (any, error) { return l.v, nil }

type objectNode struct {
	keys   []string
	values []node
}

func (o objectNode) eval(env any) (any, error) {
	out := make(map[string]any, len(o.keys))
	for i, k := range o.keys {
		v, err := o.values[i].eval(env)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

type arrayNode struct{ items []node }

func (a arrayNode) eval(env any) (any, error) {
	// An array whose single item evaluates to a sequence flattens into it.
	// That is what turns `[ { "version": passCliVersions.version } ]` into one
	// object per release rather than one object holding every version.
	if len(a.items) == 1 {
		if obj, ok := a.items[0].(objectNode); ok {
			if spread, ok, err := obj.spread(env); err != nil {
				return nil, err
			} else if ok {
				return spread, nil
			}
		}
	}
	out := make([]any, 0, len(a.items))
	for _, it := range a.items {
		v, err := it.eval(env)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// spread turns an object whose values are parallel sequences into a sequence
// of objects. It reports false when no value is a sequence, leaving the plain
// evaluation to handle it.
func (o objectNode) spread(env any) ([]any, bool, error) {
	vals := make([]any, len(o.keys))
	n := -1
	for i := range o.keys {
		v, err := o.values[i].eval(env)
		if err != nil {
			return nil, false, err
		}
		vals[i] = v
		if seq, ok := v.([]any); ok {
			if n >= 0 && n != len(seq) {
				return nil, false, nil // ragged: not a spread
			}
			n = len(seq)
		}
	}
	if n < 0 {
		return nil, false, nil
	}
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		row := make(map[string]any, len(o.keys))
		for j, k := range o.keys {
			if seq, ok := vals[j].([]any); ok {
				row[k] = seq[i]
			} else {
				row[k] = vals[j]
			}
		}
		out = append(out, row)
	}
	return out, true, nil
}

// Parse compiles an expression.
func Parse(expr string) (*Transformer, error) {
	p := &parser{src: expr}
	p.skipSpace()
	n, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos < len(p.src) {
		// A path followed by "(" is a function call, and saying so is far more
		// use than "unexpected trailing input" - JSONata's whole function
		// library is the most likely thing someone reaches for next.
		msg := "unexpected trailing input"
		switch p.src[p.pos] {
		case '(':
			msg = "unsupported construct: function calls are not implemented; " +
				"this subset covers object and array construction, dotted paths and string literals"
		case '[':
			msg = "unsupported construct: predicates and index expressions are not implemented"
		}
		return nil, &Error{expr, p.pos, p.rest(), msg}
	}
	return &Transformer{raw: expr, node: n}, nil
}

// Apply evaluates the expression against a decoded document.
func (t *Transformer) Apply(doc any) (any, error) {
	return t.node.eval(doc)
}

// Transform is the common case: parse and apply in one step.
func Transform(expr string, doc any) (any, error) {
	t, err := Parse(expr)
	if err != nil {
		return nil, err
	}
	return t.Apply(doc)
}

type parser struct {
	src string
	pos int
}

func (p *parser) rest() string {
	r := p.src[p.pos:]
	if len(r) > 30 {
		return r[:30] + "..."
	}
	return r
}

func (p *parser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n' || p.src[p.pos] == '\r') {
		p.pos++
	}
}

func (p *parser) parseValue() (node, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, &Error{p.src, p.pos, "", "expression ended early"}
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.parseObject()
	case c == '[':
		return p.parseArray()
	case c == '"' || c == '\'':
		s, err := p.parseString()
		if err != nil {
			return nil, err
		}
		return literalNode{s}, nil
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '$':
		return p.parsePath()
	default:
		return nil, &Error{p.src, p.pos, p.rest(),
			"unsupported construct; this subset implements object and array construction, dotted paths and string literals only"}
	}
}

func (p *parser) parseObject() (node, error) {
	start := p.pos
	p.pos++ // {
	obj := objectNode{}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, &Error{p.src, start, "{", "unclosed object"}
		}
		if p.src[p.pos] == '}' {
			p.pos++
			return obj, nil
		}
		key, err := p.parseString()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, &Error{p.src, p.pos, p.rest(), "expected ':' after an object key"}
		}
		p.pos++
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, key)
		obj.values = append(obj.values, val)
		p.skipSpace()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
		}
	}
}

func (p *parser) parseArray() (node, error) {
	start := p.pos
	p.pos++ // [
	arr := arrayNode{}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, &Error{p.src, start, "[", "unclosed array"}
		}
		if p.src[p.pos] == ']' {
			p.pos++
			return arr, nil
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		arr.items = append(arr.items, v)
		p.skipSpace()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
		}
	}
}

func (p *parser) parseString() (string, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return "", &Error{p.src, p.pos, "", "expected a string"}
	}
	q := p.src[p.pos]
	if q != '"' && q != '\'' {
		return "", &Error{p.src, p.pos, p.rest(), "expected a quoted string"}
	}
	p.pos++
	var sb strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\\' && p.pos+1 < len(p.src) {
			sb.WriteByte(p.src[p.pos+1])
			p.pos += 2
			continue
		}
		if c == q {
			p.pos++
			return sb.String(), nil
		}
		sb.WriteByte(c)
		p.pos++
	}
	return "", &Error{p.src, p.pos, "", "unterminated string"}
}

func (p *parser) parsePath() (node, error) {
	start := p.pos
	var parts []string
	for {
		begin := p.pos
		for p.pos < len(p.src) {
			c := p.src[p.pos]
			ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
				c >= '0' && c <= '9' || c == '_' || c == '$'
			if !ok {
				break
			}
			p.pos++
		}
		if p.pos == begin {
			return nil, &Error{p.src, start, p.rest(), "expected a path segment"}
		}
		parts = append(parts, p.src[begin:p.pos])
		if p.pos < len(p.src) && p.src[p.pos] == '.' {
			p.pos++
			continue
		}
		break
	}
	return pathNode{parts}, nil
}
