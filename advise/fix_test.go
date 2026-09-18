// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/config"
)

// Fixes apply from the highest pointer down, so a removal never moves what
// a later fix points at; conflicts and shadowed fixes are skipped by name.
func TestApplyOrdersConflictsAndShadows(t *testing.T) {
	src := "{\n  \"packageRules\": [\n    {\"a\": 1},\n    {\"b\": 2},\n    {\"c\": 3}\n  ]\n}"
	out, applied, skipped, err := Apply([]byte(src), "renovate.json", []Fix{
		{Pointer: "/packageRules/1", Op: OpRemove},
		{Pointer: "/packageRules/2/x", Op: OpSet, Value: true},
		{Pointer: "/packageRules/1/y", Op: OpSet, Value: 1.0},
		{Pointer: "/schedule", Op: OpSet, Value: []any{"before 5am"}},
		{Pointer: "/schedule", Op: OpSet, Value: []any{"before 6am"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"packageRules\": [\n    {\"a\": 1},\n    {\"c\": 3, \"x\": true}\n  ]\n}"
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	if len(applied) != 2 || applied[0].Fix.Pointer != "/packageRules/2/x" || applied[0].Line != 5 || applied[1].Fix.Pointer != "/packageRules/1" {
		t.Errorf("applied = %+v", applied)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Fix.Pointer] = s.Reason
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped = %+v, want 3", skipped)
	}
	if !strings.Contains(reasons["/packageRules/1/y"], "shadowed by the removal of /packageRules/1") {
		t.Errorf("shadow reason = %q", reasons["/packageRules/1/y"])
	}
	if !strings.Contains(reasons["/schedule"], "conflicts") {
		t.Errorf("conflict reason = %q", reasons["/schedule"])
	}
}

func TestApplyRefusesWhatItCannotRewrite(t *testing.T) {
	fixes := []Fix{{Pointer: "/schedule", Op: OpRemove}, {Pointer: "/labels", Op: OpAppend, Value: "x"}}
	out, applied, skipped, err := Apply([]byte("schedule:\n  - before 5am\n"), ".pinup.yaml", fixes)
	if err != nil || len(applied) != 0 || len(skipped) != 2 || string(out) != "schedule:\n  - before 5am\n" {
		t.Errorf("YAML: err=%v applied=%d skipped=%d out=%q", err, len(applied), len(skipped), out)
	}
	if !strings.Contains(skipped[0].Reason, "remove /schedule") || !strings.Contains(skipped[1].Reason, `append "x" to /labels`) {
		t.Errorf("manual lines = %q, %q", skipped[0].Reason, skipped[1].Reason)
	}
	if _, _, _, err := Apply([]byte("{}"), "config.toml", fixes); err == nil {
		t.Error("an unknown extension is rewritten")
	}
	_, applied, skipped, err = Apply([]byte(`{"a": `), "x.json", fixes[:1])
	if err != nil || len(applied) != 0 || len(skipped) != 1 {
		t.Errorf("unparsable source: err=%v applied=%d skipped=%+v", err, len(applied), skipped)
	}
}

func TestEncode(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{3.0, "3"},
		{2.5, "2.5"},
		{"a <b> & c", `"a <b> & c"`},
		{true, "true"},
		{nil, "null"},
		{[]any{"minor", "patch"}, `["minor", "patch"]`},
		{[]any{}, `[]`},
		{map[string]any{"b": 1.0, "a": true}, "{\n\t\"a\": true,\n\t\"b\": 1\n}"},
		{[]any{map[string]any{"a": 1.0}}, "[\n\t{\n\t\t\"a\": 1\n\t}\n]"},
	} {
		if got := string(encode(c.v, "\t")); got != c.want {
			t.Errorf("encode(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// Every fix the catalogue offers passes its own gate: applied to the case
// that produced it, the resolution changes only where the fix declared.
// This is where a check that declares too little is caught.
func TestEveryFixPassesTheGate(t *testing.T) {
	for _, c := range cases {
		in := load(t, c.cfg, c.plans...)
		findings, _ := Run(in, Catalogue())
		for _, f := range findings {
			if f.Fix == nil {
				continue
			}
			t.Run(c.name+"/"+f.ID, func(t *testing.T) {
				before := []byte(c.cfg)
				after, applied, skipped, err := Apply(before, "x.json", []Fix{*f.Fix})
				if err != nil {
					t.Fatal(err)
				}
				if len(skipped) != 0 {
					t.Fatalf("skipped: %+v", skipped)
				}
				var fixes []Fix
				for _, a := range applied {
					fixes = append(fixes, a.Fix)
				}
				if err := Verify(before, after, "x.json", sources, fixes); err != nil {
					t.Errorf("%s\nbefore: %s\nafter:  %s", err, before, after)
				}
				// And the finding is gone afterwards.
				layer, err := config.Parse(after, "x.json")
				if err != nil {
					t.Fatal(err)
				}
				fixed, err := Load(layer, sources, nil)
				if err != nil {
					t.Fatal(err)
				}
				fixed.Plans, fixed.Covers, fixed.Datasources = in.Plans, in.Covers, in.Datasources
				again, _ := Run(fixed, Catalogue())
				for _, g := range again {
					if g.ID == f.ID && g.Pointer == f.Pointer && g.Msg == f.Msg {
						t.Errorf("the finding is still there after its fix:\n  %s %s: %s\nafter: %s", g.ID, g.Pointer, g.Msg, after)
					}
				}
			})
		}
	}
}

// A commented, oddly formatted file goes through a handful of fixes and
// comes out with every comment and every untouched byte in place. The
// expected output is a golden: read, never rewritten; a change is a v2.
func TestFixesKeepComments(t *testing.T) {
	dir := filepath.Join("testdata", "v1")
	src, err := os.ReadFile(filepath.Join(dir, "commented.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "commented.expected.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	fixes := []Fix{
		{Pointer: "/schedule", Op: OpRemove, Changes: []string{"/schedule"}},
		{Pointer: "/packageRules/0/matchUpdateTypes", Op: OpSet, Value: []any{"minor", "patch"}, Changes: []string{"/packageRules/0/matchUpdateTypes"}},
		{Pointer: "/packageRules/1", Op: OpRemove, Changes: []string{"/packageRules/1"}},
		{Pointer: "/extends", Op: OpAppend, Value: "docker:pinDigests", Changes: concatChanged},
		{Pointer: "/nonesuchKey", Op: OpRemove, Changes: []string{"/nonesuchKey"}},
		{Pointer: "/major", Op: OpSet, Value: map[string]any{"automerge": false}, Changes: []string{"/major"}},
	}
	out, applied, skipped, err := Apply(src, "commented.jsonc", fixes)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 || len(applied) != len(fixes) {
		t.Fatalf("applied %d, skipped %+v", len(applied), skipped)
	}
	if string(out) != string(want) {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	if err := Verify(src, out, "commented.jsonc", sources, fixes); err != nil {
		t.Error(err)
	}
	for _, comment := range []string{"// the runner's file", "/* kept */", "// trailing", "// above"} {
		if !strings.Contains(string(out), comment) {
			t.Errorf("comment %q is gone", comment)
		}
	}
}
