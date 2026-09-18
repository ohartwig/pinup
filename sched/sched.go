// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package sched decides whether a schedule is open now, and when it next
// opens.
//
// Two grammars are needed, not one. The estate config uses Renovate's
// natural-language form ("at any time", "after 1am and before 6am") and a real
// five-field cron expression ("* 0,4,8,12,16,20 * * *") side by side, so a
// cron library alone would not do - and the natural-language half has to be
// written regardless, since no Go library speaks it.
//
// The question asked of a schedule is "is it open at this instant", not "when
// does it next fire". That is why this is not a cron scheduler: a cron library
// answers Next(), and answering IsActive() with one means reconstructing the
// window from its edges.
//
// Time is always passed in. Nothing here reads the clock.
package sched

import (
	"fmt"
	"strings"
	"time"
)

// Schedule is a set of expressions. It is open when any of them is open -
// Renovate's semantics, and the reading a user expects from a list.
type Schedule struct {
	raw   []string
	exprs []expr
	loc   *time.Location
	// always short-circuits everything. "at any time" is by far the most
	// common value in the estate, and it deserves not to walk a matcher.
	always bool
}

// Raw returns the expressions as written.
func (s *Schedule) Raw() []string { return s.raw }

// Timezone returns the location the schedule is evaluated in.
func (s *Schedule) Timezone() string {
	if s.loc == nil {
		return "UTC"
	}
	return s.loc.String()
}

type expr interface {
	active(t time.Time) bool
	String() string
}

// Error is a parse failure, naming the expression that could not be read.
type Error struct {
	Expr string
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("schedule %q: %s", e.Expr, e.Msg) }

// Parse compiles a list of schedule expressions in the given timezone. An
// empty list means "always", matching Renovate: no schedule is not a closed
// schedule.
func Parse(exprs []string, timezone string) (*Schedule, error) {
	loc := time.UTC
	if timezone != "" {
		l, err := time.LoadLocation(timezone)
		if err != nil {
			return nil, fmt.Errorf("timezone %q: %w", timezone, err)
		}
		loc = l
	}
	s := &Schedule{raw: exprs, loc: loc}
	if len(exprs) == 0 {
		s.always = true
		return s, nil
	}
	for _, e := range exprs {
		trimmed := strings.TrimSpace(e)
		if IsAnyTime(trimmed) {
			s.always = true
			continue
		}
		var (
			c   expr
			err error
		)
		if looksLikeCron(trimmed) {
			c, err = parseCron(trimmed)
		} else {
			c, err = parseNatural(trimmed)
		}
		if err != nil {
			return nil, err
		}
		s.exprs = append(s.exprs, c)
	}
	return s, nil
}

// MustParse is Parse for tests and constants.
func MustParse(exprs []string, timezone string) *Schedule {
	s, err := Parse(exprs, timezone)
	if err != nil {
		panic(err)
	}
	return s
}

// IsAnyTime reports whether one schedule expression means "no schedule":
// the spellings Renovate accepts for an always-open window.
func IsAnyTime(s string) bool {
	switch strings.ToLower(s) {
	case "at any time", "any time", "anytime", "always":
		return true
	}
	return false
}

// IsActive reports whether the schedule is open at t.
func (s *Schedule) IsActive(t time.Time) bool {
	if s.always {
		return true
	}
	local := t.In(s.loc)
	for _, e := range s.exprs {
		if e.active(local) {
			return true
		}
	}
	return false
}

// NextOpen returns the next instant at or after t when the schedule is open,
// or the zero time if it does not open within the horizon.
//
// Minute granularity, scanned forward. That is coarse and deliberate: the
// value feeds a merge-request body saying when an update thaws, where a minute
// is already more precision than the reader needs, and a scan is immune to the
// off-by-one-hour errors a closed-form computation invites around a DST
// boundary.
func (s *Schedule) NextOpen(t time.Time) time.Time {
	if s.always {
		return t
	}
	cur := t.In(s.loc).Truncate(time.Minute)
	const horizon = 366 * 24 * time.Hour
	deadline := cur.Add(horizon)
	for cur.Before(deadline) {
		if s.IsActive(cur) {
			return cur
		}
		cur = cur.Add(time.Minute)
	}
	return time.Time{}
}

func (s *Schedule) String() string {
	if s.always {
		return "at any time"
	}
	parts := make([]string, 0, len(s.exprs))
	for _, e := range s.exprs {
		parts = append(parts, e.String())
	}
	return strings.Join(parts, "; ")
}

// Kind reports which grammar a single expression uses, for the plan's schedule
// window record.
func Kind(e string) string {
	e = strings.TrimSpace(e)
	switch {
	case IsAnyTime(e):
		return "natural"
	case looksLikeCron(e):
		return "cron"
	default:
		return "natural"
	}
}
