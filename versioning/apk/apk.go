// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package apk implements Renovate's `apk` scheme: Alpine and Wolfi package
// versions, which are numeric components plus a package revision.
//
// The revision is the whole point of this scheme and the reason the estate
// needs it. Wolfi rebuilds a package without changing its upstream version and
// bumps `-rN`; a scheme that ignored the suffix would see no update and let a
// rebuilt-with-a-CVE-fix package sit. That is precisely what Repology could not
// express, and why the Node sidecar exists.
//
// Measured:
//
//	"1.2.3-r5" > "1.2.3-r4" > "1.2.3"   an absent revision is r0
//	"1.0.0-alpha" is INVALID            -rN is the only suffix allowed
//	"1.2", "1" and "1.2.3.4" are valid  components are read as far as they go
package apk

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "apk" }

type version struct {
	nums []int
	rev  int
}

func parse(s string) (version, bool) {
	var v version
	body := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	if body == "" {
		return v, false
	}
	// An -rN revision, and nothing else, may follow the numbers. Any other
	// suffix makes the whole value invalid rather than being ignored - that is
	// what separates this scheme from docker and loose.
	if i := strings.LastIndex(body, "-"); i >= 0 {
		rev := body[i+1:]
		if len(rev) < 2 || rev[0] != 'r' {
			return version{}, false
		}
		n, err := strconv.Atoi(rev[1:])
		if err != nil || n < 0 {
			return version{}, false
		}
		v.rev = n
		body = body[:i]
	}
	if body == "" {
		return version{}, false
	}
	for _, seg := range strings.Split(body, ".") {
		n, err := strconv.Atoi(seg)
		if err != nil || seg == "" {
			return version{}, false
		}
		v.nums = append(v.nums, n)
	}
	return v, true
}

// IsVersion is IsValid: this scheme has no range form.
func (v *Scheme) IsVersion(s string) bool { return v.IsValid(s) }

func (*Scheme) IsValid(s string) bool {
	_, ok := parse(s)
	return ok
}

// IsStable is true for every valid version: an apk version has no prerelease
// concept, only a revision.
func (s *Scheme) IsStable(v string) bool { return s.IsValid(v) }

func at(s string, i int) (int, bool) {
	v, ok := parse(s)
	if !ok || i >= len(v.nums) {
		return 0, false
	}
	return v.nums[i], true
}

func (*Scheme) Major(s string) (int, bool) { return at(s, 0) }
func (*Scheme) Minor(s string) (int, bool) { return at(s, 1) }
func (*Scheme) Patch(s string) (int, bool) { return at(s, 2) }

func (*Scheme) Compare(a, b string) int {
	va, okA := parse(a)
	vb, okB := parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := 0; i < len(va.nums) && i < len(vb.nums); i++ {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] < vb.nums[i] {
				return -1
			}
			return 1
		}
	}
	// Shared prefix equal: MORE components wins. Measured - "1.0.0" beats "1",
	// and they are not equal either. Padding the shorter one with zeros would
	// make a bare "1" look like a released 1.0.0.
	if len(va.nums) != len(vb.nums) {
		if len(va.nums) < len(vb.nums) {
			return -1
		}
		return 1
	}
	switch {
	case va.rev < vb.rev:
		return -1
	case va.rev > vb.rev:
		return 1
	default:
		return 0
	}
}

func (s *Scheme) Equal(a, b string) bool {
	if !s.IsValid(a) || !s.IsValid(b) {
		return a == b
	}
	return s.Compare(a, b) == 0
}

// Satisfies is exact equality. apk has no range grammar: measured,
// "1.2.3-r4" does not satisfy "1.2.3", and neither does "1.2.3.4". A revision
// is part of the version, not a detail below it.
func (s *Scheme) Satisfies(version, rng string) bool {
	if !s.IsValid(version) || !s.IsValid(rng) {
		return false
	}
	return s.Compare(version, rng) == 0
}

func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	if !s.IsValid(target) {
		return "", fmt.Errorf("apk: target %q is not a package version", target)
	}
	return target, nil
}

// Revision returns a value's package revision, and whether it carries one. The
// apk datasource needs it to tell a rebuild from an upstream release.
func Revision(s string) (int, bool) {
	v, ok := parse(s)
	if !ok {
		return 0, false
	}
	return v.rev, strings.Contains(s, "-r")
}
