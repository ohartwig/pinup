// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package versioning declares the vocabulary three stages share: how a version
// string is read, ordered and rewritten.
//
// It sits at layer 1 rather than inside a stage because extract, lookup and
// planner all consume it; putting it in any one of them would invert the tree.
// The implementations live in subpackages at layer 3 and are registered by
// wire.
//
// The contract is behavioural parity with Renovate, not correctness in the
// abstract. Several schemes here reproduce quirks on purpose - docker's
// compatibility segment yields no total order, and semver-partial calls a
// two-part version incomparable even when its major matches. Those are
// measured facts (docs/tech-spec.md §0.10), and a "fix" would be a parity bug.
package versioning

import (
	"fmt"
	"sort"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// RangeStrategy says how a new version should be written back into an existing
// constraint.
type RangeStrategy string

const (
	// StrategyAuto lets the scheme choose; what that means is per-scheme.
	StrategyAuto RangeStrategy = "auto"
	// StrategyReplace keeps the shape of the constraint and moves its bound.
	StrategyReplace RangeStrategy = "replace"
	// StrategyBump raises the lower bound but keeps the range open.
	StrategyBump RangeStrategy = "bump"
	// StrategyPin replaces the constraint with an exact version.
	StrategyPin RangeStrategy = "pin"
	// StrategyWiden extends the range to admit the new version as well.
	StrategyWiden RangeStrategy = "widen"
	// StrategyUpdateLockfile leaves the constraint alone; only the lock moves.
	StrategyUpdateLockfile RangeStrategy = "update-lockfile"
)

// Versioning reads, orders and rewrites version strings for one ecosystem.
//
// Compare is NOT required to be a total order. The docker scheme's
// compatibility segment demonstrably is not one, and pretending otherwise
// would mean inventing an answer where Renovate has none. Callers that need a
// maximum must use Latest, which is defined in terms of what the scheme can
// actually decide.
type Versioning interface {
	Name() string

	IsValid(v string) bool
	IsStable(v string) bool

	// Major, Minor and Patch report a component, and whether the scheme could
	// determine it at all. A two-part version has no patch; a non-version has
	// nothing.
	Major(v string) (int, bool)
	Minor(v string) (int, bool)
	Patch(v string) (int, bool)

	// Compare returns <0, 0 or >0. For a pair the scheme cannot order it
	// returns 0, which is why Equal is a separate question.
	Compare(a, b string) int
	// Equal is not Compare(a,b)==0: two versions can be unordered without
	// being equal.
	Equal(a, b string) bool

	// Satisfies reports whether v falls inside the constraint rng.
	Satisfies(v, rng string) bool

	// NewValue rewrites a constraint so it admits target.
	NewValue(current, target string, strategy RangeStrategy) (string, error)
}

// UpdateType classifies a move from one version to another using a scheme's
// own notion of components.
//
// It is a free function rather than a method because every scheme answers it
// the same way once Major/Minor/Patch are available, and a scheme that needs
// to differ can implement Classifier instead.
func UpdateType(v Versioning, from, to string) model.UpdateType {
	if c, ok := v.(Classifier); ok {
		return c.UpdateType(from, to)
	}
	if from == to {
		return model.UpdateUnknown
	}
	if !v.IsValid(from) || !v.IsValid(to) {
		return model.UpdateUnknown
	}
	if v.Compare(to, from) < 0 {
		return model.UpdateRollback
	}

	fMaj, okA := v.Major(from)
	tMaj, okB := v.Major(to)
	if !okA || !okB {
		return model.UpdateUnknown
	}
	if fMaj != tMaj {
		return model.UpdateMajor
	}
	fMin, okA := v.Minor(from)
	tMin, okB := v.Minor(to)
	if !okA || !okB {
		// Same major, and the scheme cannot see a minor: the move is real but
		// unclassifiable at finer grain. Reporting major would overstate it.
		return model.UpdateMinor
	}
	if fMin != tMin {
		return model.UpdateMinor
	}
	return model.UpdatePatch
}

// Classifier is implemented by schemes whose update classification does not
// follow from components alone - docker, whose compatibility segment is a
// separate axis.
type Classifier interface {
	UpdateType(from, to string) model.UpdateType
}

// Latest returns the greatest of the given versions according to v, ignoring
// any it considers invalid.
//
// Where the scheme gives no order between two candidates, the earlier one in
// the input wins. That is a deliberate tie-break rather than a sort: with a
// non-total order a sort is not well defined, and silently picking "whichever
// the algorithm landed on" is how a compatibility family gets switched by
// accident.
func Latest(v Versioning, versions []string) (string, bool) {
	best, found := "", false
	for _, c := range versions {
		if !v.IsValid(c) {
			continue
		}
		if !found {
			best, found = c, true
			continue
		}
		if v.Compare(c, best) > 0 {
			best = c
		}
	}
	return best, found
}

// Sort orders versions ascending. Only valid versions are ordered; invalid
// ones keep their relative position at the end, so nothing is silently lost.
func Sort(v Versioning, versions []string) []string {
	valid, invalid := make([]string, 0, len(versions)), make([]string, 0)
	for _, s := range versions {
		if v.IsValid(s) {
			valid = append(valid, s)
		} else {
			invalid = append(invalid, s)
		}
	}
	sort.SliceStable(valid, func(i, j int) bool { return v.Compare(valid[i], valid[j]) < 0 })
	return append(valid, invalid...)
}

// Registry maps a scheme name to its implementation. wire fills it; nothing
// else writes to it.
type Registry map[string]Versioning

// Get resolves a scheme name, including the parameterised `regex:` form.
func (r Registry) Get(name string) (Versioning, error) {
	if name == "" {
		return nil, fmt.Errorf("no versioning scheme named")
	}
	if v, ok := r[name]; ok {
		return v, nil
	}
	if f, ok := r[schemeOf(name)]; ok {
		if p, ok := f.(Parameterised); ok {
			return p.WithConfig(configOf(name))
		}
	}
	return nil, fmt.Errorf("unknown versioning scheme %q", name)
}

// Parameterised is implemented by schemes configured through their name, as in
// `regex:^alpine(?<major>[0-9]+)\.(?<minor>[0-9]+)`.
type Parameterised interface {
	WithConfig(config string) (Versioning, error)
}

func schemeOf(name string) string {
	for i := 0; i < len(name); i++ {
		if name[i] == ':' {
			return name[:i]
		}
	}
	return name
}

func configOf(name string) string {
	for i := 0; i < len(name); i++ {
		if name[i] == ':' {
			return name[i+1:]
		}
	}
	return ""
}
