// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package loose implements Renovate's `loose` scheme: accept almost anything
// that starts with a number, compare it by its numeric parts.
//
// The measured surprise is stability. `1.0.0-alpha` is STABLE here. loose has
// no notion of a prerelease, so a suffix is just text after the numbers, and
// nothing in it makes a version unstable. A scheme that treated it otherwise
// would hold back releases this one lets through.
package loose

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "loose" }

// split reads a value into its leading numeric components and whatever text
// follows them.
//
// The suffix is not decoration: measured, "1.0.0" is greater than
// "1.0.0-alpha" even though loose calls both stable. Stability and ordering
// are separate questions here, and only the second one looks at the suffix.
func split(s string) ([]int, string, bool) {
	nums, ok := parts(s)
	if !ok {
		return nil, "", false
	}
	body := strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	// Walk past exactly the numeric components that parts() accepted.
	i, consumed := 0, 0
	for consumed < len(nums) && i < len(body) {
		j := i
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		if j == i {
			break
		}
		consumed++
		i = j
		if i < len(body) && body[i] == '.' && consumed < len(nums) {
			i++
		}
	}
	return nums, body[i:], true
}

// parts splits a value into its leading numeric components. Everything from
// the first non-numeric segment onwards is suffix and does not order.
func parts(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return nil, false
	}
	// Cut at the first character that cannot begin a numeric segment.
	var nums []int
	for _, seg := range strings.Split(s, ".") {
		// A segment like "3-rc1" contributes its numeric head, then stops the
		// walk: what follows is not a version component.
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
		nums = append(nums, n)
		if i != len(seg) {
			break
		}
	}
	if len(nums) == 0 {
		return nil, false
	}
	return nums, true
}

// IsVersion is IsValid: this scheme has no range form.
func (v *Scheme) IsVersion(s string) bool { return v.IsValid(s) }

func (*Scheme) IsValid(s string) bool {
	_, ok := parts(s)
	return ok
}

// IsStable is true for anything valid: loose has no prerelease concept.
func (s *Scheme) IsStable(v string) bool { return s.IsValid(v) }

func at(s string, i int) (int, bool) {
	p, ok := parts(s)
	if !ok || i >= len(p) {
		return 0, false
	}
	return p[i], true
}

func (*Scheme) Major(s string) (int, bool) { return at(s, 0) }
func (*Scheme) Minor(s string) (int, bool) { return at(s, 1) }
func (*Scheme) Patch(s string) (int, bool) { return at(s, 2) }

// Compare orders by numeric parts, then by how many there are, then by
// suffix.
//
// Two measured rules that a naive "pad with zeros" implementation gets wrong:
// "1.0.0" is GREATER than "1", so a longer version wins a shared prefix rather
// than comparing equal; and "1.0.0" is greater than "1.0.0-alpha", so a
// suffix ranks below no suffix at all.
func (*Scheme) Compare(a, b string) int {
	pa, sa, okA := split(a)
	pb, sb, okB := split(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	if len(pa) != len(pb) {
		if len(pa) < len(pb) {
			return -1
		}
		return 1
	}
	switch {
	case sa == sb:
		return 0
	case sa == "":
		return 1
	case sb == "":
		return -1
	default:
		return strings.Compare(sa, sb)
	}
}

// Equal is exact string equality, which is NOT Compare(a,b) == 0. loose calls
// "1" and "1.0.0" different versions while still ordering one above the other.
// Folding the two together would make a bare-major pin look already-current
// against a three-part release.
func (*Scheme) Equal(a, b string) bool { return a == b }

// Satisfies is exact equality. loose has no range grammar at all: measured,
// "1.33.59" does not satisfy "1", and neither does "1.0.0".
//
// That is worth knowing before relying on it. The estate pins CI components at
// a bare major and gives some of them `versioning: loose`, which reads like a
// range and is not one - "1" matches the literal string "1" and nothing else.
// Whether that is what those rules intend is a question for the config, not
// for this package.
func (*Scheme) Satisfies(version, rng string) bool { return version == rng }

func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	if !s.IsValid(target) {
		return "", fmt.Errorf("loose: target %q has no version in it", target)
	}
	return target, nil
}
