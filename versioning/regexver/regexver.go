// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package regexver implements Renovate's parameterised `regex:` scheme, where
// the user supplies a pattern with named groups and those groups become the
// version components.
//
// The estate uses exactly one instance:
//
//	regex:^alpine(?<major>[0-9]+)\.(?<minor>[0-9]+)
//
// on the `bash` package, whose tags are alpine releases rather than versions.
// So "alpine3.21" has major 3 and minor 21, "alpine4.0" outranks it, and
// "alpine3" - which the pattern cannot match - is simply not a version.
//
// Recognised group names: major, minor, patch, build, prerelease,
// compatibility. A value whose compatibility group differs is not comparable,
// which is the same separation the docker scheme draws by hand.
package regexver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

// Scheme is unconfigured until WithConfig is called. Registered under the bare
// name "regex" so the registry can resolve "regex:<pattern>".
type Scheme struct {
	re  *regexp.Regexp
	raw string
}

// New returns the unconfigured scheme, which only knows how to configure
// itself.
func New() *Scheme { return &Scheme{} }

func (s *Scheme) Name() string {
	if s.raw == "" {
		return "regex"
	}
	return "regex:" + s.raw
}

// WithConfig compiles a pattern. RE2 is enough: the estate's patterns use no
// backreferences and no lookaround, asserted as a config lint.
func (s *Scheme) WithConfig(pattern string) (versioning.Versioning, error) {
	if pattern == "" {
		return nil, fmt.Errorf("regex versioning: no pattern given")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("regex versioning %q: %w", pattern, err)
	}
	var named bool
	for _, n := range re.SubexpNames() {
		if n != "" {
			named = true
		}
	}
	if !named {
		return nil, fmt.Errorf("regex versioning %q: no named groups, so nothing becomes a version component", pattern)
	}
	return &Scheme{re: re, raw: pattern}, nil
}

type parsed struct {
	major, minor, patch          int
	hasMajor, hasMinor, hasPatch bool
	pre                          string
	compatibility                string
}

func (s *Scheme) parse(v string) (parsed, bool) {
	var p parsed
	if s.re == nil {
		return p, false
	}
	m := s.re.FindStringSubmatch(v)
	if m == nil {
		return p, false
	}
	for i, name := range s.re.SubexpNames() {
		if name == "" || i >= len(m) || m[i] == "" {
			continue
		}
		switch name {
		case "major":
			if n, err := strconv.Atoi(m[i]); err == nil {
				p.major, p.hasMajor = n, true
			}
		case "minor":
			if n, err := strconv.Atoi(m[i]); err == nil {
				p.minor, p.hasMinor = n, true
			}
		case "patch":
			if n, err := strconv.Atoi(m[i]); err == nil {
				p.patch, p.hasPatch = n, true
			}
		case "prerelease":
			p.pre = m[i]
		case "compatibility":
			p.compatibility = m[i]
		}
	}
	// A pattern that matched but bound no major has produced no version.
	return p, p.hasMajor
}

// IsVersion is IsValid: a regex scheme has no range form.
func (s *Scheme) IsVersion(v string) bool { return s.IsValid(v) }

func (s *Scheme) IsValid(v string) bool {
	_, ok := s.parse(v)
	return ok
}

func (s *Scheme) IsStable(v string) bool {
	p, ok := s.parse(v)
	return ok && p.pre == ""
}

func (s *Scheme) Major(v string) (int, bool) {
	p, ok := s.parse(v)
	return p.major, ok && p.hasMajor
}

func (s *Scheme) Minor(v string) (int, bool) {
	p, ok := s.parse(v)
	return p.minor, ok && p.hasMinor
}

// Patch reports 0 rather than nothing when the pattern binds no patch group,
// as long as the value matched at all. Measured: getPatch("alpine3.21") is 0
// under a pattern that only names major and minor.
//
// Minor keeps the stricter reading because a pattern binding only a major -
// which the estate does not have, but a user could write - should not claim a
// minor of 0 it never saw.
func (s *Scheme) Patch(v string) (int, bool) {
	p, ok := s.parse(v)
	return p.patch, ok
}

// Compare orders by the numeric groups. Two values with different
// compatibility groups are NOT comparable and return 0 - the same separation
// the docker scheme draws, for the same reason: a compatibility segment is a
// different axis, not a lower-order component.
func (s *Scheme) Compare(a, b string) int {
	pa, okA := s.parse(a)
	pb, okB := s.parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	if pa.compatibility != pb.compatibility {
		return 0
	}
	for _, pair := range [][2]int{
		{pa.major, pb.major}, {pa.minor, pb.minor}, {pa.patch, pb.patch},
	} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	default:
		return strings.Compare(pa.pre, pb.pre)
	}
}

func (s *Scheme) Equal(a, b string) bool {
	if !s.IsValid(a) || !s.IsValid(b) {
		return a == b
	}
	pa, _ := s.parse(a)
	pb, _ := s.parse(b)
	if pa.compatibility != pb.compatibility {
		return false
	}
	return s.Compare(a, b) == 0
}

func (s *Scheme) Satisfies(version, rng string) bool {
	pv, okV := s.parse(version)
	pr, okR := s.parse(rng)
	if !okV || !okR {
		return false
	}
	if pv.compatibility != pr.compatibility {
		return false
	}
	if pr.hasMajor && pv.major != pr.major {
		return false
	}
	if pr.hasMinor && pv.minor != pr.minor {
		return false
	}
	if pr.hasPatch && pv.patch != pr.patch {
		return false
	}
	return true
}

// NewValue passes the target through without checking it against the pattern.
//
// That is deliberate and matches Renovate. A target that cannot match the
// pattern should never have become a candidate in the first place: filtering
// releases the versioning scheme cannot read is the LOOKUP stage's job, where
// a whole release set is available and a mismatch can be reported once rather
// than per rewrite. Refusing here would report the same problem later, with
// less context, after the planner had already built a branch around it.
func (s *Scheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	if s.re == nil {
		return "", fmt.Errorf("regex versioning: scheme was never configured with a pattern")
	}
	return target, nil
}
