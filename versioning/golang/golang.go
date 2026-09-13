// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package golang implements Renovate's `go` scheme: Go module versions.
//
// Measured differences from semver, all of which matter for pinup updating
// itself:
//
//   - "1.2" and "1" are valid AND stable. Go tolerates partial versions where
//     semver does not.
//   - "1.2.3.4" is valid but NOT stable.
//   - A pseudo-version - "v0.0.0-20260101000000-abcdef123456" - is valid and
//     unstable. It names an untagged commit, so it must never be offered as an
//     upgrade over a real release.
//   - "v2.0.0+incompatible" is valid and stable. The suffix marks a module
//     without a /v2 path, not a prerelease.
package golang

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "go" }

type version struct {
	nums  []int
	pre   string
	build string
	// pseudo marks the -yyyymmddhhmmss-abcdef123456 form.
	pseudo bool
	extra  bool // more than three numeric components
}

func parse(s string) (version, bool) {
	var v version
	body := strings.TrimSpace(s)
	if body == "" {
		return v, false
	}
	body = strings.TrimPrefix(strings.TrimPrefix(body, "v"), "V")

	if i := strings.IndexByte(body, '+'); i >= 0 {
		v.build = body[i+1:]
		body = body[:i]
	}
	if i := strings.IndexByte(body, '-'); i >= 0 {
		v.pre = body[i+1:]
		body = body[:i]
		v.pseudo = isPseudo(v.pre)
	}

	segs := strings.Split(body, ".")
	for _, seg := range segs {
		n, err := strconv.Atoi(seg)
		if err != nil || seg == "" {
			return version{}, false
		}
		v.nums = append(v.nums, n)
	}
	if len(v.nums) == 0 {
		return version{}, false
	}
	v.extra = len(v.nums) > 3
	return v, true
}

// isPseudo recognises the timestamp-and-hash form Go uses for an untagged
// commit: 20260101000000-abcdef123456.
func isPseudo(pre string) bool {
	parts := strings.Split(pre, "-")
	if len(parts) < 2 {
		return false
	}
	ts, hash := parts[len(parts)-2], parts[len(parts)-1]
	if len(ts) != 14 || len(hash) < 12 {
		return false
	}
	for i := 0; i < len(ts); i++ {
		if ts[i] < '0' || ts[i] > '9' {
			return false
		}
	}
	return true
}

// IsVersion is IsValid: this scheme has no range form.
func (v *Scheme) IsVersion(s string) bool { return v.IsValid(s) }

func (*Scheme) IsValid(s string) bool {
	_, ok := parse(s)
	return ok
}

// IsStable excludes prereleases, pseudo-versions and four-component values.
// "+incompatible" does NOT cost stability: it marks a module without a /v2
// path, not a prerelease.
func (*Scheme) IsStable(s string) bool {
	v, ok := parse(s)
	return ok && v.pre == "" && !v.extra
}

func at(s string, i int) (int, bool) {
	v, ok := parse(s)
	if !ok {
		return 0, false
	}
	if i < len(v.nums) {
		return v.nums[i], true
	}
	return 0, true
}

func (*Scheme) Major(s string) (int, bool) { return at(s, 0) }
func (*Scheme) Minor(s string) (int, bool) { return at(s, 1) }
func (*Scheme) Patch(s string) (int, bool) { return at(s, 2) }

// Compare orders by major, minor and patch ONLY. A prerelease, a
// pseudo-version suffix, a build tag and a fourth component are all invisible
// to it.
//
// Measured, and coarser than it looks reasonable to be: "1.0.0" is neither
// greater than nor less than "1.0.0-alpha", and "v1.2.3" equals "1.2.3.4".
// Ordering and stability are separate questions here - IsStable does exclude
// prereleases and pseudo-versions, so a pseudo-version never becomes a
// candidate even though the comparison cannot see it.
//
// That separation is what makes the coarseness safe rather than alarming, and
// it is why Equal is defined as Compare(a,b) == 0 in this scheme while it
// deliberately is not in several others.
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
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(va.nums) {
			x = va.nums[i]
		}
		if i < len(vb.nums) {
			y = vb.nums[i]
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

func (s *Scheme) Equal(a, b string) bool {
	if !s.IsValid(a) || !s.IsValid(b) {
		return a == b
	}
	return s.Compare(a, b) == 0
}

func (s *Scheme) Satisfies(ver, rng string) bool {
	vv, okV := parse(ver)
	vr, okR := parse(rng)
	if !okV || !okR {
		return false
	}
	if len(vr.nums) > len(vv.nums) {
		return false
	}
	for i := range vr.nums {
		if vv.nums[i] != vr.nums[i] {
			return false
		}
	}
	return vr.pre == "" || vr.pre == vv.pre
}

// NewValue returns the target as given, WITHOUT adding a v prefix - measured,
// rewriting "v1.27.0" yields "1.1.0".
//
// That looks wrong for a go.mod, where a reference without the v does not
// resolve, and it is not: the prefix belongs to the reference syntax, which is
// the manager's business. A versioning scheme that knew about go.mod
// line formats would be doing the manager's job, and manager/gomod would then
// have two places to look when a prefix went missing.
func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	if !s.IsValid(target) {
		return "", fmt.Errorf("go: target %q is not a module version", target)
	}
	return target, nil
}
