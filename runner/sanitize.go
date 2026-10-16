// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import "github.com/ohartwig/pinup/markdown"

// Sanitize is markdown.Sanitize: it moved to L0 on 2026-10-16 so that the
// plan diff a preview note carries goes through the same neutralising of
// quick actions and references as a description does. Kept here for the
// callers in this package.
func Sanitize(text string) string {
	return markdown.Sanitize(text)
}
