// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package composer implements Renovate's `composer` scheme.
//
// This is the first scheme here with a real range grammar, and the first whose
// rewriting preserves the shape of a constraint rather than replacing it. The
// estate's composer dependencies are the largest single group in the corpus,
// so the shape rules matter:
//
//	"^8.5" replace         -> "^1.1"            operator kept, 2 components kept
//	"^8.5" bump            -> "^1.1.0"          bump writes three components
//	"^8.5" widen           -> "^8.5 || ^1.1"    the old constraint survives
//	"~0.9" replace         -> "~1.0"
//
// A constraint is VALID but never STABLE: "^8.5" is a range, and a range does
// not name a release. Stability belongs to plain versions, and a composer
// stability flag - -alpha, -beta, -RC, -dev - removes it.
package composer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "composer" }

// stability ranks composer's release-stability flags. Higher is more stable.
//
// unrecognised sits below everything, including dev. Measured: "1.0.0-RC1" is
// LOWER than "1.0.0-alpha", while "1.0.0-rc.1" is higher than "1.0.0-beta2".
// The difference is case - composer's flags are matched lowercase, and an
// uppercase "RC1" is not read as a flag at all, so it ranks below the lot.
//
// That looks like a quirk and is load-bearing: a package publishing
// "2.0.0-RC1" tags would otherwise be offered as an upgrade over its own
// stable release.
var stability = map[string]int{
	"unrecognised": -1,
	"dev":          0,
	"alpha":        1, "a": 1,
	"beta": 2, "b": 2,
	"rc": 3,
	"":   4, "stable": 4,
	"pl": 5, "p": 5,
}

type version struct {
	nums  []int
	flag  string // normalised stability flag, "" for a plain release
	flagN int    // the number after the flag, e.g. 1 in -RC1
}

// parseVersion reads a plain version, not a constraint.
func parseVersion(s string) (version, bool) {
	var v version
	body := strings.TrimSpace(s)
	if body == "" {
		return v, false
	}
	body = strings.TrimPrefix(strings.TrimPrefix(body, "v"), "V")

	// A stability flag may be attached with - or . or nothing at all:
	// 1.0.0-RC1, 1.0.0RC1 and 1.0.0-beta2 all occur.
	// Matched against the body as written, not lowercased: the case is what
	// separates a recognised flag from an unrecognised suffix.
	for _, name := range []string{"alpha", "beta", "stable", "dev", "rc", "pl", "a", "b", "p"} {
		idx := strings.Index(body, name)
		if idx <= 0 {
			continue
		}
		// The flag must follow the numbers, optionally after a separator.
		head := strings.TrimRight(body[:idx], "-._")
		tail := body[idx+len(name):]
		n := 0
		if tail != "" {
			tail = strings.TrimLeft(tail, "-._")
			if tail != "" {
				parsed, err := strconv.Atoi(tail)
				if err != nil {
					continue
				}
				n = parsed
			}
		}
		nums, ok := numericParts(head)
		if !ok {
			continue
		}
		v.nums, v.flag, v.flagN = nums, name, n
		if v.flag == "a" {
			v.flag = "alpha"
		}
		if v.flag == "b" {
			v.flag = "beta"
		}
		if v.flag == "p" {
			v.flag = "pl"
		}
		return v, true
	}

	// No recognised flag. Either a plain version, or a version carrying a
	// suffix composer does not know - the latter is valid and ranks below
	// every recognised stability.
	if nums, ok := numericParts(body); ok {
		v.nums = nums
		return v, true
	}
	if i := strings.IndexAny(body, "-+_"); i > 0 {
		if nums, ok := numericParts(body[:i]); ok {
			v.nums, v.flag = nums, "unrecognised"
			return v, true
		}
	}
	return version{}, false
}

// numericParts accepts one to three dot-separated numbers. Four is not a
// composer version: measured, "1.2.3.4" is invalid here.
func numericParts(s string) ([]int, bool) {
	if s == "" {
		return nil, false
	}
	segs := strings.Split(s, ".")
	if len(segs) == 0 || len(segs) > 3 {
		return nil, false
	}
	out := make([]int, 0, len(segs))
	for _, seg := range segs {
		n, err := strconv.Atoi(seg)
		if err != nil || seg == "" || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func (v version) at(i int) int {
	if i < len(v.nums) {
		return v.nums[i]
	}
	return 0
}

func compareVersions(a, b version) int {
	for i := 0; i < 3; i++ {
		if a.at(i) != b.at(i) {
			if a.at(i) < b.at(i) {
				return -1
			}
			return 1
		}
	}
	sa, sb := stability[a.flag], stability[b.flag]
	if sa != sb {
		if sa < sb {
			return -1
		}
		return 1
	}
	switch {
	case a.flagN < b.flagN:
		return -1
	case a.flagN > b.flagN:
		return 1
	default:
		return 0
	}
}

// constraint is one comparison in a range.
type constraint struct {
	op string // "", "^", "~", ">=", ">", "<=", "<", "!=", "*"
	v  version
	// wildcard is true for the "1.0.*" form, where the last stated component
	// is free.
	wildcard bool
	stated   int // how many components the constraint wrote
}

// parseRange reads an OR-separated list of AND-separated constraints.
func parseRange(s string) ([][]constraint, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	var out [][]constraint
	for _, alt := range strings.Split(s, "||") {
		var group []constraint
		for _, tok := range strings.Fields(strings.ReplaceAll(alt, ",", " ")) {
			c, ok := parseConstraint(tok)
			if !ok {
				return nil, false
			}
			group = append(group, c)
		}
		if len(group) == 0 {
			return nil, false
		}
		out = append(out, group)
	}
	return out, true
}

func parseConstraint(tok string) (constraint, bool) {
	var c constraint
	tok = strings.TrimSpace(tok)
	if tok == "*" {
		c.op = "*"
		return c, true
	}
	for _, op := range []string{">=", "<=", "!=", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(tok, op) {
			c.op = op
			tok = strings.TrimSpace(tok[len(op):])
			break
		}
	}
	if strings.HasSuffix(tok, ".*") {
		c.wildcard = true
		tok = strings.TrimSuffix(tok, ".*")
	}
	v, ok := parseVersion(tok)
	if !ok {
		return constraint{}, false
	}
	c.v = v
	c.stated = len(v.nums)
	return c, true
}

// IsVersion accepts a single version; a constraint is valid but not one.
func (*Scheme) IsVersion(s string) bool {
	_, ok := parseVersion(s)
	return ok
}

func (*Scheme) IsValid(s string) bool {
	if _, ok := parseVersion(s); ok {
		return true
	}
	_, ok := parseRange(s)
	return ok
}

// IsCompatible is measured as "the candidate is a single version": a
// constraint is a valid value but never a release that could follow anything.
func (*Scheme) IsCompatible(candidate, _ string) bool {
	_, ok := parseVersion(candidate)
	return ok
}

// IsStable is true only for a plain release version. A constraint is not a
// release, and a stability flag removes stability by definition.
func (*Scheme) IsStable(s string) bool {
	v, ok := parseVersion(s)
	return ok && v.flag == ""
}

// component reads a version component, and it is deliberately more lenient
// than IsValid.
//
// Measured: getPatch("^8.5") is 0, not absent - a constraint reports the
// components it names and zero for the rest. And getMajor("1.2.3.4") is 1 even
// though "1.2.3.4" is not a valid composer version. The accessors read
// whatever numbers are there; validity is a separate question, asked
// separately.
//
// That matters for rule matching: a rule keyed on a major has to match a
// dependency pinned by range, and a range is the normal way composer
// dependencies are written.
func component(s string, i int) (int, bool) {
	nums, ok := leadingNumbers(s)
	if !ok {
		return 0, false
	}
	if i < len(nums) {
		return nums[i], true
	}
	return 0, true
}

// leadingNumbers pulls the dot-separated numbers off the front of a value,
// skipping any range operator.
func leadingNumbers(s string) ([]int, bool) {
	s = strings.TrimSpace(s)
	// Take the first token of the first alternative: ">=1.0 <2.0" is about 1.0.
	if i := strings.Index(s, "||"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if f := strings.Fields(s); len(f) > 0 {
		s = f[0]
	}
	s = strings.TrimLeft(s, "^~><=!")
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")

	var out []int
	for _, seg := range strings.Split(s, ".") {
		j := 0
		for j < len(seg) && seg[j] >= '0' && seg[j] <= '9' {
			j++
		}
		if j == 0 {
			break
		}
		n, err := strconv.Atoi(seg[:j])
		if err != nil {
			break
		}
		out = append(out, n)
		if j != len(seg) {
			break
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func (*Scheme) Major(s string) (int, bool) { return component(s, 0) }
func (*Scheme) Minor(s string) (int, bool) { return component(s, 1) }
func (*Scheme) Patch(s string) (int, bool) { return component(s, 2) }

func (*Scheme) Compare(a, b string) int {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	return compareVersions(va, vb)
}

func (s *Scheme) Equal(a, b string) bool {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return a == b
	}
	return compareVersions(va, vb) == 0
}

func (s *Scheme) Satisfies(ver, rng string) bool {
	v, ok := parseVersion(ver)
	if !ok {
		return false
	}
	groups, ok := parseRange(rng)
	if !ok {
		return false
	}
	for _, group := range groups {
		all := true
		for _, c := range group {
			if !satisfiesOne(v, c) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func satisfiesOne(v version, c constraint) bool {
	if c.op == "*" {
		return true
	}
	// A prerelease only satisfies a constraint that names one.
	if v.flag != "" && c.v.flag == "" {
		return false
	}
	cmp := compareVersions(v, c.v)
	switch {
	case c.wildcard:
		for i := 0; i < c.stated; i++ {
			if v.at(i) != c.v.at(i) {
				return false
			}
		}
		return true
	case c.op == "^":
		if cmp < 0 {
			return false
		}
		switch {
		case c.v.at(0) != 0:
			return v.at(0) == c.v.at(0)
		case c.v.at(1) != 0:
			return v.at(0) == 0 && v.at(1) == c.v.at(1)
		default:
			return v.at(0) == 0 && v.at(1) == 0
		}
	case c.op == "~":
		if cmp < 0 {
			return false
		}
		// Tilde frees the last stated component and pins the rest.
		for i := 0; i < c.stated-1; i++ {
			if v.at(i) != c.v.at(i) {
				return false
			}
		}
		return true
	case c.op == ">":
		return cmp > 0
	case c.op == ">=":
		return cmp >= 0
	case c.op == "<":
		return cmp < 0
	case c.op == "<=":
		return cmp <= 0
	case c.op == "!=":
		return cmp != 0
	default:
		return cmp == 0
	}
}

// NewValue rewrites a constraint, keeping its shape.
//
// Measured per strategy, and they genuinely differ:
//
//	replace / pin / update-lockfile / auto  keep the operator and the number
//	                                        of components: "^8.5" -> "^1.1"
//	bump                                    writes three components: "^1.1.0"
//	widen                                   appends: "^8.5 || ^1.1"
func (s *Scheme) NewValue(current, target string, strategy versioning.RangeStrategy) (string, error) {
	t, ok := parseVersion(target)
	if !ok {
		return "", fmt.Errorf("composer: target %q is not a version", target)
	}
	groups, ok := parseRange(current)
	if !ok || len(groups) == 0 || len(groups[0]) == 0 {
		return target, nil
	}
	if strings.Contains(current, "||") {
		return s.newValueOfOr(current, target, t, strategy)
	}
	c := groups[0][0]

	write := func(parts int) string {
		out := make([]string, parts)
		for i := 0; i < parts; i++ {
			out[i] = strconv.Itoa(t.at(i))
		}
		return c.op + strings.Join(out, ".")
	}

	// Tilde: measured on one vector, "~0.9" with a 1.1.0 target becomes
	// "~1.0" under every strategy, bump included - a major crossed, and
	// the components after the major are zero. The first reading of that
	// vector, "tilde always writes major.0", was wrong for a tilde that
	// stays inside its major: it turned "~0.458.0" into "~0.0" for a
	// 0.459.0 target (development/moselwal/gsc-index-info, 2026-09-14) and
	// two updates then shared one value. The reading now: a crossed major
	// zeroes what follows it, an uncrossed one keeps the stated components
	// from the target - "~0.459.0". The second half rests on composer's
	// own meaning of tilde rather than on a probe; a three-component tilde
	// in the probe grid would settle it and is worth adding.
	if c.op == "~" {
		parts := max(c.stated, 1)
		var out string
		if t.at(0) != c.v.at(0) {
			zeros := make([]string, parts)
			zeros[0] = strconv.Itoa(t.at(0))
			for i := 1; i < parts; i++ {
				zeros[i] = "0"
			}
			out = c.op + strings.Join(zeros, ".")
		} else {
			out = write(parts)
		}
		if strategy == versioning.StrategyWiden {
			return current + " || " + out, nil
		}
		return out, nil
	}

	switch strategy {
	case versioning.StrategyBump:
		return write(3), nil
	case versioning.StrategyWiden:
		return current + " || " + write(max(c.stated, 1)), nil
	default:
		return write(max(c.stated, 1)), nil
	}
}

// newValueOfOr rewrites an OR of ranges - "^6.4 || ^7.4", the shape the
// estate's libraries carry. Measured (composer.json, getNewValue): a
// target one alternative already admits leaves the constraint as it is
// under widen and update-lockfile, and under bump Renovate appends a
// duplicate alternative ("^6.4 || ^7.4 || ^6.4") that never reaches a
// branch - so it is read as no change here. A target beyond every
// alternative is appended as one more caret alternative - the target's
// major line, minor zero - under bump, widen and pin ("^6.4 || ^7.4 ||
// ^8.0"), and replaces the whole constraint with that caret under replace
// and update-lockfile ("^8.0").
func (s *Scheme) newValueOfOr(current, target string, t version, strategy versioning.RangeStrategy) (string, error) {
	if s.Satisfies(target, current) {
		return current, nil
	}
	// The new alternative is the target's major line, minor zero:
	// measured, 8.1.6 joins "^7.4 || ^8.0" as "^8.0", not "^8.1".
	caret := "^" + strconv.Itoa(t.at(0)) + ".0"
	switch strategy {
	case versioning.StrategyReplace, versioning.StrategyUpdateLockfile:
		return caret, nil
	default:
		return current + " || " + caret, nil
	}
}
