// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package hashicorp implements Renovate's `hashicorp` scheme: Terraform
// version constraints.
//
// 22 corpus vectors use it, all terraform provider requirements. The syntax is
// comma-separated AND constraints with Terraform's pessimistic operator:
//
//	">= 1.0, < 2.0"
//	"~> 3.1"          allows 3.x, not 4.0
//
// Measured: a partial like "1.2" or "1" is valid but NOT stable - it is a
// constraint, and a constraint names no release. "1.2.3.4" is invalid, and so
// is the empty string, which npm accepts.
package hashicorp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/semverx"
	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "hashicorp" }

func exact(s string) (semverx.Version, bool) {
	return semverx.Parse(strings.TrimSpace(s))
}

type constraint struct {
	op     string // "", "=", "!=", ">", ">=", "<", "<=", "~>"
	nums   []int
	pre    []string
	stated int
}

func parseConstraints(s string) ([]constraint, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	var out []constraint
	for _, part := range strings.Split(s, ",") {
		c, ok := parseOne(part)
		if !ok {
			return nil, false
		}
		out = append(out, c)
	}
	return out, true
}

func parseOne(tok string) (constraint, bool) {
	var c constraint
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return c, false
	}
	for _, op := range []string{"~>", ">=", "<=", "!=", ">", "<", "="} {
		if strings.HasPrefix(tok, op) {
			c.op = op
			tok = strings.TrimSpace(tok[len(op):])
			break
		}
	}
	tok = strings.TrimPrefix(strings.TrimPrefix(tok, "v"), "V")
	if tok == "" {
		return constraint{}, false
	}
	if i := strings.IndexByte(tok, '-'); i >= 0 {
		c.pre = strings.Split(tok[i+1:], ".")
		tok = tok[:i]
	}
	segs := strings.Split(tok, ".")
	if len(segs) > 3 {
		return constraint{}, false
	}
	for _, seg := range segs {
		n, err := strconv.Atoi(seg)
		if err != nil || seg == "" {
			return constraint{}, false
		}
		c.nums = append(c.nums, n)
	}
	c.stated = len(c.nums)
	return c, true
}

func (c constraint) at(i int) int {
	if i < len(c.nums) {
		return c.nums[i]
	}
	return 0
}

func (c constraint) version() semverx.Version {
	return semverx.Version{Major: c.at(0), Minor: c.at(1), Patch: c.at(2), Pre: c.pre}
}

// IsCompatible is measured as "the candidate is a single version"; a
// constraint is a valid value but never a release.
func (*Scheme) IsCompatible(candidate, _ string) bool {
	_, ok := exact(candidate)
	return ok
}

// IsVersion accepts a single exact version; a constraint is valid but not one.
func (*Scheme) IsVersion(s string) bool {
	_, ok := exact(s)
	return ok
}

func (*Scheme) IsValid(s string) bool {
	if _, ok := exact(s); ok {
		return true
	}
	_, ok := parseConstraints(s)
	return ok
}

// IsStable is true only for a complete release version. A constraint - which
// is what "1.2" and "1" are here - names no release.
func (*Scheme) IsStable(s string) bool {
	v, ok := exact(s)
	return ok && len(v.Pre) == 0
}

func component(s string, pick func(semverx.Version) int) (int, bool) {
	v, ok := exact(s)
	if !ok {
		return 0, false
	}
	return pick(v), true
}

func (*Scheme) Major(s string) (int, bool) {
	return component(s, func(v semverx.Version) int { return v.Major })
}
func (*Scheme) Minor(s string) (int, bool) {
	return component(s, func(v semverx.Version) int { return v.Minor })
}
func (*Scheme) Patch(s string) (int, bool) {
	return component(s, func(v semverx.Version) int { return v.Patch })
}

func (*Scheme) Compare(a, b string) int {
	va, okA := exact(a)
	vb, okB := exact(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	return semverx.CompareVersions(va, vb)
}

func (s *Scheme) Equal(a, b string) bool {
	va, okA := exact(a)
	vb, okB := exact(b)
	if !okA || !okB {
		return a == b
	}
	return semverx.CompareVersions(va, vb) == 0
}

func (s *Scheme) Satisfies(ver, rng string) bool {
	v, ok := exact(ver)
	if !ok {
		return false
	}
	cs, ok := parseConstraints(rng)
	if !ok {
		return false
	}
	for _, c := range cs {
		if !satisfiesOne(v, c) {
			return false
		}
	}
	return true
}

func satisfiesOne(v semverx.Version, c constraint) bool {
	if len(v.Pre) > 0 && len(c.pre) == 0 {
		return false
	}
	cmp := semverx.CompareVersions(v, c.version())
	switch c.op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "!=":
		return cmp != 0
	case "~>":
		// Terraform's pessimistic operator: the last stated component may
		// move, everything above it is pinned. "~> 3.1" allows 3.2 but not
		// 4.0; "~> 3.1.4" allows 3.1.5 but not 3.2.0.
		if cmp < 0 {
			return false
		}
		for i := 0; i < c.stated-1; i++ {
			if componentOf(v, i) != c.at(i) {
				return false
			}
		}
		return true
	default:
		// A bare partial pins the components it states.
		for i := 0; i < c.stated; i++ {
			if componentOf(v, i) != c.at(i) {
				return false
			}
		}
		return true
	}
}

func componentOf(v semverx.Version, i int) int {
	switch i {
	case 0:
		return v.Major
	case 1:
		return v.Minor
	default:
		return v.Patch
	}
}

// NewValue keeps the operator and the number of stated components, which is
// what makes "~> 3.1" stay a minor-level constraint rather than becoming an
// exact pin.
func (s *Scheme) NewValue(current, target string, strategy versioning.RangeStrategy) (string, error) {
	t, ok := exact(target)
	if !ok {
		return "", fmt.Errorf("hashicorp: target %q is not a version", target)
	}
	if strategy == versioning.StrategyPin {
		return t.String(), nil
	}
	if _, isExact := exact(current); isExact {
		return t.String(), nil
	}
	cs, ok := parseConstraints(current)
	if !ok || len(cs) == 0 {
		return t.String(), nil
	}
	c := cs[0]
	parts := []string{strconv.Itoa(t.Major), strconv.Itoa(t.Minor), strconv.Itoa(t.Patch)}
	n := c.stated
	if n == 0 || n > 3 {
		n = 3
	}
	sep := ""
	if c.op != "" {
		sep = " "
	}
	return strings.TrimSpace(c.op + sep + strings.Join(parts[:n], ".")), nil
}
