// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/versioning"
)

// The runner's own configuration, with the golden plans, is what advise is
// for. Floors per category, not counts - like expect.json - and two exact
// answers: no error, because the file runs the estate; and no automerge
// over majors, because rule 4 lists its update types.
func TestAdviceOverTheRunnerConfiguration(t *testing.T) {
	layer, err := config.LoadFile(fixture.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	in, err := Load(layer, preset.Builtin(), versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(fixture.Path(t, "golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(fixture.Path(t, "golden", e.Name()), "plan.json"))
		if err != nil {
			continue
		}
		p, err := model.ReadPlan(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		in.Plans = append(in.Plans, p)
	}
	if len(in.Plans) < 3 {
		t.Fatalf("only %d golden plans; the fixture root is not what the test expects", len(in.Plans))
	}
	findings, skipped := Run(in, Catalogue())
	if len(skipped) != 0 {
		t.Errorf("with plans nothing is skipped: %v", skipped)
	}

	byCategory := map[Category]int{}
	byID := map[string][]Finding{}
	for _, f := range findings {
		byCategory[f.Category]++
		byID[f.ID] = append(byID[f.ID], f)
		if f.Severity == Error {
			t.Errorf("the runner's configuration has an error: %s %s: %s", f.ID, f.Pointer, f.Msg)
		}
	}
	for cat, floor := range map[Category]int{Hygiene: 1, Performance: 1, Compat: 2} {
		if byCategory[cat] < floor {
			t.Errorf("%s: %d findings, want at least %d:\n%s", cat, byCategory[cat], floor, describe(findings))
		}
	}
	// The three the plan named: the ignore list the file replaces, the
	// any-time schedule it spells out, the two inert presets it extends.
	if got := byID["perf/ignore-paths-replaced"]; len(got) != 1 || got[0].Fix == nil {
		t.Errorf("perf/ignore-paths-replaced: %d findings with a fix, want 1", len(got))
	}
	if got := byID["hygiene/schedule-anytime-explicit"]; len(got) != 1 {
		t.Errorf("hygiene/schedule-anytime-explicit: %d findings, want 1", len(got))
	}
	if got := byID["compat/preset-inert"]; len(got) < 2 {
		t.Errorf("compat/preset-inert: %d findings, want the two inert presets", len(got))
	}
	if got := byID["sec/automerge-major"]; len(got) != 0 {
		t.Errorf("rule 4 lists minor, patch, digest and pin; automerge-major must not fire: %+v", got)
	}
	// The estate golden records a custom datasource whose lookups failed.
	if got := byID["plan/datasource-failing"]; len(got) != 1 || !strings.HasPrefix(got[0].Pointer, "/customDatasources/") {
		t.Errorf("plan/datasource-failing: %+v, want one under /customDatasources/", got)
	}
	// Every fix points into the file as written.
	for _, f := range findings {
		if f.Fix == nil {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(f.Fix.Pointer, "/"), "/")
		if _, ok := walk(in.Layer.Raw, segs[:len(segs)-1]); !ok {
			t.Errorf("%s: fix at %s has no parent in the file", f.ID, f.Fix.Pointer)
		}
	}
	t.Logf("%d findings over %s with %d plans:\n%s", len(findings), fixture.Config(t), len(in.Plans), describe(findings))
}
