// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package docker implements Renovate's `docker` scheme: a tag is leading
// numbers followed by a compatibility suffix.
//
// The suffix rule is the thing to understand, and it is not what it looks
// like. When the numeric parts are equal, suffixes order in REVERSE
// lexicographic order - measured across all 18 such pairs in the captured
// table, with no exceptions:
//
//	"1.0.0"         > "1.0.0-alpha"    (an empty suffix beats any suffix)
//	"22-alpine3.20" > "22-alpine3.21"  (the earlier string is the greater tag)
//	"22-alpine3.21" > "22-bookworm"
//
// So it is a total order, just an inverted one. Following it would mean
// proposing alpine3.21 -> alpine3.20 as an upgrade, and crossing from bookworm
// to alpine without being asked. That is why a change of suffix is classified
// as UpdateCompatibility rather than as a version move, and why a change of
// FAMILY - the alphabetic head of the suffix - is not offered at all.
//
// Like loose, this scheme has no concept of a prerelease: "1.0.0-alpha" is
// stable, because "-alpha" is just a compatibility suffix here.
package docker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "docker" }

type tag struct {
	nums   []int
	suffix string
}

func parse(s string) (tag, bool) {
	var t tag
	body := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	if body == "" {
		return t, false
	}
	i := 0
	for {
		j := i
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		if j == i {
			break
		}
		n, err := strconv.Atoi(body[i:j])
		if err != nil {
			break
		}
		t.nums = append(t.nums, n)
		i = j
		if i < len(body) && body[i] == '.' {
			// Only continue if a digit follows; "1.x" ends the numeric run.
			if i+1 < len(body) && body[i+1] >= '0' && body[i+1] <= '9' {
				i++
				continue
			}
		}
		break
	}
	if len(t.nums) == 0 {
		return tag{}, false
	}
	t.suffix = body[i:]
	return t, true
}

// IsVersion is IsValid: this scheme has no range form.
func (v *Scheme) IsVersion(s string) bool { return v.IsValid(s) }

func (*Scheme) IsValid(s string) bool {
	_, ok := parse(s)
	return ok
}

// IsStable is true for every valid tag. A suffix is compatibility information,
// not a prerelease marker.
func (s *Scheme) IsStable(v string) bool { return s.IsValid(v) }

func at(s string, i int) (int, bool) {
	t, ok := parse(s)
	if !ok || i >= len(t.nums) {
		return 0, false
	}
	return t.nums[i], true
}

func (*Scheme) Major(s string) (int, bool) { return at(s, 0) }
func (*Scheme) Minor(s string) (int, bool) { return at(s, 1) }
func (*Scheme) Patch(s string) (int, bool) { return at(s, 2) }

func (*Scheme) Compare(a, b string) int {
	ta, okA := parse(a)
	tb, okB := parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	// Compare the components they share.
	for i := 0; i < len(ta.nums) && i < len(tb.nums); i++ {
		if ta.nums[i] != tb.nums[i] {
			if ta.nums[i] < tb.nums[i] {
				return -1
			}
			return 1
		}
	}
	// Shared prefix equal: FEWER components is greater. Measured - "1" beats
	// "1.0.0", and "1.2" beats "1.2.3.4". Note this is the opposite of the
	// loose scheme, where a longer version wins the same comparison. The two
	// look alike and are not.
	if len(ta.nums) != len(tb.nums) {
		if len(ta.nums) > len(tb.nums) {
			return -1
		}
		return 1
	}
	// Same components: the suffix decides, in reverse lexicographic order.
	return -strings.Compare(ta.suffix, tb.suffix)
}

func (s *Scheme) Equal(a, b string) bool {
	if !s.IsValid(a) || !s.IsValid(b) {
		return a == b
	}
	return s.Compare(a, b) == 0
}

func (s *Scheme) Satisfies(version, rng string) bool {
	tv, okV := parse(version)
	tr, okR := parse(rng)
	if !okV || !okR {
		return false
	}
	if len(tr.nums) > len(tv.nums) {
		return false
	}
	for i := range tr.nums {
		if tv.nums[i] != tr.nums[i] {
			return false
		}
	}
	return tv.suffix == tr.suffix
}

// IsCompatible is the measured rule: same number of numeric components and
// the same suffix, byte for byte. So "22-alpine3.21" may not follow
// "22-alpine3.20", and "1.2" may not follow "1.0.0" - the first is a different
// base image, the second a different precision, and Renovate offers neither.
// A compatibility move is a distinct, opt-in update type, not a candidate.
func (*Scheme) IsCompatible(candidate, current string) bool {
	tc, okC := parse(candidate)
	tr, okR := parse(current)
	if !okC || !okR {
		return false
	}
	return len(tc.nums) == len(tr.nums) && tc.suffix == tr.suffix
}

// Family is the alphabetic head of a tag's compatibility suffix: "alpine" for
// "22-alpine3.21", "" for "22". Crossing families is never an update.
func Family(s string) string {
	t, ok := parse(s)
	if !ok {
		return ""
	}
	suf := strings.TrimLeft(t.suffix, "-_.")
	i := 0
	for i < len(suf) && (suf[i] >= 'a' && suf[i] <= 'z' || suf[i] >= 'A' && suf[i] <= 'Z') {
		i++
	}
	return suf[:i]
}

// UpdateType classifies a docker tag move.
//
// Three cases the component-based default cannot express:
//
//   - A different compatibility family is not an update at all. alpine to
//     bookworm changes the base operating system; nothing about the tags says
//     it is safe, and the reverse-lexicographic order would happily propose it.
//   - Same version, different suffix within one family is
//     UpdateCompatibility - a distinct, opt-in type rather than a version move.
//   - Otherwise the numeric parts decide, using the generic classification.
func (s *Scheme) UpdateType(from, to string) model.UpdateType {
	if from == to {
		return model.UpdateUnknown
	}
	tf, okF := parse(from)
	tt, okT := parse(to)
	if !okF || !okT {
		return model.UpdateUnknown
	}
	if Family(from) != Family(to) {
		return model.UpdateUnknown
	}

	sameNumbers := len(tf.nums) == len(tt.nums)
	if sameNumbers {
		for i := range tf.nums {
			if tf.nums[i] != tt.nums[i] {
				sameNumbers = false
				break
			}
		}
	}
	if sameNumbers {
		if tf.suffix == tt.suffix {
			return model.UpdateUnknown
		}
		return model.UpdateCompatibility
	}

	// Compare the numeric parts alone, so the inverted suffix order cannot
	// turn a version bump into a rollback.
	switch {
	case numsLess(tt.nums, tf.nums):
		return model.UpdateRollback
	case len(tf.nums) > 0 && len(tt.nums) > 0 && tf.nums[0] != tt.nums[0]:
		return model.UpdateMajor
	case len(tf.nums) > 1 && len(tt.nums) > 1 && tf.nums[1] != tt.nums[1]:
		return model.UpdateMinor
	case len(tf.nums) > 2 && len(tt.nums) > 2 && tf.nums[2] != tt.nums[2]:
		return model.UpdatePatch
	default:
		return model.UpdateMinor
	}
}

func numsLess(a, b []int) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		va, vb := 0, 0
		if i < len(a) {
			va = a[i]
		}
		if i < len(b) {
			vb = b[i]
		}
		if va != vb {
			return va < vb
		}
	}
	return false
}

// NewValue keeps the compatibility suffix. Writing "3.21" where the file said
// "22-alpine3.21" would change the image, not update it.
func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	tt, ok := parse(target)
	if !ok {
		return "", fmt.Errorf("docker: target %q is not a tag", target)
	}
	tc, ok := parse(current)
	if !ok || tc.suffix == "" || tt.suffix != "" {
		return target, nil
	}
	return target + tc.suffix, nil
}
