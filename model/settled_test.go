// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

// Only the configuration's own verdicts are settled. Everything that
// passes with time, approval or a plugin is a wait, and its request must
// survive a prune - closing it would throw away a branch the next run
// writes again.
func TestOnlyTheConfigurationsVerdictsAreSettled(t *testing.T) {
	for _, tc := range []struct {
		r       BlockReason
		settled bool
	}{
		{BlockDisabled, true},
		{BlockAllowedVersions, true},
		{BlockMinimumReleaseAge, false},
		{BlockSchedule, false},
		{BlockHourlyLimit, false},
		{BlockConcurrentLimit, false},
		{BlockDashboardApproval, false},
		{BlockInternalChecks, false},
		{BlockPluginRequired, false},
		{BlockRollingMajor, false},
		{BlockTaskRefused, false},
		{BlockNothingToRefresh, false},
		{BlockPublishFailed, false},
		{BlockClosedByHand, false},
		{"", false},
	} {
		if got := tc.r.Settled(); got != tc.settled {
			t.Errorf("%q.Settled() = %t, want %t", tc.r, got, tc.settled)
		}
	}
}
