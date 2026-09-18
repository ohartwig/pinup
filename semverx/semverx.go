// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package semverx parses and orders strict semantic versions.
//
// It is a layer-0 primitive rather than part of the semver scheme, because two
// schemes need it: `semver` itself, and `semver-partial`, which reads a
// complete three-part version exactly the way semver does and treats
// everything else as a pattern. A scheme importing another scheme is what the
// layering rule forbids, so the shared part lives below both.
//
// Strict means strict, deliberately: "1.2" and "1" are not versions here. The
// schemes that accept them - loose, coerced, partial - do their own reading
// and do not come through this package.
package semverx

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version.
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // dot-separated prerelease identifiers
	Build               string
	HadVPrefix          bool
}

// Parse reads a strict three-part semantic version.
func Parse(s string) (Version, bool) {
	var v Version
	if s == "" {
		return v, false
	}
	if s[0] == 'v' || s[0] == 'V' {
		v.HadVPrefix = true
		s = s[1:]
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build = s[i+1:]
		s = s[:i]
		if v.Build == "" {
			return Version{}, false
		}
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre := s[i+1:]
		s = s[:i]
		if pre == "" {
			return Version{}, false
		}
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" {
				return Version{}, false
			}
		}
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, ok := Numeric(p)
		if !ok {
			return Version{}, false
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, true
}

// Numeric parses a non-negative integer, rejecting a leading zero on a
// multi-digit number as the specification requires.
func Numeric(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// CompareVersions orders two parsed versions. Build metadata is ignored, as
// the specification requires.
func CompareVersions(a, b Version) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	return comparePre(a.Pre, b.Pre)
}

// comparePre orders prerelease identifiers. A version without a prerelease
// outranks one with; numeric identifiers order numerically and rank below
// alphanumeric ones.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		na, aNum := Numeric(a[i])
		nb, bNum := Numeric(b[i])
		switch {
		case aNum && bNum:
			if c := cmpInt(na, nb); c != 0 {
				return c
			}
		case aNum:
			return -1
		case bNum:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
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
	default:
		return 0
	}
}
