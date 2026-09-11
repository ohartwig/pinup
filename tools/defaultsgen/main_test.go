// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"os"
	"testing"
)

func TestCommittedDefaultsAreGenerated(t *testing.T) {
	got, err := Generate("../../"+fullPath, "../../"+directPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../" + outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("config/defaults.json differs from the generator's output; run go run ./tools/defaultsgen")
	}
}
