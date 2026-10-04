// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/report"
)

// A ticked box lifts its own hold and a schedule hold behind it, never an
// approval or a release age behind it. The first case is the one that kept
// frankenphp 1.13.0 off wolfi-packages on 2026-10-04: release age in front,
// schedule behind, and every run showed the age box again.
func TestLiftByDashboardScheduleBehindTick(t *testing.T) {
	const branch = "pinup/dunglas-frankenphp-1.x"
	for _, tc := range []struct {
		name   string
		blocks []model.BlockReason
		ticked string
		want   model.BlockReason
	}{
		{"age ticked, schedule behind", []model.BlockReason{model.BlockMinimumReleaseAge, model.BlockSchedule}, "approvePr-branch", ""},
		{"schedule ticked, nothing behind", []model.BlockReason{model.BlockSchedule}, "unschedule-branch", ""},
		{"schedule ticked, age behind stays", []model.BlockReason{model.BlockSchedule, model.BlockMinimumReleaseAge}, "unschedule-branch", model.BlockMinimumReleaseAge},
		{"approval ticked, age behind stays", []model.BlockReason{model.BlockDashboardApproval, model.BlockMinimumReleaseAge, model.BlockSchedule}, "approve-branch", model.BlockMinimumReleaseAge},
		{"nothing ticked, schedule stays", []model.BlockReason{model.BlockSchedule}, "", model.BlockSchedule},
		{"other branch ticked, age stays", []model.BlockReason{model.BlockMinimumReleaseAge, model.BlockSchedule}, "", model.BlockMinimumReleaseAge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := model.Update{DepKey: "dunglas/frankenphp", NewValue: "1.13.0"}
			for _, r := range tc.blocks {
				u.Blocks = append(u.Blocks, model.Block{Reason: r})
			}
			u.SuppressedBy = tc.blocks[0]
			updates := []model.Update{u}
			b := model.Branch{Name: branch, UpdateKeys: []string{u.Key()}, SuppressedBy: tc.blocks[0]}

			body := "nothing ticked"
			if tc.ticked != "" {
				body = " - [x] <!-- " + tc.ticked + "=" + branch + " -->update"
			} else if tc.name == "other branch ticked, age stays" {
				body = " - [x] <!-- approvePr-branch=pinup/other -->update"
			}
			liftByDashboard(&b, updates, report.ParseChecks(body))

			if b.SuppressedBy != tc.want {
				t.Errorf("branch held by %q, want %q", b.SuppressedBy, tc.want)
			}
			if updates[0].SuppressedBy != tc.want {
				t.Errorf("update held by %q, want %q", updates[0].SuppressedBy, tc.want)
			}
		})
	}
}
