// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/config/preset"
)

func TestFlattenNotationAndOrder(t *testing.T) {
	doc := map[string]any{
		"packageRules": []any{
			map[string]any{"automerge": true, "matchManagers": []any{"custom.regex"}},
			map[string]any{"enabled": false},
		},
		"labels":   []any{},
		"schedule": "at any time",
		"lock":     map[string]any{},
	}
	got := Flatten(doc)
	want := []string{
		`labels []`,
		`lock {}`,
		`packageRules[0].automerge true`,
		`packageRules[0].matchManagers[0] "custom.regex"`,
		`packageRules[1].enabled false`,
		`schedule "at any time"`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	if p := PointerOf("packageRules[25].matchManagers[0]"); p != "/packageRules/25/matchManagers/0" {
		t.Errorf("PointerOf = %q", p)
	}
}

// Swapping two rules must show up as every differing line of both, not as
// a reordering a tree differ would hide.
func TestDiffSeesARuleSwap(t *testing.T) {
	a := Flatten(map[string]any{"packageRules": []any{
		map[string]any{"automerge": true, "matchManagers": []any{"composer"}},
		map[string]any{"enabled": false, "matchDatasources": []any{"npm"}},
	}})
	b := Flatten(map[string]any{"packageRules": []any{
		map[string]any{"enabled": false, "matchDatasources": []any{"npm"}},
		map[string]any{"automerge": true, "matchManagers": []any{"composer"}},
	}})
	d := Diff(a, b)
	if len(d) != 8 {
		t.Fatalf("a swap of two two-key rules is 8 differing lines, got %d:\n%s", len(d), strings.Join(d, "\n"))
	}
	if len(Diff(a, a)) != 0 {
		t.Error("a document differs from itself")
	}
}

// print-config parity: the estate configuration resolved here, flattened,
// equals the pinned container's direct resolution of the same file - every
// path, every value, in order. The line count is asserted so an empty
// comparison cannot pass.
func TestPrintConfigParityWithTheCapturedResolution(t *testing.T) {
	r, _, err := ResolveFile("../testdata/parity/config/default.json", preset.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../testdata/parity/renovate-43.288.0/presets/default-resolved.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	mine, theirs := Flatten(r.Raw), Flatten(want)
	if len(theirs) < 5000 {
		t.Fatalf("only %d lines captured; the snapshot was barely read", len(theirs))
	}
	d := Diff(mine, theirs)
	if len(d) != 0 {
		limit := min(len(d), 20)
		t.Errorf("%d lines differ from the captured resolution (first %d):\n%s", len(d), limit, strings.Join(d[:limit], "\n"))
	}
	t.Logf("%d flattened lines agree", len(theirs))
}
