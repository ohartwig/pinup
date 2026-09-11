// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package semver implements Renovate's `semver` scheme: strict three-part
// semantic versions, with an optional leading v.
//
// Measured behaviour, from the captured table rather than from the semver
// specification:
//
//   - "1.2" and "1" are INVALID. Two-part versions belong to semver-partial.
//   - "v1.2.3" is valid and its major is 1. The prefix is cosmetic.
//   - A prerelease is valid but not stable, and does not satisfy a range that
//     carries no prerelease of its own.
//   - getNewValue returns the bare target version for EVERY range strategy,
//     including replace on a caret range: "^1.0.0" becomes "1.1.0", not
//     "^1.1.0". Range-preserving rewriting is the npm scheme's job, not this
//     one's.
package semver

import (
	"fmt"
	"strconv"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// Scheme is the semver implementation.
type Scheme struct{}

// New returns the scheme.
func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "semver" }

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
		n, ok := numeric(p)
		if !ok {
			return Version{}, false
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, true
}

// numeric parses a non-negative integer, rejecting a leading zero on a
// multi-digit number as the semver specification requires.
func numeric(s string) (int, bool) {
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

func (*Scheme) IsValid(s string) bool {
	_, ok := Parse(s)
	return ok
}

func (*Scheme) IsStable(s string) bool {
	v, ok := Parse(s)
	return ok && len(v.Pre) == 0
}

func (*Scheme) Major(s string) (int, bool) {
	v, ok := Parse(s)
	return v.Major, ok
}

func (*Scheme) Minor(s string) (int, bool) {
	v, ok := Parse(s)
	return v.Minor, ok
}

func (*Scheme) Patch(s string) (int, bool) {
	v, ok := Parse(s)
	return v.Patch, ok
}

func (*Scheme) Compare(a, b string) int {
	va, okA := Parse(a)
	vb, okB := Parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	return CompareVersions(va, vb)
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
		na, aNum := numeric(a[i])
		nb, bNum := numeric(b[i])
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

func (s *Scheme) Equal(a, b string) bool {
	va, okA := Parse(a)
	vb, okB := Parse(b)
	if !okA || !okB {
		return a == b
	}
	return CompareVersions(va, vb) == 0
}

func (s *Scheme) Satisfies(version, rng string) bool {
	v, ok := Parse(version)
	if !ok {
		return false
	}
	return satisfies(v, rng)
}

func satisfies(v Version, rng string) bool {
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" {
		return len(v.Pre) == 0
	}

	op, rest := splitOp(rng)
	bound, ok := Parse(rest)
	if !ok {
		return false
	}

	// A prerelease only satisfies a constraint whose own bound carries a
	// prerelease on the same major.minor.patch. Without this, 1.0.0-alpha
	// would satisfy ">=1.0.0", which it does not.
	if len(v.Pre) > 0 {
		if len(bound.Pre) == 0 ||
			v.Major != bound.Major || v.Minor != bound.Minor || v.Patch != bound.Patch {
			return false
		}
	}

	c := CompareVersions(v, bound)
	switch op {
	case "":
		return c == 0
	case "=":
		return c == 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case "^":
		// Caret: up to the next non-zero leading component.
		if c < 0 {
			return false
		}
		switch {
		case bound.Major != 0:
			return v.Major == bound.Major
		case bound.Minor != 0:
			return v.Major == 0 && v.Minor == bound.Minor
		default:
			return v.Major == 0 && v.Minor == 0 && v.Patch == bound.Patch
		}
	case "~":
		// Tilde: patch-level changes within the stated minor.
		if c < 0 {
			return false
		}
		return v.Major == bound.Major && v.Minor == bound.Minor
	default:
		return false
	}
}

func splitOp(s string) (op, rest string) {
	for _, o := range []string{">=", "<=", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(s, o) {
			return o, strings.TrimSpace(s[len(o):])
		}
	}
	return "", s
}

// NewValue returns the target version. Every range strategy behaves the same
// way in this scheme, which the captured table is explicit about: a caret
// range under `replace` becomes a bare version, not a wider caret.
//
// The one thing not in the table, and decided here: a v prefix on the current
// value is preserved. Returning "1.1.0" where the file said "v1.0.0" would
// write a reference that does not name a tag.
func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	t, ok := Parse(target)
	if !ok {
		return "", fmt.Errorf("semver: target %q is not a version", target)
	}
	out := t.String()
	if c, ok := Parse(current); ok && c.HadVPrefix {
		out = "v" + out
	}
	return out, nil
}
