// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

func refDep(src, value, digest string) model.Dependency {
	d := model.Dependency{DepName: "img", CurrentValue: value, CurrentDigest: digest,
		Locus: model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest}}
	if value != "" {
		d.Locus.ValueStart = strings.Index(src, value)
		d.Locus.ValueEnd = d.Locus.ValueStart + len(value)
	}
	if digest != "" {
		d.Locus.DigestStart = strings.Index(src, digest)
		d.Locus.DigestEnd = d.Locus.DigestStart + len(digest)
	}
	return d
}

func apply(src string, e model.Edit) string { return src[:e.Start] + e.New + src[e.End:] }

func TestEditRefMovesExactlyWhatChanges(t *testing.T) {
	const old = "sha256:aaaa"
	const fresh = "sha256:bbbb"
	for _, tc := range []struct {
		name, src, value, digest, newValue, newDigest, want string
	}{
		{"value only", "FROM a:1.0\n", "1.0", "", "1.1", "", "FROM a:1.1\n"},
		{"value and digest", "FROM a:1.0@" + old + "\n", "1.0", old, "1.1", fresh, "FROM a:1.1@" + fresh + "\n"},
		{"digest only", "FROM a:1.0@" + old + "\n", "1.0", old, "1.0", fresh, "FROM a:1.0@" + fresh + "\n"},
		{"digest without a tag", "FROM a@" + old + "\n", "", old, "", fresh, "FROM a@" + fresh + "\n"},
		{"pin a digest", "FROM a:1.0\n", "1.0", "", "1.0", fresh, "FROM a:1.0@" + fresh + "\n"},
		{"a release digest does not pin an unpinned tag", "x@1.0\n", "1.0", "", "1.1", "9b4933", "x@1.1\n"},
	} {
		d := refDep(tc.src, tc.value, tc.digest)
		typ := model.UpdateMinor
		if tc.name == "pin a digest" {
			typ = model.UpdatePinDigest
		}
		e, err := EditRef("t", File{Path: "f", Content: []byte(tc.src)}, model.Update{Dep: d, NewValue: tc.newValue, NewDigest: tc.newDigest, Type: typ})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := apply(tc.src, e); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The guard this helper exists for: a digest-pinned reference is never
// moved by tag alone, because the runtime would keep pulling the old
// digest while the file claims the new tag.
func TestEditRefRefusesANewTagAgainstTheOldDigest(t *testing.T) {
	src := "FROM a:1.0@sha256:aaaa\n"
	d := refDep(src, "1.0", "sha256:aaaa")
	_, err := EditRef("t", File{Path: "f", Content: []byte(src)}, model.Update{Dep: d, NewValue: "1.1"})
	if err == nil || !strings.Contains(err.Error(), "no digest for the new value") {
		t.Fatalf("err = %v", err)
	}
	// And bytes that moved are refused whichever span they are on.
	moved := "FROM a:1.0@sha256:cccc\n"
	if _, err := EditRef("t", File{Path: "f", Content: []byte(moved)}, model.Update{Dep: d, NewValue: "1.1", NewDigest: "sha256:bbbb"}); err == nil || !strings.Contains(err.Error(), "changed since extraction") {
		t.Errorf("moved digest: err = %v", err)
	}
	if _, err := EditRef("t", File{Path: "f", Content: []byte("FROM a:2.0\n")}, model.Update{Dep: refDep(src, "1.0", ""), NewValue: "1.1"}); err == nil {
		t.Error("moved value was not refused")
	}
	if _, err := EditRef("t", File{Path: "f", Content: []byte(src)}, model.Update{Dep: d, NewValue: "1.0", NewDigest: "sha256:aaaa"}); err == nil {
		t.Error("an update that changes nothing was not refused")
	}
	// A new tag the planner resolved to the digest already pinned - a
	// release that changed nothing in the image - moves the tag alone.
	e, err := EditRef("t", File{Path: "f", Content: []byte(src)}, model.Update{Dep: d, NewValue: "1.1", NewDigest: "sha256:aaaa"})
	if err != nil {
		t.Fatalf("same digest, looked up: %v", err)
	}
	if got := src[:e.Start] + e.New + src[e.End:]; got != "FROM a:1.1@sha256:aaaa\n" {
		t.Errorf("edited = %q", got)
	}
}

// A value and a digest written apart - `tag:` on one line and
// `expected-commit:` on the next, the koh/gitlab-checkout shape in melange
// recipes - move as two edits in file order. EditRef, which hands back one
// edit, refuses the case rather than moving the tag alone (measured
// 2026-10-14/15 in devops/wolfi-packages: seven epoch-only merge requests).
func TestEditRefsMovesATagAndItsCommitWrittenApart(t *testing.T) {
	const oldCommit = "d2463c13229a748e589111696df1320b0714d487"
	const newCommit = "c509e9cf891b81cc7a42b5f875b27154201887e0"
	src := "    with:\n      tag: v1.0.1\n      expected-commit: " + oldCommit + "\n      destination: x\n"
	d := refDep(src, "v1.0.1", oldCommit)
	up := model.Update{Dep: d, NewValue: "v1.0.2", NewDigest: newCommit, Type: model.UpdatePatch}
	edits, err := EditRefs("t", File{Path: "f", Content: []byte(src)}, up)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 || edits[0].Start >= edits[1].Start || edits[0].End > edits[1].Start {
		t.Fatalf("edits = %+v", edits)
	}
	out := src
	for i := len(edits) - 1; i >= 0; i-- {
		out = apply(out, edits[i])
	}
	want := "    with:\n      tag: v1.0.2\n      expected-commit: " + newCommit + "\n      destination: x\n"
	if out != want {
		t.Errorf("got %q", out)
	}
	if _, err := EditRef("t", File{Path: "f", Content: []byte(src)}, up); err == nil || !strings.Contains(err.Error(), "two") {
		t.Errorf("EditRef on separated spans: err = %v", err)
	}
	// The contiguous shape still comes back as one edit through EditRefs.
	joined := "FROM a:1.0@sha256:aaaa\n"
	one, err := EditRefs("t", File{Path: "f", Content: []byte(joined)}, model.Update{Dep: refDep(joined, "1.0", "sha256:aaaa"), NewValue: "1.1", NewDigest: "sha256:bbbb", Type: model.UpdateMinor})
	if err != nil || len(one) != 1 || apply(joined, one[0]) != "FROM a:1.1@sha256:bbbb\n" {
		t.Errorf("joined: %+v, %v", one, err)
	}
}
