// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/model"
)

// The coverage checks ask the question the others cannot: what in the
// repository is pinned and not updated? A configuration can be flawless
// and still leave a compose image or a CI variable to rot, because no
// manager reads the file or no annotation reaches the line. Measured on
// ten of the estate's repositories (2026-09-25): 774 version literals, 374
// in the plans, 27 annotated, about 22 real pins nobody updated - compose
// images behind a disabled manager, lint images in lefthook.yml, `*_VERSION`
// variables in CI, and annotations the plans already called orphaned.
//
// "Untracked" is not "a gap": a repository may keep pins on purpose - an
// unbuilt upstream recipe, a test fixture. ignorePaths and the marker
// `pinup: coverage-ignore` (on the line or the one above;
// `pinup: coverage-ignore-file` anywhere in a file) say so.
var coverageChecks = []Check{
	{ID: "coverage/orphan-annotation", Run: orphanAnnotation, Plan: true},
	{ID: "coverage/manifest-without-lock", Run: manifestWithoutLock, Plan: true},
	{ID: "coverage/unmanaged-pin", Run: unmanagedPin, Plan: true, Repo: true},
}

// orphanAnnotation surfaces the extract warnings for annotations no
// dependency claimed. The plan records each one, and a warning in a plan is
// read by nobody; the pin beneath it is not updated.
func orphanAnnotation(in *Input) []Finding {
	var out []Finding
	for _, p := range in.Plans {
		var lines []string
		for _, w := range p.Warnings {
			if w.Stage != "extract" || !strings.Contains(w.Msg, "names no dependency") {
				continue
			}
			line, _, _ := strings.Cut(strings.TrimPrefix(w.Msg, "line "), ":")
			lines = append(lines, w.File+":"+line)
		}
		if len(lines) == 0 {
			continue
		}
		what := "the annotation at %s names no dependency, and the pin it meant is not updated"
		if len(lines) > 1 {
			what = "the annotations at %s name no dependency, and the pins they meant are not updated"
		}
		out = append(out, Finding{ID: "coverage/orphan-annotation", Category: Coverage, Severity: Warn, Pointer: "/", Frame: FrameResolved, Origin: in.originAt("/"),
			Msg: fmt.Sprintf("plan %s: "+what+" - the pinned value must sit on the line right after the annotation",
				p.Repo.Path, strings.Join(lines, ", "))})
	}
	return out
}

// lockedManagers are the managers whose manifests take ranges and whose
// lock file is what pins them.
var lockedManagers = map[string]string{"npm": "package-lock.json, pnpm-lock.yaml or yarn.lock", "composer": "composer.lock"}

// exactVersion is a manifest value that pins by itself.
var exactVersion = regexp.MustCompile(`^v?\d+(\.\d+){0,3}([-+][0-9A-Za-z.-]+)?$`)

// manifestWithoutLock names manifests whose ranges no lock pins. A range
// without a lock resolves anew on every install; under update-lockfile a
// release the range admits moves nothing pinup could edit, so the version
// in use changes and no merge request ever says so. A manifest the plan
// warned about - a lock it could not read - is left out: there is a lock,
// pinup only cannot see it.
func manifestWithoutLock(in *Input) []Finding {
	var out []Finding
	for _, p := range in.Plans {
		warned := map[string]bool{}
		for _, w := range p.Warnings {
			warned[w.File] = true
		}
		locked, ranged := map[string]bool{}, map[string]int{}
		for _, d := range p.Deps {
			if _, ok := lockedManagers[d.Manager]; !ok || d.CustomManager != model.NoCustomManager {
				continue
			}
			if d.LockedVersion != "" {
				locked[d.File] = true
			}
			if d.SkipReason == "" && len(d.LockFiles) > 0 && !exactVersion.MatchString(d.CurrentValue) {
				ranged[d.File]++
			}
		}
		for _, file := range slices.Sorted(maps.Keys(ranged)) {
			if locked[file] || warned[file] {
				continue
			}
			var manager string
			for _, d := range p.Deps {
				if d.File == file {
					manager = d.Manager
					break
				}
			}
			out = append(out, Finding{ID: "coverage/manifest-without-lock", Category: Coverage, Severity: Warn, Pointer: "/", Frame: FrameResolved, Origin: in.originAt("/"),
				Msg: fmt.Sprintf("plan %s: %s declares ranges no lock pins (%d dependencies; %s); every install resolves them anew, and a release inside a range reaches it without a merge request",
					p.Repo.Path, file, ranged[file], lockedManagers[manager])})
		}
	}
	return out
}

// pinPatterns are the two shapes that measured precise enough to report:
// an image reference with a registry path, and an upper-case
// `*_VERSION`/`*_VER`/`*_TAG` variable - both with a full three-part
// version. A two-part one (`PHP_VERSION: "8.5"`, `franken-php/ci:8.5`,
// `valkey:8.1-alpine3.22`) chooses a line whose patches it already follows,
// and moving the line is a decision, not an update. Bare `name@1.2.3` and URLs with a version
// inside were measured too and were mostly noise.
var pinPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:[\w.-]+\.[a-z]{2,}(?::\d+)?/)?[\w.-]+(?:/[\w.-]+)+:(v?\d+\.\d+\.\d+(?:-[\w.]+)?)(?:[\s"'@,\]}]|$)`),
	regexp.MustCompile(`\b[A-Z][A-Z0-9_]*_(?:VERSION|VER|TAG)["']?\s*[:=]\s*["']?(v?\d+\.\d+\.\d+(?:-[\w.]+)?)\b`),
}

// proseWords is how many plain words make a line a sentence rather than a
// pin: "ksops:2.1.6, a Git tag with" in a docstring, an image named in a
// rule's description. Measured on the estate, the commands and keys that
// carry real pins have four at most (`docker run … composer audit`). A
// word ending in a colon is a key (`run:`, `image:`), not prose.
const proseWords = 5

func prose(line string) bool {
	n := 0
	for w := range strings.FieldsSeq(line) {
		w = strings.TrimRight(strings.TrimLeft(w, `"'(`), `"'),.;`)
		if w != "" && strings.IndexFunc(w, func(r rune) bool { return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') }) < 0 {
			n++
		}
	}
	return n >= proseWords
}

// skipFile are files whose versions are records, not pins: locks,
// changelogs, prose, vendored and generated trees, tests and fixtures - and
// the configuration itself, whose versions are rules about pins.
var skipFile = regexp.MustCompile(`(?i)((^|/)(renovate\.json5?|\.renovaterc(\.json5?)?|\.pinup\.(ya?ml|jsonc?))$|\.lock$|lock\.(json|ya?ml)$|\.lock\.hcl$|go\.sum$|changelog|\.(md|rst|txt|adoc|svg|map|html?|xml)$|\.min\.(js|css)$|(^|/)(vendor|node_modules|testdata|fixtures?|tests?|__tests__|docs?)/|_test\.\w+$|\.test\.\w+$)`)

var (
	// annotationAbove is a `renovate:`/`pinup:` annotation - key=value
	// fields, which the ignore marker has none of.
	annotationAbove = regexp.MustCompile(`(?:renovate|pinup):[ \t]*\w+=`)
	ignoreLine      = "pinup: coverage-ignore"
	ignoreFile      = "pinup: coverage-ignore-file"
)

// maxScanned bounds the size of a file the scan reads; a larger one is data.
const maxScanned = 400 << 10

// unmanagedPin scans the repository for pinned versions no plan holds and
// no annotation claims. A version the plans record for the same file - on
// the same line, or anywhere in it with the same value - is held. No fix:
// the answer is an annotation, a manager, or the ignore marker, and which
// one is the repository's call.
func unmanagedPin(in *Input) []Finding {
	lines, values := map[string]bool{}, map[string]bool{}
	for _, p := range in.Plans {
		for _, d := range p.Deps {
			lines[fmt.Sprintf("%s:%d", d.File, d.Locus.Line)] = true
			for _, v := range []string{d.CurrentValue, d.LockedVersion} {
				if v != "" {
					values[d.File+"|"+strings.TrimPrefix(v, "v")] = true
				}
			}
		}
	}
	ignored := glob.NewIgnoreSet(in.Decoded.IgnorePaths)
	byFile := map[string][]string{}
	_ = fs.WalkDir(in.Repo, ".", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if name := e.Name(); path != "." && (strings.HasPrefix(name, ".") && name != ".gitlab" && name != ".github" || name == "node_modules" || name == "vendor") {
				return fs.SkipDir
			}
			return nil
		}
		if skipFile.MatchString(path) || ignored.Ignores(path) {
			return nil
		}
		if info, err := e.Info(); err != nil || info.Size() > maxScanned {
			return nil
		}
		body, err := fs.ReadFile(in.Repo, path)
		if err != nil || bytes.IndexByte(body, 0) >= 0 || bytes.Contains(body, []byte(ignoreFile)) {
			return nil
		}
		prev := ""
		for n, line := range strings.Split(string(body), "\n") {
			n++
			above := prev
			prev = line
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, ignoreLine) || strings.Contains(above, ignoreLine) || annotationAbove.MatchString(above) {
				continue
			}
			if lines[fmt.Sprintf("%s:%d", path, n)] || prose(line) {
				continue
			}
			for _, rx := range pinPatterns {
				m := rx.FindStringSubmatch(line)
				if m == nil || values[path+"|"+strings.TrimPrefix(m[1], "v")] {
					continue
				}
				byFile[path] = append(byFile[path], fmt.Sprintf("%d (%s)", n, m[1]))
				break
			}
		}
		return nil
	})
	var out []Finding
	for _, path := range slices.Sorted(maps.Keys(byFile)) {
		out = append(out, Finding{ID: "coverage/unmanaged-pin", Category: Coverage, Severity: Info, Pointer: path, Frame: FrameRepo, Origin: in.originAt("/"),
			Msg: fmt.Sprintf("%s: no plan updates the version pinned at line %s - annotate it, enable the manager that reads the file, or mark it `%s`",
				path, strings.Join(byFile[path], ", "), ignoreLine)})
	}
	return out
}
