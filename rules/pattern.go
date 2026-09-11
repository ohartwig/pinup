// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package rules

import (
	"fmt"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/glob"
	"git.ole-hartwig.eu/pinup/pinup/re2x"
)

// pattern is one entry of a match list in Renovate's string-pattern form:
//
//   - `/regex/` or `/regex/i` - a regular expression, anchored nowhere
//   - anything else - a glob, matched case-insensitively
//   - a leading `!` negates either
//
// Globs are case-insensitive by Renovate's documented string-pattern rule;
// a regex is case-sensitive unless it carries the i flag.
type pattern struct {
	raw    string
	negate bool
	re     *re2x.Regexp
	glob   *glob.Matcher
	exact  string
	nocase bool
	all    bool
	// swallow marks a glob that ended in `/**`: glob holds the prefix and
	// the rest of the input is swallowed after a slash.
	swallow bool
}

func compilePattern(raw string) (pattern, error) {
	p := pattern{raw: raw}
	body := raw
	if strings.HasPrefix(body, "!") {
		p.negate = true
		body = body[1:]
	}
	if len(body) >= 2 && strings.HasPrefix(body, "/") && strings.LastIndex(body, "/") > 0 {
		end := strings.LastIndex(body, "/")
		expr, flags := body[1:end], body[end+1:]
		for _, f := range flags {
			switch f {
			case 'i':
				expr = "(?i)" + expr
			default:
				return p, fmt.Errorf("pattern %q: unsupported regex flag %q", raw, string(f))
			}
		}
		re, err := re2x.Compile(expr)
		if err != nil {
			return p, fmt.Errorf("pattern %q: %w", raw, err)
		}
		p.re = re
		return p, nil
	}
	p.nocase = true
	if body == "*" {
		// Measured: a bare `*` matches every name, slashes included, where
		// a minimatch `*` would stop at the first `/`. Three of the estate's
		// rules are written this way and fire for every dependency.
		p.all = true
		return p, nil
	}
	if glob.HasMeta(body) {
		// A pattern ending in `/**` is measured (source-urls.ndjson) to
		// need the slash in the input: `foo/**` admits `foo/` and `foo/x`
		// but not `foo`. The glob package lets a trailing globstar match
		// zero segments, so that suffix is matched here by hand.
		if strings.HasSuffix(body, "/**") {
			p.swallow = true
			body = strings.TrimSuffix(body, "/**")
		}
		p.glob = glob.Compile(strings.ToLower(body))
	} else {
		p.exact = strings.ToLower(body)
	}
	return p, nil
}

// matchesGlobOrExact applies minimatch's one leniency: an input that ends in
// `/` matches a pattern that does not, because the empty last segment is
// forgiven once the pattern has run out. The reverse is not forgiven - a
// pattern ending in `/` needs the slash in the input. Measured in
// source-urls.ndjson; the same rule holds for every string matcher, since
// Renovate runs them all through the same function.
func (p pattern) matchesGlobOrExact(s string) bool {
	s = strings.ToLower(s)
	if p.swallow {
		// `prefix/**`: the prefix has to be a whole run of segments, and
		// the input has to continue with a slash - with anything or nothing
		// after it.
		for i := 0; i < len(s); i++ {
			if s[i] == '/' && p.glob.Match(s[:i]) {
				return true
			}
		}
		return false
	}
	hit := func(s string) bool {
		if p.glob != nil {
			return p.glob.Match(s)
		}
		return s == p.exact
	}
	return hit(s) || (strings.HasSuffix(s, "/") && hit(strings.TrimSuffix(s, "/")))
}

// matches ignores negation; the list decides what a negative hit means.
func (p pattern) matches(s string) bool {
	switch {
	case p.all:
		return true
	case p.re != nil:
		_, ok := p.re.Find(s)
		return ok
	default:
		return p.matchesGlobOrExact(s)
	}
}

// patternList is Renovate's matchRegexOrGlobList:
//
//	match <=> (no positive patterns OR some positive matches)
//	          AND no negative matches
//
// The same predicate glob.Set implements, extended with the regex form.
type patternList struct {
	positive []pattern
	negative []pattern
}

func compileList(raw []string) (*patternList, error) {
	l := &patternList{}
	for _, r := range raw {
		p, err := compilePattern(r)
		if err != nil {
			return nil, err
		}
		if p.negate {
			l.negative = append(l.negative, p)
		} else {
			l.positive = append(l.positive, p)
		}
	}
	return l, nil
}

func (l *patternList) match(s string) bool {
	for _, p := range l.negative {
		if p.matches(s) {
			return false
		}
	}
	if len(l.positive) == 0 {
		return true
	}
	for _, p := range l.positive {
		if p.matches(s) {
			return true
		}
	}
	return false
}

// matchAny is match over several candidate inputs - for matchPackageNames,
// which Renovate checks against packageName and falls back to depName.
func (l *patternList) matchAny(ss ...string) bool {
	for _, s := range ss {
		if s != "" && l.match(s) {
			return true
		}
	}
	return false
}
