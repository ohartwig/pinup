// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package dockerfile

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ohartwig/pinup/model"
)

// Unmanaged version pins.
//
// WHY THIS EXISTS, measured on 2026-09-22. A tenant's scheduler image carried
// sixteen open findings, seven of them HIGH, all from Go's standard library at
// v1.26.2. Every pin along the chain above it was current - the apk package,
// the base image, the runtime image, all bumped within the week. The stale
// thing was one line:
//
//	# renovate: datasource=gitlab-tags depName=devops/images/php-runtime
//	ARG PHP_RUNTIME_IMAGE_TAG=3.0.31@sha256:9e974ad6...   <- managed, current
//	ARG SUPERCRONIC_VERSION=0.2.45                        <- nobody's job
//
// The second line had been written once and never touched again while its
// upstream moved four releases on. Nothing was broken: a bot updates what is
// declared to it, and that value was declared to no one.
//
// That silence is what this reports. It is not a vulnerability check and it
// does not know whether an update exists - it says that a line which looks
// exactly like a version pin has no datasource behind it, so the plan can say
// so instead of saying nothing. Absence is never expressed as absence.
//
// WHAT IT DELIBERATELY DOES NOT FLAG, because a warning nobody can act on is
// noise that teaches people to skip the warnings that matter:
//
//   - a build argument whose value this manager already resolved into a
//     dependency (FROM ${TAG}), because that one is managed;
//   - a checksum, a commit or any other value that is not shaped like a
//     version, because there is no datasource that could serve it;
//   - a name that does not announce itself as a pin (_VERSION, _TAG, _REF);
//   - a value with fewer than three numeric components, because 8.5 and 1.27
//     name a line rather than a release - ARG PHP_VERSION=8.5 chooses which
//     PHP to build against and is answered by a decision, not by a bot;
//   - anything carrying an annotation comment, because a custom manager
//     claims those - this manager does not see the other managers' results,
//     but it does see the line they key on.

var (
	// reAssign matches an ARG or ENV that carries a value. ENV is here
	// because the estate writes both forms, and an ENV pin ages exactly as
	// quietly. The value stops at whitespace and at a trailing comment, the
	// same reading reArg uses.
	reAssign = regexp.MustCompile(`(?i)^[ \t]*(?:ARG|ENV)[ \t]+(?P<name>[A-Za-z_][A-Za-z0-9_]*)=(?P<val>[^ \t#]+)`)

	// rePinName is the naming convention a version pin actually uses. It is
	// intentionally narrow: _SHA1, _DIGEST and _COMMIT name the checksum that
	// travels with a pin, not the pin.
	rePinName = regexp.MustCompile(`(?i)_(VERSION|TAG|REF)$`)

	// reVersionValue is a release as a datasource would recognise it:
	// 1.2.3, v1.2.3, 0.2.45, 1.12.7-r3, 3.0.31@sha256:... - and not a
	// forty-character hex string, not a word, not a path. Three numeric
	// components at least: 8.5 and 1.27 are lines, and a line moves when
	// somebody decides it does.
	reVersionValue = regexp.MustCompile(`^v?\d+(\.\d+){2,}(-[0-9A-Za-z][0-9A-Za-z.]*)?(@sha256:[0-9a-f]{64})?$`)

	// reAnnotated is the comment a custom manager keys on. Renovate's spelling
	// is what the estate writes; pinup reads its own name too, so a repository
	// that has moved on does not start warning about lines it just annotated.
	reAnnotated = regexp.MustCompile(`(?i)^[ \t]*#[ \t]*(renovate|pinup)[ \t]*:`)
)

// unmanagedPins reports version-shaped assignments that nothing manages.
//
// deps are the dependencies this manager extracted from the same file; a line
// one of them sits on is managed by definition and never reported.
func unmanagedPins(path string, lines []lineInfo, deps []model.Dependency) []model.Warning {
	if len(lines) == 0 {
		return nil
	}

	// covered marks every line a dependency of this manager reported. The
	// Locus carries byte offsets into the file, so the line is found by the
	// range it falls into rather than by re-matching the text.
	covered := make(map[int]bool, len(deps))
	for _, d := range deps {
		for i, ln := range lines {
			end := ln.start + len(ln.text)
			if d.Locus.ValueStart >= ln.start && d.Locus.ValueStart <= end {
				covered[i] = true
				break
			}
		}
	}

	var out []model.Warning
	for i, ln := range lines {
		if covered[i] {
			continue
		}
		bare := strings.TrimSuffix(ln.text, "\r")
		idx, ok := matchNamed(reAssign, bare)
		if !ok {
			continue
		}
		nameSpan, valSpan := idx["name"], idx["val"]
		name := bare[nameSpan[0]:nameSpan[1]]
		val := bare[valSpan[0]:valSpan[1]]
		if !rePinName.MatchString(name) || !reVersionValue.MatchString(val) {
			continue
		}
		if annotatedAbove(lines, i) {
			continue
		}
		out = append(out, model.Warning{
			Stage: "extract",
			File:  path,
			Msg: fmt.Sprintf(
				"unmanaged version pin: %s=%s on line %d has no datasource annotation, so no update will ever be proposed for it",
				name, val, i+1),
		})
	}
	return out
}

// annotatedAbove reports whether the nearest preceding comment line - blank
// lines and further comments skipped - announces a datasource. A pin is
// commonly preceded by a paragraph of prose with the annotation as its last
// line, and by the annotation with prose above it; both count.
func annotatedAbove(lines []lineInfo, i int) bool {
	for j := i - 1; j >= 0; j-- {
		bare := strings.TrimSpace(strings.TrimSuffix(lines[j].text, "\r"))
		if bare == "" {
			continue
		}
		if !strings.HasPrefix(bare, "#") {
			return false
		}
		if reAnnotated.MatchString(lines[j].text) {
			return true
		}
	}
	return false
}
