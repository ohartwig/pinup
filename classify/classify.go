// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package classify declares what an analyzer does for an update: look at
// what actually changed between two versions and label it, with the
// evidence. The declared label comes from the version strings and is
// always there; the effective one comes from here, only when a rule asked
// for it (`analyze: true`), and only relaxes a decision when a rule says
// it may (`trustEffective: true`) - the stricter of the two labels wins
// otherwise.
//
// Layer 2. It knows the shapes and nothing about Helm or OCI; analyzer/*
// fills them in and wire hands them over. The motivating case is a chart
// vendor that bumps the major on every release while the application and
// the values are what they were.
package classify

import (
	"context"
	"errors"

	"github.com/ohartwig/pinup/model"
)

// ErrNotApplicable is an analyzer's answer when the thing turned out not
// to be its kind - a docker tag that is an image, not a chart. The next
// analyzer is asked; none is a verdict of unknown, not a warning.
var ErrNotApplicable = errors.New("not applicable")

// Effective is an analyzer's verdict: the label and what it rests on.
type Effective struct {
	Risk     model.Risk
	Evidence []model.Evidence
}

// Analyzer classifies the change between two versions of a dependency.
type Analyzer interface {
	Name() string
	// Applies reports whether this analyzer can say anything about the
	// dependency - by datasource and by the shape of the thing behind it.
	Applies(dep model.Dependency) bool
	// Analyze compares from and to. An error is the analyzer's failure to
	// look, not a verdict: the caller leaves Effective unknown and warns.
	Analyze(ctx context.Context, dep model.Dependency, from, to string) (Effective, error)
}

// Registry is every analyzer the run has, asked in order; the first that
// applies answers.
type Registry []Analyzer

// Run asks every analyzer that applies, in order, until one answers.
// ok is false when none did; err is the first analyzer's failure to look.
func (r Registry) Run(ctx context.Context, dep model.Dependency, from, to string) (Effective, string, bool, error) {
	for _, a := range r {
		if !a.Applies(dep) {
			continue
		}
		e, err := a.Analyze(ctx, dep, from, to)
		if errors.Is(err, ErrNotApplicable) {
			continue
		}
		if err != nil {
			return Effective{}, a.Name(), false, err
		}
		return e, a.Name(), true, nil
	}
	return Effective{}, "", false, nil
}
