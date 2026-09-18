// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package glob implements the subset of minimatch that the estate's configs
// actually use, over slash-separated names.
//
// Go's path.Match is not enough: it has no brace expansion, no globstar, and
// its * crosses no separator but also cannot express "any number of path
// segments". Measured against the real config, the surface needed is:
//
//	52  exact strings                     registry.ole-hartwig.eu/devops/images/golang
//	40  globs                             @moselwal/**  **/composer.json  cgr.dev/**
//	12  the same, negated with a leading ! (handled by the caller)
//	 1  a /regex/ form                    (handled by the caller)
//
// including brace alternation (`containers/{/,}**`) and ** in the middle of a
// pattern (`registry.ole-hartwig.eu/**/sources`).
//
// Semantics, matching minimatch:
//
//   - `*` matches any run of characters within one segment, never a `/`.
//   - `?` matches exactly one character within a segment.
//   - `[abc]`, `[a-z]`, `[!abc]` match one character from a class.
//   - `**` as a whole segment matches zero or more segments. Zero is the case
//     that matters: `a/**` matches `a` itself, which is why the `{/,}**` idiom
//     in the config works.
//   - Matching is case-sensitive; package names are.
package glob

import (
	"strings"
)

// Matcher is a compiled pattern. Compiling once and matching many times is the
// point: the rule engine runs every pattern against every dependency.
type Matcher struct {
	raw string
	// alts holds one segment list per brace alternative. A pattern without
	// braces has exactly one.
	alts [][]string
	// exact is set when the pattern has no metacharacters at all, which is the
	// majority case and worth a string comparison instead of a walk.
	exact   string
	isExact bool
}

// Raw returns the pattern as written.
func (m *Matcher) Raw() string { return m.raw }

// IsExact reports whether the pattern is a plain string.
func (m *Matcher) IsExact() bool { return m.isExact }

// Compile prepares a pattern for matching.
func Compile(pattern string) *Matcher {
	m := &Matcher{raw: pattern}
	if !HasMeta(pattern) {
		m.isExact, m.exact = true, pattern
		return m
	}
	for _, alt := range ExpandBraces(pattern) {
		m.alts = append(m.alts, strings.Split(alt, "/"))
	}
	return m
}

// HasMeta reports whether a pattern contains any glob metacharacter. The rule
// engine uses it to sort exact names into a map lookup.
func HasMeta(s string) bool {
	return strings.ContainsAny(s, "*?[{")
}

// Match reports whether name matches.
func (m *Matcher) Match(name string) bool {
	if m.isExact {
		return name == m.exact
	}
	segs := strings.Split(name, "/")
	for _, pat := range m.alts {
		if matchSegments(pat, segs) {
			return true
		}
	}
	return false
}

// matchSegments walks pattern and name segments together, letting ** consume
// any number of name segments including none.
func matchSegments(pat, name []string) bool {
	switch {
	case len(pat) == 0:
		return len(name) == 0
	case pat[0] == "**":
		// Zero segments consumed, then one, then two... Trying zero first
		// makes `a/**` match `a`, which several patterns in the config rely
		// on.
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	case len(name) == 0:
		return false
	case matchOne(pat[0], name[0]):
		return matchSegments(pat[1:], name[1:])
	default:
		return false
	}
}

// matchOne matches a single segment against a single pattern segment. It never
// crosses a separator, because neither operand contains one.
func matchOne(pat, s string) bool {
	// Fast path: no metacharacters in this segment.
	if !strings.ContainsAny(pat, "*?[") {
		return pat == s
	}
	return matchHere(pat, s)
}

func matchHere(pat, s string) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case '*':
			// Collapse runs of * - within a segment they mean the same thing.
			for len(pat) > 0 && pat[0] == '*' {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true // trailing * matches the rest of the segment
			}
			// Try every split point. Segments are short; this is cheap and it
			// avoids a backtracking engine.
			for i := 0; i <= len(s); i++ {
				if matchHere(pat, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pat, s = pat[1:], s[1:]
		case '[':
			if len(s) == 0 {
				return false
			}
			n, ok := matchClass(pat, s[0])
			if !ok {
				return false
			}
			pat, s = pat[n:], s[1:]
		default:
			if len(s) == 0 || s[0] != pat[0] {
				return false
			}
			pat, s = pat[1:], s[1:]
		}
	}
	return len(s) == 0
}

// matchClass matches one character against a [...] class and returns how many
// pattern bytes the class occupied.
func matchClass(pat string, c byte) (int, bool) {
	i := 1 // skip '['
	negate := false
	if i < len(pat) && (pat[i] == '!' || pat[i] == '^') {
		negate = true
		i++
	}
	matched := false
	first := true
	for i < len(pat) && (pat[i] != ']' || first) {
		first = false
		if i+2 < len(pat) && pat[i+1] == '-' && pat[i+2] != ']' {
			if c >= pat[i] && c <= pat[i+2] {
				matched = true
			}
			i += 3
			continue
		}
		if pat[i] == c {
			matched = true
		}
		i++
	}
	if i >= len(pat) {
		// Unterminated class: treat the '[' as a literal, which is what
		// minimatch does rather than erroring.
		return 1, c == '['
	}
	i++ // skip ']'
	return i, matched != negate
}

// ExpandBraces turns `a{b,c}d` into []string{"abd","acd"}, recursively, so
// nested and multiple groups both work. An unbalanced brace is left alone
// rather than treated as an error - the pattern then simply matches nothing,
// which is easier to see than a config that failed to load.
func ExpandBraces(pattern string) []string {
	start := strings.IndexByte(pattern, '{')
	if start < 0 {
		return []string{pattern}
	}
	depth, end := 0, -1
	for i := start; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return []string{pattern}
	}

	prefix, body, suffix := pattern[:start], pattern[start+1:end], pattern[end+1:]

	// Split the body on commas at depth zero, so {a,{b,c}} keeps its nesting.
	var parts []string
	depth, last := 0, 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[last:i])
				last = i + 1
			}
		}
	}
	parts = append(parts, body[last:])

	var out []string
	for _, p := range parts {
		out = append(out, ExpandBraces(prefix+p+suffix)...)
	}
	return out
}

// Set is an ordered list of patterns with the negation semantics the rule
// engine needs.
//
// The predicate is the load-bearing part, and it is not "any match":
//
//	match  <=>  (no positive patterns OR some positive matches)
//	            AND no negative matches
//
// so a rule listing only negations selects everything outside its exclusions -
// which is exactly what rule 36 in the estate config does - and a rule mixing
// `registry.ole-hartwig.eu/**` with `!registry.ole-hartwig.eu/devops/ci-mirrors/**`
// selects the first minus the second.
type Set struct {
	positive []*Matcher
	negative []*Matcher
	// exact positives, for a map lookup before any walking. 52 of the 105
	// entries measured are plain strings.
	exactPos map[string]bool
}

// NewSet compiles a list of entries. A leading '!' marks a negation.
//
// BEWARE the empty set. NewSet(nil).Match(x) is TRUE for every x, because a
// set with no positive patterns selects everything outside its negations. That
// is right for the rule engine, where a rule listing only exclusions governs
// all the packages it does not exclude - and it is exactly wrong for an
// ignore list, where "nothing configured" must mean "ignore nothing".
//
// Use NewIgnoreSet for that reading rather than special-casing the emptiness
// at each call site. The distinction cost one caller a debugging round before
// it was written down here.
func NewSet(entries []string) *Set {
	s := &Set{exactPos: map[string]bool{}}
	for _, e := range entries {
		neg := strings.HasPrefix(e, "!")
		body := strings.TrimPrefix(e, "!")
		m := Compile(body)
		switch {
		case neg:
			s.negative = append(s.negative, m)
		case m.IsExact():
			s.exactPos[body] = true
		default:
			s.positive = append(s.positive, m)
		}
	}
	return s
}

// HasPositive reports whether the set constrains what it selects, as opposed
// to only excluding.
func (s *Set) HasPositive() bool { return len(s.positive) > 0 || len(s.exactPos) > 0 }

// Match applies the predicate above.
func (s *Set) Match(name string) bool {
	for _, m := range s.negative {
		if m.Match(name) {
			return false
		}
	}
	if !s.HasPositive() {
		return true
	}
	if s.exactPos[name] {
		return true
	}
	for _, m := range s.positive {
		if m.Match(name) {
			return true
		}
	}
	return false
}

// IgnoreSet is a list of patterns to exclude, with the reading an ignore list
// needs: an EMPTY set excludes nothing.
//
// It exists because Set answers the opposite question. Set is an allow-list
// whose empty case means "everything qualifies"; an ignore list's empty case
// means "nothing is ignored". Both are right for their own caller and each is
// a bug in the other's, so the difference is a type rather than a comment.
type IgnoreSet struct {
	set *Set
}

// NewIgnoreSet compiles exclusion patterns.
func NewIgnoreSet(patterns []string) *IgnoreSet {
	if len(patterns) == 0 {
		return &IgnoreSet{}
	}
	return &IgnoreSet{set: NewSet(patterns)}
}

// Ignores reports whether name is excluded. An empty set ignores nothing.
func (s *IgnoreSet) Ignores(name string) bool {
	if s == nil || s.set == nil {
		return false
	}
	return s.set.Match(name)
}

// Empty reports whether any pattern was configured.
func (s *IgnoreSet) Empty() bool { return s == nil || s.set == nil }
