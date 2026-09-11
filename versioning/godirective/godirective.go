// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package godirective implements Renovate's `go-mod-directive` scheme: the
// version written after `go` or `toolchain` in a go.mod file.
//
// Measured oddities worth knowing (docs/tech-spec.md's parity table,
// testdata/parity/renovate-43.288.0/versioning/go-mod-directive.json):
//
//   - IsValid and IsVersion disagree on which shape they accept, and neither
//     is a subset of the other. IsValid takes the DIRECTIVE shape: two or
//     three plain numeric components, no "v" prefix, no prerelease -
//     "1.27" and "1.27.0" are both valid, "1" and "v1.2.3" are not. IsVersion
//     (and IsSingleVersion, which the container reports identically) takes a
//     complete semantic version instead, "v" prefix and prerelease allowed -
//     "1.27" is NOT one, "v1.2.3" and "1.0.0-rc.1" ARE. A two-part directive
//     is therefore a valid constraint that names no version at all.
//   - Matches treats a directive as a caret range fixed at its own major:
//     "1.27" and "1.27.0" both accept any full release with major 1 that is
//     >= 1.27.0 - so 1.28.0 matches "1.27" - and reject anything with a
//     different major, anything below the floor, and any prerelease.
//   - GetNewValue treats `bump` and `replace` differently from the rest.
//     Bump always rewrites to the target's own major.minor[.patch], dropping
//     a trailing zero patch: bumping to "1.1.0" yields "1.1". Replace
//     rewrites only when the directive, read as the caret floor above, does
//     not already admit the target - replacing "1.27" with "1.1.0" (outside
//     it) yields "1.1.0", but replacing it with "1.28.1" (inside it) leaves
//     "1.27" untouched. Pin, widen, update-lockfile and auto are
//     unconditional no-ops: a go.mod directive is not renumbered by
//     Renovate for those strategies; only `go mod tidy`, run by the plugin,
//     ever moves it.
//   - IsCompatible, like npm's, is "the candidate is a complete version" -
//     a bare directive such as "1.27" cannot follow anything, because it
//     names no release to compare.
package godirective

import (
	"fmt"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/semverx"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// Scheme implements versioning.Versioning for go.mod's `go` and `toolchain`
// directives.
type Scheme struct{}

// New returns a ready-to-use Scheme.
func New() *Scheme { return &Scheme{} }

// Name implements versioning.Versioning.
func (*Scheme) Name() string { return "go-mod-directive" }

// directiveParts reads the directive shape: two or three plain numeric
// components, no "v" prefix and no prerelease or build suffix. It is
// deliberately stricter than semverx.Parse, which is what a full version
// (IsVersion, Major, Compare, ...) reads instead - the two shapes overlap on
// nothing longer than an accident, and the table above is what makes that
// non-obvious fact a specification rather than a guess.
func directiveParts(s string) ([]int, bool) {
	if s == "" || strings.ContainsAny(s, "-+") {
		return nil, false
	}
	if s[0] == 'v' || s[0] == 'V' {
		return nil, false
	}
	segs := strings.Split(s, ".")
	if len(segs) < 2 || len(segs) > 3 {
		return nil, false
	}
	out := make([]int, 0, len(segs))
	for _, seg := range segs {
		n, ok := semverx.Numeric(seg)
		if !ok {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// IsValid implements versioning.Versioning: the directive shape, not a full
// version - see directiveParts.
func (*Scheme) IsValid(s string) bool {
	_, ok := directiveParts(s)
	return ok
}

// IsVersion implements versioning.Versioning: a complete semantic version.
// Measured: this accepts what IsValid rejects ("1.0.0-rc.1", "v1.2.3") and
// rejects what IsValid accepts ("1.27") - the two are reading different
// shapes, not one relaxing the other.
func (*Scheme) IsVersion(s string) bool {
	_, ok := semverx.Parse(s)
	return ok
}

// IsStable implements versioning.Versioning: a complete version without a
// prerelease. A bare directive like "1.27" is never stable - it names no
// release to be stable about.
func (*Scheme) IsStable(s string) bool {
	v, ok := semverx.Parse(s)
	return ok && len(v.Pre) == 0
}

func component(s string, pick func(semverx.Version) int) (int, bool) {
	v, ok := semverx.Parse(s)
	if !ok {
		return 0, false
	}
	return pick(v), true
}

// Major implements versioning.Versioning.
func (*Scheme) Major(s string) (int, bool) {
	return component(s, func(v semverx.Version) int { return v.Major })
}

// Minor implements versioning.Versioning.
func (*Scheme) Minor(s string) (int, bool) {
	return component(s, func(v semverx.Version) int { return v.Minor })
}

// Patch implements versioning.Versioning.
func (*Scheme) Patch(s string) (int, bool) {
	return component(s, func(v semverx.Version) int { return v.Patch })
}

// Compare implements versioning.Versioning. A pair where either side is not a
// complete version has no opinion, the same convention versioning/partial
// uses: the table only ever asks this of complete versions and treats
// anything else as an error, never a real comparison.
func (*Scheme) Compare(a, b string) int {
	va, okA := semverx.Parse(a)
	vb, okB := semverx.Parse(b)
	if !okA || !okB {
		return 0
	}
	return semverx.CompareVersions(va, vb)
}

// Equal implements versioning.Versioning.
func (*Scheme) Equal(a, b string) bool {
	va, okA := semverx.Parse(a)
	vb, okB := semverx.Parse(b)
	if !okA || !okB {
		return false
	}
	return semverx.CompareVersions(va, vb) == 0
}

// Satisfies implements versioning.Versioning. Measured: a directive - two or
// three components - behaves as a caret range fixed at its own major: the
// candidate must be a complete, stable release, share the directive's major,
// and be no lower than the directive read as a version (missing components
// default to zero). A version's own minor may exceed the directive's -
// "1.28.0" satisfies "1.27" - which is what makes this a floor, not a
// major.minor pin.
func (s *Scheme) Satisfies(version, rng string) bool {
	rparts, ok := directiveParts(rng)
	if !ok {
		return false
	}
	v, ok := semverx.Parse(version)
	if !ok || len(v.Pre) > 0 {
		return false
	}
	floor := semverx.Version{Major: rparts[0]}
	if len(rparts) > 1 {
		floor.Minor = rparts[1]
	}
	if len(rparts) > 2 {
		floor.Patch = rparts[2]
	}
	if v.Major != floor.Major {
		return false
	}
	return semverx.CompareVersions(v, floor) >= 0
}

// NewValue implements versioning.Versioning.
//
// Bump always rewrites, to the target's own major.minor[.patch] with a
// trailing zero patch dropped - "1.1.0" becomes "1.1" - independent of how
// the current directive was shaped.
//
// Replace rewrites only when the directive does not already admit the
// target: measured, replacing "1.27" with a target of "1.1.0" (which "1.27"
// as a caret floor does NOT admit) yields "1.1.0" verbatim, but replacing it
// with "1.28.1" (which it DOES admit) leaves "1.27" untouched. A go.mod
// directive is a floor, not a pin, so "already satisfied" is a real question
// here in a way it is not for pin, widen, update-lockfile or auto, which are
// unconditional no-ops: those never rewrite the directive at all, because
// only `go mod tidy` - the plugin, not this scheme - is allowed to move it.
func (s *Scheme) NewValue(current, target string, strategy versioning.RangeStrategy) (string, error) {
	switch strategy {
	case versioning.StrategyBump:
		v, ok := semverx.Parse(target)
		if !ok {
			return "", fmt.Errorf("go-mod-directive: target %q is not a version", target)
		}
		if v.Patch == 0 {
			return fmt.Sprintf("%d.%d", v.Major, v.Minor), nil
		}
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch), nil

	case versioning.StrategyReplace:
		if s.Satisfies(target, current) {
			return current, nil
		}
		return target, nil

	default:
		return current, nil
	}
}

// IsCompatible implements versioning.Compatible. Measured the same way as
// npm's: a candidate is compatible exactly when it is a complete version, not
// a directive - a bare "1.27" cannot follow anything, because it names no
// release. current is unused: nothing about it narrows which candidates the
// container ever accepted here.
func (*Scheme) IsCompatible(candidate, _ string) bool {
	_, ok := semverx.Parse(candidate)
	return ok
}
