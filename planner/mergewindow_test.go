// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package planner

import (
	"slices"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
)

func TestTheMergeScheduleIsAutomergeScheduleWhenSetElseTheSchedule(t *testing.T) {
	night := []string{"* 3-5 * * *"}
	weekend := []string{"every weekend"}
	anyTime := []string{"at any time"}
	for _, tc := range []struct {
		name      string
		security  bool
		schedule  []string
		automerge []string
		explicit  bool
		want      []string
	}{
		{"the schedule alone", false, night, anyTime, false, night},
		{"no schedule, no window", false, nil, anyTime, false, nil},
		{"a schedule of at any time is no window", false, anyTime, anyTime, false, nil},
		{"automergeSchedule set decides", false, night, weekend, true, weekend},
		{"automergeSchedule set without the schedule", false, nil, weekend, true, weekend},
		{"automergeSchedule at any time set explicitly opens the merge", false, night, anyTime, true, nil},
		{"a security fix merges at any time", true, night, weekend, true, nil},
		{"a security fix under the schedule alone", true, night, anyTime, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := Named{Update: model.Update{SecurityFix: tc.security}, Schedule: tc.schedule, AutomergeSchedule: tc.automerge}
			if got := MergeSchedule(n, tc.explicit); !slices.Equal(got, tc.want) {
				t.Errorf("MergeSchedule = %v, want %v", got, tc.want)
			}
		})
	}
}

// The window rides on an automerging branch - also one whose update the
// schedule holds now, since an open request is lifted from that hold and
// still must not merge - and on no other.
func TestComposeCarriesTheMergeWindowOnAnAutomergingBranch(t *testing.T) {
	held := model.Update{DepKey: "mariadb", NewValue: "3.6.82", Blocks: []model.Block{{Reason: model.BlockSchedule}}, SuppressedBy: model.BlockSchedule}
	for _, tc := range []struct {
		name      string
		update    model.Update
		automerge bool
		want      []string
	}{
		{"automerge, actionable", model.Update{DepKey: "mariadb", NewValue: "3.6.82"}, true, []string{"* 3-5 * * *"}},
		{"automerge, held by the schedule", held, true, []string{"* 3-5 * * *"}},
		{"no automerge", model.Update{DepKey: "mariadb", NewValue: "3.6.82"}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := Named{Update: tc.update, Branch: "pinup/mariadb", Prefix: "pinup/", Title: "mariadb", Automerge: tc.automerge,
				Schedule: []string{"* 3-5 * * *"}, Timezone: "Europe/Berlin", MergeSchedule: []string{"* 3-5 * * *"}}
			branches, err := Compose([]Named{n})
			if err != nil {
				t.Fatal(err)
			}
			w := branches[0].MergeWindow
			if !slices.Equal(w.Raw, tc.want) {
				t.Fatalf("merge window %+v, want raw %v", w, tc.want)
			}
			if tc.want != nil && w.Timezone != "Europe/Berlin" {
				t.Errorf("timezone %q", w.Timezone)
			}
		})
	}
}

func TestJudgeMergeWindowSaysWhetherItIsOpenAndWhenItOpens(t *testing.T) {
	b := model.Branch{MergeWindow: model.Window{Timezone: "Europe/Berlin", Raw: []string{"* 3-5 * * *"}}}
	at := time.Date(2026, 11, 11, 8, 13, 0, 0, time.UTC) // 09:13 in Berlin
	if err := JudgeMergeWindow(&b, at); err != nil {
		t.Fatal(err)
	}
	if b.MergeWindow.Active || !b.MergeWindow.NextOpen.Equal(time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("at 09:13 Berlin: %+v", b.MergeWindow)
	}
	if err := JudgeMergeWindow(&b, time.Date(2026, 11, 11, 4, 18, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if !b.MergeWindow.Active || !b.MergeWindow.NextOpen.IsZero() {
		t.Errorf("at 05:18 Berlin: %+v", b.MergeWindow)
	}
}
