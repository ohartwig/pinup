// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package helmchart

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/yamlx"
)

func values(t *testing.T, src string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yamlx.Unmarshal([]byte(src), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCollectImagesReadsBothShapes(t *testing.T) {
	got := collectImages(values(t, `
image:
  registry: docker.io
  repository: bitnami/redis
  tag: 7.4.2
controller:
  image: ghcr.io/vendor/controller:v1.2.3
sidecars:
  metrics:
    image:
      repository: quay.io/exporter
      digest: sha256:aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999
initImage: busybox:1.36
`))

	want := map[string]struct {
		ref    string
		pinned bool
	}{
		"controller.image":       {"ghcr.io/vendor/controller:v1.2.3", false},
		"image":                  {"docker.io/bitnami/redis:7.4.2", false},
		"initImage":              {"busybox:1.36", false},
		"sidecars.metrics.image": {"quay.io/exporter@sha256:aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999", true},
	}
	if len(got) != len(want) {
		t.Fatalf("found %d images, want %d: %+v", len(got), len(want), got)
	}
	for _, g := range got {
		w, ok := want[g.path]
		if !ok {
			t.Fatalf("unexpected path %q", g.path)
		}
		if g.ref != w.ref || g.pinned != w.pinned {
			t.Errorf("%s = %q pinned=%v, want %q pinned=%v", g.path, g.ref, g.pinned, w.ref, w.pinned)
		}
	}
}

// A templated value is a PART of a reference, not one. Reporting it would put
// a fragment in the evidence as though it were an image.
func TestCollectImagesSkipsTemplatesAndProse(t *testing.T) {
	got := collectImages(values(t, `
image: "{{ .Values.global.registry }}/app:1.0"
description: "the image is pulled from our registry"
emptyImage: ""
repositoryUrl: https://example.invalid/docs
`))
	for _, g := range got {
		t.Errorf("nothing should be reported, got %s = %q", g.path, g.ref)
	}
}

// The shape that started this: a map with a repository and nothing else.
func TestCollectImagesBareRepository(t *testing.T) {
	got := collectImages(values(t, "foo:\n  repository: registry.example.invalid/ns/app\n"))
	if len(got) != 1 || got[0].ref != "registry.example.invalid/ns/app" || got[0].path != "foo" {
		t.Fatalf("got %+v", got)
	}
}

func TestImagesEvidenceCountsAndDiffs(t *testing.T) {
	old := collectImages(values(t, "image:\n  repository: ns/app\n  tag: 1.0.0\n"))
	new := collectImages(values(t, "image:\n  repository: ns/app\n  tag: 2.0.0\nextra:\n  image: ns/side:1.0\n"))

	notes := imagesEvidence(old, new)
	if len(notes) == 0 {
		t.Fatal("no evidence")
	}
	if !strings.Contains(notes[0], "2 image(s)") || !strings.Contains(notes[0], "2 without a digest") {
		t.Errorf("summary = %q", notes[0])
	}
	joined := strings.Join(notes, " | ")
	if !strings.Contains(joined, "image ns/app:1.0.0 -> ns/app:2.0.0") {
		t.Errorf("no move reported: %s", joined)
	}
	if !strings.Contains(joined, "extra.image added ns/side:1.0") {
		t.Errorf("no addition reported: %s", joined)
	}
}

// Silence would read as "this chart places nothing", which is a different
// statement from "the values name nothing".
func TestImagesEvidenceSaysWhenItFoundNothing(t *testing.T) {
	notes := imagesEvidence(nil, nil)
	if len(notes) != 1 || !strings.Contains(notes[0], "a template may still compose one") {
		t.Fatalf("got %q", notes)
	}
}
