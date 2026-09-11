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
		p.glob = glob.Compile(strings.ToLower(body))
	} else {
		p.exact = strings.ToLower(body)
	}
	return p, nil
}

// matches ignores negation; the list decides what a negative hit means.
func (p pattern) matches(s string) bool {
	switch {
	case p.all:
		return true
	case p.re != nil:
		_, ok := p.re.Find(s)
		return ok
	case p.glob != nil:
		return p.glob.Match(strings.ToLower(s))
	default:
		return strings.ToLower(s) == p.exact
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
