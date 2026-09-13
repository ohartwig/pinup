// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/git"
	"github.com/ohartwig/pinup/model"
)

func TestDescriptionCarriesUpdatesNotesAndReleases(t *testing.T) {
	published := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	updates := []model.Update{
		{
			DepKey: "dockerfile\x00Containerfile\x00alpine", Dep: model.Dependency{DepName: "alpine", CurrentValue: "3.20", SourceURL: "https://github.com/alpinelinux/docker-alpine"},
			NewValue: "3.21", Type: model.UpdateMinor,
			CompareURL: "https://github.com/alpinelinux/docker-alpine/compare/3.20...3.21",
			Notes: []model.ReleaseNote{
				{Version: "v3.21", Title: "Alpine 3.21 <beta>", Body: "musl bump", URL: "https://github.com/alpinelinux/docker-alpine/releases/tag/v3.21", Published: published},
				{Version: "v3.20.1", Title: "v3.20.1", Body: ""},
			},
		},
		{
			DepKey: "gomod\x00go.mod\x00golang.org/x/mod", Dep: model.Dependency{DepName: "golang.org/x/mod", CurrentValue: "v0.20.0", LockedVersion: "v0.19.0", SourceURL: "https://go.googlesource.com/mod"},
			NewValue: "v0.25.0", Type: model.UpdatePatch, SecurityFix: true,
		},
		{
			DepKey: "other\x00x\x00not-on-branch", Dep: model.Dependency{DepName: "not-on-branch"}, NewValue: "9", Type: model.UpdateMajor,
			Notes: []model.ReleaseNote{{Version: "9", Body: "MUST NOT APPEAR"}},
		},
	}
	b := &model.Branch{
		Name: "renovate/x", UpdateKeys: []string{updates[0].Key(), updates[1].Key()},
		Edits: []model.Edit{{File: "Containerfile", Old: "3.20", New: "3.21"}},
		Tasks: []model.Task{{FileFilters: []string{"go.sum"}, Command: []string{"go", "mod", "tidy"}}},
		Body:  "**Roll the box in the same window.**",
	}
	got := description(b, updates, "by pinup")

	for _, want := range []string{
		"| `alpine` | minor | `3.20` → `3.21` | [compare](https://github.com/alpinelinux/docker-alpine/compare/3.20...3.21), 2 releases |",
		"| `golang.org/x/mod` | patch | `v0.20.0 (v0.19.0)` → `v0.25.0` | [source](https://go.googlesource.com/mod), security fix |",
		"| `Containerfile` | `3.20` → `3.21` |",
		"| `go.sum` | `go mod tidy` |",
		"\n---\n\n**Roll the box in the same window.**\n",
		`<summary><a href="https://github.com/alpinelinux/docker-alpine/releases/tag/v3.21">alpine v3.21: Alpine 3.21 &lt;beta&gt;</a></summary>`,
		"\n\nmusl bump\n\n</details>",
		"<summary>alpine v3.20.1</summary>\n\n*(no notes)*",
		"\nby pinup\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "MUST NOT APPEAR") || strings.Contains(got, "not-on-branch") {
		t.Errorf("an update off the branch leaked in:\n%s", got)
	}
	// Order: updates, files, notes, releases, footer.
	idx := func(s string) int { return strings.Index(got, s) }
	if !(idx("| Dependency |") < idx("| File |") && idx("| File |") < idx("Roll the box") && idx("Roll the box") < idx("<details>") && idx("<details>") < idx("by pinup")) {
		t.Errorf("sections out of order:\n%s", got)
	}
}

func TestDescriptionChangeCells(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    model.Update
		want string
	}{
		{"digest", model.Update{Type: model.UpdateDigest, Dep: model.Dependency{CurrentValue: "3.20", CurrentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, NewValue: "3.20", NewDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbb"}, "`sha256:aaaaaaaaaaaa` → `sha256:bbbbbbbbbbbb`"},
		{"pinDigest", model.Update{Type: model.UpdatePinDigest, Dep: model.Dependency{CurrentValue: "3.20"}, NewValue: "3.20", NewDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbb"}, "`3.20` → `3.20@sha256:bbbbbbbbbbbb`"},
		{"lock", model.Update{Type: model.UpdateLockFileMaintenance}, "lock file refresh"},
		{"pin from empty", model.Update{Type: model.UpdatePin, NewValue: "1.2.3"}, "`1.2.3`"},
	} {
		if got := change(tc.u); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDescriptionCapsReleaseSections(t *testing.T) {
	var updates []model.Update
	var keys []string
	for i := 0; i < 5; i++ {
		u := model.Update{DepKey: fmt.Sprintf("d%d", i), Dep: model.Dependency{DepName: fmt.Sprintf("d%d", i), CurrentValue: "1"}, NewValue: "2", CompareURL: fmt.Sprintf("https://x/d%d/compare/1...2", i)}
		for j := 0; j < 20; j++ {
			u.Notes = append(u.Notes, model.ReleaseNote{Version: fmt.Sprintf("1.%d", j), Body: "x"})
		}
		updates = append(updates, u)
		keys = append(keys, u.Key())
	}
	// The same dependency read from a second place: one row, no more sections.
	twin := updates[0]
	twin.DepKey = "d0-again"
	updates = append(updates, twin)
	keys = append(keys, twin.Key())
	got := description(&model.Branch{UpdateKeys: keys}, updates, "")
	if n := strings.Count(got, "| `d0` |"); n != 1 {
		t.Errorf("d0 has %d rows, want 1", n)
	}
	// Ten per update, forty in all: d0..d3 show ten each, d4 none.
	if n := strings.Count(got, "<details>"); n != maxNotesShown {
		t.Errorf("%d sections, want %d", n, maxNotesShown)
	}
	for _, want := range []string{
		"*… and 10 more releases of `d0`: [compare](https://x/d0/compare/1...2).*",
		"*… and 20 more releases of `d4`: [compare](https://x/d4/compare/1...2).*",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if n := strings.Count(got, "more releases"); n != 5 {
		t.Errorf("%d trailers, want 5", n)
	}
}

// An empty repository - no commit, no HEAD - is planned as empty and does
// not fail on the way back to where it started.
func TestAnEmptyRepositoryDoesNotFailTheRun(t *testing.T) {
	if err := git.Available(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "--initial-branch=main", ".")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	repo.Env = testEnv
	pf := &platformfake.Platform{}
	outcomes, err := Execute(context.Background(), plan(), options(repo, pf))
	if err != nil || len(outcomes) != 0 {
		t.Errorf("empty repository: %v %+v", err, outcomes)
	}
}
