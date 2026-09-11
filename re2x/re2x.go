// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package re2x wraps the standard library regexp package to fix the one
// thing it gets awkward: telling a named group that matched an empty
// string apart from a named group that did not participate in the match
// at all. Upstream tooling that generates the estate's regex patterns
// (Renovate custom managers, in particular) treats a non-participating
// optional group as "leave this config field unset", which is a different
// outcome than "set it to empty string". regexp's FindStringSubmatch
// collapses both cases to "", so callers that need the distinction have
// to go through FindStringSubmatchIndex, where a non-participating group
// reports -1 for its byte offsets. That is what this package does, once,
// so every caller downstream doesn't have to.
package re2x

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Regexp wraps a compiled pattern together with the ordered list of its
// named capture groups, so callers can enumerate group names without
// re-deriving them from the underlying regexp.Regexp on every call.
type Regexp struct {
	re    *regexp.Regexp
	names []string
}

// Match holds the result of one match against a Regexp. It distinguishes
// a group that did not participate in the match (Absent reports true,
// Get reports present=false) from a group that participated and matched
// the empty string (Get reports present=true, value ""). Byte spans are
// offsets into the original source string that produced the match, so a
// caller can splice replacement text in without re-serializing anything.
type Match struct {
	src   string
	names []string
	spans map[string][2]int
	whole [2]int
}

// Compile parses pattern as a regular expression and reports the named
// capture groups it contains. It runs CheckRE2 first: a pattern authored
// for a JavaScript engine (this project's source of truth is Renovate's
// customManagers, which are JS regexes) that happens to use a construct
// RE2 cannot express should be rejected with a message that names the
// construct, rather than left to whatever cryptic parse error the
// standard library produces - or worse, silently compiled into something
// that matches differently than the author intended.
func Compile(pattern string) (*Regexp, error) {
	if err := CheckRE2(pattern); err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("re2x: %w", err)
	}
	var names []string
	for _, name := range re.SubexpNames()[1:] {
		if name != "" {
			names = append(names, name)
		}
	}
	return &Regexp{re: re, names: names}, nil
}

// Names returns the pattern's named capture groups, in the order they
// appear in the pattern.
func (re *Regexp) Names() []string {
	return slices.Clone(re.names)
}

// Find returns the first match of the Regexp in src, or ok=false if there
// is none.
func (re *Regexp) Find(src string) (Match, bool) {
	loc := re.re.FindStringSubmatchIndex(src)
	if loc == nil {
		return Match{}, false
	}
	return newMatch(src, re.re, loc), true
}

// FindAll returns every non-overlapping match of the Regexp in src, in
// order of occurrence.
func (re *Regexp) FindAll(src string) []Match {
	locs := re.re.FindAllStringSubmatchIndex(src, -1)
	matches := make([]Match, 0, len(locs))
	for _, loc := range locs {
		matches = append(matches, newMatch(src, re.re, loc))
	}
	return matches
}

// newMatch builds a Match from one FindStringSubmatchIndex-style result.
// loc[2*i] and loc[2*i+1] are the start/end byte offsets of group i, or
// -1 for both when group i did not participate in the match - that -1 is
// the entire reason this package exists, since FindStringSubmatch alone
// throws the distinction away and reports "" either way.
func newMatch(src string, re *regexp.Regexp, loc []int) Match {
	subexpNames := re.SubexpNames()
	spans := make(map[string][2]int, len(subexpNames))
	var order []string
	for i, name := range subexpNames {
		if i == 0 || name == "" {
			continue
		}
		spans[name] = [2]int{loc[2*i], loc[2*i+1]}
		order = append(order, name)
	}
	return Match{
		whole: [2]int{loc[0], loc[1]}, src: src, names: order, spans: spans}
}

// Get returns the substring captured by the named group and whether it
// participated in the match. A non-participating optional group reports
// present=false, not value="" - callers that need "unset the config
// field" versus "set it to empty string" must check present, not value.
func (m Match) Get(name string) (value string, present bool) {
	span, ok := m.spans[name]
	if !ok || span[0] < 0 {
		return "", false
	}
	return m.src[span[0]:span[1]], true
}

// Absent reports whether the named group did not participate in the
// match. It is the mirror of Get's present return, spelled for call
// sites that only care about the negative case (e.g. deciding whether a
// templated field should be left unset).
func (m Match) Absent(name string) bool {
	span, ok := m.spans[name]
	return !ok || span[0] < 0
}

// Names returns the match's named capture groups, in the order they
// appear in the pattern, regardless of whether each one participated.
func (m Match) Names() []string {
	return slices.Clone(m.names)
}

// Span returns the byte offsets of the named group within the original
// source string passed to Find or FindAll, so a caller can replace
// exactly those bytes in place. It reports ok=false for a group that did
// not participate, mirroring Get.
func (m Match) Span(name string) (start, end int, ok bool) {
	span, exists := m.spans[name]
	if !exists || span[0] < 0 {
		return 0, 0, false
	}
	return span[0], span[1], true
}

// CheckRE2 reports an error naming the construct and its byte offset if
// pattern uses a backreference (\1..\9) or lookaround ((?= (?! (?<= (?<!)
// - constructs a JavaScript regex engine supports but RE2 (and therefore
// Go's regexp package) does not. It exists as a config lint: the estate
// config is authored against Renovate's JS-flavored regex dialect, and a
// pattern using one of these constructs would either fail to compile or,
// worse, compile into something that matches differently than intended.
// Refusing it here, at config-load time, catches that before it ships.
//
// The check is careful to accept (?<name>...) named groups, which are
// pattern-compatible with (?<=...) lookbehind only in their first three
// characters: the distinguishing character is the fourth, '=' or '!' for
// lookbehind versus a name character for a named group. The estate's
// patterns are full of named groups, so getting this wrong would make
// the lint reject nearly every real pattern in the config.
func CheckRE2(pattern string) error {
	inClass := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]

		if c == '\\' {
			// A backslash escape. Check for a backreference (\1..\9)
			// before consuming the pair - backreferences only mean
			// "backreference" outside a character class, where \1 would
			// instead be an (unusual but legal) octal escape.
			if i+1 < len(pattern) {
				next := pattern[i+1]
				if !inClass && next >= '1' && next <= '9' {
					return fmt.Errorf("re2x: backreference \\%c at byte offset %d: RE2 does not support backreferences", next, i)
				}
			}
			i++ // skip the escaped character; it needs no further inspection
			continue
		}

		if inClass {
			if c == ']' {
				inClass = false
			}
			continue
		}

		switch c {
		case '[':
			inClass = true
		case '(':
			rest := pattern[i:]
			switch {
			case strings.HasPrefix(rest, "(?<="):
				return fmt.Errorf("re2x: lookbehind (?<= at byte offset %d: RE2 does not support lookaround", i)
			case strings.HasPrefix(rest, "(?<!"):
				return fmt.Errorf("re2x: negative lookbehind (?<! at byte offset %d: RE2 does not support lookaround", i)
			case strings.HasPrefix(rest, "(?="):
				return fmt.Errorf("re2x: lookahead (?= at byte offset %d: RE2 does not support lookaround", i)
			case strings.HasPrefix(rest, "(?!"):
				return fmt.Errorf("re2x: negative lookahead (?! at byte offset %d: RE2 does not support lookaround", i)
			}
			// Anything else starting with "(?", including a named group
			// such as "(?<depName>...)", is left alone: its fourth
			// character is a name character rather than '=' or '!', so
			// none of the prefixes above matched.
		}
	}
	return nil
}

// Whole reports the span of the entire match in the source, which a caller
// needs when one pattern narrows a region for the next one to search inside.
//
// That is the recursive matchStrings strategy: the outer pattern exists to
// stop the inner one matching text that happens to look right somewhere else
// in the file, so the inner search has to be confined to exactly these bytes.
func (m Match) Whole() (start, end int, ok bool) {
	if m.whole[1] < m.whole[0] {
		return 0, 0, false
	}
	return m.whole[0], m.whole[1], true
}
