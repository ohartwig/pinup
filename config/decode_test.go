// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"testing"
)

// Decoding the real configuration is the only test that matters here: a
// synthetic one would only prove the decoder agrees with itself.
func TestDecodeTheRealConfig(t *testing.T) {
	d, _, err := DecodeFile("../testdata/parity/config/default.json")
	if err != nil {
		t.Fatal(err)
	}

	if len(d.CustomManagers) != 25 {
		t.Errorf("decoded %d custom managers, expected 25", len(d.CustomManagers))
	}
	if len(d.EnabledManagers) != 9 {
		t.Errorf("decoded %d enabled managers, expected 9", len(d.EnabledManagers))
	}
	if d.Timezone != "Europe/Berlin" {
		t.Errorf("timezone = %q", d.Timezone)
	}
	if len(d.IgnorePaths) != 1 {
		t.Errorf("ignorePaths = %v, expected one entry", d.IgnorePaths)
	}

	// Every definition must carry file patterns, or discovery would offer it
	// nothing and the manager would look like it found nothing.
	for _, cm := range d.CustomManagers {
		if len(cm.FilePatterns) == 0 {
			t.Errorf("custom manager %d has no file patterns", cm.Index)
		}
		if len(cm.MatchStrings) == 0 {
			t.Errorf("custom manager %d has no matchStrings", cm.Index)
		}
		if got := d.FilePatterns[CustomManagerName(cm.Index)]; len(got) == 0 {
			t.Errorf("custom manager %d is not registered for discovery", cm.Index)
		}
	}

	// Spot-checks against definitions this project has read closely.
	if got := d.CustomManagers[0].MatchStrategy; got != "recursive" {
		t.Errorf("definition 0 strategy = %q, want recursive", got)
	}
	if got := d.CustomManagers[10].PackageNameTemplate; got == "" {
		t.Error("definition 10 lost its packageNameTemplate")
	}
	if got := d.CustomManagers[20].DepNameTemplate; got != "php-frankenphp-{{{phpSeries}}}" {
		t.Errorf("definition 20 depNameTemplate = %q", got)
	}
}

// description is a string in some entries and a list in others. Both must
// decode rather than one of them panicking at load.
func TestStringSliceAcceptsBothShapes(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int
	}{
		{nil, 0},
		{"one", 1},
		{[]any{"a", "b"}, 2},
		{[]any{}, 0},
	} {
		if got := len(stringSlice(c.in)); got != c.want {
			t.Errorf("stringSlice(%#v) has %d entries, want %d", c.in, got, c.want)
		}
	}
}
