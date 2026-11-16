// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/git"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
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

// GitLab may not see a branch pushed a moment earlier: the request is
// retried with a doubling wait and opens on the attempt that succeeds.
func TestCreateRetriesUntilTheBranchIsVisible(t *testing.T) {
	pf := &platformfake.Platform{CreateErr: errors.New(`gitlab: status 400: {"source_branch":["does not exist"]}`), CreateErrTimes: 4}
	var waits []time.Duration
	o := Options{Platform: pf, Sleep: func(d time.Duration) { waits = append(waits, d) }}
	mr, _, err := createWithRetry(context.Background(), o, publish.Request{SourceBranch: "renovate/x"}, "")
	if err != nil || mr.IID == 0 {
		t.Fatalf("mr=%+v err=%v", mr, err)
	}
	if len(waits) != 4 || waits[0] != 2*time.Second || waits[3] != 16*time.Second {
		t.Errorf("waits = %v", waits)
	}
	// Any other error stands at once.
	pf = &platformfake.Platform{CreateErr: errors.New("gitlab: status 403")}
	waits = nil
	if _, _, err := createWithRetry(context.Background(), Options{Platform: pf, Sleep: o.Sleep}, publish.Request{}, ""); err == nil || len(waits) != 0 {
		t.Errorf("a 403 was retried: %v %v", err, waits)
	}
}

// Under load GitLab showed a pushed branch only after more than a minute
// (2026-11-16, ~3,300 CI jobs an hour): the old minute of retries gave up
// first. The run now waits for the branches API to show the branch at the
// pushed head, past that minute, and asks for the request once.
func TestCreateWaitsPastTheOldMinuteForThePushedBranch(t *testing.T) {
	pf := &platformfake.Platform{
		HiddenBranches: map[string]int{"renovate/x": 8},
		BranchHeads:    map[string]string{"renovate/x": "abc1234"},
	}
	var waits []time.Duration
	o := Options{Platform: pf, Sleep: func(d time.Duration) { waits = append(waits, d) }}
	mr, adopted, err := createWithRetry(context.Background(), o, publish.Request{SourceBranch: "renovate/x"}, "abc1234")
	if err != nil || mr.IID == 0 || adopted {
		t.Fatalf("mr=%+v adopted=%v err=%v", mr, adopted, err)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	if !slices.Equal(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
	var total time.Duration
	for _, w := range waits {
		total += w
	}
	if total <= 62*time.Second {
		t.Errorf("waited %v in all, not past the old budget of 62s", total)
	}
	if n := slices.IndexFunc(pf.Calls, func(c string) bool { return strings.HasPrefix(c, "Create ") }); n < 0 || slices.ContainsFunc(pf.Calls[n+1:], func(c string) bool { return strings.HasPrefix(c, "Create ") }) {
		t.Errorf("want exactly one create, after the branch showed: %v", pf.Calls)
	}
}

// A branch shown at its previous head is not yet the pushed one: the run
// waits for the push, not merely for a branch of that name.
func TestCreateWaitsForThePushedHeadNotAnOldOne(t *testing.T) {
	pf := &platformfake.Platform{BranchHeads: map[string]string{"renovate/x": "0ld0000"}}
	var waits []time.Duration
	o := Options{Platform: pf, Sleep: func(d time.Duration) { waits = append(waits, d) }}
	_, _, err := createWithRetry(context.Background(), o, publish.Request{SourceBranch: "renovate/x"}, "abc1234")
	if err == nil || !strings.Contains(err.Error(), "not yet at the pushed abc1234") {
		t.Errorf("err = %v, want one naming the pushed head", err)
	}
	if slices.ContainsFunc(pf.Calls, func(c string) bool { return strings.HasPrefix(c, "Create ") }) {
		t.Errorf("created against the old head: %v", pf.Calls)
	}
}

// The wait is bounded: five minutes, then the error says what was waited
// for and how long, and nothing was created.
func TestCreateGivesUpAfterTheBranchBudgetNamingTheWait(t *testing.T) {
	pf := &platformfake.Platform{HiddenBranches: map[string]int{"renovate/x": 1000}}
	var total time.Duration
	o := Options{Platform: pf, Sleep: func(d time.Duration) { total += d }}
	_, _, err := createWithRetry(context.Background(), o, publish.Request{SourceBranch: "renovate/x"}, "abc1234")
	if err == nil {
		t.Fatal("no error after the budget")
	}
	for _, want := range []string{"waited 5m0s for the platform to see the pushed branch", "no branch renovate/x at abc1234"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	if total != branchBudget {
		t.Errorf("waited %v, want the budget %v", total, branchBudget)
	}
	if slices.ContainsFunc(pf.Calls, func(c string) bool { return strings.HasPrefix(c, "Create ") }) {
		t.Errorf("created a request for a branch never seen: %v", pf.Calls)
	}

	// A branch the branches API shows but the create still calls missing
	// is waited out within the same budget, and the create's own answer
	// is what the error carries.
	pf = &platformfake.Platform{CreateErr: errors.New(`gitlab: status 400: {"source_branch":["does not exist"]}`)}
	total = 0
	o.Platform = pf
	_, _, err = createWithRetry(context.Background(), o, publish.Request{SourceBranch: "renovate/x"}, "abc1234")
	if err == nil || !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "waited 5m0s") {
		t.Errorf("err = %v", err)
	}
	if total != branchBudget {
		t.Errorf("waited %v, want the budget %v", total, branchBudget)
	}
}

// A 409 "another open merge request already exists" is a concurrent run
// that opened the request first: it is looked up and adopted, not failed.
func TestCreateAdoptsTheRequestAConcurrentRunOpened(t *testing.T) {
	pf := &platformfake.Platform{
		CreateErr: fmt.Errorf("gitlab: status 409 for project %q: Another open merge request already exists for this source branch: !7: %w", "g/p", publish.ErrAlreadyOpen),
		MRs:       []publish.MergeRequest{{IID: 7, State: "opened", SourceBranch: "renovate/x"}},
	}
	o := Options{Platform: pf, Sleep: func(d time.Duration) { t.Errorf("waited %v on a 409", d) }}
	mr, adopted, err := createWithRetry(context.Background(), o, publish.Request{SourceBranch: "renovate/x"}, "abc1234")
	if err != nil || !adopted || mr.IID != 7 {
		t.Errorf("mr=%+v adopted=%v err=%v, want !7 adopted", mr, adopted, err)
	}
}

// End to end: the run whose create meets a request a concurrent run opened
// brings that request in line - title, description, labels - and reports
// it updated, not created and not failed.
func TestARequestOpenedConcurrentlyIsUpdatedNotFailed(t *testing.T) {
	_, repo := fixture(t)
	pf := &platformfake.Platform{CreateErr: fmt.Errorf("gitlab: status 409: Another open merge request already exists for this source branch: %w", publish.ErrAlreadyOpen)}
	p := plan(model.Branch{Name: "renovate/a", Title: "chore(deps): update a", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit("3.20", "3.21")}})
	outs, err := Execute(context.Background(), p, options(repo, pf))
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Action != "updated" || outs[0].MRIID != 1 {
		t.Fatalf("outcomes %+v, want !1 updated", outs)
	}
	if p.Branches[0].SuppressedBy == model.BlockPublishFailed {
		t.Error("the branch is marked failed")
	}
	if len(pf.MRs) != 1 || pf.MRs[0].Title != "chore(deps): update a" || !strings.Contains(pf.MRs[0].Description, "by pinup") {
		t.Errorf("the adopted request was not brought in line: %+v", pf.MRs)
	}
	if !slices.ContainsFunc(p.Warnings, func(w model.Warning) bool { return strings.Contains(w.Msg, "concurrent run") }) {
		t.Errorf("no warning names the concurrent run: %+v", p.Warnings)
	}
}

// Automerge is withheld only for a change that merged before and was
// taken back - the same edits, not the same title: a security bump of
// the same module in other files shares Renovate's title and branch, and
// Renovate held it for two days for that (wolfi-packages!404).
func TestAutomergeIsWithheldForARevertedChangeNotForASharedTitle(t *testing.T) {
	_, repo := fixture(t)
	edit1 := edit("3.20", "3.21")
	mk := func() *model.Plan {
		p := plan(model.Branch{Name: "renovate/go-x-crypto-vulnerability", Title: "fix(deps): update module x/crypto to v0.56.0 [security]", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit1}, Automerge: true})
		return p
	}
	// A merged request on the branch with the same title but other edits.
	pf := &platformfake.Platform{Verification: "verified", MRs: []publish.MergeRequest{{
		IID: 182, State: "merged", SourceBranch: "renovate/go-x-crypto-vulnerability",
		Title:       "fix(deps): update module x/crypto to v0.56.0 [security]",
		Description: "| File | Change |\n|---|---|\n| `other/Containerfile` | `3.20` → `3.21` |\n",
	}}}
	p := mk()
	out, err := Execute(context.Background(), p, options(repo, pf))
	if err != nil || len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("%v %+v", err, out)
	}
	if !pf.MRs[len(pf.MRs)-1].Automerge {
		t.Error("a shared title alone withheld automerge")
	}
	if wanted, _, _ := automergeWanted(pf.MRs[len(pf.MRs)-1].Description); !wanted {
		t.Error("a request that wants automerge carries no marker for the arm watch")
	}

	// The same edits merged before: withheld, said so, request still opened.
	pf = &platformfake.Platform{Verification: "verified", MRs: []publish.MergeRequest{{
		IID: 182, State: "merged", SourceBranch: "renovate/go-x-crypto-vulnerability",
		Title:       "fix(deps): update module x/crypto to v0.56.0 [security]",
		Description: "| File | Change |\n|---|---|\n| `Containerfile` | `3.20` → `3.21` |\n",
	}}}
	_, repo2 := fixture(t)
	p = mk()
	out, err = Execute(context.Background(), p, options(repo2, pf))
	if err != nil || len(out) != 1 || out[0].Action != "created" {
		t.Fatalf("%v %+v", err, out)
	}
	last := pf.MRs[len(pf.MRs)-1]
	if last.Automerge || !strings.Contains(last.Description, "Automerge withheld: !182") {
		t.Errorf("reverted change: automerge=%v description:\n%s", last.Automerge, last.Description)
	}
	// Withheld means no marker either: the arm watch must not arm what
	// the run decided against.
	if strings.Contains(last.Description, markerPrefix) {
		t.Errorf("a withheld automerge carries the marker:\n%s", last.Description)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[len(p.Warnings)-1].Msg, "automerge withheld") {
		t.Errorf("the plan does not say: %+v", p.Warnings)
	}

	// The next run finds the request open and withholds again: it used to
	// arm it and drop the note (collab-rte!248, 2026-11-12).
	p = mk()
	if _, err := Execute(context.Background(), p, options(repo2, pf)); err != nil {
		t.Fatal(err)
	}
	again := pf.MRs[len(pf.MRs)-1]
	if again.IID != last.IID || again.Automerge || !strings.Contains(again.Description, "Automerge withheld: !182") || strings.Contains(again.Description, markerPrefix) {
		t.Errorf("second run on the open request: !%d automerge=%v description:\n%s", again.IID, again.Automerge, again.Description)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[len(p.Warnings)-1].Msg, "automerge withheld, !182") {
		t.Errorf("the second run's plan does not say: %+v", p.Warnings)
	}
}

// A fix for an exploited CVE says so before the table, with the catalog's
// dates; a major one adds that it was opened rather than held. A security
// fix without a KEV mark carries no such line.
func TestDescriptionNamesAnExploitedCVE(t *testing.T) {
	for _, tc := range []struct {
		name    string
		typ     model.UpdateType
		kev     *model.Exploited
		want    []string
		mustNot []string
	}{
		{"major fix", model.UpdateMajor, &model.Exploited{CVE: "CVE-2021-44228", DateAdded: "2021-12-10", DueDate: "2021-12-24", Ransomware: "Known"},
			[]string{"> **Known exploited:** CVE-2021-44228 (`log4j-core`) is in CISA's KEV catalog since 2021-12-10, remediation due 2021-12-24, used in ransomware campaigns. This fix was planned ahead of every other update. **It is a major update**"}, nil},
		{"minor fix", model.UpdateMinor, &model.Exploited{CVE: "CVE-2021-44228", DateAdded: "2021-12-10"},
			[]string{"> **Known exploited:** CVE-2021-44228 (`log4j-core`) is in CISA's KEV catalog since 2021-12-10. This fix was planned ahead of every other update.\n"}, []string{"major update"}},
		{"not listed", model.UpdateMinor, nil, nil, []string{"Known exploited"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := model.Update{DepKey: "maven\x00pom.xml\x00log4j-core", NewValue: "2.17.1", Type: tc.typ, SecurityFix: true,
				Dep: model.Dependency{DepName: "log4j-core", CurrentValue: "2.14.1", Advisories: []model.Advisory{{ID: "GHSA-jfh8-c2jp-5v3q", Exploited: tc.kev}}}}
			got := description(&model.Branch{UpdateKeys: []string{u.Key()}}, []model.Update{u}, "")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("description lacks %q\n%s", w, got)
				}
			}
			for _, w := range tc.mustNot {
				if strings.Contains(got, w) {
					t.Errorf("description has %q\n%s", w, got)
				}
			}
		})
	}
}
