// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gitlabci

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
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
	// The templated host is recorded verbatim, as Renovate records it, for
	// a rule to rewrite; the literal host is a registry.
	for _, d := range res.Deps {
		switch d.DepName {
		case "devops/ci-cd-components/lint-tools":
			if d.SkipReason != "" || len(d.RegistryURLs) != 1 || d.RegistryURLs[0] != "https://${CI_SERVER_HOST}" {
				t.Errorf("templated host: skip=%q registry=%v", d.SkipReason, d.RegistryURLs)
			}
		case "devops/ci-cd-components/release-tools":
			if d.SkipReason != "" || len(d.RegistryURLs) != 1 || d.RegistryURLs[0] != "https://git.example" {
				t.Errorf("literal host: skip=%q registry=%v", d.SkipReason, d.RegistryURLs)
			}
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

// Live, a component include once came out as "1.63.3@9b49...": the
// gitlab-tags release carries its commit id as a digest, and the digest
// path appended it. A release's digest never pins an unpinned reference;
// only a pinDigest update does that.
func TestAReleaseDigestDoesNotPinAComponentInclude(t *testing.T) {
	src := "include:\n  - component: git.example/devops/ci-cd-components/lint-tools/lint-ci-yaml@1.22.3\n"
	f := extract.File{Path: ".gitlab-ci.yml", Content: []byte(src)}
	res, _ := run(t, src), 0
	up := model.Update{Dep: res.Deps[0], NewValue: "1.63.3", NewVersion: "1.63.3", NewDigest: "9b49336126056907f46fcc0f954acb62639c1fbe", Type: model.UpdateMinor}
	e, err := New().Edit(context.Background(), f, up)
	if err != nil {
		t.Fatal(err)
	}
	if out := src[:e.Start] + e.New + src[e.End:]; out != strings.Replace(src, "1.22.3", "1.63.3", 1) {
		t.Errorf("applied edit = %q", out)
	}
}

func TestInvalidYAMLIsAWarningNotAFailure(t *testing.T) {
	res := run(t, "job:\n  image: [unclosed\n")
	if len(res.Warnings) != 1 || len(res.Deps) != 0 {
		t.Errorf("warnings=%d deps=%d; a broken file should warn and yield nothing", len(res.Warnings), len(res.Deps))
	}
}

// TestAgreesWithTheCorpus compares this manager's extraction of every
// pipeline file in the fixture root's corpus against what the pinned
// Renovate container extracted from the same file: every actionable image
// and component the capture records, pinup records, and nothing more. A
// capture whose checkout is not on this machine is skipped; the public
// root's repositories are in the tree. This is the manager's acceptance
// test.
func TestAgreesWithTheCorpus(t *testing.T) {
	type key struct{ depName, value, digest, datasource string }
	ran := 0
	for _, c := range fixture.Corpora(t) {
		for _, e := range c.Entries(t, "gitlabci") {
			if c.Tree == "" {
				t.Logf("%s: checkout not present; %s skipped", c.Name, e.PackageFile)
				continue
			}
			ran++
			t.Run(c.Name+"/"+e.PackageFile, func(t *testing.T) {
				var deps []struct {
					DepName       string `json:"depName"`
					CurrentValue  string `json:"currentValue"`
					CurrentDigest string `json:"currentDigest"`
					Datasource    string `json:"datasource"`
					SkipReason    string `json:"skipReason"`
				}
				if err := json.Unmarshal(e.Deps, &deps); err != nil {
					t.Fatal(err)
				}
				want := map[key]int{}
				for _, d := range deps {
					if d.SkipReason != "" {
						continue
					}
					want[key{d.DepName, d.CurrentValue, d.CurrentDigest, d.Datasource}]++
				}
				if len(want) == 0 {
					t.Fatalf("the capture records nothing actionable in %s; the test would check nothing", e.PackageFile)
				}
				src := c.Read(t, e.PackageFile)
				res, err := New().Extract(context.Background(), extract.File{Path: e.PackageFile, Content: src}, extract.ManagerConfig{})
				if err != nil {
					t.Fatal(err)
				}
				got := map[key]int{}
				for _, d := range res.Deps {
					if d.SkipReason != "" {
						continue
					}
					got[key{d.DepName, d.CurrentValue, d.CurrentDigest, d.Datasource}]++
					if slice := string(src[d.Locus.ValueStart:d.Locus.ValueEnd]); slice != d.CurrentValue {
						t.Errorf("%s: offsets point at %q", d.DepName, slice)
					}
				}
				for k, n := range want {
					if got[k] != n {
						t.Errorf("%s %s@%s (%s): the capture has %d, pinup %d", k.depName, k.value, k.digest, k.datasource, n, got[k])
					}
				}
				for k, n := range got {
					if want[k] == 0 {
						t.Errorf("pinup extracted %s %s@%s (%s) x%d, which the capture does not record", k.depName, k.value, k.digest, k.datasource, n)
					}
				}
				t.Logf("%d actionable dependencies compared", len(want))
			})
		}
	}
	if ran == 0 {
		t.Skip("no gitlabci capture with its files on this machine")
	}
}

// A digest-only image is a digest pin of latest and stays actionable; a
// bare name with neither tag nor digest is skipped (measured on a
// Dockerfile, wolfi-packages !426; the planner treats both managers alike).
func TestDigestOnlyImageIsADigestPin(t *testing.T) {
	src := "a:\n  image: registry.example.test/tool@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\nb:\n  image: alpine\n"
	res, err := New().Extract(context.Background(), extract.File{Path: ".gitlab-ci.yml", Content: []byte(src)}, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]model.Dependency{}
	for _, d := range res.Deps {
		byName[d.DepName] = d
	}
	if d := byName["registry.example.test/tool"]; d.SkipReason != "" || d.CurrentValue != "" || !strings.HasPrefix(d.CurrentDigest, "sha256:bbbb") {
		t.Errorf("digest-only image = %+v", d)
	}
	if d := byName["alpine"]; d.SkipReason == "" {
		t.Error("a bare image name must be skipped")
	}
}
