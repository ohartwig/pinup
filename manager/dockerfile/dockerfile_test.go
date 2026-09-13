// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package dockerfile

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
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

// The estate's own golang-image Containerfile, read directly rather than
// copied into testdata - the fixture this manager must actually handle. The
// file moves with that repository, so the expectations are derived from its
// FROM lines rather than pinned: every FROM that names a tag and a digest
// must come out as exactly one digest-pinned, unskipped dependency.
func TestRealGolangImageContainerfile(t *testing.T) {
	path := "/Volumes/Samsung_X5/Projects/golang-image/Containerfile"
	content, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("real fixture not available: %v", err)
	}
	f := extract.File{Path: path, Content: content}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}

	froms := 0
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		ref := fields[1]
		if strings.HasPrefix(ref, "--") && len(fields) > 2 {
			ref = fields[2]
		}
		name, rest, hasTag := strings.Cut(ref, ":")
		if !hasTag {
			continue
		}
		tag, digest, hasDigest := strings.Cut(rest, "@")
		if !hasDigest {
			continue
		}
		froms++
		d := depNamed(t, res.Deps, name)
		if d.CurrentValue != tag || d.CurrentDigest != digest {
			t.Errorf("%s: extracted %s@%s, the line says %s@%s", name, d.CurrentValue, d.CurrentDigest, tag, digest)
		}
		if d.SkipReason != "" {
			t.Errorf("%s is digest-pinned with a tag; it must not be skipped, got %q", name, d.SkipReason)
		}
	}
	// The denominator: a Containerfile without a pinned FROM would make
	// this test pass by looking at nothing.
	if froms == 0 {
		t.Fatal("no digest-pinned FROM line in the real fixture; the test checked nothing")
	}

	// Re-confirm the offset invariant against real, unmodified bytes.
	for _, d := range res.Deps {
		if got := string(content[d.Locus.ValueStart:d.Locus.ValueEnd]); got != d.CurrentValue {
			t.Errorf("%s: real-file offsets do not match CurrentValue: got %q, want %q", d.DepName, got, d.CurrentValue)
		}
	}
}
