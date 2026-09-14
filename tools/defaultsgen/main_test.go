// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"os"
	"testing"
)

func TestCommittedDefaultsAreGenerated(t *testing.T) {
	if _, err := os.Stat("../../" + fullPath); err != nil {
		t.Skipf("the upstream captures are not in this tree (%v); the generator's output is checked where they are", err)
	}
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
