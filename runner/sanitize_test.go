// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

func TestSanitizeNeutralisesQuickActionsAndReferences(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"quick action", "/merge", "&#47;merge"},
		{"indented quick action", "  /approve now", "  &#47;approve now"},
		{"slash inside a line", "a /b", "a /b"},
		{"issue reference", "fixes #123 and !45", "fixes #&#8203;123 and !&#8203;45"},
		{"mention", "thanks @renovate[bot] and @x_y", "thanks @&#8203;renovate[bot] and @&#8203;x_y"},
		{"heading stays", "## What's Changed", "## What's Changed"},
		{"hash without digit stays", "C# and #hashtag", "C# and #hashtag"},
		{"url untouched", "see https://github.com/o/r/pull/5267#issuecomment-1 and https://x/@user", "see https://github.com/o/r/pull/5267#issuecomment-1 and https://x/@user"},
		{"markdown link untouched", "[#12](https://x/issues/12#note_3) then #12", "[#&#8203;12](https://x/issues/12#note_3) then #&#8203;12"},
		{"inline code untouched", "run `git log #1` for @a", "run `git log #1` for @&#8203;a"},
		{"fenced code untouched", "```\n/merge\n#1 @a\n```\n/close", "```\n/merge\n#1 @a\n```\n&#47;close"},
		{"email is a mention too", "mail@example.org", "mail@&#8203;example.org"},
		{"entities already there stay", "a &#47;b &#8203;c #1", "a &#47;b &#8203;c #&#8203;1"},
		{"idempotent", Sanitize("/merge #1 @a"), Sanitize(Sanitize("/merge #1 @a"))},
	} {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

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
