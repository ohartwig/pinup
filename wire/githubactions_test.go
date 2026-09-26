// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/planner"
)

// actionsRepo speaks the GitHub endpoints a SHA-pinned action needs: the
// tag listing, the exact tag ref, and the annotated tag object behind it.
// v7.1.0 is annotated, as most action releases are, so the plan only comes
// out right if the tag object is dereferenced to its commit.
type actionsRepo struct{}

const (
	pinnedSHA  = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	releaseSHA = "99012661954931238ded8c8b007157a8430204e1"
	tagObject  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func (actionsRepo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var body any
	switch r.URL.Path {
	case "/repos/actions/checkout/tags":
		body = []map[string]any{{"name": "v7"}, {"name": "v7.1.0"}, {"name": "v7.0.1"}}
	case "/repos/actions/checkout/git/ref/tags/v7":
		// The rolling major tag, moved to the newest release.
		body = map[string]any{"ref": "refs/tags/v7", "object": map[string]any{"sha": releaseSHA, "type": "commit"}}
	case "/repos/actions/checkout/git/ref/tags/v7.1.0":
		body = map[string]any{"ref": "refs/tags/v7.1.0", "object": map[string]any{"sha": tagObject, "type": "tag"}}
	case "/repos/actions/checkout/git/ref/tags/v7.0.1":
		body = map[string]any{"ref": "refs/tags/v7.0.1", "object": map[string]any{"sha": pinnedSHA, "type": "commit"}}
	case "/repos/actions/checkout/git/tags/" + tagObject:
		body = map[string]any{"sha": tagObject, "object": map[string]any{"sha": releaseSHA, "type": "commit"}}
	default:
		w.WriteHeader(http.StatusNotFound)
		body = map[string]any{"message": "Not Found"}
	}
	_ = json.MarshalWrite(w, body)
}

// A workflow pinned by SHA, through every stage the run takes it: the
// manager wire registers extracts it, the github-tags datasource lists the
// tags and resolves the new one to its commit, the planner plans with that
// commit, and the manager's edit moves SHA and comment together.
//
// Under the datasource's default versioning (semver) a full version moves
// to the newest release; a rolling major (`# v7`) is no semver version, so
// the tag stays and the SHA follows the commit the tag points to now.
func TestAShaPinnedActionMovesEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, comment string
		wantType      model.UpdateType
		wantValue     string
	}{
		{name: "a full version moves to the newest release", comment: "v7.0.1", wantType: model.UpdateMinor, wantValue: "v7.1.0"},
		{name: "a rolling major follows its tag", comment: "v7", wantType: model.UpdateDigest, wantValue: "v7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow := "jobs:\n  build:\n    steps:\n      - uses: actions/checkout@" + pinnedSHA + "  # " + tc.comment + "\n"
			rec := &harness.Recorder{}
			rt := harness.NewRefusingTransport(rec)
			rt.Handle("api.github.com", actionsRepo{})
			client := httpx.New(httpx.Options{Transport: rt, Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}})

			m, err := Managers().Get("github-actions")
			if err != nil {
				t.Fatal(err)
			}
			f := extract.File{Path: ".github/workflows/images.yml", Content: []byte(workflow)}
			res, err := m.Extract(t.Context(), f, extract.ManagerConfig{})
			if err != nil || len(res.Deps) != 1 {
				t.Fatalf("extract: %v, %d deps", err, len(res.Deps))
			}

			ds := Datasources(client, DatasourceOptions{})
			fetcher := &lookup.Fetcher{Registry: ds}
			results := fetcher.Fetch(t.Context(), res.Deps)
			plan := planner.Plan(planner.Request{
				Deps: res.Deps,
				Releases: func(d model.Dependency) *model.ReleaseSet {
					return results[lookup.RefOf(d).Key()].Releases
				},
				Versionings:       Versionings(),
				DefaultVersioning: DefaultVersioning(ds),
				Digest: func(d model.Dependency, version string) (string, error) {
					return fetcher.Digest(t.Context(), d, version)
				},
				Now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
			})
			if rec.Failed() {
				t.Fatalf("the transport was unhappy: %s", rec.String())
			}
			if len(plan.Updates) != 1 {
				t.Fatalf("got %d updates (skip %q, warnings %v), want one", len(plan.Updates), plan.Deps[0].SkipReason, plan.Warnings)
			}
			up := plan.Updates[0]
			if up.NewValue != tc.wantValue || up.NewDigest != releaseSHA || up.Type != tc.wantType {
				t.Fatalf("planned %s to %q at %q, want %s to %q at %s", up.Type, up.NewValue, up.NewDigest, tc.wantType, tc.wantValue, releaseSHA)
			}
			e, err := m.Edit(t.Context(), f, up)
			if err != nil {
				t.Fatal(err)
			}
			got := workflow[:e.Start] + e.New + workflow[e.End:]
			want := strings.Replace(workflow, pinnedSHA+"  # "+tc.comment, releaseSHA+"  # "+tc.wantValue, 1)
			if got != want {
				t.Errorf("wrote\n%s\nwant\n%s", got, want)
			}
		})
	}
}
