// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package hbs

import (
	"strings"
)

type parser struct {
	src string
	pos int
}

// parseNodes reads until end of input or until it meets a closing tag that
// belongs to `stop` ("if" when parsing a branch, "" at the top level).
func (p *parser) parseNodes(stop string) ([]node, error) {
	var nodes []node
	for p.pos < len(p.src) {
		open := strings.Index(p.src[p.pos:], "{{")
		if open < 0 {
			nodes = append(nodes, textNode{p.src[p.pos:]})
			p.pos = len(p.src)
			break
		}
		if open > 0 {
			nodes = append(nodes, textNode{p.src[p.pos : p.pos+open]})
			p.pos += open
		}

		// A closing or else tag ends this run of nodes; the caller consumes it.
		if stop != "" {
			if p.peekTag("{{/" + stop + "}}") {
				return nodes, nil
			}
			if p.peekTag("{{else}}") {
				return nodes, nil
			}
		}

		n, err := p.parseTag()
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if stop != "" {
		return nil, &Error{p.src, p.pos, "", "unclosed {{#" + stop + "}}"}
	}
	return nodes, nil
}

func (p *parser) peekTag(tag string) bool {
	return strings.HasPrefix(p.src[p.pos:], tag)
}

func (p *parser) parseTag() (node, error) {
	start := p.pos
	rest := p.src[p.pos:]

	// Triple-stash first: {{{ is a prefix of {{, so order matters.
	if strings.HasPrefix(rest, "{{{") {
		end := strings.Index(rest, "}}}")
		if end < 0 {
			return nil, &Error{p.src, start, trunc(rest), "unclosed {{{"}
		}
		name := strings.TrimSpace(rest[3:end])
		p.pos += end + 3
		if err := checkName(p.src, start, name); err != nil {
			return nil, err
		}
		return interpNode{name: name, raw: true, offset: start}, nil
	}

	end := strings.Index(rest, "}}")
	if end < 0 {
		return nil, &Error{p.src, start, trunc(rest), "unclosed {{"}
	}
	body := strings.TrimSpace(rest[2:end])
	p.pos += end + 2

	switch {
	case strings.HasPrefix(body, "#if"):
		cond, err := parseCondition(p.src, start, strings.TrimSpace(body[3:]))
		if err != nil {
			return nil, err
		}
		then, err := p.parseNodes("if")
		if err != nil {
			return nil, err
		}
		var els []node
		if p.peekTag("{{else}}") {
			p.pos += len("{{else}}")
			els, err = p.parseNodes("if")
			if err != nil {
				return nil, err
			}
		}
		if !p.peekTag("{{/if}}") {
			return nil, &Error{p.src, p.pos, trunc(p.src[p.pos:]), "expected {{/if}}"}
		}
		p.pos += len("{{/if}}")
		return ifNode{cond: cond, then: then, els: els, offset: start}, nil

	case strings.HasPrefix(body, "#"):
		return nil, &Error{p.src, start, "{{" + body + "}}",
			"unsupported block helper; this subset implements #if only"}

	case strings.HasPrefix(body, "/"):
		return nil, &Error{p.src, start, "{{" + body + "}}", "unexpected closing tag"}

	case body == "else":
		return nil, &Error{p.src, start, "{{else}}", "{{else}} outside an {{#if}}"}

	default:
		if err := checkName(p.src, start, body); err != nil {
			return nil, err
		}
		return interpNode{name: body, raw: false, offset: start}, nil
	}
}

// parseCondition accepts a bare name or `(equals name 'literal')`.
func parseCondition(src string, off int, s string) (condition, error) {
	if !strings.HasPrefix(s, "(") {
		if err := checkName(src, off, s); err != nil {
			return condition{}, err
		}
		return condition{name: s}, nil
	}
	if !strings.HasSuffix(s, ")") {
		return condition{}, &Error{src, off, s, "unclosed subexpression"}
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	const helper = "equals"
	if !strings.HasPrefix(inner, helper+" ") {
		name := inner
		if i := strings.IndexByte(inner, ' '); i > 0 {
			name = inner[:i]
		}
		return condition{}, &Error{src, off, s,
			"unsupported helper " + quote(name) + "; this subset implements (equals x 'literal') only"}
	}
	args := strings.TrimSpace(inner[len(helper)+1:])
	i := strings.IndexByte(args, ' ')
	if i < 0 {
		return condition{}, &Error{src, off, s, "equals needs two arguments"}
	}
	name := strings.TrimSpace(args[:i])
	lit := strings.TrimSpace(args[i+1:])
	if len(lit) < 2 || (lit[0] != '\'' && lit[0] != '"') || lit[len(lit)-1] != lit[0] {
		return condition{}, &Error{src, off, s,
			"the second argument of equals must be a quoted literal"}
	}
	if err := checkName(src, off, name); err != nil {
		return condition{}, err
	}
	return condition{name: name, isEquals: true, literal: lit[1 : len(lit)-1]}, nil
}

// checkName rejects anything that is not a plain identifier. Path expressions
// (`a.b`), `this`, `@index` and the rest of Handlebars are not implemented, and
// they must say so rather than resolve to nothing.
func checkName(src string, off int, name string) error {
	if name == "" {
		return &Error{src, off, "{{}}", "empty interpolation"}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
		if !ok {
			return &Error{src, off, name,
				"unsupported name; this subset implements plain identifiers only"}
		}
	}
	return nil
}

// Names returns every name a template references, so a caller can check a
// template against the capture groups a manager actually produces.
func (t *Template) Names() []string {
	seen := map[string]bool{}
	var out []string
	var walk func([]node)
	walk = func(ns []node) {
		for _, n := range ns {
			switch n := n.(type) {
			case interpNode:
				if !seen[n.name] {
					seen[n.name] = true
					out = append(out, n.name)
				}
			case ifNode:
				if !seen[n.cond.name] {
					seen[n.cond.name] = true
					out = append(out, n.cond.name)
				}
				walk(n.then)
				walk(n.els)
			}
		}
	}
	walk(t.nodes)
	return out
}

func trunc(s string) string {
	const n = 40
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func quote(s string) string { return "\"" + s + "\"" }
