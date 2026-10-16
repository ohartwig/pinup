// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package artifactm

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

// The recipe shape of devops/wolfi-packages on 2026-10-16: a koh/go-raise
// step with a deps block, one module per line. The estate's pin pattern is
// the one its custom manager reads the lines with, so the inserted line is
// a dependency on the next run.
const recipe = `package:
  name: grafana
  epoch: 1
pipeline:
  - uses: koh/go-raise
    with:
      deps: |-
        golang.org/x/crypto@v0.58.0
        google.golang.org/grpc@v1.83.2
  - runs: |
      echo done
`

const after = `\n(?<indent>[ \t]+)[a-z0-9.-]+\.[a-z]{2,}/[^\s@]+@v[0-9][^\s]*`
const line = `{{{indent}}}{{{depName}}}@{{{newValue}}}`

func update(after, line string) model.Update {
	return model.Update{
		Dep: model.Dependency{
			Manager: model.ArtifactManager, File: "grafana.yaml", DepName: "github.com/getkin/kin-openapi", PackageName: "github.com/getkin/kin-openapi",
			CurrentValue: "v0.144.0", Datasource: "go", CustomManager: model.NoCustomManager,
			Artifact: &model.ArtifactRef{Manifest: 0, Source: "artifact audit:published:aarch64:scan-reports/grafana.trivy.json", After: after, Line: line},
		},
		NewValue: "v0.151.0", NewVersion: "v0.151.0", Type: model.UpdateMinor, SecurityFix: true,
	}
}

func TestEditInsertsThePinAfterTheLastAnchor(t *testing.T) {
	m := New()
	e, err := m.Edit(context.Background(), extract.File{Path: "grafana.yaml", Content: []byte(recipe)}, update(after, line))
	if err != nil {
		t.Fatal(err)
	}
	if e.Start != e.End || e.Old != "" || e.Manager != model.ArtifactManager {
		t.Fatalf("edit = %+v, want a zero-width insertion", e)
	}
	got := applied(t, recipe, e)
	want := strings.Replace(recipe, "        google.golang.org/grpc@v1.83.2\n", "        google.golang.org/grpc@v1.83.2\n        github.com/getkin/kin-openapi@v0.151.0\n", 1)
	if string(got) != want {
		t.Errorf("after the edit:\n%s\nwant:\n%s", got, want)
	}
}

// The indentation comes from the anchor line when the pattern binds no
// indent group; a template may also use the match's other groups.
func TestPlaceTakesTheIndentFromTheAnchorLine(t *testing.T) {
	ins, err := Place([]byte(recipe), `google\.golang\.org/grpc@(?<grpc>v[0-9.]+)`, `{{{indent}}}{{{depName}}}@{{{newValue}}} # beside grpc {{{grpc}}}`, update("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if ins.Text != "\n        github.com/getkin/kin-openapi@v0.151.0 # beside grpc v1.83.2" {
		t.Errorf("text = %q", ins.Text)
	}
	if !strings.HasPrefix(recipe[ins.Offset:], "\n  - runs:") {
		t.Errorf("offset %d points at %q", ins.Offset, recipe[ins.Offset:ins.Offset+12])
	}
}

// A file without the anchor line is refused, not patched somewhere else;
// a last line without a line break is still a place.
func TestPlaceRefusesAMissingAnchorAndHandlesTheLastLine(t *testing.T) {
	if _, err := Place([]byte("package:\n  name: x\n"), after, line, update(after, line)); err == nil || !strings.Contains(err.Error(), "matches nothing") {
		t.Fatalf("err = %v", err)
	}
	ins, err := Place([]byte("deps:\n  a.io/b@v1.0.0"), after, line, update(after, line))
	if err != nil {
		t.Fatal(err)
	}
	if ins.Offset != len("deps:\n  a.io/b@v1.0.0") || ins.Text != "\n  github.com/getkin/kin-openapi@v0.151.0" {
		t.Errorf("insertion = %+v", ins)
	}
}

func TestEditRefusesWhatItCannotWrite(t *testing.T) {
	m := New()
	f := extract.File{Path: "grafana.yaml", Content: []byte(recipe)}
	for _, c := range []struct {
		name string
		up   model.Update
		want string
	}{
		{"no manifest", model.Update{Dep: model.Dependency{DepName: "x"}, NewValue: "v1"}, "not read from an artifact manifest"},
		{"no pin", update("", ""), "no pin configured"},
		{"no value", func() model.Update { u := update(after, line); u.NewValue = ""; return u }(), "names no value"},
		{"bad pattern", update(`(`, line), "does not compile"},
		{"template renders nothing", update(after, `{{{nothing}}}`), "rendered nothing"},
		{"two lines", update(after, "{{{depName}}}\n{{{newValue}}}"), "more than one line"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := m.Edit(context.Background(), f, c.up)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// Two pins into the same block are two insertions at the same offset; the
// applier takes both, and neither conflicts with a value edit elsewhere.
func TestTwoPinsDoNotConflict(t *testing.T) {
	m := New()
	f := extract.File{Path: "grafana.yaml", Content: []byte(recipe)}
	a, err := m.Edit(context.Background(), f, update(after, line))
	if err != nil {
		t.Fatal(err)
	}
	other := update(after, line)
	other.Dep.DepName, other.Dep.PackageName, other.NewValue = "golang.org/x/net", "golang.org/x/net", "v0.61.0"
	b, err := m.Edit(context.Background(), f, other)
	if err != nil {
		t.Fatal(err)
	}
	if a.Overlaps(b) {
		t.Fatalf("the two insertions overlap: %+v %+v", a, b)
	}
	got := applied(t, recipe, a, b)
	if !strings.Contains(string(got), "kin-openapi@v0.151.0\n") || !strings.Contains(string(got), "golang.org/x/net@v0.61.0\n") {
		t.Errorf("after both edits:\n%s", got)
	}
}

func TestExtractFindsNothing(t *testing.T) {
	res, err := New().Extract(context.Background(), extract.File{Path: "grafana.yaml", Content: []byte(recipe)}, extract.ManagerConfig{})
	if err != nil || len(res.Deps) != 0 || New().FilePatterns() != nil {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

// applied writes edits into content the way the applier does - from the end
// backwards, each range checked - without importing it: a manager's test
// may reach no stage package but the one that declares its interface.
func applied(t *testing.T, content string, edits ...model.Edit) string {
	t.Helper()
	sorted := append([]model.Edit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start > sorted[j].Start })
	out := content
	for _, e := range sorted {
		if e.Start < 0 || e.End > len(out) || e.Start > e.End || out[e.Start:e.End] != e.Old {
			t.Fatalf("edit %+v does not fit the content", e)
		}
		out = out[:e.Start] + e.New + out[e.End:]
	}
	return out
}
