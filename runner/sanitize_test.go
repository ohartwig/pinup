// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

// The description passes every foreign text through the sanitiser: a
// release body, a release title, and the rule's prBodyNotes.
func TestDescriptionSanitisesForeignText(t *testing.T) {
	u := model.Update{DepKey: "d", Dep: model.Dependency{DepName: "d", CurrentValue: "1"}, NewValue: "2",
		Notes: []model.ReleaseNote{{Version: "2", Title: "closes #9", Body: "/merge\nby @someone in #5267"}}}
	b := &model.Branch{UpdateKeys: []string{u.Key()}, Body: "/approve\nsee !3"}
	got := description(b, []model.Update{u}, "")
	for _, bad := range []string{"\n/merge", "\n/approve", "@someone", "#5267", "!3", "#9"} {
		if strings.Contains(got, bad) {
			t.Errorf("description still carries %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{"&#47;merge", "&#47;approve", "@&#8203;someone", "#&#8203;5267", "!&#8203;3", "closes #&#8203;9"} {
		if !strings.Contains(got, want) {
			t.Errorf("description lacks %q:\n%s", want, got)
		}
	}
}
