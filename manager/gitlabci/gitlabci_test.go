// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gitlabci

import (
	"context"
	"os"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

func run(t *testing.T, src string) extract.Result {
	t.Helper()
	res, err := New().Extract(context.Background(),
		extract.File{Path: ".gitlab-ci.yml", Content: []byte(src)}, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The three shapes the corpus records, with the offsets checked by slicing
// the source - if the recorded range does not contain exactly the value,
// nothing downstream can edit it.
func TestTheThreeShapes(t *testing.T) {
	src := `include:
  # renovate: datasource=gitlab-tags depName=devops/ci-cd-components/lint-tools
  - component: ${CI_SERVER_HOST}/devops/ci-cd-components/lint-tools/lint-ci-yaml@1
  - component: git.example/devops/ci-cd-components/release-tools/semantic-release@1.17.59

build:
  image: registry.ole-hartwig.eu/devops/ci-mirrors/container-scanning:8.6.34
  services:
    - registry.ole-hartwig.eu/devops/ci-mirrors/alpine:3.22
    - name: "registry.ole-hartwig.eu/devops/ci-mirrors/alpine:3.22"

sign:
  image:
    name: registry.ole-hartwig.eu/devops/ci-mirrors/buildkit:v0.32.2-rootless@sha256:0000000000000000000000000000000000000000000000000000000000000000
`
	res := run(t, src)

	type want struct {
		depType, name, value, ds string
	}
	wants := []want{
		{"repository", "devops/ci-cd-components/lint-tools", "1", "gitlab-tags"},
		{"repository", "devops/ci-cd-components/release-tools", "1.17.59", "gitlab-tags"},
		{"image", "registry.ole-hartwig.eu/devops/ci-mirrors/container-scanning", "8.6.34", "docker"},
		{"image-name", "registry.ole-hartwig.eu/devops/ci-mirrors/alpine", "3.22", "docker"},
		{"image-name", "registry.ole-hartwig.eu/devops/ci-mirrors/alpine", "3.22", "docker"},
		{"image-name", "registry.ole-hartwig.eu/devops/ci-mirrors/buildkit", "v0.32.2-rootless", "docker"},
	}
	if len(res.Deps) != len(wants) {
		for _, d := range res.Deps {
			t.Logf("  got %-11s %-60s %q skip=%q", d.DepType, d.DepName, d.CurrentValue, d.SkipReason)
		}
		t.Fatalf("got %d deps, want %d", len(res.Deps), len(wants))
	}
	seen := map[want]int{}
	for _, d := range res.Deps {
		seen[want{d.DepType, d.DepName, d.CurrentValue, d.Datasource}]++
		if slice := src[d.Locus.ValueStart:d.Locus.ValueEnd]; slice != d.CurrentValue {
			t.Errorf("%s: offsets point at %q, not at %q", d.DepName, slice, d.CurrentValue)
		}
		if d.DepType == "repository" && d.Versioning != "semver-partial" {
			t.Errorf("%s: versioning = %q, want semver-partial", d.DepName, d.Versioning)
		}
	}
	for _, w := range wants {
		if seen[w] == 0 {
			t.Errorf("missing: %+v", w)
		}
	}
	// The digest-pinned buildkit reference must carry its digest span too.
	for _, d := range res.Deps {
		if d.CurrentDigest != "" {
			if got := src[d.Locus.DigestStart:d.Locus.DigestEnd]; got != d.CurrentDigest {
				t.Errorf("digest span points at %q", got[:20])
			}
		}
	}
}

// A quoted scalar is positioned at its quote by the parser. The recorded
// offset must skip it, or an Edit would replace the quote with the tag.
func TestQuotedScalarOffsetsSkipTheQuote(t *testing.T) {
	src := "job:\n  image: \"alpine:3.21\"\n"
	res := run(t, src)
	if len(res.Deps) != 1 {
		t.Fatalf("got %d deps", len(res.Deps))
	}
	d := res.Deps[0]
	if got := src[d.Locus.ValueStart:d.Locus.ValueEnd]; got != "3.21" {
		t.Errorf("offsets point at %q, want 3.21", got)
	}
}

// Values GitLab resolves itself are skipped with a reason rather than looked
// up as image names. Each is a real shape from the estate.
func TestUnresolvableValuesAreSkippedWithAReason(t *testing.T) {
	src := "a:\n  image: $IMAGE\nb:\n  image: ${GO_IMAGE}\nc:\n  image: $[[ inputs.image-config ]]\nd:\n  image: registry/name\n"
	res := run(t, src)
	if len(res.Deps) != 4 {
		t.Fatalf("got %d deps, want 4 recorded skips", len(res.Deps))
	}
	for _, d := range res.Deps {
		if d.SkipReason == "" {
			t.Errorf("%q was not skipped; it cannot be looked up", d.DepName)
		}
	}
}

// A registry port is not a tag. The tag is the colon after the last slash.
func TestSplitImage(t *testing.T) {
	for _, c := range []struct{ ref, name, tag, digest string }{
		{"alpine:3.21", "alpine", "3.21", ""},
		{"registry.example:5000/img:1.0", "registry.example:5000/img", "1.0", ""},
		{"registry.example:5000/img", "registry.example:5000/img", "", ""},
		{"img@sha256:abc", "img", "", "sha256:abc"},
		{"img:1.0@sha256:abc", "img", "1.0", "sha256:abc"},
	} {
		n, tg, dg := splitImage(c.ref)
		if n != c.name || tg != c.tag || dg != c.digest {
			t.Errorf("splitImage(%q) = (%q,%q,%q), want (%q,%q,%q)", c.ref, n, tg, dg, c.name, c.tag, c.digest)
		}
	}
}

func TestEditRefusesAChangedFile(t *testing.T) {
	src := "job:\n  image: alpine:3.20\n"
	f := extract.File{Path: ".gitlab-ci.yml", Content: []byte(src)}
	res, _ := New().Extract(context.Background(), f, extract.ManagerConfig{})
	up := model.Update{Dep: res.Deps[0], NewValue: "3.21"}

	e, err := New().Edit(context.Background(), f, up)
	if err != nil {
		t.Fatal(err)
	}
	if out := src[:e.Start] + e.New + src[e.End:]; out != "job:\n  image: alpine:3.21\n" {
		t.Errorf("applied edit = %q", out)
	}
	changed := extract.File{Path: ".gitlab-ci.yml", Content: []byte("job:\n  image: alpine:9.99\n")}
	if _, err := New().Edit(context.Background(), changed, up); err == nil {
		t.Error("a changed file was accepted")
	}
}

func TestInvalidYAMLIsAWarningNotAFailure(t *testing.T) {
	res := run(t, "job:\n  image: [unclosed\n")
	if len(res.Warnings) != 1 || len(res.Deps) != 0 {
		t.Errorf("warnings=%d deps=%d; a broken file should warn and yield nothing", len(res.Warnings), len(res.Deps))
	}
}

// The real ci-tools pipeline, against what the pinned container extracted
// from it. This is the manager's acceptance test.
func TestRealCiToolsPipeline(t *testing.T) {
	const path = "/Volumes/Samsung_X5/Projects/moselwal/devops/images/ci-tools/.gitlab-ci.yml"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("checkout not present: %v", err)
	}
	res, err := New().Extract(context.Background(),
		extract.File{Path: ".gitlab-ci.yml", Content: src}, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, d := range res.Deps {
		if d.SkipReason != "" {
			continue
		}
		got[d.DepName+"|"+d.CurrentValue]++
		if slice := string(src[d.Locus.ValueStart:d.Locus.ValueEnd]); slice != d.CurrentValue {
			t.Errorf("%s: offsets point at %q", d.DepName, slice)
		}
	}
	// From testdata/parity/renovate-43.288.0/extract/ci-tools.json, gitlabci.
	for _, want := range []string{
		"moby/buildkit|v0.32.2-rootless",
		"registry.ole-hartwig.eu/devops/ci-mirrors/container-scanning|8.6.34",
		"registry.ole-hartwig.eu/devops/ci-mirrors/alpine|3.22",
		"devops/ci-cd-components/lint-tools|1",
		"devops/ci-cd-components/release-tools|1",
		"devops/ci-cd-components/container-scanning|3",
		"devops/ci-cd-components/supply-chain-verify|2",
	} {
		if got[want] == 0 {
			t.Errorf("not extracted: %s", want)
		}
	}
	t.Logf("%d actionable dependencies from the real file", len(got))
}
