// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package githubactions

import (
	"os"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

const (
	shaOld = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	shaNew = "99012661954931238ded8c8b007157a8430204e1"
)

func run(t *testing.T, path, src string) extract.Result {
	t.Helper()
	res, err := New().Extract(t.Context(), extract.File{Path: path, Content: []byte(src)}, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// byName indexes the dependencies by depName and fails on a duplicate, so a
// test cannot read the wrong one of two.
func byName(t *testing.T, deps []model.Dependency) map[string]model.Dependency {
	t.Helper()
	out := map[string]model.Dependency{}
	for _, d := range deps {
		if _, dup := out[d.DepName]; dup {
			t.Fatalf("two dependencies named %q", d.DepName)
		}
		out[d.DepName] = d
	}
	return out
}

// The shapes GitHub's workflow syntax allows, with the offsets checked by
// slicing the source: a range that does not hold exactly the value or the
// SHA cannot be edited.
func TestTheShapes(t *testing.T) {
	src := `on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: docker/setup-qemu-action@` + shaOld + `  # v4.4.0
      - name: a sub-path action
        uses: github/codeql-action/init@v3.28.0
      - run: echo no uses here
        with:
          uses: not/a-dependency@v1.0.0
  call:
    uses: octo/workflows/.github/workflows/release.yml@v2.1.0
`
	res := run(t, ".github/workflows/ci.yml", src)
	for _, w := range []struct {
		depName, pkg, value, digest string
	}{
		{"actions/checkout", "actions/checkout", "v7.0.1", ""},
		{"docker/setup-qemu-action", "docker/setup-qemu-action", "v4.4.0", shaOld},
		{"github/codeql-action/init", "github/codeql-action", "v3.28.0", ""},
		{"octo/workflows/.github/workflows/release.yml", "octo/workflows", "v2.1.0", ""},
	} {
		d, ok := byName(t, res.Deps)[w.depName]
		if !ok {
			t.Errorf("missing %s", w.depName)
			continue
		}
		if d.PackageName != w.pkg || d.CurrentValue != w.value || d.CurrentDigest != w.digest {
			t.Errorf("%s: package %q value %q digest %q, want %q %q %q", w.depName, d.PackageName, d.CurrentValue, d.CurrentDigest, w.pkg, w.value, w.digest)
		}
		if d.Datasource != "github-tags" || d.DepType != "action" || d.Manager != "github-actions" || d.SkipReason != "" {
			t.Errorf("%s: datasource %q depType %q manager %q skip %q", w.depName, d.Datasource, d.DepType, d.Manager, d.SkipReason)
		}
		if d.Versioning != "" {
			t.Errorf("%s: versioning %q; the datasource's default is meant to apply", w.depName, d.Versioning)
		}
		if got := src[d.Locus.ValueStart:d.Locus.ValueEnd]; got != w.value {
			t.Errorf("%s: value range holds %q", w.depName, got)
		}
		if w.digest != "" {
			if got := src[d.Locus.DigestStart:d.Locus.DigestEnd]; got != w.digest {
				t.Errorf("%s: digest range holds %q", w.depName, got)
			}
		} else if d.Locus.DigestStart != model.NoDigest {
			t.Errorf("%s: a digest range without a digest", w.depName)
		}
	}
	if len(res.Deps) != 4 {
		t.Errorf("got %d dependencies, want 4 - a uses: under with: is an input, not a step", len(res.Deps))
	}
}

// A composite action's steps are steps too.
func TestACompositeActionsSteps(t *testing.T) {
	src := "name: setup\nruns:\n  using: composite\n  steps:\n    - uses: actions/setup-go@v6.0.0\n"
	res := run(t, "action.yml", src)
	if len(res.Deps) != 1 || res.Deps[0].DepName != "actions/setup-go" || res.Deps[0].CurrentValue != "v6.0.0" {
		t.Fatalf("got %+v", res.Deps)
	}
}

// The comment beside a SHA is read in the spellings people write, and the
// recorded range holds exactly the version - v and all, since the tag is
// called that. A quoted scalar's closing quote is stepped over.
func TestVersionCommentForms(t *testing.T) {
	for _, tc := range []struct {
		line, want string
	}{
		{"      - uses: a/b@" + shaOld + " # v7.0.1\n", "v7.0.1"},
		{"      - uses: a/b@" + shaOld + "  #v7.0.1\n", "v7.0.1"},
		{"      - uses: a/b@" + shaOld + " # 7.0.1\n", "7.0.1"},
		{"      - uses: a/b@" + shaOld + " # v7\n", "v7"},
		{"      - uses: a/b@" + shaOld + " # v7.0.1 - pinned for the scan\n", "v7.0.1"},
		{"      - uses: a/b@" + shaOld + "\t# v1.2.3-rc.1", "v1.2.3-rc.1"},
		{"      - uses: \"a/b@" + shaOld + "\" # v2.0.0\n", "v2.0.0"},
		{"      - uses: 'a/b@" + shaOld + "' # v2.0.0\n", "v2.0.0"},
	} {
		src := "jobs:\n  j:\n    steps:\n" + tc.line
		res := run(t, ".github/workflows/x.yml", src)
		if len(res.Deps) != 1 {
			t.Fatalf("%q: got %d deps", tc.line, len(res.Deps))
		}
		d := res.Deps[0]
		if d.SkipReason != "" || d.CurrentValue != tc.want || d.CurrentDigest != shaOld {
			t.Errorf("%q: value %q digest %q skip %q, want %q", tc.line, d.CurrentValue, d.CurrentDigest, d.SkipReason, tc.want)
			continue
		}
		if got := src[d.Locus.ValueStart:d.Locus.ValueEnd]; got != tc.want {
			t.Errorf("%q: value range holds %q", tc.line, got)
		}
		if got := src[d.Locus.DigestStart:d.Locus.DigestEnd]; got != shaOld {
			t.Errorf("%q: digest range holds %q", tc.line, got)
		}
	}
}

// What cannot be looked up is recorded with the reason, never dropped: a
// plan explains why nothing happens.
func TestWhatIsNotManagedSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		uses, reason string
	}{
		{"./.github/actions/local", "local action"},
		{"docker://alpine:3.22", "docker://"},
		{"${{ matrix.action }}@v1", "expression"},
		{"a/b@" + shaOld, "no version comment"},
		{"a/b@" + shaOld + " # pinned by hand", "no version comment"},
		{"a/b", "no @ref"},
		{"lonely@v1", "owner/repo"},
		{"https://gitea.example/a/b@v1", "URL"},
	} {
		src := "jobs:\n  j:\n    steps:\n      - uses: " + tc.uses + "\n"
		res := run(t, ".github/workflows/x.yml", src)
		if len(res.Deps) != 1 {
			t.Fatalf("%s: got %d deps, want one recorded skip", tc.uses, len(res.Deps))
		}
		if d := res.Deps[0]; !strings.Contains(d.SkipReason, tc.reason) {
			t.Errorf("%s: skip reason %q does not say %q", tc.uses, d.SkipReason, tc.reason)
		}
	}
}

func TestBrokenYAMLIsAWarningNotAnError(t *testing.T) {
	res := run(t, ".github/workflows/x.yml", "jobs: [unclosed\n")
	if len(res.Deps) != 0 || len(res.Warnings) != 1 {
		t.Fatalf("got %d deps and %d warnings, want 0 and 1", len(res.Deps), len(res.Warnings))
	}
}

// Every action this repository's own workflow uses is pinned by SHA with
// its version beside it - the shape this manager exists for. None may be
// skipped.
func TestThisRepositorysWorkflow(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/images.yml")
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, ".github/workflows/images.yml", string(raw))
	if len(res.Deps) < 5 {
		t.Fatalf("found %d actions in images.yml", len(res.Deps))
	}
	for _, d := range res.Deps {
		if d.SkipReason != "" || d.CurrentDigest == "" || d.CurrentValue == "" {
			t.Errorf("%s: value %q digest %q skip %q", d.DepName, d.CurrentValue, d.CurrentDigest, d.SkipReason)
		}
	}
}

// apply writes one edit the way the applier does, after checking it
// replaces what it says it replaces.
func apply(t *testing.T, src string, e model.Edit) string {
	t.Helper()
	if got := src[e.Start:e.End]; got != e.Old {
		t.Fatalf("edit claims to replace %q, the range holds %q", e.Old, got)
	}
	return src[:e.Start] + e.New + src[e.End:]
}

// A SHA pin moves its SHA and its comment's version together, in one edit
// that keeps everything between them; a tag moves as a tag; a pin writes
// the SHA with the version beside it. What would leave the file claiming
// a version it does not run is refused.
func TestEdits(t *testing.T) {
	const pinned = "      - uses: actions/checkout@" + shaOld + "  # v7.0.1\n"
	const tagged = "      - uses: actions/checkout@v7.0.1\n"
	for _, tc := range []struct {
		name    string
		line    string
		up      func(model.Update) model.Update
		want    string
		wantErr string
	}{
		{name: "sha and version move together", line: pinned,
			up:   func(u model.Update) model.Update { u.NewValue, u.NewDigest = "v7.1.0", shaNew; return u },
			want: "      - uses: actions/checkout@" + shaNew + "  # v7.1.0\n"},
		{name: "a new version without a new sha is refused", line: pinned,
			up:      func(u model.Update) model.Update { u.NewValue = "v7.1.0"; return u },
			wantErr: "carries no commit SHA"},
		{name: "a moved tag moves only the sha", line: pinned,
			up: func(u model.Update) model.Update {
				u.NewValue, u.NewDigest, u.Type = u.Dep.CurrentValue, shaNew, model.UpdateDigest
				return u
			},
			want: "      - uses: actions/checkout@" + shaNew + "  # v7.0.1\n"},
		{name: "a new tag on the same commit moves only the comment", line: pinned,
			up:   func(u model.Update) model.Update { u.NewValue, u.NewDigest = "v7.0.2", shaOld; return u },
			want: "      - uses: actions/checkout@" + shaOld + "  # v7.0.2\n"},
		{name: "a tag moves as a tag", line: tagged,
			up:   func(u model.Update) model.Update { u.NewValue = "v7.1.0"; return u },
			want: "      - uses: actions/checkout@v7.1.0\n"},
		{name: "pinning writes the sha and the version beside it", line: tagged,
			up: func(u model.Update) model.Update {
				u.NewValue, u.NewDigest, u.Type = "v7.0.1", shaNew, model.UpdatePinDigest
				return u
			},
			want: "      - uses: actions/checkout@" + shaNew + " # v7.0.1\n"},
		{name: "pinning keeps a closing quote", line: "      - uses: \"actions/checkout@v7.0.1\"\n",
			up: func(u model.Update) model.Update {
				u.NewValue, u.NewDigest, u.Type = "v7.0.1", shaNew, model.UpdatePinDigest
				return u
			},
			want: "      - uses: \"actions/checkout@" + shaNew + "\" # v7.0.1\n"},
		{name: "pinning over a comment is refused", line: "      - uses: actions/checkout@v7.0.1 # keep\n",
			up: func(u model.Update) model.Update {
				u.NewValue, u.NewDigest, u.Type = "v7.0.1", shaNew, model.UpdatePinDigest
				return u
			},
			wantErr: "not pinning over it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "jobs:\n  j:\n    steps:\n" + tc.line
			f := extract.File{Path: ".github/workflows/x.yml", Content: []byte(src)}
			res, err := New().Extract(t.Context(), f, extract.ManagerConfig{})
			if err != nil || len(res.Deps) != 1 {
				t.Fatalf("extract: %v, %d deps", err, len(res.Deps))
			}
			up := tc.up(model.Update{Dep: res.Deps[0], Type: model.UpdateMinor})
			e, err := New().Edit(t.Context(), f, up)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v and edit %+v, want an error saying %q", err, e, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := apply(t, src, e); got != "jobs:\n  j:\n    steps:\n"+tc.want {
				t.Errorf("wrote\n%s\nwant\n%s", got, "jobs:\n  j:\n    steps:\n"+tc.want)
			}
		})
	}
}

// A file that changed since extraction is refused, not patched.
func TestAChangedFileIsRefused(t *testing.T) {
	src := "jobs:\n  j:\n    steps:\n      - uses: actions/checkout@" + shaOld + " # v7.0.1\n"
	f := extract.File{Path: ".github/workflows/x.yml", Content: []byte(src)}
	res, err := New().Extract(t.Context(), f, extract.ManagerConfig{})
	if err != nil || len(res.Deps) != 1 {
		t.Fatalf("extract: %v", err)
	}
	f.Content = []byte(strings.Replace(src, "v7.0.1", "v7.0.9", 1))
	up := model.Update{Dep: res.Deps[0], NewValue: "v7.1.0", NewDigest: shaNew, Type: model.UpdateMinor}
	if e, err := New().Edit(t.Context(), f, up); err == nil || !strings.Contains(err.Error(), "changed since extraction") {
		t.Fatalf("got %v and edit %+v, want a refusal", err, e)
	}
}
