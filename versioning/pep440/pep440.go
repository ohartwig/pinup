// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package pep440 implements the `pep440` scheme: Python's version
// identification, as PEP 440 (now packaging.python.org's version
// specifiers specification) defines it.
//
// The grammar is the public specification's, not Renovate's:
//
//	[N!]N(.N)*[{a|b|rc}N][.postN][.devN][+local]
//
// with the normalisations the specification lists - case folding,
// "alpha"/"beta"/"c"/"pre"/"preview" as the pre-release spellings,
// "rev"/"r" and a bare "-N" as post-release spellings, "-" and "_"
// accepted as separators, and trailing zeros in the release segment not
// changing the version's order. Ordering is the specification's too: an
// epoch first, then the release, and within one release
// dev < pre-release < final < post-release, with a dev release of a
// pre- or post-release sorting just below it.
//
// Ranges are version specifier sets: comma-separated clauses with one of
// ===, ==, !=, <=, >=, <, >, ~=, where == and != accept a trailing ".*"
// prefix match and ~= is the compatible-release operator. The estate
// pins exact versions (measured 2026-09-15: nine pypi dependencies, every
// one a plain version), so NewValue rewrites a single-clause range and
// declines a compound one rather than guessing at which bound to move.
package pep440

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "pep440" }

// version is a parsed PEP 440 version.
type version struct {
	epoch   int
	release []int
	// pre is the pre-release phase: "" (none), "a", "b" or "rc"; preN its
	// number.
	pre  string
	preN int
	// post and dev are -1 when absent.
	post  int
	dev   int
	local []localPart
}

type localPart struct {
	num   int
	str   string
	isNum bool
}

// parse reads one version. It accepts the normalised and the tolerated
// spellings alike and returns the canonical form's parts.
func parse(s string) (version, bool) {
	v := version{post: -1, dev: -1}
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return v, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		local := s[i+1:]
		s = s[:i]
		if local == "" {
			return v, false
		}
		for _, p := range strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '-' || r == '_' }) {
			if n, err := strconv.Atoi(p); err == nil {
				v.local = append(v.local, localPart{num: n, isNum: true})
				continue
			}
			for _, r := range p {
				if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
					return v, false
				}
			}
			v.local = append(v.local, localPart{str: p})
		}
	}
	if i := strings.IndexByte(s, '!'); i >= 0 {
		n, err := strconv.Atoi(s[:i])
		if err != nil || n < 0 {
			return v, false
		}
		v.epoch = n
		s = s[i+1:]
	}
	// Release segment: digits and dots up to the first letter or separator
	// that is not followed by a digit-only continuation of the release.
	i := 0
	for i < len(s) {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			return v, false
		}
		n, _ := strconv.Atoi(s[i:j])
		v.release = append(v.release, n)
		i = j
		if i < len(s) && s[i] == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			i++
			continue
		}
		break
	}
	rest := s[i:]
	// Suffixes, in the order the specification allows them: pre, post, dev.
	rest, ok := v.readPre(rest)
	if !ok {
		return v, false
	}
	rest, ok = v.readPost(rest)
	if !ok {
		return v, false
	}
	rest, ok = v.readDev(rest)
	if !ok || rest != "" {
		return v, false
	}
	return v, true
}

// sepNum reads an optional separator (., -, _) followed by an optional
// number; a missing number is 0.
func sepNum(s string) (int, string) {
	s = strings.TrimLeft(s, ".-_")
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, s
	}
	n, _ := strconv.Atoi(s[:j])
	return n, s[j:]
}

func (v *version) readPre(s string) (string, bool) {
	t := strings.TrimLeft(s, ".-_")
	for _, sp := range []struct{ word, phase string }{
		{"alpha", "a"}, {"beta", "b"}, {"preview", "rc"}, {"pre", "rc"}, {"rc", "rc"}, {"c", "rc"}, {"a", "a"}, {"b", "b"},
	} {
		if strings.HasPrefix(t, sp.word) {
			v.pre = sp.phase
			v.preN, t = sepNum(t[len(sp.word):])
			return t, true
		}
	}
	return s, true
}

func (v *version) readPost(s string) (string, bool) {
	t := strings.TrimLeft(s, "._")
	for _, word := range []string{"post", "rev", "r"} {
		if strings.HasPrefix(t, word) {
			v.post, t = sepNum(t[len(word):])
			return t, true
		}
	}
	// The implicit post-release: "1.0-1".
	if strings.HasPrefix(s, "-") && len(s) > 1 && s[1] >= '0' && s[1] <= '9' {
		v.post, t = sepNum(s)
		return t, true
	}
	return s, true
}

func (v *version) readDev(s string) (string, bool) {
	t := strings.TrimLeft(s, ".-_")
	if strings.HasPrefix(t, "dev") {
		v.dev, t = sepNum(t[3:])
		return t, true
	}
	return s, true
}

// String is the canonical (normalised) spelling.
func (v version) String() string {
	var b strings.Builder
	if v.epoch > 0 {
		fmt.Fprintf(&b, "%d!", v.epoch)
	}
	for i, n := range v.release {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n))
	}
	if v.pre != "" {
		fmt.Fprintf(&b, "%s%d", v.pre, v.preN)
	}
	if v.post >= 0 {
		fmt.Fprintf(&b, ".post%d", v.post)
	}
	if v.dev >= 0 {
		fmt.Fprintf(&b, ".dev%d", v.dev)
	}
	if len(v.local) > 0 {
		b.WriteByte('+')
		for i, p := range v.local {
			if i > 0 {
				b.WriteByte('.')
			}
			if p.isNum {
				b.WriteString(strconv.Itoa(p.num))
			} else {
				b.WriteString(p.str)
			}
		}
	}
	return b.String()
}

// releaseCmp orders two release segments with trailing zeros ignored.
func releaseCmp(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// compare orders two parsed versions per the specification.
func compare(a, b version) int {
	if a.epoch != b.epoch {
		return cmpInt(a.epoch, b.epoch)
	}
	if c := releaseCmp(a.release, b.release); c != 0 {
		return c
	}
	// Pre-release key: dev-only releases sort below every pre-release;
	// a final release sorts above every pre-release; a post-only release
	// is a final release for this key.
	if c := cmpInt(preRank(a), preRank(b)); c != 0 {
		return c
	}
	if a.pre != "" && a.pre == b.pre && a.preN != b.preN {
		return cmpInt(a.preN, b.preN)
	}
	// Post: absent sorts below any post.
	if c := cmpInt(a.post, b.post); c != 0 {
		return c
	}
	// Dev: absent sorts above any dev.
	if a.dev != b.dev {
		switch {
		case a.dev < 0:
			return 1
		case b.dev < 0:
			return -1
		default:
			return cmpInt(a.dev, b.dev)
		}
	}
	return localCmp(a.local, b.local)
}

// preRank places a version's pre-release phase on one axis: dev-only
// below alpha, alpha < beta < rc < final.
func preRank(v version) int {
	switch v.pre {
	case "a":
		return 1
	case "b":
		return 2
	case "rc":
		return 3
	}
	if v.dev >= 0 && v.post < 0 {
		return 0
	}
	return 4
}

func localCmp(a, b []localPart) int {
	// A version without a local segment sorts below the same version with
	// one.
	if len(a) == 0 || len(b) == 0 {
		return cmpInt(len(a), len(b))
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		switch {
		case x.isNum && y.isNum:
			if c := cmpInt(x.num, y.num); c != 0 {
				return c
			}
		case x.isNum != y.isNum:
			// Numeric parts sort above alphanumeric ones.
			if x.isNum {
				return 1
			}
			return -1
		default:
			if c := strings.Compare(x.str, y.str); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// clause is one specifier of a set.
type clause struct {
	op   string
	raw  string // the version text as written, for wildcard and === clauses
	v    version
	wild bool // "==1.2.*" / "!=1.2.*"
}

func parseClauses(s string) ([]clause, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	var out []clause
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		var c clause
		for _, op := range []string{"===", "==", "!=", "<=", ">=", "~=", "<", ">"} {
			if strings.HasPrefix(part, op) {
				c.op = op
				part = strings.TrimSpace(part[len(op):])
				break
			}
		}
		if c.op == "" || part == "" {
			return nil, false
		}
		c.raw = part
		if c.op == "===" {
			out = append(out, c)
			continue
		}
		if strings.HasSuffix(part, ".*") {
			if c.op != "==" && c.op != "!=" {
				return nil, false
			}
			c.wild = true
			part = strings.TrimSuffix(part, ".*")
		}
		v, ok := parse(part)
		if !ok {
			return nil, false
		}
		if c.op == "~=" && len(v.release) < 2 {
			return nil, false
		}
		c.v = v
		out = append(out, c)
	}
	return out, true
}

// matches reports whether v satisfies one clause.
func (c clause) matches(v version, raw string) bool {
	switch c.op {
	case "===":
		return strings.TrimSpace(raw) == c.raw
	case "==", "!=":
		var eq bool
		if c.wild {
			eq = prefixMatch(v, c.v)
		} else {
			eq = compare(v, c.v) == 0
		}
		if c.op == "!=" {
			return !eq
		}
		return eq
	case "<=":
		return compare(v, c.v) <= 0
	case ">=":
		return compare(v, c.v) >= 0
	case "<":
		// An exclusive upper bound does not admit pre-releases of the bound
		// itself.
		if compare(v, c.v) >= 0 {
			return false
		}
		if v.pre != "" && c.v.pre == "" && releaseCmp(v.release, c.v.release) == 0 {
			return false
		}
		return true
	case ">":
		// An exclusive lower bound does not admit post-releases of the bound
		// itself.
		if compare(v, c.v) <= 0 {
			return false
		}
		if v.post >= 0 && c.v.post < 0 && releaseCmp(v.release, c.v.release) == 0 && v.pre == c.v.pre && v.preN == c.v.preN {
			return false
		}
		return true
	case "~=":
		if compare(v, c.v) < 0 {
			return false
		}
		prefix := version{epoch: c.v.epoch, release: c.v.release[:len(c.v.release)-1]}
		return prefixMatch(v, prefix)
	}
	return false
}

// prefixMatch reports whether v's release starts with p's release (trailing
// zeros of p are significant here: "==1.0.*" matches 1.0.x, not 1.1).
func prefixMatch(v, p version) bool {
	if v.epoch != p.epoch {
		return false
	}
	rel := v.release
	for len(rel) < len(p.release) {
		rel = append(rel, 0)
	}
	for i, n := range p.release {
		if rel[i] != n {
			return false
		}
	}
	return true
}

// IsValid accepts a version or a specifier set.
func (s *Scheme) IsValid(v string) bool {
	if s.IsVersion(v) {
		return true
	}
	_, ok := parseClauses(v)
	return ok
}

func (*Scheme) IsVersion(v string) bool {
	_, ok := parse(v)
	return ok
}

// IsStable is false for pre-releases and dev releases; a post-release of a
// final version is stable.
func (*Scheme) IsStable(s string) bool {
	v, ok := parse(s)
	return ok && v.pre == "" && v.dev < 0
}

func at(s string, i int) (int, bool) {
	v, ok := parse(s)
	if !ok || i >= len(v.release) {
		return 0, false
	}
	return v.release[i], true
}

func (*Scheme) Major(s string) (int, bool) { return at(s, 0) }
func (*Scheme) Minor(s string) (int, bool) { return at(s, 1) }
func (*Scheme) Patch(s string) (int, bool) { return at(s, 2) }

func (*Scheme) Compare(a, b string) int {
	x, okA := parse(a)
	y, okB := parse(b)
	if !okA || !okB {
		return 0
	}
	return compare(x, y)
}

// Equal is equality under normalisation: "1.0" and "1.0.0" are the same
// version, and so are "1.0rc1" and "1.0.0-RC1".
func (*Scheme) Equal(a, b string) bool {
	x, okA := parse(a)
	y, okB := parse(b)
	return okA && okB && compare(x, y) == 0
}

// Satisfies reports whether version falls inside rng, which may itself be
// a plain version (exact match).
func (*Scheme) Satisfies(ver, rng string) bool {
	v, ok := parse(ver)
	if !ok {
		return false
	}
	if r, ok := parse(rng); ok {
		return compare(v, r) == 0
	}
	clauses, ok := parseClauses(rng)
	if !ok {
		return false
	}
	for _, c := range clauses {
		if !c.matches(v, ver) {
			return false
		}
	}
	return true
}

// NewValue rewrites current so it admits target. A plain version becomes
// the target as spelled; a single-clause range keeps its operator and
// moves its version, with a wildcard or compatible-release clause keeping
// its precision. A compound range is declined: which bound to move is a
// policy this package does not invent.
func (*Scheme) NewValue(current, target string, strategy versioning.RangeStrategy) (string, error) {
	t, ok := parse(target)
	if !ok {
		return "", fmt.Errorf("pep440: target %q is not a version", target)
	}
	if _, ok := parse(current); ok {
		return target, nil
	}
	clauses, ok := parseClauses(current)
	if !ok {
		return "", fmt.Errorf("pep440: current %q is neither a version nor a specifier set", current)
	}
	if strategy == versioning.StrategyPin {
		return "==" + target, nil
	}
	if len(clauses) != 1 {
		return "", fmt.Errorf("pep440: %q is a compound specifier set; rewriting it is not supported", current)
	}
	c := clauses[0]
	switch c.op {
	case "===":
		return "===" + target, nil
	case "~=", "==", "!=":
		if c.wild {
			return c.op + truncate(t, len(c.v.release)) + ".*", nil
		}
		if c.op == "~=" {
			return c.op + truncate(t, len(c.v.release)), nil
		}
		if c.op == "!=" {
			return current, nil
		}
		return "==" + target, nil
	case ">=", ">":
		return c.op + target, nil
	case "<=", "<":
		// An upper bound that the target already passes is left alone, and
		// one it violates is not something to widen blindly.
		if c.matches(t, target) {
			return current, nil
		}
		return "", fmt.Errorf("pep440: %q excludes %s; moving an upper bound is not supported", current, target)
	}
	return "", fmt.Errorf("pep440: %q cannot be rewritten", current)
}

// truncate spells v's release at n components, padding with zeros.
func truncate(v version, n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		x := 0
		if i < len(v.release) {
			x = v.release[i]
		}
		parts = append(parts, strconv.Itoa(x))
	}
	return strings.Join(parts, ".")
}
