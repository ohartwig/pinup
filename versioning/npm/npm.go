// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package npm implements Renovate's `npm` scheme: node-semver versions and
// ranges.
//
// Measured oddities worth knowing:
//
//   - The EMPTY STRING is valid. It is the range "*", which accepts anything.
//   - "1.2" and "1" are valid but NOT stable: in npm they are ranges, not
//     versions, so they name no release.
//   - "1.2.3.4" is invalid, unlike in go and docker.
//   - Component accessors throw for a range, so they report nothing rather
//     than guessing.
package npm

import (
	"fmt"
	"strconv"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/semverx"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "npm" }

// exact reports whether a value is a plain version rather than a range.
func exact(s string) (semverx.Version, bool) {
	return semverx.Parse(strings.TrimSpace(s))
}

// comparator is one bound in a range.
type comparator struct {
	op     string
	v      semverx.Version
	stated int
	anyVer bool // "*", "x", "" - matches everything
}

func parseRange(s string) ([][]comparator, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return [][]comparator{{{anyVer: true}}}, true
	}
	var out [][]comparator
	for _, alt := range strings.Split(s, "||") {
		var group []comparator
		fields := strings.Fields(alt)
		for i := 0; i < len(fields); i++ {
			// Hyphen range: "1.2.3 - 2.3.4".
			if fields[i] == "-" && i > 0 && i+1 < len(fields) {
				continue
			}
			c, ok := parseComparator(fields[i])
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

func parseComparator(tok string) (comparator, bool) {
	var c comparator
	tok = strings.TrimSpace(tok)
	if tok == "" || tok == "*" || tok == "x" || tok == "X" {
		c.anyVer = true
		return c, true
	}
	for _, op := range []string{">=", "<=", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(tok, op) {
			c.op = op
			tok = strings.TrimSpace(tok[len(op):])
			break
		}
	}
	tok = strings.TrimPrefix(strings.TrimPrefix(tok, "v"), "V")

	// An x-range: 1.x, 1.2.x, 1.*
	segs := strings.Split(tok, ".")
	nums := make([]int, 0, 3)
	for _, seg := range segs {
		if seg == "x" || seg == "X" || seg == "*" {
			break
		}
		n, err := strconv.Atoi(seg)
		if err != nil {
			return comparator{}, false
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		return comparator{}, false
	}
	if len(nums) > 3 {
		return comparator{}, false
	}
	c.stated = len(nums)
	c.v = semverx.Version{Major: nums[0]}
	if len(nums) > 1 {
		c.v.Minor = nums[1]
	}
	if len(nums) > 2 {
		c.v.Patch = nums[2]
	}
	// A prerelease attached to a bound, e.g. >=1.0.0-rc.1
	if i := strings.IndexByte(tok, '-'); i >= 0 {
		if full, ok := semverx.Parse(tok); ok {
			c.v = full
		}
	}
	return c, true
}

// IsVersion accepts a single exact version; a range is valid but not one.
func (*Scheme) IsVersion(s string) bool {
	_, ok := exact(s)
	return ok
}

func (*Scheme) IsValid(s string) bool {
	if _, ok := exact(s); ok {
		return true
	}
	_, ok := parseRange(s)
	return ok
}

// IsStable is true only for a plain release version. "1.2" is a range in npm,
// and a range names no release.
// IsCompatible is measured as "the candidate is a single version": a range
// is a valid value but never a release that could follow anything.
func (*Scheme) IsCompatible(candidate, _ string) bool {
	_, ok := exact(candidate)
	return ok
}

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

func satisfiesOne(v semverx.Version, c comparator) bool {
	if c.anyVer {
		return len(v.Pre) == 0
	}
	if len(v.Pre) > 0 && len(c.v.Pre) == 0 {
		return false
	}
	cmp := semverx.CompareVersions(v, c.v)
	switch c.op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "^":
		if cmp < 0 {
			return false
		}
		switch {
		case c.v.Major != 0:
			return v.Major == c.v.Major
		case c.v.Minor != 0:
			return v.Major == 0 && v.Minor == c.v.Minor
		default:
			return v.Major == 0 && v.Minor == 0 && v.Patch == c.v.Patch
		}
	case "~":
		if cmp < 0 {
			return false
		}
		if c.stated >= 2 {
			return v.Major == c.v.Major && v.Minor == c.v.Minor
		}
		return v.Major == c.v.Major
	default:
		// A bare partial is an x-range: "1" means 1.x.x.
		for i, want := range []int{c.v.Major, c.v.Minor, c.v.Patch} {
			if i >= c.stated {
				return true
			}
			got := []int{v.Major, v.Minor, v.Patch}[i]
			if got != want {
				return false
			}
		}
		return true
	}
}

// NewValue preserves the shape of a range: a caret stays a caret. This is the
// scheme that does range-preserving rewriting, which is why the plain semver
// scheme does not have to.
func (s *Scheme) NewValue(current, target string, strategy versioning.RangeStrategy) (string, error) {
	t, ok := exact(target)
	if !ok {
		return "", fmt.Errorf("npm: target %q is not a version", target)
	}
	if strategy == versioning.StrategyPin {
		return t.String(), nil
	}
	if _, isExact := exact(current); isExact {
		return t.String(), nil
	}
	groups, ok := parseRange(current)
	if !ok || len(groups) == 0 || len(groups[0]) == 0 {
		return t.String(), nil
	}
	c := groups[0][0]
	if c.anyVer {
		return current, nil
	}
	parts := []string{strconv.Itoa(t.Major), strconv.Itoa(t.Minor), strconv.Itoa(t.Patch)}
	n := c.stated
	if n == 0 || n > 3 {
		n = 3
	}
	out := c.op + strings.Join(parts[:n], ".")
	if strategy == versioning.StrategyWiden {
		return current + " || " + out, nil
	}
	return out, nil
}
