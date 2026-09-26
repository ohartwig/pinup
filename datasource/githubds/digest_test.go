// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package githubds

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/lookup"
)

const (
	commitA = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	commitB = "99012661954931238ded8c8b007157a8430204e1"
	tagObjA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tagObjB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// A lightweight tag names its commit; an annotated one names a tag object,
// which is dereferenced to the commit - the SHA a workflow pins. A tag of a
// tag is followed as well. The request count says the walk took exactly the
// hops it needed: none more for a lightweight tag, one per tag object.
func TestDigestIsTheCommitATagPointsTo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tag      string
		want     string
		requests int
	}{
		{name: "lightweight tag", tag: "v7.0.1", want: commitA, requests: 1},
		{name: "annotated tag", tag: "v7.1.0", want: commitB, requests: 2},
		{name: "tag of an annotated tag", tag: "v7.2.0", want: commitB, requests: 3},
		{name: "tag with a slash", tag: "release/1.0", want: commitA, requests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, srv, rec, _ := newFixture(t)
			srv.refs["actions/checkout"] = map[string]gitObject{
				"v7.0.1":      {SHA: commitA, Type: "commit"},
				"v7.1.0":      {SHA: tagObjA, Type: "tag"},
				"v7.2.0":      {SHA: tagObjB, Type: "tag"},
				"release/1.0": {SHA: commitA, Type: "commit"},
			}
			srv.tagObjects["actions/checkout"] = map[string]gitObject{
				tagObjA: {SHA: commitB, Type: "commit"},
				tagObjB: {SHA: tagObjA, Type: "tag"},
			}
			got, err := New(Tags, client, "").Digest(t.Context(), lookup.Ref{Datasource: "github-tags", PackageName: "actions/checkout"}, tc.tag)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Failed() {
				t.Fatalf("the transport was unhappy: %s", rec.String())
			}
			if got != tc.want {
				t.Errorf("Digest(%s) = %s, want %s", tc.tag, got, tc.want)
			}
			if n := len(srv.requests); n != tc.requests {
				t.Errorf("made %d requests, want %d: %v", n, tc.requests, srv.requests)
			}
			if !strings.HasPrefix(srv.requests[0], "/repos/actions/checkout/git/ref/tags/"+tc.tag) {
				t.Errorf("first request %q is not the exact tag ref", srv.requests[0])
			}
		})
	}
}

// What cannot be a pin is an error, never an empty or a wrong SHA: the
// planner writes whatever this answers into the workflow.
func TestDigestRefusesWhatIsNoCommit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tag     string
		status  int
		wantMsg string
	}{
		{name: "no such tag", tag: "v9.9.9", wantMsg: `has no tag "v9.9.9"`},
		{name: "a tag on a tree", tag: "tree-tag", wantMsg: `points to a "tree"`},
		{name: "a malformed commit id", tag: "short", wantMsg: "not a commit SHA"},
		{name: "a loop of tag objects", tag: "loop", wantMsg: "chain of more than"},
		{name: "rate limited", tag: "v7.0.1", status: http.StatusTooManyRequests, wantMsg: "rate limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, srv, _, _ := newFixture(t)
			srv.refs["actions/checkout"] = map[string]gitObject{
				"v7.0.1":   {SHA: commitA, Type: "commit"},
				"tree-tag": {SHA: commitB, Type: "tree"},
				"short":    {SHA: "3d3c42e", Type: "commit"},
				"loop":     {SHA: tagObjA, Type: "tag"},
			}
			srv.tagObjects["actions/checkout"] = map[string]gitObject{tagObjA: {SHA: tagObjA, Type: "tag"}}
			if tc.status != 0 {
				srv.status["actions/checkout"] = tc.status
			}
			got, err := New(Tags, client, "").Digest(t.Context(), lookup.Ref{Datasource: "github-tags", PackageName: "actions/checkout"}, tc.tag)
			if err == nil {
				t.Fatalf("Digest(%s) = %q, want an error", tc.tag, got)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not say %q", err, tc.wantMsg)
			}
		})
	}
}

// A GitHub Enterprise registry changes where the request goes, never which
// repository it names; api.github.com is not asked.
func TestDigestAsksTheDependencysRegistry(t *testing.T) {
	client, srv, rec, rt := newFixture(t)
	rt.Handle("ghe.example", srv)
	srv.refs["octo/tools"] = map[string]gitObject{"v1.0.0": {SHA: commitA, Type: "commit"}}
	got, err := New(Tags, client, "").Digest(t.Context(), lookup.Ref{
		Datasource: "github-tags", PackageName: "octo/tools", RegistryURLs: []string{"https://ghe.example/"},
	}, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	if got != commitA {
		t.Errorf("Digest = %s, want %s", got, commitA)
	}
	if rt.Count("ghe.example") != 1 || rt.Count("api.github.com") != 0 {
		t.Errorf("asked ghe.example %d and api.github.com %d times, want 1 and 0", rt.Count("ghe.example"), rt.Count("api.github.com"))
	}
}
