// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"strings"
	"testing"
)

// The diagnostic names where two descriptions part, so a log line is
// enough to see what the platform did to the text.
func TestDescriptionDiffNamesWhereTheTextsPart(t *testing.T) {
	got := DescriptionDiff("notes for 0.17.0\n", "notes for 0.18.0")
	for _, want := range []string{"shown 16 bytes", "sent 16", "byte 13", `"7.0"`, `"8.0"`} {
		if !strings.Contains(got, want) {
			t.Errorf("DescriptionDiff = %q, missing %q", got, want)
		}
	}
	// A multi-byte rune is never cut in half: the window starts on its
	// first byte.
	got = DescriptionDiff("a—b", "a–b")
	if !strings.Contains(got, "byte 1") || !strings.Contains(got, `"—b"`) {
		t.Errorf("DescriptionDiff = %q, want the window on the rune boundary", got)
	}
}
