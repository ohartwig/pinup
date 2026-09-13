// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package planner

import (
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
)

func origins(key string) model.Origin {
	return model.Origin{Source: "packageRules", Rule: map[string]int{
		"enabled": 1, "dependencyDashboardApproval": 2, "minimumReleaseAge": 3, "schedule": 4, "automerge": 5,
	}[key]}
}

func update(src model.TimeSource, released time.Time) model.Update {
	return model.Update{DepKey: "k", NewValue: "1.1.0", Type: model.UpdatePatch, TimeSource: src, ReleaseTime: released}
}

func TestParseAge(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
		bad  bool
	}{
		{"", 0, false}, {"0", 0, false}, {"3 days", 72 * time.Hour, false}, {"24 hours", 24 * time.Hour, false},
		{"7 days", 7 * 24 * time.Hour, false}, {"30 minutes", 30 * time.Minute, false}, {"2 weeks", 14 * 24 * time.Hour, false},
		{"1 year", 365 * 24 * time.Hour, false}, {"1.5 hours", 90 * time.Minute, false},
		{"soon", 0, true}, {"3 fortnights", 0, true}, {"-1 days", 0, true},
	} {
		got, err := ParseAge(c.in)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v, bad=%v", c.in, got, err, c.want, c.bad)
		}
	}
}

// The P1e.2 acceptance: a docker release with no timestamp is released under
// timestamp-optional and held under the default, and every held update
// records Until, the origin, and where its age came from.
func TestMinimumReleaseAgeAndTimestampBehaviour(t *testing.T) {
	cfg := map[string]any{"minimumReleaseAge": "24 hours"}

	// Age unknown, default behaviour: held, with the reason and no Until.
	u, err := Decide(update(model.TimeUnknown, time.Time{}), PolicyOf(cfg, origins), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Blocks) != 1 || u.Blocks[0].Reason != model.BlockMinimumReleaseAge || u.Blocks[0].Org.Rule != 3 || !u.Blocks[0].Until.IsZero() {
		t.Errorf("unknown age under the default must be held by rule 3 without a thaw time: %+v", u.Blocks)
	}
	if u.SuppressedBy != model.BlockMinimumReleaseAge {
		t.Errorf("suppressedBy = %q", u.SuppressedBy)
	}

	// Age unknown, timestamp-optional: released.
	cfg["minimumReleaseAgeBehaviour"] = "timestamp-optional"
	u, _ = Decide(update(model.TimeUnknown, time.Time{}), PolicyOf(cfg, origins), now)
	if len(u.Blocks) != 0 {
		t.Errorf("timestamp-optional must release an update of unknown age, got %+v", u.Blocks)
	}

	// Age from firstseen, too young: held until it is old enough.
	seen := now.Add(-6 * time.Hour)
	u, _ = Decide(update(model.TimeFromFirstSeen, seen), PolicyOf(cfg, origins), now)
	if len(u.Blocks) != 1 || !u.Blocks[0].Until.Equal(seen.Add(24*time.Hour)) {
		t.Errorf("held until 24h after first seen, got %+v", u.Blocks)
	}
	if !strings.Contains(u.Blocks[0].Note, "firstseen") || !strings.Contains(u.Blocks[0].Note, "6 hours") {
		t.Errorf("the note must say where the age came from and how old it is: %q", u.Blocks[0].Note)
	}

	// Age from the datasource, old enough: released.
	u, _ = Decide(update(model.TimeFromDatasource, now.Add(-48*time.Hour)), PolicyOf(cfg, origins), now)
	if len(u.Blocks) != 0 {
		t.Errorf("an old enough release is not held, got %+v", u.Blocks)
	}

	// "0" and null: no hold at all, even with an unknown age.
	cfg = map[string]any{"minimumReleaseAge": "0"}
	if u, _ := Decide(update(model.TimeUnknown, time.Time{}), PolicyOf(cfg, origins), now); len(u.Blocks) != 0 {
		t.Errorf("\"0\" holds nothing, got %+v", u.Blocks)
	}
	cfg = map[string]any{"minimumReleaseAge": nil}
	if u, _ := Decide(update(model.TimeUnknown, time.Time{}), PolicyOf(cfg, origins), now); len(u.Blocks) != 0 {
		t.Errorf("null holds nothing, got %+v", u.Blocks)
	}
}

func TestScheduleHoldsUntilTheNextWindow(t *testing.T) {
	// The estate's cron-throttled rule: every four hours, on the hour.
	cfg := map[string]any{"schedule": []any{"* 0,4,8,12,16,20 * * *"}, "timezone": "Europe/Berlin"}
	// 12:00 UTC on 2026-09-11 is 14:00 in Berlin: closed.
	u, err := Decide(update(model.TimeFromDatasource, now.Add(-72*time.Hour)), PolicyOf(cfg, origins), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Blocks) != 1 || u.Blocks[0].Reason != model.BlockSchedule || u.Blocks[0].Org.Rule != 4 {
		t.Fatalf("closed window must hold by rule 4: %+v", u.Blocks)
	}
	// Next open: 16:00 Berlin = 14:00 UTC.
	if want := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC); !u.Blocks[0].Until.Equal(want) {
		t.Errorf("thaws at %v, want %v", u.Blocks[0].Until, want)
	}
	// 16:30 Berlin: open.
	u, _ = Decide(update(model.TimeFromDatasource, now.Add(-72*time.Hour)), PolicyOf(cfg, origins), now.Add(2*time.Hour+30*time.Minute))
	if len(u.Blocks) != 0 {
		t.Errorf("open window must not hold, got %+v", u.Blocks)
	}
	// "at any time" overrides.
	cfg["schedule"] = []any{"at any time"}
	if u, _ := Decide(update(model.TimeFromDatasource, now.Add(-72*time.Hour)), PolicyOf(cfg, origins), now); len(u.Blocks) != 0 {
		t.Errorf("at any time holds nothing, got %+v", u.Blocks)
	}
}

func TestBlockOrderAndSuppressedBy(t *testing.T) {
	cfg := map[string]any{
		"enabled": false, "dependencyDashboardApproval": true, "minimumReleaseAge": "7 days",
		"schedule": []any{"after 10pm and before 5am"}, "timezone": "UTC",
	}
	u, err := Decide(update(model.TimeFromDatasource, now.Add(-time.Hour)), PolicyOf(cfg, origins), now)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.BlockReason{model.BlockDisabled, model.BlockDashboardApproval, model.BlockMinimumReleaseAge, model.BlockSchedule}
	if len(u.Blocks) != len(want) {
		t.Fatalf("blocks %+v", u.Blocks)
	}
	for i, b := range u.Blocks {
		if b.Reason != want[i] {
			t.Errorf("block %d = %s, want %s", i, b.Reason, want[i])
		}
	}
	if u.SuppressedBy != model.BlockDisabled {
		t.Errorf("suppressedBy = %s", u.SuppressedBy)
	}
	if _, err := Decide(update(model.TimeUnknown, time.Time{}), PolicyOf(map[string]any{"minimumReleaseAge": "soon"}, origins), now); err == nil {
		t.Error("an unparseable age must be an error, not a silent release")
	}
}

// A lock refresh keeps the planner's pluginRequired block and is not aged.
func TestLockFileMaintenanceKeepsItsBlockAndIsNotAged(t *testing.T) {
	u := model.Update{DepKey: "lock", Type: model.UpdateLockFileMaintenance, TimeSource: model.TimeUnknown,
		Blocks: []model.Block{{Reason: model.BlockPluginRequired}}, SuppressedBy: model.BlockPluginRequired}
	got, err := Decide(u, PolicyOf(map[string]any{"minimumReleaseAge": "24 hours"}, origins), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 1 || got.Blocks[0].Reason != model.BlockPluginRequired || got.SuppressedBy != model.BlockPluginRequired {
		t.Errorf("blocks %+v", got.Blocks)
	}
}

// A digest move has no release to be old: the tag stays, only the digest
// behind it changes. Measured: devops/wolfi-packages!364 opened under a
// 24-hour minimumReleaseAge for a git-refs branch pin with no timestamp.
func TestDigestMovesAreNotAged(t *testing.T) {
	for _, typ := range []model.UpdateType{model.UpdateDigest, model.UpdatePinDigest} {
		u := model.Update{DepKey: "img", Type: typ, TimeSource: model.TimeUnknown}
		got, err := Decide(u, PolicyOf(map[string]any{"minimumReleaseAge": "24 hours"}, origins), now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Blocks) != 0 {
			t.Errorf("%s: blocks %+v, want none", typ, got.Blocks)
		}
	}
}

// flexible's fallback waives the age: the policy holds the same update
// otherwise.
func TestAWaivedAgeIsNotHeld(t *testing.T) {
	u := model.Update{DepKey: "x", Type: model.UpdatePatch, TimeSource: model.TimeUnknown, AgeWaived: true}
	got, err := Decide(u, PolicyOf(map[string]any{"minimumReleaseAge": "3 days"}, origins), now)
	if err != nil || len(got.Blocks) != 0 {
		t.Errorf("waived: %+v %v", got.Blocks, err)
	}
	u.AgeWaived = false
	if got, _ := Decide(u, PolicyOf(map[string]any{"minimumReleaseAge": "3 days"}, origins), now); len(got.Blocks) != 1 {
		t.Errorf("not waived: %+v", got.Blocks)
	}
}
