// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/model"
)

// annotationLine is a `# renovate:` or `# pinup:` comment: the promise that
// the line beneath it is a pinned dependency.
var annotationLine = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*(?:renovate|pinup):[ \t]*([^\n]*)$`)

// orphanAnnotations reports every annotation no dependency claims.
//
// The annotation managers - dockerfileVersions and the estate's regex
// managers alike - allow exactly one whitespace character between the
// comment and the pinned line. A blank line or a comment between the two
// does not hold the update; it removes the dependency from the plan, and
// nothing said so. Measured 2026-09-17 across the fifty golden-image
// Containerfiles: ksops sat four comment lines below its annotation at
// 4.5.1-r16 while the index carried r19 with the grpc fix, and two exporters
// had a blank line each. A pin nothing reads is exactly the absence the plan
// exists to explain, so it is a warning per annotation, not a silence.
//
// An annotation counts as claimed when a dependency's value starts on the
// annotation's own line or the one right after it, whatever manager found
// it. Only files a manager was offered are scanned: those are the ones the
// run read anyway.
func orphanAnnotations(contents map[string][]byte, deps []model.Dependency) []model.Warning {
	starts := map[string][]int{}
	for _, d := range deps {
		starts[d.File] = append(starts[d.File], d.Locus.ValueStart)
	}
	var out []model.Warning
	for _, path := range slices.Sorted(maps.Keys(contents)) {
		body := contents[path]
		for _, m := range annotationLine.FindAllSubmatchIndex(body, -1) {
			// The claim window runs from the annotation to the end of the
			// next line: one line for the pinned value, no more.
			from, to := m[0], m[1]
			if nl := bytes.IndexByte(body[to:], '\n'); nl >= 0 {
				to += nl + 1
				if nl2 := bytes.IndexByte(body[to:], '\n'); nl2 >= 0 {
					to += nl2
				} else {
					to = len(body)
				}
			}
			claimed := slices.ContainsFunc(starts[path], func(s int) bool { return s >= from && s <= to })
			if claimed {
				continue
			}
			line := 1 + bytes.Count(body[:m[0]], []byte("\n"))
			out = append(out, model.Warning{
				Stage: "extract", File: path,
				Msg: fmt.Sprintf("line %d: annotation `%s` names no dependency: no manager found a pinned value on the line beneath it - a blank or comment line between the annotation and the pin hides it from every manager, and this pin is not being updated",
					line, strings.TrimSpace(string(body[m[2]:m[3]]))),
			})
		}
	}
	return out
}
