// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

func TestPlandiffReadsTwoReports(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, p model.Plan) string {
		raw, _ := json.Marshal(p)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := write("base.json", model.Plan{})
	head := write("head.json", model.Plan{Branches: []model.Branch{{Name: "pinup/k3s", Title: "update k3s"}}})

	var out, errw bytes.Buffer
	if err := run([]string{"plandiff", "--base", base, "--head", head}, &out, &errw); err != nil {
		t.Fatalf("%v: %s", err, errw.String())
	}
	if !strings.Contains(out.String(), "`pinup/k3s` — update k3s") {
		t.Errorf("output:\n%s", out.String())
	}
	if err := run([]string{"plandiff", "--base", base}, &out, &errw); err == nil {
		t.Error("a missing --head must be an error")
	}
	if err := run([]string{"plandiff", "--base", base, "--head", filepath.Join(dir, "none.json")}, &out, &errw); err == nil {
		t.Error("an unreadable plan must be an error")
	}
}
