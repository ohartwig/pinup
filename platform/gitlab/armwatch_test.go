// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// The estate-wide listing walks every page, keeps only open requests with
// the label, and names each request's project from references.full.
func TestOpenLabelledListsAcrossProjectsWithTheirPaths(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	a := srv.addProject("group/a", "main")
	b := srv.addProject("group/sub/b", "main")
	a.mrs = []*fakeMR{
		{iid: 1, state: "opened", labels: []string{"pinup"}, draft: true},
		{iid: 2, state: "opened", labels: []string{"other"}},
		{iid: 3, state: "merged", labels: []string{"pinup"}},
		{iid: 4, state: "opened", labels: []string{"pinup", "x"}},
	}
	b.mrs = []*fakeMR{{iid: 7, state: "opened", labels: []string{"pinup"}, automerge: true}}

	got, err := pf.OpenLabelled(context.Background(), "pinup")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range got {
		names = append(names, fmt.Sprintf("%s!%d", m.Project, m.IID))
	}
	if want := []string{"group/a!1", "group/a!4", "group/sub/b!7"}; !slices.Equal(names, want) {
		t.Fatalf("listed %v, want %v", names, want)
	}
	if !got[0].Draft || got[1].Draft || !got[2].Automerge {
		t.Errorf("draft/automerge not carried: %+v", got)
	}
	if n := srv.requestCountOf("GET", "/api/v4/merge_requests?"); n != 2 {
		t.Errorf("%d list pages read, want 2 (page size 2, three requests)", n)
	}
	if pf.Calls() != 2 {
		t.Errorf("Calls() = %d, want 2", pf.Calls())
	}
}

// The per-request reads: the head pipeline from the request itself (none
// is two empty strings), every commit's author, and the cancellations by
// anyone but the token's own user - GitLab's own "aborted" note is not one.
func TestTheArmWatchReadsPipelineAuthorsAndCancels(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	p := srv.addProject("group/a", "main")
	p.mrs = []*fakeMR{
		{iid: 1, state: "opened", sha: "h1", pipelineStatus: "running", pipelineSHA: "h1",
			commitAuthors: []string{"bot@x", "bot@x", "someone@x"},
			notes: []fakeNote{
				{body: "canceled the automatic merge", system: true, authorID: 322, username: "pinup-bot"},
				{body: "aborted the automatic merge because the source branch was updated", system: true, authorID: 5, username: "ole"},
				{body: "canceled the automatic merge", system: false, authorID: 6, username: "talker"},
				{body: "canceled the automatic merge", system: true, authorID: 5, username: "ole"},
			}},
		{iid: 2, state: "opened", sha: "h2"},
	}
	ctx := context.Background()
	status, sha, err := pf.HeadPipeline(ctx, "group/a", 1)
	if err != nil || status != "running" || sha != "h1" {
		t.Errorf("pipeline %q %q %v", status, sha, err)
	}
	if status, sha, err := pf.HeadPipeline(ctx, "group/a", 2); err != nil || status != "" || sha != "" {
		t.Errorf("no pipeline read as %q %q %v", status, sha, err)
	}
	authors, err := pf.CommitAuthors(ctx, "group/a", 1)
	if err != nil || !slices.Equal(authors, []string{"bot@x", "bot@x", "someone@x"}) {
		t.Errorf("authors %v %v", authors, err)
	}
	people, err := pf.AutomergeCanceledBy(ctx, "group/a", 1)
	if err != nil || !slices.Equal(people, []string{"ole"}) {
		t.Errorf("canceled by %v %v, want [ole]", people, err)
	}
	if people, err := pf.AutomergeCanceledBy(ctx, "group/a", 2); err != nil || len(people) != 0 {
		t.Errorf("no notes: %v %v", people, err)
	}
}

// Arming goes through the merge endpoint bound to the head: armed when
// GitLab arms, "not yet" (neither armed nor refused) without a pipeline,
// and with direct a request GitLab calls mergeable is merged.
func TestArmAutomergeIsTheMergeEndpointBoundToTheHead(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	p := srv.addProject("group/a", "main")
	p.mrs = []*fakeMR{
		{iid: 1, state: "opened", sha: "h1", canMerge: true},
		{iid: 2, state: "opened", sha: "h2"},
		{iid: 3, state: "opened", sha: "h3", canMerge: true, acceptsButDoesNotArm: true, detailedMergeStatus: "mergeable"},
	}
	ctx := context.Background()
	mr, err := pf.ArmAutomerge(ctx, "group/a", 1, "h1", false)
	if err != nil || !mr.Automerge || mr.Project != "group/a" {
		t.Errorf("armed: %+v %v", mr, err)
	}
	var body map[string]any
	_ = json.Unmarshal(srv.lastRequestBody("PUT", "/merge_requests/1/merge"), &body)
	if body["sha"] != "h1" || body["auto_merge"] != true {
		t.Errorf("merge call carried %v", body)
	}
	mr, err = pf.ArmAutomerge(ctx, "group/a", 2, "h2", true)
	if err != nil || mr.Automerge || mr.AutomergeRefused != "" || mr.State != "opened" {
		t.Errorf("not yet: %+v %v", mr, err)
	}
	mr, err = pf.ArmAutomerge(ctx, "group/a", 3, "h3", true)
	if err != nil || mr.State != "merged" {
		t.Errorf("direct: %+v %v", mr, err)
	}
}

// A merge inside a window is the merge endpoint without auto_merge, asked
// only of a request GitLab calls mergeable and whose head is the one the
// watch saw: never an arm that could carry the merge past the window.
func TestMergeNowMergesOnlyAMergeableRequestAndNeverArms(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	p := srv.addProject("group/a", "main")
	p.mrs = []*fakeMR{
		{iid: 1, state: "opened", sha: "h1", canMerge: true, detailedMergeStatus: "mergeable"},
		{iid: 2, state: "opened", sha: "h2", canMerge: true, detailedMergeStatus: "ci_still_running"},
		{iid: 3, state: "opened", sha: "h3", canMerge: true, detailedMergeStatus: "mergeable"},
	}
	ctx := context.Background()
	mr, err := pf.MergeNow(ctx, "group/a", 1, "h1")
	if err != nil || mr.State != "merged" || mr.Project != "group/a" {
		t.Errorf("mergeable: %+v %v", mr, err)
	}
	var body map[string]any
	_ = json.Unmarshal(srv.lastRequestBody("PUT", "/merge_requests/1/merge"), &body)
	if body["sha"] != "h1" || body["auto_merge"] != nil || body["merge_when_pipeline_succeeds"] != nil {
		t.Errorf("the merge call carried %v", body)
	}
	if mr, err := pf.MergeNow(ctx, "group/a", 2, "h2"); err != nil || mr.State != "opened" || mr.Automerge {
		t.Errorf("pipeline running: %+v %v", mr, err)
	}
	if mr, err := pf.MergeNow(ctx, "group/a", 3, "older"); err != nil || mr.State != "opened" {
		t.Errorf("another head: %+v %v", mr, err)
	}
	if n := srv.requestCountOf("PUT", "/merge"); n != 1 {
		t.Errorf("%d merge calls, want 1 (only the mergeable request at its head)", n)
	}
}

// The watch takes back its own arm and leaves a person's.
func TestCancelAutomergeTakesBackOnlyTheBotsArm(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	p := srv.addProject("group/a", "main")
	p.mrs = []*fakeMR{
		{iid: 1, state: "opened", sha: "h1", automerge: true},
		{iid: 2, state: "opened", sha: "h2", automerge: true, armedBy: 5},
		{iid: 3, state: "opened", sha: "h3"},
	}
	ctx := context.Background()
	if mr, err := pf.CancelAutomerge(ctx, "group/a", 1); err != nil || mr.Automerge || p.mrs[0].automerge {
		t.Errorf("the bot's arm: %+v %v", mr, err)
	}
	mr, err := pf.CancelAutomerge(ctx, "group/a", 2)
	if err != nil || !mr.Automerge || !p.mrs[1].automerge || mr.AutomergeRefused != "armed by user5; left to them" {
		t.Errorf("a person's arm: %+v %v", mr, err)
	}
	if mr, err := pf.CancelAutomerge(ctx, "group/a", 3); err != nil || mr.Automerge {
		t.Errorf("nothing armed: %+v %v", mr, err)
	}
	if n := srv.requestCountOf("POST", "cancel_auto_merge"); n != 1 {
		t.Errorf("cancel_auto_merge called %d times, want 1", n)
	}
}
