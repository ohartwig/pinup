// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"os"
	"testing"
)

const goldenPlan = "testdata/plan-minimal.json"

// TestGoldenPlanRoundTrips is the contract the whole harness rests on: the
// bytes on disk parse, and re-serializing what was parsed reproduces them
// exactly. Without that, a golden comparison is comparing formatting.
func TestGoldenPlanRoundTrips(t *testing.T) {
	want, err := os.ReadFile(goldenPlan)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ReadPlan(bytes.NewReader(want))
	if err != nil {
		t.Fatalf("reading %s: %v", goldenPlan, err)
	}
	var got bytes.Buffer
	if err := WritePlan(&got, p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("re-serializing %s is not byte-identical\n--- got %d bytes, want %d bytes",
			goldenPlan, got.Len(), len(want))
		for i := 0; i < min(got.Len(), len(want)); i++ {
			if got.Bytes()[i] != want[i] {
				lo := max(0, i-60)
				t.Errorf("first difference at byte %d:\n got: %q\nwant: %q",
					i, got.Bytes()[lo:min(got.Len(), i+60)], want[lo:min(len(want), i+60)])
				break
			}
		}
	}
}

// TestGoldenPlanIsNotVacuous guards the golden itself. A plan that asserts
// nothing passes every comparison.
func TestGoldenPlanIsNotVacuous(t *testing.T) {
	f, err := os.Open(goldenPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := ReadPlan(f)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case len(p.Deps) == 0:
		t.Error("golden plan has no dependencies")
	case len(p.Updates) == 0:
		t.Error("golden plan has no updates")
	case len(p.Branches) == 0:
		t.Error("golden plan has no branches")
	}
	// It must exercise the cases that are easy to get wrong.
	var sawBlocked, sawSkipped, sawFirstSeen, sawAbsentGroup bool
	for _, u := range p.Updates {
		if u.Blocked() {
			sawBlocked = true
		}
		if u.TimeSource == TimeFromFirstSeen {
			sawFirstSeen = true
		}
	}
	for _, d := range p.Deps {
		if d.SkipReason != "" {
			sawSkipped = true
		}
		if len(d.Absent) > 0 {
			sawAbsentGroup = true
		}
	}
	for name, ok := range map[string]bool{
		"a blocked update":            sawBlocked,
		"a skipped dependency":        sawSkipped,
		"a firstseen-derived age":     sawFirstSeen,
		"a non-participating capture": sawAbsentGroup,
	} {
		if !ok {
			t.Errorf("golden plan does not cover %s", name)
		}
	}
}

func TestWriteIsDeterministic(t *testing.T) {
	f, err := os.Open(goldenPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := ReadPlan(f)
	if err != nil {
		t.Fatal(err)
	}
	var a, b bytes.Buffer
	if err := WritePlan(&a, p); err != nil {
		t.Fatal(err)
	}
	if err := WritePlan(&b, p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("two writes of the same plan differ - map iteration order is leaking")
	}
}

// TestValidateRejects is the negative half. Each case is an invariant that,
// unenforced, produces a plan that looks fine and is not.
func TestValidateRejects(t *testing.T) {
	base := func() *Plan {
		d := Dependency{File: "f", DepName: "d", CurrentValue: "1", CustomManager: NoCustomManager}
		return &Plan{
			SchemaVersion: SchemaVersion,
			Deps:          []Dependency{d},
			Updates:       []Update{{DepKey: d.Key(), Dep: d, NewValue: "2", Type: UpdateMinor}},
			Branches: []Branch{{
				Name: "renovate/d", UpdateKeys: []string{d.Key() + ">2"},
			}},
		}
	}
	for _, c := range []struct {
		name string
		bend func(*Plan)
		want string
	}{
		{"wrong schema version", func(p *Plan) { p.SchemaVersion = 99 }, "schemaVersion"},
		{"dependency with neither update nor reason", func(p *Plan) { p.Updates = nil; p.Branches = nil }, "neither an update nor a skipReason"},
		{"blocked without suppressedBy", func(p *Plan) {
			p.Updates[0].Blocks = []Block{{Reason: BlockSchedule}}
		}, "names no suppressedBy"},
		{"branch with no updates", func(p *Plan) { p.Branches[0].UpdateKeys = nil }, "carries no updates"},
		{"branch referencing an unknown update", func(p *Plan) {
			p.Branches[0].UpdateKeys = []string{"nope"}
		}, "unknown update"},
	} {
		p := base()
		c.bend(p)
		err := p.Validate()
		if err == nil {
			t.Errorf("%s: Validate accepted it", c.name)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte(c.want)) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
	// And the positive control: the untouched plan must pass, or every case
	// above is passing for the wrong reason.
	if err := base().Validate(); err != nil {
		t.Errorf("the base plan should be valid: %v", err)
	}
}

func TestReadPlanRejectsUnknownFields(t *testing.T) {
	_, err := ReadPlan(bytes.NewReader([]byte(`{"schemaVersion":1,"nosuchfield":1}`)))
	if err == nil {
		t.Error("an unknown field was accepted: reader and writer can drift apart unnoticed")
	}
}

func TestStricterNeverRelaxes(t *testing.T) {
	all := []Risk{RiskUnknown, RiskPatch, RiskMinor, RiskMajor, RiskBreakingValues}
	// Exhaustive, not sampled: the domain is small and the one relaxing
	// combination is exactly what a random draw would miss.
	for _, declared := range all {
		for _, effective := range all {
			u := Update{Declared: declared, Effective: effective}
			if got := u.AutomergeRisk(false); got < declared {
				t.Errorf("declared=%v effective=%v: automerge risk %v is lower than declared",
					declared, effective, got)
			}
			// An effective label that was never computed cannot change
			// anything, even when it is trusted.
			if effective == RiskUnknown {
				if got := u.AutomergeRisk(true); got != declared {
					t.Errorf("declared=%v with no effective label: trusted risk %v, want %v",
						declared, got, declared)
				}
			}
		}
	}
}

func TestEditOverlaps(t *testing.T) {
	e := Edit{File: "a", Start: 10, End: 20}
	for _, c := range []struct {
		other Edit
		want  bool
	}{
		{Edit{File: "a", Start: 15, End: 25}, true},
		{Edit{File: "a", Start: 5, End: 15}, true},
		{Edit{File: "a", Start: 12, End: 18}, true},
		{Edit{File: "a", Start: 20, End: 30}, false}, // adjacent, not overlapping
		{Edit{File: "a", Start: 0, End: 10}, false},
		{Edit{File: "b", Start: 12, End: 18}, false},
	} {
		if got := e.Overlaps(c.other); got != c.want {
			t.Errorf("Overlaps(%v) = %v, want %v", c.other, got, c.want)
		}
	}
}

func TestUpdateTypeRoundTrip(t *testing.T) {
	for u := UpdateUnknown; u <= UpdateMajorAvailable; u++ {
		b, err := u.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var back UpdateType
		if err := back.UnmarshalJSON(b); err != nil {
			t.Errorf("%v: %v", u, err)
			continue
		}
		if back != u {
			t.Errorf("%v round-tripped to %v", u, back)
		}
	}
}
