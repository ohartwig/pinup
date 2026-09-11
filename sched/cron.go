// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package sched

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronExpr is a five-field cron expression: minute hour day-of-month month
// day-of-week.
//
// It is a matcher, not a scheduler. The question is "is this minute in the
// schedule", which for cron means every field matches. A cron library answers
// "when does this next fire", and using one here would mean reconstructing a
// window from its firing edges - more code, and wrong at a DST boundary.
//
// Day-of-month and day-of-week follow the usual cron rule: if both are
// restricted, either matching is enough.
type cronExpr struct {
	raw                          string
	min, hour, dom, month, dow   [60]bool
	domRestricted, dowRestricted bool
}

func (c *cronExpr) String() string { return c.raw }

func (c *cronExpr) active(t time.Time) bool {
	if !c.min[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	switch {
	case c.domRestricted && c.dowRestricted:
		return c.dom[t.Day()] || c.dow[int(t.Weekday())]
	case c.domRestricted:
		return c.dom[t.Day()]
	case c.dowRestricted:
		return c.dow[int(t.Weekday())]
	default:
		return true
	}
}

// looksLikeCron reports whether an expression should be read as cron rather
// than as prose. Five whitespace-separated fields made only of cron characters
// is unambiguous; no natural-language schedule looks like that.
func looksLikeCron(s string) bool {
	f := strings.Fields(s)
	if len(f) != 5 {
		return false
	}
	for _, field := range f {
		for _, r := range field {
			switch {
			case r >= '0' && r <= '9', r == '*', r == ',', r == '-', r == '/':
			default:
				return false
			}
		}
	}
	return true
}

func parseCron(s string) (expr, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, &Error{s, fmt.Sprintf("expected 5 fields, found %d", len(f))}
	}
	c := &cronExpr{raw: s}
	for i, spec := range []struct {
		field    string
		set      *[60]bool
		min, max int
	}{
		{f[0], &c.min, 0, 59},
		{f[1], &c.hour, 0, 23},
		{f[2], &c.dom, 1, 31},
		{f[3], &c.month, 1, 12},
		{f[4], &c.dow, 0, 6},
	} {
		restricted, err := fillField(spec.set, spec.field, spec.min, spec.max)
		if err != nil {
			return nil, &Error{s, fmt.Sprintf("field %d (%q): %v", i+1, spec.field, err)}
		}
		switch i {
		case 2:
			c.domRestricted = restricted
		case 4:
			c.dowRestricted = restricted
		}
	}
	return c, nil
}

// fillField marks the values a field selects and reports whether it restricts
// anything - "*" does not, and the day-of-month / day-of-week rule needs to
// know.
func fillField(set *[60]bool, field string, min, max int) (bool, error) {
	restricted := true
	for _, part := range strings.Split(field, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return false, fmt.Errorf("bad step %q", s)
			}
			step = n
			part = base
		}

		lo, hi := min, max
		switch {
		case part == "*":
			if step == 1 {
				restricted = false
			}
		default:
			if a, b, ok := strings.Cut(part, "-"); ok {
				var err error
				if lo, err = strconv.Atoi(a); err != nil {
					return false, fmt.Errorf("bad range start %q", a)
				}
				if hi, err = strconv.Atoi(b); err != nil {
					return false, fmt.Errorf("bad range end %q", b)
				}
			} else {
				n, err := strconv.Atoi(part)
				if err != nil {
					return false, fmt.Errorf("bad value %q", part)
				}
				lo, hi = n, n
			}
		}

		// Cron accepts 7 for Sunday as well as 0.
		if max == 6 && hi == 7 {
			set[0] = true
			hi = 6
			if lo == 7 {
				continue
			}
		}
		if lo < min || hi > max || lo > hi {
			return false, fmt.Errorf("value out of range %d-%d", min, max)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	return restricted, nil
}
