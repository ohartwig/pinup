// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package sched

import (
	"strings"
	"testing"
	"time"
)

const tz = "Europe/Berlin"

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", tz, err)
	}
	return loc
}

// The acceptance check: the two expressions the estate actually uses, sampled
// every 15 minutes across both DST transitions, plus "at any time".
//
// The transitions are the interesting part. On the spring-forward day
// 02:00-02:59 local does not exist at all, so a window of "after 1am and
// before 6am" is an hour shorter than on any other day; on the autumn day
// 02:00-02:59 happens twice. A schedule that computed its window in absolute
// time rather than local wall-clock time would be wrong on both.
func TestTheRealSchedulesAcrossDST(t *testing.T) {
	loc := berlin(t)

	for _, day := range []struct {
		name  string
		start time.Time
		steps int
	}{
		{"ordinary day", time.Date(2026, 9, 11, 0, 0, 0, 0, loc), 96},
		{"spring forward", time.Date(2026, 3, 29, 0, 0, 0, 0, loc), 92},
		{"fall back", time.Date(2026, 10, 25, 0, 0, 0, 0, loc), 100},
	} {
		window := MustParse([]string{"after 1am and before 6am"}, tz)
		cron := MustParse([]string{"* 0,4,8,12,16,20 * * *"}, tz)
		always := MustParse([]string{"at any time"}, tz)

		var windowOpen, cronOpen int
		now := day.start
		for i := 0; i < day.steps; i++ {
			local := now.In(loc)
			h, m := local.Hour(), local.Minute()

			wantWindow := h >= 1 && h < 6
			if got := window.IsActive(now); got != wantWindow {
				t.Errorf("%s: %s (%02d:%02d) window active=%v, want %v",
					day.name, local.Format(time.RFC3339), h, m, got, wantWindow)
			}
			if wantWindow {
				windowOpen++
			}

			wantCron := h == 0 || h == 4 || h == 8 || h == 12 || h == 16 || h == 20
			if got := cron.IsActive(now); got != wantCron {
				t.Errorf("%s: %s (%02d:%02d) cron active=%v, want %v",
					day.name, local.Format(time.RFC3339), h, m, got, wantCron)
			}
			if wantCron {
				cronOpen++
			}

			if !always.IsActive(now) {
				t.Errorf("%s: %s - \"at any time\" must always be open", day.name, local)
			}

			now = now.Add(15 * time.Minute)
		}

		// Denominators: a loop that evaluated nothing would pass every
		// assertion above.
		t.Logf("%s: window open at %d of %d samples, cron at %d",
			day.name, windowOpen, day.steps, cronOpen)
		if windowOpen == 0 || cronOpen == 0 {
			t.Errorf("%s: a schedule was never open across a whole day; the sampling is wrong", day.name)
		}
	}
}

// The spring-forward window really is shorter, and that is the observable
// consequence of evaluating in local wall-clock time.
func TestSpringForwardShortensTheWindow(t *testing.T) {
	loc := berlin(t)
	s := MustParse([]string{"after 1am and before 6am"}, tz)

	count := func(start time.Time) int {
		n := 0
		for i, cur := 0, start; i < 24*4; i, cur = i+1, cur.Add(15*time.Minute) {
			if s.IsActive(cur) {
				n++
			}
		}
		return n
	}
	ordinary := count(time.Date(2026, 9, 11, 0, 0, 0, 0, loc))
	spring := count(time.Date(2026, 3, 29, 0, 0, 0, 0, loc))

	if ordinary != 20 {
		t.Errorf("an ordinary day has %d open quarter-hours, want 20 (01:00-05:59)", ordinary)
	}
	if spring != 16 {
		t.Errorf("the spring-forward day has %d open quarter-hours, want 16 - one hour does not exist", spring)
	}
}

func TestNaturalForms(t *testing.T) {
	loc := berlin(t)
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, 9, day, hour, minute, 0, 0, loc)
	}
	// 2026-09-11 is a Friday; 12th Saturday, 13th Sunday, 14th Monday.
	for _, c := range []struct {
		expr string
		open []time.Time
		shut []time.Time
	}{
		{"before 6am", []time.Time{at(11, 0, 0), at(11, 5, 59)}, []time.Time{at(11, 6, 0), at(11, 23, 0)}},
		{"after 10pm", []time.Time{at(11, 22, 0), at(11, 23, 59)}, []time.Time{at(11, 21, 59), at(11, 6, 0)}},
		{"after 10pm and before 5am", []time.Time{at(11, 23, 0), at(11, 2, 0)}, []time.Time{at(11, 6, 0), at(11, 21, 0)}},
		{"on monday", []time.Time{at(14, 12, 0)}, []time.Time{at(11, 12, 0), at(13, 12, 0)}},
		{"on monday and friday", []time.Time{at(14, 9, 0), at(11, 9, 0)}, []time.Time{at(12, 9, 0)}},
		{"every weekend", []time.Time{at(12, 9, 0), at(13, 9, 0)}, []time.Time{at(11, 9, 0), at(14, 9, 0)}},
		{"every weekday", []time.Time{at(11, 9, 0), at(14, 9, 0)}, []time.Time{at(12, 9, 0), at(13, 9, 0)}},
		{"after 1am and before 6am every weekday", []time.Time{at(11, 3, 0)}, []time.Time{at(12, 3, 0), at(11, 9, 0)}},
		{"on the first day of the month", []time.Time{at(1, 9, 0)}, []time.Time{at(2, 9, 0)}},
	} {
		s, err := Parse([]string{c.expr}, tz)
		if err != nil {
			t.Errorf("%q: %v", c.expr, err)
			continue
		}
		for _, tm := range c.open {
			if !s.IsActive(tm) {
				t.Errorf("%q should be open at %s", c.expr, tm.Format("Mon 15:04"))
			}
		}
		for _, tm := range c.shut {
			if s.IsActive(tm) {
				t.Errorf("%q should be shut at %s", c.expr, tm.Format("Mon 15:04"))
			}
		}
	}
}

func TestCronForms(t *testing.T) {
	loc := berlin(t)
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, 9, day, hour, minute, 0, 0, loc)
	}
	for _, c := range []struct {
		expr string
		open []time.Time
		shut []time.Time
	}{
		{"* * * * *", []time.Time{at(11, 3, 7)}, nil},
		{"0 2 * * *", []time.Time{at(11, 2, 0)}, []time.Time{at(11, 2, 1), at(11, 3, 0)}},
		{"*/15 * * * *", []time.Time{at(11, 1, 0), at(11, 1, 15)}, []time.Time{at(11, 1, 7)}},
		{"0 1-5 * * *", []time.Time{at(11, 1, 0), at(11, 5, 0)}, []time.Time{at(11, 6, 0)}},
		{"* * * * 1", []time.Time{at(14, 9, 0)}, []time.Time{at(11, 9, 0)}},
		{"* * * * 7", []time.Time{at(13, 9, 0)}, []time.Time{at(14, 9, 0)}}, // 7 is Sunday too
	} {
		s, err := Parse([]string{c.expr}, tz)
		if err != nil {
			t.Errorf("%q: %v", c.expr, err)
			continue
		}
		for _, tm := range c.open {
			if !s.IsActive(tm) {
				t.Errorf("%q should be open at %s", c.expr, tm.Format("Mon 15:04:05"))
			}
		}
		for _, tm := range c.shut {
			if s.IsActive(tm) {
				t.Errorf("%q should be shut at %s", c.expr, tm.Format("Mon 15:04:05"))
			}
		}
	}
}

func TestUnparseableExpressionsAreLocatedErrors(t *testing.T) {
	for _, c := range []struct{ expr, want string }{
		{"whenever I feel like it", "unrecognised token"},
		{"after tuesday", "expected a time"},
		{"* * * *", "unrecognised token"},
		{"60 * * * *", "out of range"},
		{"* * * * 9", "out of range"},
		{"* */0 * * *", "bad step"},
		{"on the last day of the month", "not implemented"},
	} {
		_, err := Parse([]string{c.expr}, tz)
		if err == nil {
			t.Errorf("%q was accepted; an unreadable schedule must not silently gate nothing", c.expr)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %q does not mention %q", c.expr, err, c.want)
		}
		if !strings.Contains(err.Error(), c.expr) {
			t.Errorf("%q: error %q does not name the expression", c.expr, err)
		}
	}
}

func TestEmptyScheduleIsAlwaysOpen(t *testing.T) {
	s := MustParse(nil, tz)
	if !s.IsActive(time.Now()) {
		t.Error("no schedule must mean always open, not never")
	}
}

func TestUnionOfExpressions(t *testing.T) {
	loc := berlin(t)
	s := MustParse([]string{"before 6am", "after 10pm"}, tz)
	for _, c := range []struct {
		hour int
		want bool
	}{{3, true}, {23, true}, {12, false}} {
		got := s.IsActive(time.Date(2026, 9, 11, c.hour, 0, 0, 0, loc))
		if got != c.want {
			t.Errorf("at %02d:00 active=%v, want %v - a list is a union", c.hour, got, c.want)
		}
	}
}

func TestNextOpen(t *testing.T) {
	loc := berlin(t)
	s := MustParse([]string{"after 1am and before 6am"}, tz)

	// From inside the window, now.
	inside := time.Date(2026, 9, 11, 3, 0, 0, 0, loc)
	if got := s.NextOpen(inside); !got.Equal(inside) {
		t.Errorf("NextOpen inside the window = %v, want %v", got, inside)
	}
	// From outside, the next opening edge.
	outside := time.Date(2026, 9, 11, 9, 0, 0, 0, loc)
	want := time.Date(2026, 9, 12, 1, 0, 0, 0, loc)
	if got := s.NextOpen(outside); !got.Equal(want) {
		t.Errorf("NextOpen outside the window = %v, want %v", got, want)
	}
	// Always-open schedules answer immediately.
	if got := MustParse([]string{"at any time"}, tz).NextOpen(outside); !got.Equal(outside) {
		t.Errorf("NextOpen on an always-open schedule = %v, want %v", got, outside)
	}
}

func TestKind(t *testing.T) {
	for _, c := range []struct{ expr, want string }{
		{"at any time", "natural"},
		{"after 1am and before 6am", "natural"},
		{"* 0,4,8,12,16,20 * * *", "cron"},
		{"*/15 * * * *", "cron"},
	} {
		if got := Kind(c.expr); got != c.want {
			t.Errorf("Kind(%q) = %q, want %q", c.expr, got, c.want)
		}
	}
}
