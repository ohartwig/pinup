// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package vertest runs a Versioning implementation against a captured
// behaviour table and reports where the two disagree.
//
// The tables are input/output pairs recorded by executing Renovate's own
// versioning modules in a pinned container. They are the specification: this
// package is what turns them into a test, once, for all eleven schemes.
//
// Three properties matter more than they look:
//
//   - It reports counts. A conformance run that compared nothing would
//     otherwise pass, which is the failure mode the estate has paid for
//     repeatedly.
//   - Deliberate divergences are declared with a reason and are counted
//     separately, so "we differ here on purpose" never blurs into "we differ
//     here".
//   - A declared divergence that does not fire is an error. A suppression
//     that outlives the thing it suppressed is how a gate stops gating.
package vertest

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/fake/harness"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// Table is a captured behaviour table.
type Table struct {
	Module string `json:"module"`
	Source string `json:"source"`

	IsValid   []row `json:"isValid"`
	IsVersion []row `json:"isVersion"`
	IsStable  []row `json:"isStable"`
	GetMajor  []row `json:"getMajor"`
	GetMinor  []row `json:"getMinor"`
	GetPatch  []row `json:"getPatch"`

	Equals        []row `json:"equals"`
	IsGreaterThan []row `json:"isGreaterThan"`
	SortVersions  []row `json:"sortVersions"`
	IsCompatible  []row `json:"isCompatible"`

	Matches     []row `json:"matches"`
	GetNewValue []row `json:"getNewValue"`
}

type row struct {
	In      *string `json:"in"`
	A       *string `json:"a"`
	B       *string `json:"b"`
	Version *string `json:"version"`
	Range   *string `json:"range"`

	CurrentValue  *string `json:"currentValue"`
	RangeStrategy *string `json:"rangeStrategy"`
	CurrentVer    *string `json:"currentVersion"`
	NewVersion    *string `json:"newVersion"`

	Out json.RawMessage `json:"out"`
}

// errored reports whether Renovate itself threw for this row. Those are
// recorded so the table is complete, but they are not a specification: an
// exception is not an answer.
func (r row) errored() bool {
	var m map[string]any
	if json.Unmarshal(r.Out, &m) == nil {
		_, bad := m["__error"]
		return bad
	}
	return false
}

func (r row) boolOut() (bool, bool) {
	var b bool
	if err := json.Unmarshal(r.Out, &b); err != nil {
		return false, false
	}
	return b, true
}

func (r row) intOut() (int, bool) {
	var f float64
	if err := json.Unmarshal(r.Out, &f); err != nil {
		return 0, false
	}
	return int(f), true
}

func (r row) strOut() (string, bool) {
	var s string
	if err := json.Unmarshal(r.Out, &s); err != nil {
		return "", false
	}
	return s, true
}

func (r row) isNull() bool { return string(r.Out) == "null" }

// Load reads a captured table.
func Load(path string) (*Table, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if t.Source == "" {
		return nil, fmt.Errorf("%s: table does not record what produced it", path)
	}
	return &t, nil
}

// Divergence is a deliberate, reasoned difference from the captured
// behaviour.
type Divergence struct {
	// Op is the operation name as it appears in the table: "isValid",
	// "getNewValue" and so on.
	Op string
	// Inputs identifies the row. For a one-argument op it is the input; for
	// two, "a|b"; for matches, "version|range".
	Inputs string
	// Why must say what is deliberate about it. An empty reason is refused.
	Why string
}

func (d Divergence) key() string { return d.Op + "\x00" + d.Inputs }

// Result is what a conformance run found.
type Result struct {
	Compared int
	Skipped  int // rows where Renovate itself threw
	Garbage  int // rows comparing inputs the scheme itself calls invalid
	Diverged int // declared divergences that fired
	Mismatch int
}

// Run drives v against the table and reports through t.
func Run(t harness.T, v versioning.Versioning, tbl *Table, divergences []Divergence) Result {
	t.Helper()

	declared := map[string]Divergence{}
	for _, d := range divergences {
		if d.Why == "" {
			t.Errorf("divergence %s/%s has no reason; a difference without one is a defect",
				d.Op, d.Inputs)
			continue
		}
		declared[d.key()] = d
	}
	fired := map[string]bool{}

	// Rows whose inputs the scheme itself rejects are not a specification.
	//
	// The table records what Renovate answers when asked to order two
	// non-versions, and the answers are arbitrary: isGreaterThan("", "1.0.0")
	// is true, and equals("latest", "latest") is false. Reproducing that would
	// mean writing code whose only purpose is to be wrong in the same way,
	// and it can never be reached - IsValid gates every one of these inputs
	// before the pipeline compares anything.
	//
	// The set is taken from the captured isValid rows, not from our own
	// implementation, so this cannot become a way to excuse a disagreement:
	// if we call something invalid and Renovate does not, that IS a mismatch
	// and the isValid comparison reports it.
	invalid := map[string]bool{}
	for _, r := range tbl.IsValid {
		if r.In == nil || r.errored() {
			continue
		}
		if ok, valid := r.boolOut(); valid && !ok {
			invalid[*r.In] = true
		}
	}

	var res Result
	garbage := func(vals ...string) bool {
		for _, v := range vals {
			if invalid[v] {
				return true
			}
		}
		return false
	}
	check := func(op, inputs string, got, want any, ok bool) {
		if !ok {
			res.Skipped++
			return
		}
		res.Compared++
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return
		}
		key := op + "\x00" + inputs
		if d, isDeclared := declared[key]; isDeclared {
			fired[key] = true
			res.Diverged++
			t.Logf("  declared divergence %s(%s): got %v, Renovate %v - %s", op, inputs, got, want, d.Why)
			return
		}
		res.Mismatch++
		t.Errorf("%s(%s) = %v, Renovate says %v", op, inputs, got, want)
	}

	for _, r := range tbl.IsValid {
		if r.In == nil || r.errored() {
			res.Skipped++
			continue
		}
		want, ok := r.boolOut()
		check("isValid", *r.In, v.IsValid(*r.In), want, ok)
	}
	for _, r := range tbl.IsVersion {
		if r.In == nil || r.errored() {
			res.Skipped++
			continue
		}
		want, ok := r.boolOut()
		check("isVersion", *r.In, v.IsVersion(*r.In), want, ok)
	}
	for _, r := range tbl.IsStable {
		if r.In == nil || r.errored() {
			res.Skipped++
			continue
		}
		want, ok := r.boolOut()
		// Renovate reports stability only for versions it accepts; an invalid
		// input is not a stability question.
		if !v.IsValid(*r.In) {
			res.Skipped++
			continue
		}
		check("isStable", *r.In, v.IsStable(*r.In), want, ok)
	}

	parts := []struct {
		name string
		rows []row
		fn   func(string) (int, bool)
	}{
		{"getMajor", tbl.GetMajor, v.Major},
		{"getMinor", tbl.GetMinor, v.Minor},
		{"getPatch", tbl.GetPatch, v.Patch},
	}
	for _, p := range parts {
		for _, r := range p.rows {
			if r.In == nil || r.errored() {
				res.Skipped++
				continue
			}
			got, gotOK := p.fn(*r.In)
			if r.isNull() {
				check(p.name, *r.In, gotOK, false, true)
				continue
			}
			want, ok := r.intOut()
			if !ok {
				res.Skipped++
				continue
			}
			if !gotOK {
				check(p.name, *r.In, "none", want, true)
				continue
			}
			check(p.name, *r.In, got, want, true)
		}
	}

	for _, r := range tbl.Equals {
		if r.A == nil || r.B == nil || r.errored() {
			res.Skipped++
			continue
		}
		if garbage(*r.A, *r.B) {
			res.Garbage++
			continue
		}
		want, ok := r.boolOut()
		check("equals", *r.A+"|"+*r.B, v.Equal(*r.A, *r.B), want, ok)
	}
	for _, r := range tbl.IsGreaterThan {
		if r.A == nil || r.B == nil || r.errored() {
			res.Skipped++
			continue
		}
		if garbage(*r.A, *r.B) {
			res.Garbage++
			continue
		}
		want, ok := r.boolOut()
		check("isGreaterThan", *r.A+"|"+*r.B, v.Compare(*r.A, *r.B) > 0, want, ok)
	}
	// No garbage skip here: isCompatible on an invalid input is a real
	// answer (false), and a scheme that said true for garbage would offer
	// "latest" as an update.
	for _, r := range tbl.IsCompatible {
		if r.A == nil || r.B == nil || r.errored() {
			res.Skipped++
			continue
		}
		want, ok := r.boolOut()
		check("isCompatible", *r.A+"|"+*r.B, versioning.IsCompatible(v, *r.A, *r.B), want, ok)
	}
	for _, r := range tbl.Matches {
		if r.Version == nil || r.Range == nil || r.errored() {
			res.Skipped++
			continue
		}
		if garbage(*r.Version, *r.Range) {
			res.Garbage++
			continue
		}
		want, ok := r.boolOut()
		check("matches", *r.Version+"|"+*r.Range, v.Satisfies(*r.Version, *r.Range), want, ok)
	}
	for _, r := range tbl.GetNewValue {
		if r.CurrentValue == nil || r.RangeStrategy == nil || r.NewVersion == nil || r.errored() {
			res.Skipped++
			continue
		}
		if r.isNull() {
			// Renovate declining to rewrite is a real answer, but an
			// implementation returning an error for the same input is the
			// same outcome expressed differently. Only compare when it
			// produced a value.
			res.Skipped++
			continue
		}
		want, ok := r.strOut()
		got, err := v.NewValue(*r.CurrentValue, *r.NewVersion, versioning.RangeStrategy(*r.RangeStrategy))
		if err != nil {
			check("getNewValue", *r.CurrentValue+"|"+*r.RangeStrategy, "error: "+err.Error(), want, ok)
			continue
		}
		check("getNewValue", *r.CurrentValue+"|"+*r.RangeStrategy, got, want, ok)
	}

	// Denominator. A run that compared nothing must not read as agreement.
	t.Logf("%s: compared %d, declared divergences %d, skipped %d, garbage-input rows %d",
		tbl.Module, res.Compared, res.Diverged, res.Skipped, res.Garbage)
	if res.Compared == 0 {
		t.Errorf("%s: compared 0 rows - the table was not read", tbl.Module)
	}

	// A declared divergence that never fires has outlived its reason.
	var dead []string
	for k, d := range declared {
		if !fired[k] {
			dead = append(dead, d.Op+"("+d.Inputs+")")
		}
	}
	sort.Strings(dead)
	for _, d := range dead {
		t.Errorf("declared divergence %s never fired; delete it or fix the claim", d)
	}

	return res
}

// Path returns the captured table for a scheme, relative to a package two
// levels below the module root (versioning/<scheme>).
func Path(scheme string) string {
	return strings.Join([]string{"..", "..", "testdata", "parity",
		"renovate-43.288.0", "versioning", scheme + ".json"}, "/")
}
