// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// Only "conflicted" reaches the plan: it is the one value the run acts on,
// and config:recommended's "auto" must not add a field to every plan.
func TestRebaseWhenReachesThePlanOnlyWhenItActs(t *testing.T) {
	for in, want := range map[any]string{
		"conflicted":         "conflicted",
		"auto":               "",
		"behind-base-branch": "",
		"never":              "",
		nil:                  "",
		true:                 "",
	} {
		if got := rebaseWhenOf(in); got != want {
			t.Errorf("rebaseWhenOf(%v) = %q, want %q", in, got, want)
		}
	}
}
