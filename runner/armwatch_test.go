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

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

// The run writes the marker exactly when the request wants automerge, as
// the last line, with "direct" when automergeDirect is on (the default);
// without automerge there is none.
func TestTheDescriptionCarriesTheAutomergeMarkerOnlyWhenAutomergeIsWanted(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name               string
		automerge          bool
		direct             *bool
		wanted, wantDirect bool
	}{
		{"automerge, direct by default", true, nil, true, true},
		{"automerge, not direct", true, &no, true, false},
		{"no automerge", false, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := fixture(t)
			pf := &platformfake.Platform{Verification: "verified"}
			p := plan(model.Branch{Name: "renovate/a", Title: "a", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit("3.20", "3.21")},
				Automerge: tc.automerge, AutomergeDirect: tc.direct})
			if _, err := Execute(context.Background(), p, options(repo, pf)); err != nil {
				t.Fatal(err)
			}
			desc := pf.MRs[0].Description
			wanted, direct := automergeWanted(desc)
			if wanted != tc.wanted || direct != tc.wantDirect {
				t.Errorf("marker read as wanted=%v direct=%v, want %v %v:\n%s", wanted, direct, tc.wanted, tc.wantDirect, desc)
			}
			if !tc.automerge && strings.Contains(desc, markerPrefix) {
				t.Errorf("a request without automerge carries a marker:\n%s", desc)
			}
			// The fingerprint the revert and closed-by-hand checks compare
			// is the file table alone, with or without the marker.
			if editRows(desc) != editRows(description(&p.Branches[0], p.Updates, "")) {
				t.Errorf("the marker changed the edit rows:\n%s", desc)
			}
		})
	}
}

// Foreign text that reads like the marker - a release note, a body - does
// not arm anything: only the last line counts, and a look-alike is
// neutralised before the marker is written.
func TestAMarkerInForeignTextDoesNotCount(t *testing.T) {
	foreign := "notes\n" + directMarker + "\n"
	if wanted, _ := automergeWanted(withAutomergeMarker(foreign, false, false)); wanted {
		t.Error("a marker in the text armed a request without automerge")
	}
	if strings.Contains(withAutomergeMarker(foreign, false, false), markerPrefix) {
		t.Error("the look-alike survived")
	}
	if wanted, direct := automergeWanted(withAutomergeMarker(foreign, true, false)); !wanted || direct {
		t.Errorf("wanted=%v direct=%v, want the run's own marker (not direct)", wanted, direct)
	}
	if wanted, _ := automergeWanted(automergeMarker + "\n\nmore text"); wanted {
		t.Error("a marker that is not the last line counted")
	}
}

const botEmail = "pinup@example.invalid"

// armable is a request the watch arms: marker, unarmed, a pipeline for its
// head, every commit the bot's, no cancel.
func armable(iid int, marker string) (publish.MergeRequest, string) {
	mr := publish.MergeRequest{
		Project: "group/app", IID: iid, State: "opened", SHA: "head",
		Description: "| File | Change |\n\n" + marker + "\n",
	}
	return mr, fmt.Sprintf("%s!%d", mr.Project, mr.IID)
}

func TestTheArmWatchArmsOnlyWhatIsReadyAndTheBots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(mr *publish.MergeRequest, w *platformfake.ArmWatcher, k string)
		action string
		reason string
		// armed is the arm call expected, "" for none.
		armed string
	}{
		{"happy path, direct", func(*publish.MergeRequest, *platformfake.ArmWatcher, string) {}, "armed", "direct", "group/app!1 head true"},
		{"happy path, merge when the pipeline succeeds", func(mr *publish.MergeRequest, _ *platformfake.ArmWatcher, _ string) {
			mr.Description = "x\n\n" + automergeMarker + "\n"
		}, "armed", "pipeline running", "group/app!1 head false"},
		{"direct and mergeable now: merged", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.MergeableNow = map[string]bool{k: true}
			w.Pipelines[k] = platformfake.Pipeline{Status: "success", SHA: "head"}
		}, "merged", "pipeline success", "group/app!1 head true"},
		{"marker missing", func(mr *publish.MergeRequest, _ *platformfake.ArmWatcher, _ string) {
			mr.Description = "| File | Change |\n"
		}, "skipped", "no automerge marker", ""},
		{"already armed", func(mr *publish.MergeRequest, _ *platformfake.ArmWatcher, _ string) { mr.Automerge = true }, "skipped", "already armed", ""},
		{"draft", func(mr *publish.MergeRequest, _ *platformfake.ArmWatcher, _ string) { mr.Draft = true }, "skipped", "draft", ""},
		{"conflicted", func(mr *publish.MergeRequest, _ *platformfake.ArmWatcher, _ string) { mr.Conflict = true }, "skipped", "conflicted", ""},
		{"pipeline missing", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) { delete(w.Pipelines, k) }, "skipped", "no pipeline yet", ""},
		{"pipeline for an older head", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.Pipelines[k] = platformfake.Pipeline{Status: "success", SHA: "older"}
		}, "skipped", "not the head", ""},
		{"pipeline failed", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.Pipelines[k] = platformfake.Pipeline{Status: "failed", SHA: "head"}
		}, "skipped", "pipeline failed", ""},
		{"pipeline canceled", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.Pipelines[k] = platformfake.Pipeline{Status: "canceled", SHA: "head"}
		}, "skipped", "pipeline canceled", ""},
		{"a person's commit on the branch", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.Authors[k] = []string{"someone@example.invalid", botEmail}
		}, "skipped", "someone@example.invalid", ""},
		{"no commits read", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) { delete(w.Authors, k) }, "skipped", "no commits", ""},
		{"a person canceled the automatic merge", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.Cancels = map[string][]string{k: {"ole"}}
		}, "skipped", "canceled by ole", ""},
		{"GitLab not ready", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.NotYet = map[string]bool{k: true}
		}, "not-yet", "", "group/app!1 head true"},
		{"GitLab refuses", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, k string) {
			w.Refuse = map[string]string{k: "the bot may not merge here"}
		}, "refused", "may not merge", "group/app!1 head true"},
		{"a read fails", func(_ *publish.MergeRequest, w *platformfake.ArmWatcher, _ string) {
			w.ReadErr = errors.New("boom")
		}, "failed", "boom", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr, k := armable(1, directMarker)
			w := &platformfake.ArmWatcher{
				Pipelines: map[string]platformfake.Pipeline{k: {Status: "running", SHA: "head"}},
				Authors:   map[string][]string{k: {"PINUP@example.invalid"}},
			}
			tc.change(&mr, w, k)
			w.MRs = []publish.MergeRequest{mr}
			got, err := ArmWatch(context.Background(), w, ArmWatchOptions{Label: "pinup", AuthorEmail: botEmail})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Action != tc.action || !strings.Contains(got[0].Reason, tc.reason) {
				t.Fatalf("got %v, want %s (%s)", got, tc.action, tc.reason)
			}
			var want []string
			if tc.armed != "" {
				want = []string{tc.armed}
			}
			if !slices.Equal(w.Armed, want) {
				t.Errorf("arm calls %v, want %v", w.Armed, want)
			}
		})
	}
}

// A pass is idempotent: what it armed is "already armed" the next time,
// without a further read. Projects outside the filter are not read at all.
func TestTheArmWatchIsIdempotentAndStaysInItsFilter(t *testing.T) {
	mr, k := armable(1, directMarker)
	out := mr
	out.Project, out.IID = "elsewhere/app", 2
	w := &platformfake.ArmWatcher{
		MRs:       []publish.MergeRequest{mr, out},
		Pipelines: map[string]platformfake.Pipeline{k: {Status: "running", SHA: "head"}},
		Authors:   map[string][]string{k: {botEmail}},
	}
	o := ArmWatchOptions{Label: "pinup", AuthorEmail: botEmail, Only: glob.NewSet([]string{"group/**"})}
	first, err := ArmWatch(context.Background(), w, o)
	if err != nil || len(first) != 1 || first[0].Action != "armed" {
		t.Fatalf("first pass: %v %v", first, err)
	}
	calls := len(w.Calls)
	second, err := ArmWatch(context.Background(), w, o)
	if err != nil || len(second) != 1 || second[0].Action != "skipped" || second[0].Reason != "already armed" {
		t.Fatalf("second pass: %v %v", second, err)
	}
	if extra := w.Calls[calls:]; len(extra) != 1 || extra[0] != "OpenLabelled pinup" {
		t.Errorf("the second pass read more than the listing: %v", extra)
	}
	for _, c := range w.Calls {
		if strings.Contains(c, "elsewhere/") {
			t.Errorf("a project outside the filter was read: %s", c)
		}
	}
	w.ListErr = errors.New("listing down")
	if _, err := ArmWatch(context.Background(), w, o); err == nil {
		t.Error("a failed listing is not an error")
	}
}
