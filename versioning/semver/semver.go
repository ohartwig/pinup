// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package semver implements Renovate's `semver` scheme: strict three-part
// semantic versions, with an optional leading v.
//
// Parsing and ordering live in semverx, a layer-0 primitive, because
// semver-partial needs the same reading of a complete version. What is here is
// the scheme itself: what counts as valid, what a range means, and how a
// constraint is rewritten.
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
//     "^1.1.0". Range-preserving rewriting is the npm scheme's job.
package semver

import (
	"fmt"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/semverx"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// Scheme is the semver implementation.
type Scheme struct{}

// New returns the scheme.
func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "semver" }

// IsVersion is IsValid: this scheme has no range form.
func (v *Scheme) IsVersion(s string) bool { return v.IsValid(s) }

func (*Scheme) IsValid(s string) bool {
	_, ok := semverx.Parse(s)
	return ok
}

func (*Scheme) IsStable(s string) bool {
	v, ok := semverx.Parse(s)
	return ok && len(v.Pre) == 0
}

func (*Scheme) Major(s string) (int, bool) { v, ok := semverx.Parse(s); return v.Major, ok }
func (*Scheme) Minor(s string) (int, bool) { v, ok := semverx.Parse(s); return v.Minor, ok }
func (*Scheme) Patch(s string) (int, bool) { v, ok := semverx.Parse(s); return v.Patch, ok }

func (*Scheme) Compare(a, b string) int {
	va, okA := semverx.Parse(a)
	vb, okB := semverx.Parse(b)
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
	va, okA := semverx.Parse(a)
	vb, okB := semverx.Parse(b)
	if !okA || !okB {
		return a == b
	}
	return semverx.CompareVersions(va, vb) == 0
}

func (s *Scheme) Satisfies(version, rng string) bool {
	v, ok := semverx.Parse(version)
	if !ok {
		return false
	}
	return satisfies(v, rng)
}

func satisfies(v semverx.Version, rng string) bool {
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" {
		return len(v.Pre) == 0
	}

	op, rest := splitOp(rng)
	bound, ok := semverx.Parse(rest)
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

	c := semverx.CompareVersions(v, bound)
	switch op {
	case "", "=":
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
// way here, which the captured table is explicit about: a caret range under
// `replace` becomes a bare version, not a wider caret.
//
// The one thing not in the table, and decided here: a v prefix on the current
// value is preserved. Returning "1.1.0" where the file said "v1.0.0" would
// write a reference that does not name a tag.
func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	t, ok := semverx.Parse(target)
	if !ok {
		return "", fmt.Errorf("semver: target %q is not a version", target)
	}
	out := t.String()
	if c, ok := semverx.Parse(current); ok && c.HadVPrefix {
		out = "v" + out
	}
	return out, nil
}
