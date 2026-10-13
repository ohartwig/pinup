// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/runner"
)

// A branch that failed to publish in the fast lane was a warning line in a
// green job (2026-10-13): no merge request, no dashboard entry, no red. These
// pin that a narrowed run now fails, and that a full run - whose dashboard
// carries the failure - keeps its exit status.
func TestNarrowedRunFailure(t *testing.T) {
	failed := []runner.Outcome{
		{Branch: "pinup/first-party-composer", Action: "failed", Message: "push: plugin: composer update: exit status 2"},
		{Branch: "pinup/other", Action: "created"},
	}
	clean := []runner.Outcome{{Branch: "pinup/other", Action: "created"}, {Branch: "pinup/x", Action: "held"}}

	for _, tc := range []struct {
		name     string
		o        *runOptions
		outcomes []runner.Outcome
		wantErr  bool
	}{
		{"fast lane with a failed branch", &runOptions{released: "development/moselwal/crowdsec-bridge"}, failed, true},
		{"--package with a failed branch", &runOptions{pkg: "composer:moselwal/crowdsec-bridge"}, failed, true},
		{"fast lane without failures", &runOptions{released: "a/b"}, clean, false},
		{"full run with a failed branch: the dashboard reports it", &runOptions{}, failed, false},
		{"dry run: nothing executed", &runOptions{released: "a/b"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := narrowedRunFailure(tc.o, "development/moselwal/moselwal-websites", tc.outcomes)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				for _, want := range []string{"moselwal-websites", "pinup/first-party-composer", "composer update"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not name %q", err, want)
					}
				}
				if strings.Contains(err.Error(), "pinup/other") {
					t.Errorf("error %q names a branch that did not fail", err)
				}
			}
		})
	}
}
