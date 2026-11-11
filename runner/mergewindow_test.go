// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

// nightWindow is devops/koh-gitops' NIGHT WINDOW rule: images that
// restart a tenant's database, cache or proxy pod.
var nightWindow = model.Window{Expr: "* 3-5 * * *", Kind: "cron", Timezone: "Europe/Berlin", Raw: []string{"* 3-5 * * *"}}

// berlin is a wall-clock moment in Europe/Berlin on the incident's day.
func berlin(t *testing.T, hhmm string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", "2026-11-11 "+hhmm, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestArmDecisionFollowsTheMergeWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		window   model.Window
		at       string
		watch    bool
		arm      bool
		deferred bool
	}{
		{"no window arms at any hour", model.Window{}, "09:13", true, true, false},
		{"outside the window, with the watch", nightWindow, "09:13", true, false, true},
		{"outside the window, without the watch", nightWindow, "09:13", false, false, true},
		{"inside the window, the watch merges it once green", nightWindow, "05:18", true, false, false},
		{"inside the window, without the watch the run arms it", nightWindow, "05:18", false, true, false},
		{"the first minute after the window", nightWindow, "06:00", false, false, true},
		{"the last minute of the window", nightWindow, "05:59", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arm, note, err := armDecision(tc.window, berlin(t, tc.at), tc.watch)
			if err != nil {
				t.Fatal(err)
			}
			if arm != tc.arm || (note != "") != tc.deferred {
				t.Errorf("arm=%v note=%q, want arm=%v deferred=%v", arm, note, tc.arm, tc.deferred)
			}
			if tc.deferred && (!strings.Contains(note, "automerge deferred") || !strings.Contains(note, "2026-11-12 03:00 CET")) {
				t.Errorf("the deferral does not say why and when: %q", note)
			}
		})
	}
}

// devops/koh-gitops!8807, replayed through the run: the request was open
// from inside the window, the four-hourly run at 08:56 Berlin kept it
// current - and must neither arm it nor leave an older arm in place.
func TestARunOutsideTheWindowKeepsTheRequestCurrentAndDoesNotMergeIt(t *testing.T) {
	for _, watch := range []bool{true, false} {
		t.Run(map[bool]string{true: "with the arm watch", false: "without it"}[watch], func(t *testing.T) {
			_, repo := fixture(t)
			pf := &platformfake.Platform{Verification: "verified"}
			b := model.Branch{Name: "renovate/mariadb", Title: "mariadb", UpdateKeys: []string{"mariadb"},
				Edits: []model.Edit{edit("3.20", "3.21")}, Automerge: true, MergeWindow: nightWindow}
			o := options(repo, pf)
			o.MergeWatch = watch
			o.Now, o.Clock = berlin(t, "05:18"), func() time.Time { return berlin(t, "05:18") }
			if _, err := Execute(context.Background(), plan(b), o); err != nil {
				t.Fatal(err)
			}
			// An arm left from inside the window, by an older pinup.
			pf.MRs[0].Automerge = true
			b.Edits = []model.Edit{edit("3.20", "3.22")}
			b.UpdateKeys = []string{"mariadb"}
			// Planned at 08:48, published at 08:56: the clock decides.
			o.Now, o.Clock = berlin(t, "08:48"), func() time.Time { return berlin(t, "08:56") }
			outcomes, err := Execute(context.Background(), plan(b), o)
			if err != nil {
				t.Fatal(err)
			}
			if pf.MRs[0].Automerge {
				t.Error("the request stays armed outside its merge window")
			}
			last := pf.Requests[len(pf.Requests)-1]
			if last.Automerge {
				t.Errorf("the run asked to arm the request outside its window: %+v", last)
			}
			if len(outcomes) != 1 || !strings.Contains(outcomes[0].Message, "automerge deferred: outside the merge window") {
				t.Errorf("the run's line does not say the merge was deferred: %+v", outcomes)
			}
			wanted, _, w := automergeWanted(pf.MRs[0].Description)
			if !wanted || !slices.Equal(w.Raw, nightWindow.Raw) || w.Timezone != "Europe/Berlin" {
				t.Errorf("the marker lost the decision or the window (wanted=%v window=%+v):\n%s", wanted, w, pf.MRs[0].Description)
			}
		})
	}
}

// Inside the window the run arms only where nothing else merges: with the
// arm watch it never arms, since an arm outlasts the window; without it,
// the platform's own automerge is the only merge there is.
func TestARunInsideTheWindowArmsOnlyWithoutTheWatch(t *testing.T) {
	for _, watch := range []bool{true, false} {
		t.Run(map[bool]string{true: "with the arm watch", false: "without it"}[watch], func(t *testing.T) {
			_, repo := fixture(t)
			pf := &platformfake.Platform{Verification: "verified"}
			o := options(repo, pf)
			o.MergeWatch = watch
			o.Clock = func() time.Time { return berlin(t, "04:10") }
			p := plan(model.Branch{Name: "renovate/mariadb", Title: "mariadb", UpdateKeys: []string{"mariadb"},
				Edits: []model.Edit{edit("3.20", "3.21")}, Automerge: true, MergeWindow: nightWindow})
			outcomes, err := Execute(context.Background(), p, o)
			if err != nil {
				t.Fatal(err)
			}
			if pf.MRs[0].Automerge == watch {
				t.Errorf("armed=%v with watch=%v", pf.MRs[0].Automerge, watch)
			}
			if strings.Contains(outcomes[0].Message, "deferred") {
				t.Errorf("a merge inside the window was reported deferred: %q", outcomes[0].Message)
			}
		})
	}
}

// A branch without a window arms as before, whatever the hour and the
// watch - the change is confined to windowed merges.
func TestABranchWithoutAWindowArmsAtAnyHour(t *testing.T) {
	_, repo := fixture(t)
	pf := &platformfake.Platform{Verification: "verified"}
	o := options(repo, pf)
	o.MergeWatch = true
	o.Clock = func() time.Time { return berlin(t, "09:13") }
	p := plan(model.Branch{Name: "renovate/a", Title: "a", UpdateKeys: []string{"a"}, Edits: []model.Edit{edit("3.20", "3.21")}, Automerge: true})
	if _, err := Execute(context.Background(), p, o); err != nil {
		t.Fatal(err)
	}
	if !pf.MRs[0].Automerge {
		t.Error("an automerge without a window was not armed")
	}
	if _, _, w := automergeWanted(pf.MRs[0].Description); len(w.Raw) > 0 {
		t.Errorf("a marker without a window carries one: %+v", w)
	}
}

func TestTheMarkerCarriesTheWindowAndRefusesWhatItCannotRead(t *testing.T) {
	two := model.Window{Timezone: "Europe/Berlin", Raw: []string{"* 3-5 * * *", "after 10pm"}}
	desc := withAutomergeMarker("| File | Change |\n", true, true, two)
	if !strings.HasSuffix(desc, "<!-- pinup:automerge direct tz=Europe/Berlin schedule=* 3-5 * * * | after 10pm -->\n") {
		t.Fatalf("marker as written:\n%s", desc)
	}
	wanted, direct, w := automergeWanted(desc)
	if !wanted || !direct || !slices.Equal(w.Raw, two.Raw) || w.Timezone != two.Timezone {
		t.Errorf("read back as wanted=%v direct=%v window=%+v", wanted, direct, w)
	}
	for _, line := range []string{
		"<!-- pinup:automerge tz=Europe/Berlin -->",
		"<!-- pinup:automerge tz= schedule=* 3-5 * * * -->",
		"<!-- pinup:automerge schedule=* 3-5 * * * -->",
		"<!-- pinup:automerge direct later -->",
		"<!-- pinup:automergedirect -->",
		"<!-- pinup:automerge tz=Europe/Berlin schedule= -->",
	} {
		if wanted, _, _ := automergeWanted("x\n\n" + line); wanted {
			t.Errorf("%q read as a marker", line)
		}
	}
	// The markers every earlier pinup wrote read as they always did.
	for line, direct := range map[string]bool{automergeMarker: false, directMarker: true} {
		if wanted, d, w := automergeWanted("x\n\n" + line); !wanted || d != direct || len(w.Raw) > 0 {
			t.Errorf("%q read as wanted=%v direct=%v window=%+v", line, wanted, d, w)
		}
	}
}

// windowed is an armable request whose marker carries the night window.
func windowed(iid int) (publish.MergeRequest, string) {
	return armable(iid, "<!-- pinup:automerge direct tz=Europe/Berlin schedule=* 3-5 * * * -->")
}

// The arm watch is what merged devops/koh-gitops!8807 at 09:13 Berlin: a
// marker, a green pipeline for the head, the bot's commits. Each case is
// one moment and one state of the request.
func TestTheArmWatchMergesAWindowedRequestOnlyInsideItsWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		at       string
		armed    bool
		other    string
		pipeline string
		action   string
		calls    []string // the writes expected, in order
	}{
		{"the incident: green at 09:13, outside", "09:13", false, "", "success", "deferred", nil},
		{"armed inside, the window closed", "06:20", true, "", "running", "disarmed", []string{"CancelAutomerge"}},
		{"armed by a person, outside: theirs", "09:13", true, "someone", "running", "skipped", []string{"CancelAutomerge"}},
		{"green inside the window: merged, never armed", "04:40", false, "", "success", "merged", []string{"MergeNow"}},
		{"running inside the window: not armed", "04:40", false, "", "running", "skipped", nil},
		{"armed inside the window: taken back, merged once green", "04:40", true, "", "success", "merged", []string{"CancelAutomerge", "MergeNow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr, k := windowed(1)
			mr.Automerge = tc.armed
			w := &platformfake.ArmWatcher{
				MRs:          []publish.MergeRequest{mr},
				Pipelines:    map[string]platformfake.Pipeline{k: {Status: tc.pipeline, SHA: "head"}},
				Authors:      map[string][]string{k: {botEmail}},
				MergeableNow: map[string]bool{k: true},
				ArmedByOther: map[string]string{k: tc.other},
			}
			got, err := ArmWatch(context.Background(), w, ArmWatchOptions{Label: "pinup", AuthorEmail: botEmail, Now: func() time.Time { return berlin(t, tc.at) }})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Action != tc.action {
				t.Fatalf("got %v, want %s", got, tc.action)
			}
			if tc.action == "deferred" && !strings.Contains(got[0].Reason, "next opens 2026-11-12 03:00 CET") {
				t.Errorf("the deferral does not say when: %q", got[0].Reason)
			}
			var writes []string
			for _, c := range w.Calls {
				name, _, _ := strings.Cut(c, " ")
				if name == "ArmAutomerge" || name == "MergeNow" || name == "CancelAutomerge" {
					writes = append(writes, name)
				}
			}
			if !slices.Equal(writes, tc.calls) {
				t.Errorf("writes %v, want %v (calls %v)", writes, tc.calls, w.Calls)
			}
		})
	}
}

// Without a clock no window can be judged, and nothing is merged.
func TestTheArmWatchWithoutAClockLeavesWindowedRequestsAlone(t *testing.T) {
	mr, k := windowed(1)
	w := &platformfake.ArmWatcher{
		MRs:          []publish.MergeRequest{mr},
		Pipelines:    map[string]platformfake.Pipeline{k: {Status: "success", SHA: "head"}},
		Authors:      map[string][]string{k: {botEmail}},
		MergeableNow: map[string]bool{k: true},
	}
	got, err := ArmWatch(context.Background(), w, ArmWatchOptions{Label: "pinup", AuthorEmail: botEmail})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Action != "skipped" || len(w.Armed)+len(w.Merged) > 0 {
		t.Errorf("got %v, armed %v, merged %v", got, w.Armed, w.Merged)
	}
}
