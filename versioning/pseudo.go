// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package versioning

import "regexp"

// goPseudoRE matches a Go pseudo-version - a version the go command makes
// up for an untagged commit: vX.Y.Z-yyyymmddhhmmss-abcdefabcdef, or with a
// pre-release ("-pre.0.") or a "-0." between the base and the timestamp.
// The last twelve hex digits are the commit.
var goPseudoRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]*?)?[-.][0-9]{14}-([0-9a-f]{12})$`)

// GoPseudoCommit returns the commit a Go pseudo-version names, its twelve
// hex digits, and "" for any other version. A dependency written that way
// is pinned to a commit, not a release: what moves is the commit, and
// Renovate reports the move as a digest update ("update golang.org/x/mobile
// digest to 8b95e45", measured on development/s3mail/ios!20).
func GoPseudoCommit(version string) string {
	m := goPseudoRE.FindStringSubmatch(version)
	if m == nil {
		return ""
	}
	return m[1]
}
