// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package dockerfile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
)

// depNamed finds the one dependency with the given DepName, or fails the
// test - most of these tests care about one specific line in a multi-line
// fixture, not the whole slice.
func depNamed(t *testing.T, deps []model.Dependency, depName string) model.Dependency {
	t.Helper()
	var matches []model.Dependency
	for _, d := range deps {
		if d.DepName == depName {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("found %d dependencies named %q, want exactly 1 (deps: %+v)", len(matches), depName, deps)
	}
	return matches[0]
}

func extractAll(t *testing.T, src string) (extract.File, []model.Dependency) {
	t.Helper()
	f := extract.File{Path: "Containerfile", Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	return f, res.Deps
}

// multiStage exercises a digest-pinned FROM, a plain-tag FROM and a stage
// referenced later by both FROM and COPY --from.
const multiStage = `FROM registry.example.com/wolfi-base:latest@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa AS base

FROM golang:1.27 AS builder
COPY --from=base /etc/ssl /etc/ssl

FROM base
COPY --from=builder /out/app /app
`

func TestMultiStageContainerfile(t *testing.T) {
	_, deps := extractAll(t, multiStage)

	wolfi := depNamed(t, deps, "registry.example.com/wolfi-base")
	if wolfi.CurrentValue != "latest" {
		t.Errorf("wolfi CurrentValue = %q, want %q", wolfi.CurrentValue, "latest")
	}
	if wolfi.CurrentDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("wolfi CurrentDigest = %q", wolfi.CurrentDigest)
	}
	if wolfi.SkipReason != "" {
		t.Errorf("wolfi carries a digest-pinned tag; it must not be skipped, got reason %q", wolfi.SkipReason)
	}

	golang := depNamed(t, deps, "golang")
	if golang.CurrentValue != "1.27" || golang.SkipReason != "" {
		t.Errorf("golang = %+v, want CurrentValue 1.27 and no skip", golang)
	}

	// COPY --from=builder also names an earlier stage.
	builderRef := depNamed(t, deps, "builder")
	if builderRef.SkipReason == "" {
		t.Error("COPY --from=builder (an earlier stage) was not skipped")
	}

	// "base" is named twice: once by the second FROM (an earlier AS stage,
	// an internal reference) and once by the earlier COPY --from=base (the
	// same stage, referenced before the FROM that repeats its name). Both
	// must be skipped, with a reason.
	var baseCount int
	for _, d := range deps {
		if d.DepName != "base" {
			continue
		}
		baseCount++
		if d.SkipReason == "" {
			t.Errorf("dependency named base at line %d was not skipped: %+v", d.Locus.Line, d)
		}
	}
	if baseCount != 2 {
		t.Errorf("got %d dependencies named base (one FROM, one COPY --from), want 2", baseCount)
	}
}

// The renovate-annotation form used heavily across the estate's Dockerfiles.
const renovateArg = `FROM golang:1.27

# renovate: datasource=custom.wolfi depName=go-1.27 versioning=apk
ARG GO_APK_VERSION=1.27.0-r1
`

func TestAnnotatedArgIsNotThisManagersDependency(t *testing.T) {
	// Measured: Renovate's dockerfile manager reports FROM references
	// only. The annotated ARG is extracted by the
	// customManagers:dockerfileVersions preset (see manager/regexm), and
	// reporting it here too made every such dependency appear twice.
	_, deps := extractAll(t, renovateArg)
	for _, d := range deps {
		if d.DepName == "go-1.27" {
			t.Fatalf("the annotated ARG was extracted by the dockerfile manager: %+v", d)
		}
	}
	if len(deps) != 1 || deps[0].DepName != "golang" {
		t.Errorf("want only the FROM dependency, got %+v", deps)
	}
}

// A manager-config default must apply only when extraction named none.
func TestManagerConfigDefaultsFillGaps(t *testing.T) {
	src := "FROM golang:1.27\n"
	f := extract.File{Path: "Dockerfile", Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{RegistryURLs: []string{"https://mirror.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	dep := depNamed(t, res.Deps, "golang")
	// FROM dependencies always name "docker" versioning themselves, so the
	// config default must not appear.
	if dep.Versioning != "docker" {
		t.Errorf("Versioning = %q, want docker (own extraction wins)", dep.Versioning)
	}
	// FROM dependencies never name a registry URL, so the config default
	// must be stamped on.
	if len(dep.RegistryURLs) != 1 || dep.RegistryURLs[0] != "https://mirror.example.com" {
		t.Errorf("RegistryURLs = %v, want the config default stamped on", dep.RegistryURLs)
	}
}

// Every returned Locus must bracket exactly CurrentValue and, when present,
// CurrentDigest - the invariant everything downstream depends on.
func TestOffsetsMatchCurrentValueAndDigest(t *testing.T) {
	src := multiStage + renovateArg
	f, deps := extractAll(t, src)
	if len(deps) == 0 {
		t.Fatal("no dependencies extracted; nothing to check")
	}
	for _, d := range deps {
		got := string(f.Content[d.Locus.ValueStart:d.Locus.ValueEnd])
		if got != d.CurrentValue {
			t.Errorf("%s: source[%d:%d] = %q, want CurrentValue %q",
				d.DepName, d.Locus.ValueStart, d.Locus.ValueEnd, got, d.CurrentValue)
		}
		if d.Locus.DigestStart == model.NoDigest {
			if d.CurrentDigest != "" {
				t.Errorf("%s: no digest locus but CurrentDigest = %q", d.DepName, d.CurrentDigest)
			}
			continue
		}
		gotDigest := string(f.Content[d.Locus.DigestStart:d.Locus.DigestEnd])
		if gotDigest != d.CurrentDigest {
			t.Errorf("%s: source[%d:%d] = %q, want CurrentDigest %q",
				d.DepName, d.Locus.DigestStart, d.Locus.DigestEnd, gotDigest, d.CurrentDigest)
		}
	}
}

// --- skip cases -------------------------------------------------------------

func TestSkipDigestOnlyNoTag(t *testing.T) {
	src := "FROM golang@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"
	_, deps := extractAll(t, src)
	dep := depNamed(t, deps, "golang")
	if dep.SkipReason == "" {
		t.Error("a digest-only reference with no tag must carry a SkipReason")
	}
	if dep.CurrentValue != "" {
		t.Errorf("CurrentValue = %q, want empty (no tag was written)", dep.CurrentValue)
	}
}

func TestSkipFromScratch(t *testing.T) {
	src := "FROM scratch\n"
	_, deps := extractAll(t, src)
	dep := depNamed(t, deps, "scratch")
	if dep.SkipReason == "" {
		t.Error("FROM scratch must carry a SkipReason")
	}
}

func TestSkipEarlierStageReference(t *testing.T) {
	src := "FROM golang:1.27 AS builder\nFROM builder\n"
	_, deps := extractAll(t, src)
	if len(deps) != 2 {
		t.Fatalf("got %d deps, want 2 (the real FROM and the stage reference)", len(deps))
	}
	// The first FROM is a real dependency.
	real := depNamed(t, deps, "golang")
	if real.SkipReason != "" {
		t.Errorf("the real FROM must not be skipped, got reason %q", real.SkipReason)
	}
	// The second FROM references the first stage by name.
	var stageRefs int
	for _, d := range deps {
		if d.DepName == "builder" {
			stageRefs++
			if d.SkipReason == "" {
				t.Error("FROM builder (an earlier stage) must carry a SkipReason")
			}
		}
	}
	if stageRefs != 1 {
		t.Fatalf("got %d deps named builder, want 1", stageRefs)
	}
}

func TestSkipUnresolvableBuildArgTag(t *testing.T) {
	// TAG is never declared as an ARG anywhere in the file, so it cannot be
	// resolved - exactly the case CLAUDE.md's contract calls out.
	src := "FROM img:${TAG}\n"
	_, deps := extractAll(t, src)
	dep := depNamed(t, deps, "img")
	if dep.SkipReason == "" {
		t.Error("a tag that is an unresolvable build argument must carry a SkipReason")
	}
	if dep.CurrentValue != "${TAG}" {
		t.Errorf("CurrentValue = %q, want the literal ${TAG} text", dep.CurrentValue)
	}
}

// A FROM through global ARGs is the image the ARGs name, and the edit lands
// on the ARG line that carries the tag - measured on a customer repository's
// scheduler Containerfile, where Renovate's replaceString is the ARG line.
func TestFromThroughGlobalArgsEditsTheArgLine(t *testing.T) {
	src := "# renovate: datasource=gitlab-tags depName=devops/images/php-runtime\n" +
		"ARG PHP_RUNTIME_IMAGE_TAG=1.0.5\n" +
		"ARG PHP_RUNTIME_IMAGE=registry.example.test/devops/images/php-runtime\n" +
		"FROM ${PHP_RUNTIME_IMAGE}:${PHP_RUNTIME_IMAGE_TAG}\n" +
		"ARG LATE=9.9.9\n" +
		"FROM $PHP_RUNTIME_IMAGE:$LATE AS late\n" +
		"FROM ${PHP_RUNTIME_IMAGE}:${MISSING:-1.0}\n"
	f := extract.File{Path: "Containerfile", Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var resolved, unresolved []model.Dependency
	for _, d := range res.Deps {
		if d.SkipReason == "" {
			resolved = append(resolved, d)
		} else {
			unresolved = append(unresolved, d)
		}
	}
	if len(resolved) != 1 || len(unresolved) != 2 {
		t.Fatalf("resolved %d, unresolved %d, want 1 and 2: %+v", len(resolved), len(unresolved), res.Deps)
	}
	d := resolved[0]
	if d.DepName != "registry.example.test/devops/images/php-runtime" || d.CurrentValue != "1.0.5" || d.Datasource != "docker" {
		t.Errorf("resolved to %s:%s (%s)", d.DepName, d.CurrentValue, d.Datasource)
	}
	if got := src[d.Locus.ValueStart:d.Locus.ValueEnd]; got != "1.0.5" || !strings.HasSuffix(src[:d.Locus.ValueStart], "ARG PHP_RUNTIME_IMAGE_TAG=") {
		t.Errorf("the locus brackets %q at %d, want the ARG line's value", got, d.Locus.ValueStart)
	}
	up := model.Update{DepKey: d.Key(), Dep: d, NewValue: "1.0.6"}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatal(err)
	}
	if edited := src[:edit.Start] + edit.New + src[edit.End:]; !strings.Contains(edited, "ARG PHP_RUNTIME_IMAGE_TAG=1.0.6\n") || strings.Count(edited, "1.0.6") != 1 {
		t.Errorf("edited file = %q", edited)
	}
	// An ARG declared after the first FROM is not one a FROM may use, and an
	// inline default is unmeasured: both stay held with the literal text.
	for _, u := range unresolved {
		if !strings.Contains(u.CurrentValue, "$") {
			t.Errorf("unresolved reference lost its literal text: %q", u.CurrentValue)
		}
	}
}

// The BuildKit syntax directive names the frontend image and is a
// dependency of depType "syntax", read before the first instruction only.
func TestSyntaxDirectiveIsADependency(t *testing.T) {
	src := "# syntax=docker.io/docker/dockerfile-upstream:1.24.0@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89\n" +
		"FROM alpine:3.22\n" +
		"# syntax=docker/dockerfile:1\n"
	_, deps := extractAll(t, src)
	if len(deps) != 2 {
		t.Fatalf("%d deps, want the directive and the FROM: %+v", len(deps), deps)
	}
	d := depNamed(t, deps, "docker.io/docker/dockerfile-upstream")
	if d.DepType != "syntax" || d.CurrentValue != "1.24.0" || !strings.HasPrefix(d.CurrentDigest, "sha256:87999") || d.SkipReason != "" {
		t.Errorf("directive = %+v", d)
	}
	if got := src[d.Locus.ValueStart:d.Locus.ValueEnd]; got != "1.24.0" {
		t.Errorf("locus brackets %q", got)
	}
	if got := src[d.Locus.DigestStart:d.Locus.DigestEnd]; got != d.CurrentDigest {
		t.Errorf("digest locus brackets %q", got)
	}
}

func TestSkipCopyFromStageIndex(t *testing.T) {
	src := "FROM golang:1.27\nFROM alpine:3.19\nCOPY --from=0 /a /b\n"
	_, deps := extractAll(t, src)
	dep := depNamed(t, deps, "0")
	if dep.SkipReason == "" {
		t.Error("COPY --from=0 (a positional stage index) must carry a SkipReason")
	}
}

// --- Edit --------------------------------------------------------------

// Edit must copy every byte outside the replaced range through unchanged -
// the whole point of returning a byte range instead of rewriting the file.
func TestEditLeavesSurroundingBytesUntouched(t *testing.T) {
	src := "FROM golang:1.27\nRUN echo hi\n"
	f := extract.File{Path: "Dockerfile", Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	dep := depNamed(t, res.Deps, "golang")

	up := model.Update{DepKey: dep.Key(), Dep: dep, NewValue: "1.28"}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}

	// Apply the edit the way `apply` would: splice New in at [Start:End].
	var out []byte
	out = append(out, f.Content[:edit.Start]...)
	out = append(out, edit.New...)
	out = append(out, f.Content[edit.End:]...)

	if got, want := string(out[:edit.Start]), src[:edit.Start]; got != want {
		t.Errorf("bytes before the edit changed: got %q, want %q", got, want)
	}
	wantTail := src[edit.End:]
	gotTail := string(out[edit.Start+len(edit.New):])
	if gotTail != wantTail {
		t.Errorf("bytes after the edit changed: got %q, want %q", gotTail, wantTail)
	}
	if string(out) != "FROM golang:1.28\nRUN echo hi\n" {
		t.Errorf("edited file = %q", string(out))
	}
}

// A digest-only bump must touch only the digest bytes, leaving the tag and
// everything else untouched.
func TestEditDigestOnly(t *testing.T) {
	src := "FROM golang:1.27@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	f := extract.File{Path: "Dockerfile", Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	dep := depNamed(t, res.Deps, "golang")

	newDigest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	up := model.Update{DepKey: dep.Key(), Dep: dep, NewValue: dep.CurrentValue, NewDigest: newDigest}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}
	if edit.Old != dep.CurrentDigest || edit.New != newDigest {
		t.Errorf("edit = %+v", edit)
	}
	var out []byte
	out = append(out, f.Content[:edit.Start]...)
	out = append(out, edit.New...)
	out = append(out, f.Content[edit.End:]...)
	want := "FROM golang:1.27@" + newDigest + "\n"
	if string(out) != want {
		t.Errorf("edited file = %q, want %q", string(out), want)
	}
}

// Edit must refuse rather than guess when the file changed since Extract saw
// it - writing anyway would corrupt whatever is there now.
func TestEditRefusesWhenFileChangedSinceExtraction(t *testing.T) {
	src := "FROM golang:1.27\n"
	f := extract.File{Path: "Dockerfile", Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	dep := depNamed(t, res.Deps, "golang")

	// The file on disk changed at the exact bytes the Locus points at: 1.27
	// became 1.30, a different length, so a stale offset would silently
	// write into the middle of the wrong token.
	changed := extract.File{Path: "Dockerfile", Content: []byte("FROM golang:1.30\n")}
	up := model.Update{DepKey: dep.Key(), Dep: dep, NewValue: "1.28"}
	if _, err := (&Manager{}).Edit(context.Background(), changed, up); err == nil {
		t.Error("Edit did not refuse a file that changed since extraction")
	}
}

// --- iface ---------------------------------------------------------------

func TestNeedsPluginIsNil(t *testing.T) {
	if got := (&Manager{}).NeedsPlugin(); got != nil {
		t.Errorf("NeedsPlugin() = %+v, want nil: this is a pure text manager", got)
	}
}

func TestNameAndFilePatterns(t *testing.T) {
	m := &Manager{}
	if m.Name() != "dockerfile" {
		t.Errorf("Name() = %q, want dockerfile", m.Name())
	}
	if len(m.FilePatterns()) == 0 {
		t.Error("FilePatterns() returned nothing")
	}
}

// --- the real fixture -------------------------------------------------------

// TestAgreesWithTheCorpus compares this manager's extraction of every
// Containerfile and Dockerfile in the fixture root's corpus against what
// the pinned Renovate container extracted from the same file: name, tag,
// digest, datasource, and whether the line was skipped. A capture whose
// checkout is not on this machine is skipped; the public root's
// repositories are in the tree.
func TestAgreesWithTheCorpus(t *testing.T) {
	type key struct{ depName, value, digest, datasource string }
	ran := 0
	for _, c := range fixture.Corpora(t) {
		for _, e := range c.Entries(t, "dockerfile") {
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
				content := c.Read(t, e.PackageFile)
				res, err := (&Manager{}).Extract(context.Background(), extract.File{Path: e.PackageFile, Content: content}, extract.ManagerConfig{})
				if err != nil {
					t.Fatal(err)
				}
				got := map[key]int{}
				for _, d := range res.Deps {
					if d.SkipReason != "" {
						continue
					}
					got[key{d.DepName, d.CurrentValue, d.CurrentDigest, d.Datasource}]++
					if slice := string(content[d.Locus.ValueStart:d.Locus.ValueEnd]); slice != d.CurrentValue {
						t.Errorf("%s: offsets point at %q, not at %q", d.DepName, slice, d.CurrentValue)
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
		t.Skip("no dockerfile capture with its files on this machine")
	}
}
