// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package partial implements Renovate's `semver-partial`: a scheme where a
// partial version is a valid CONSTRAINT but not a comparable VERSION.
//
// This is the scheme the estate's bare `@N` component pins use, and its rule
// is not the obvious one:
//
//	matches("1.10.17", "1") = true
//	matches("1.22",    "1") = false
//
// "1.22" has a matching major and still does not match, because a two-part
// value is not a version here. Accordingly a partial has NO major, minor or
// patch of its own and is never stable - it is a pattern, and asking a pattern
// for its patch level is a category error rather than a missing feature.
package partial

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/semverx"
	"github.com/ohartwig/pinup/versioning"
)

type Scheme struct{}

func New() *Scheme { return &Scheme{} }

func (*Scheme) Name() string { return "semver-partial" }

// numericParts returns the dot-separated numeric components of a value, and
// whether every component was numeric.
func numericParts(s string) ([]int, bool) {
	body := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	if body == "" {
		return nil, false
	}
	// A prerelease suffix belongs to the full-version case, which semver
	// handles; strip it for the purpose of counting components.
	if i := strings.IndexAny(body, "-+"); i >= 0 {
		body = body[:i]
	}
	segs := strings.Split(body, ".")
	out := make([]int, 0, len(segs))
	for _, seg := range segs {
		n, err := strconv.Atoi(seg)
		if err != nil || seg == "" {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// full reports whether a value is a complete three-part version, which is the
// only shape this scheme treats as a version rather than a pattern.
func full(s string) (semverx.Version, bool) {
	parts, ok := numericParts(s)
	if !ok || len(parts) != 3 {
		return semverx.Version{}, false
	}
	return semverx.Parse(s)
}

func (*Scheme) IsValid(s string) bool {
	_, ok := numericParts(s)
	return ok
}

// IsVersion is true only for a full three-part version. "1" is valid and is
// not a version: it is the range every 1.x.y satisfies, which is how a
// rolling major pin is read.
func (*Scheme) IsVersion(s string) bool {
	_, ok := full(s)
	return ok
}

// IsStable is true only for a complete version without a prerelease. A
// partial is never stable, because it does not name a release.
func (*Scheme) IsStable(s string) bool {
	v, ok := full(s)
	return ok && len(v.Pre) == 0
}

func component(s string, pick func(semverx.Version) int) (int, bool) {
	v, ok := full(s)
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

// Compare orders complete versions only. A partial does not participate at
// all: measured, "1.0.0" is not greater than "1", nor "1" than "1.0.0".
//
// So any pair involving a partial returns 0, meaning "no opinion" rather than
// "equal". This is the second scheme whose order is not total - docker being
// the other - and it is why the interface does not promise one, and why Equal
// exists separately from Compare.
func (*Scheme) Compare(a, b string) int {
	va, okA := full(a)
	vb, okB := full(b)
	if !okA || !okB {
		return 0
	}
	return semverx.CompareVersions(va, vb)
}

// Equal answers only for complete versions. A partial is not equal to
// anything, not even to an identical string: measured, equals("1","1") is
// false.
//
// That follows from the scheme's own thesis rather than being an oddity. "1"
// is a pattern, and two occurrences of a pattern are not the same release.
// Treating them as equal would let a bare-major pin report itself
// already-current and never be looked at again.
func (*Scheme) Equal(a, b string) bool {
	va, okA := full(a)
	vb, okB := full(b)
	if !okA || !okB {
		return false
	}
	return semverx.CompareVersions(va, vb) == 0
}

// Satisfies matches a complete, released version against a partial prefix.
// The version must be complete; the constraint need not be.
//
// A prerelease never satisfies a partial: measured, "1.0.0-alpha" does not
// match "1". A bare-major pin is a statement about releases.
func (s *Scheme) Satisfies(version, rng string) bool {
	if !s.IsStable(version) {
		return false
	}
	v, ok := numericParts(version)
	if !ok || len(v) != 3 {
		return false
	}
	r, ok := numericParts(rng)
	if !ok || len(r) > 3 {
		return false
	}
	for i := range r {
		if v[i] != r[i] {
			return false
		}
	}
	return true
}

func (s *Scheme) NewValue(current, target string, strategy versioning.RangeStrategy) (string, error) {
	tp, ok := numericParts(target)
	if !ok {
		return "", fmt.Errorf("semver-partial: target %q is not a version", target)
	}
	// pin is the one strategy that discards the shape: asking to pin a
	// bare-major reference means asking for an exact version, so "1" becomes
	// "1.1.0". Every other strategy keeps it a major pin - widening one
	// silently is exactly the automatic adoption the estate's rolling-tag
	// decision forbids.
	if strategy == versioning.StrategyPin {
		return target, nil
	}
	cp, ok := numericParts(current)
	if !ok || len(cp) >= len(tp) {
		return target, nil
	}
	out := make([]string, len(cp))
	for i := range cp {
		out[i] = strconv.Itoa(tp[i])
	}
	return strings.Join(out, "."), nil
}
