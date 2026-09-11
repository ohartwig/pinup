// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package sched

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// naturalExpr is a time-of-day window optionally restricted to certain
// weekdays or days of the month.
//
// The grammar is Renovate's, which comes from the `later` library, and only
// the part of it the estate actually uses is implemented:
//
//	after 1am and before 6am
//	before 6am
//	after 10pm
//	on monday
//	on monday and tuesday
//	every weekend
//	every weekday
//	on the first day of the month
//	after 10pm every weekday
//
// Anything else is a located error rather than a silent always-open or
// always-closed, because either of those is a schedule that looks like it
// works.
type naturalExpr struct {
	raw string

	hasWindow bool
	afterMin  int // minutes from midnight, inclusive
	beforeMin int // minutes from midnight, exclusive
	wraps     bool

	days     map[time.Weekday]bool // nil means every day
	monthDay int                   // 0 means any
}

func (n *naturalExpr) String() string { return n.raw }

func (n *naturalExpr) active(t time.Time) bool {
	if n.days != nil && !n.days[t.Weekday()] {
		return false
	}
	if n.monthDay != 0 && t.Day() != n.monthDay {
		return false
	}
	if !n.hasWindow {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if n.wraps {
		// e.g. "after 10pm and before 5am" - the window crosses midnight.
		return m >= n.afterMin || m < n.beforeMin
	}
	return m >= n.afterMin && m < n.beforeMin
}

var weekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

func parseNatural(s string) (expr, error) {
	n := &naturalExpr{raw: s, afterMin: 0, beforeMin: 24 * 60}
	low := strings.ToLower(strings.TrimSpace(s))

	// "on the first day of the month". The whole phrase is consumed,
	// ordinal included - leaving "first" behind would trip the
	// unrecognised-token check at the end, which is doing its job.
	if strings.Contains(low, "day of the month") {
		ord := []struct {
			word string
			day  int
		}{{"first", 1}, {"second", 2}, {"third", 3}, {"fourth", 4}, {"last", 0}}
		matched := false
		for _, o := range ord {
			phrase := o.word + " day of the month"
			if !strings.Contains(low, phrase) {
				continue
			}
			if o.day == 0 {
				return nil, &Error{s, "\"last day of the month\" is not implemented"}
			}
			n.monthDay = o.day
			// Strip the longest form present, so "on the first day of the
			// month" leaves nothing rather than "on the".
			for _, full := range []string{"on the " + phrase, "the " + phrase, phrase} {
				if strings.Contains(low, full) {
					low = strings.ReplaceAll(low, full, " ")
					break
				}
			}
			matched = true
			break
		}
		if !matched {
			return nil, &Error{s, "unrecognised day-of-month phrase"}
		}
	}

	switch {
	case strings.Contains(low, "every weekend"), strings.Contains(low, "on weekend"):
		n.days = map[time.Weekday]bool{time.Saturday: true, time.Sunday: true}
		low = removePhrase(low, "weekend")
	case strings.Contains(low, "every weekday"), strings.Contains(low, "on weekday"):
		n.days = map[time.Weekday]bool{
			time.Monday: true, time.Tuesday: true, time.Wednesday: true,
			time.Thursday: true, time.Friday: true,
		}
		low = removePhrase(low, "weekday")
	}

	// "on monday", "on monday and tuesday", "on mon, tue"
	if i := strings.Index(low, "on "); i >= 0 {
		rest := low[i+3:]
		var found map[time.Weekday]bool
		for _, tok := range strings.FieldsFunc(rest, func(r rune) bool {
			return r == ' ' || r == ',' || r == '\t'
		}) {
			tok = strings.TrimSpace(tok)
			if tok == "and" {
				continue
			}
			d, ok := weekdays[strings.TrimSuffix(tok, "s")]
			if !ok {
				break
			}
			if found == nil {
				found = map[time.Weekday]bool{}
			}
			found[d] = true
		}
		if found != nil {
			n.days = found
			low = low[:i]
		}
	}

	// "after X" / "before Y", in either order, joined by "and" or not.
	var sawAfter, sawBefore bool
	if i := strings.Index(low, "after "); i >= 0 {
		m, rest, err := parseClock(low[i+6:])
		if err != nil {
			return nil, &Error{s, err.Error()}
		}
		n.afterMin, sawAfter = m, true
		low = low[:i] + rest
	}
	if i := strings.Index(low, "before "); i >= 0 {
		m, rest, err := parseClock(low[i+7:])
		if err != nil {
			return nil, &Error{s, err.Error()}
		}
		n.beforeMin, sawBefore = m, true
		low = low[:i] + rest
	}

	n.hasWindow = sawAfter || sawBefore
	if n.hasWindow && sawAfter && sawBefore && n.afterMin > n.beforeMin {
		n.wraps = true
	}

	// Whatever is left must be filler. Anything else means the expression said
	// something this parser did not understand, and guessing is how a schedule
	// silently stops gating.
	for _, tok := range strings.Fields(low) {
		switch tok {
		case "and", "every", "on", "at", "the", "day", "of", "month", ",":
		default:
			return nil, &Error{s, fmt.Sprintf("unrecognised token %q; supported forms are "+
				`"at any time", "after <time>", "before <time>", "after X and before Y", `+
				`"on <weekday>", "every weekday", "every weekend", "on the <n>th day of the month"`, tok)}
		}
	}
	if !n.hasWindow && n.days == nil && n.monthDay == 0 {
		return nil, &Error{s, "expression restricts nothing; use \"at any time\" if that is meant"}
	}
	return n, nil
}

func removePhrase(s, phrase string) string {
	s = strings.ReplaceAll(s, "every "+phrase, " ")
	s = strings.ReplaceAll(s, "on "+phrase, " ")
	s = strings.ReplaceAll(s, phrase, " ")
	return s
}

// parseClock reads "1am", "6am", "10pm", "13:30", "6" and returns minutes from
// midnight plus the unconsumed remainder.
func parseClock(s string) (int, string, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == ':') {
		i++
	}
	if i == 0 {
		return 0, "", fmt.Errorf("expected a time, found %q", firstWord(s))
	}
	num, rest := s[:i], s[i:]

	hour, minute := 0, 0
	if h, m, ok := strings.Cut(num, ":"); ok {
		var err error
		if hour, err = strconv.Atoi(h); err != nil {
			return 0, "", fmt.Errorf("bad hour %q", h)
		}
		if minute, err = strconv.Atoi(m); err != nil {
			return 0, "", fmt.Errorf("bad minute %q", m)
		}
	} else {
		var err error
		if hour, err = strconv.Atoi(num); err != nil {
			return 0, "", fmt.Errorf("bad hour %q", num)
		}
	}

	rest = strings.TrimLeft(rest, " ")
	switch {
	case strings.HasPrefix(rest, "am"):
		if hour == 12 {
			hour = 0
		}
		rest = rest[2:]
	case strings.HasPrefix(rest, "pm"):
		if hour != 12 {
			hour += 12
		}
		rest = rest[2:]
	}
	if hour > 24 || minute > 59 {
		return 0, "", fmt.Errorf("time out of range: %d:%02d", hour, minute)
	}
	return hour*60 + minute, " " + rest, nil
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}
