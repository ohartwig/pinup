// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package coerced implements Renovate's `semver-coerced` scheme: read whatever
// leading numbers are there and make a three-part version out of them.
//
// Measured: "1.2" becomes 1.2.0 and "1" becomes 1.0.0, both stable. "1.2.3.4"
// is valid with major 1 and minor 2 but is NOT stable - the extra component is
// treated as something after the version rather than part of it.
package coerced

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "semver-coerced" }

type coerced struct {
	major, minor, patch int
	// extra is true when the input carried more than three numeric components
	// or a suffix, either of which costs it stability.
	extra bool
}

func parse(s string) (coerced, bool) {
	var c coerced
	body := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	if body == "" {
		return c, false
	}
	segs := strings.Split(body, ".")
	nums := make([]int, 0, 3)
	for _, seg := range segs {
		i := 0
		for i < len(seg) && seg[i] >= '0' && seg[i] <= '9' {
			i++
		}
		if i == 0 {
			break
		}
		n, err := strconv.Atoi(seg[:i])
		if err != nil {
			break
		}
		if len(nums) < 3 {
			nums = append(nums, n)
		} else {
			c.extra = true
		}
		if i != len(seg) {
			c.extra = true
			break
		}
	}
	if len(nums) == 0 {
		return coerced{}, false
	}
	for len(nums) < 3 {
		nums = append(nums, 0)
	}
	c.major, c.minor, c.patch = nums[0], nums[1], nums[2]
	return c, true
}

// IsVersion is IsValid: this scheme has no range form.
func (v *Scheme) IsVersion(s string) bool { return v.IsValid(s) }

func (*Scheme) IsValid(s string) bool {
	_, ok := parse(s)
	return ok
}

func (*Scheme) IsStable(s string) bool {
	c, ok := parse(s)
	return ok && !c.extra
}

func (*Scheme) Major(s string) (int, bool) { c, ok := parse(s); return c.major, ok }
func (*Scheme) Minor(s string) (int, bool) { c, ok := parse(s); return c.minor, ok }
func (*Scheme) Patch(s string) (int, bool) { c, ok := parse(s); return c.patch, ok }

func (*Scheme) Compare(a, b string) int {
	ca, okA := parse(a)
	cb, okB := parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for _, p := range [][2]int{{ca.major, cb.major}, {ca.minor, cb.minor}, {ca.patch, cb.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (s *Scheme) Equal(a, b string) bool {
	if !s.IsValid(a) || !s.IsValid(b) {
		return a == b
	}
	return s.Compare(a, b) == 0
}

func (s *Scheme) Satisfies(version, rng string) bool {
	if !s.IsValid(version) || !s.IsValid(rng) {
		return false
	}
	return s.Compare(version, rng) == 0
}

func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	if !s.IsValid(target) {
		return "", fmt.Errorf("semver-coerced: target %q has no version in it", target)
	}
	return target, nil
}
