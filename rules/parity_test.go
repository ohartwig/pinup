// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package rules

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

const (
	resolvedPath = "../testdata/parity/renovate-43.288.0/full-resolved.json"
	vectorsPath  = "../testdata/parity/renovate-43.288.0/rules/vectors.ndjson"
	// vectorFloor is asserted so an empty or truncated table cannot pass.
	vectorFloor = 3000
)

type vector struct {
	Repo    string         `json:"repo"`
	Input   map[string]any `json:"input"`
	Matched []int          `json:"matched"`
	Delta   map[string]any `json:"delta"`
}

func loadVectors(t *testing.T) (map[string]any, []vector) {
	t.Helper()
	raw, err := os.ReadFile(resolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var vs []vector
	first := true
	for sc.Scan() {
		if first {
			first = false // header
			continue
		}
		var v vector
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	return base, vs
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func subjectOf(in map[string]any) Subject {
	return Subject{
		DepName: str(in, "depName"), PackageName: str(in, "packageName"),
		Datasource: str(in, "datasource"), Manager: str(in, "manager"),
		PackageFile: str(in, "packageFile"), DepType: str(in, "depType"),
		CurrentValue: str(in, "currentValue"), CurrentVersion: str(in, "currentVersion"),
		Versioning: str(in, "versioning"), UpdateType: str(in, "updateType"),
	}
}

// The differential harness: every vector Renovate resolved, resolved here,
// and both the fired-rule list and the resulting delta must agree exactly.
func TestResolvesEveryVectorAsRenovateDid(t *testing.T) {
	base, vs := loadVectors(t)
	if len(vs) < vectorFloor {
		t.Fatalf("only %d vectors; the table was barely read", len(vs))
	}
	rulesRaw, _ := base["packageRules"].([]any)
	eng, err := Compile(rulesRaw, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range eng.Warnings {
		t.Logf("compile: %s", w)
	}

	matchedMismatch, deltaMismatch := 0, 0
	byKey := map[string]int{}
	for i, v := range vs {
		res := eng.Apply(base, subjectOf(v.Input))
		if !equalInts(res.Matched, v.Matched) {
			matchedMismatch++
			if matchedMismatch <= 5 {
				t.Errorf("vector %d %s %s (%s): fired %v, Renovate fired %v",
					i, v.Repo, str(v.Input, "depName"), str(v.Input, "updateType"), res.Matched, v.Matched)
			}
			continue
		}
		mine := deltaOf(base, v.Input, res)
		if !reflect.DeepEqual(mine, v.Delta) {
			deltaMismatch++
			for _, k := range diffKeys(mine, v.Delta) {
				byKey[k]++
				if byKey[k] <= 2 {
					t.Errorf("vector %d %s %s (%s) key %s: got %s, Renovate %s",
						i, v.Repo, str(v.Input, "depName"), str(v.Input, "updateType"), k, short(mine[k]), short(v.Delta[k]))
				}
			}
		}
	}
	t.Logf("%d vectors: %d fired-rule mismatches, %d delta mismatches; keys: %v",
		len(vs), matchedMismatch, deltaMismatch, byKey)
	if matchedMismatch+deltaMismatch != 0 {
		t.Fail()
	}
}

// deltaOf mirrors the probe: every key whose resolved value differs from
// the base (or from the input, for input keys a rule rewrote), with removed
// keys marked. description is excluded there too - matched carries it.
func deltaOf(base, input map[string]any, res Resolution) map[string]any {
	out := map[string]any{}
	for k, v := range res.Config {
		if k == "packageRules" || k == "description" {
			continue
		}
		if in, ok := input[k]; ok {
			// The subject's fields sit in the config in Renovate, so an
			// input key counts as changed only when a rule rewrote it.
			if len(res.Wrote[k]) > 0 && !reflect.DeepEqual(v, in) {
				out[k] = v
			}
			continue
		}
		if !reflect.DeepEqual(v, base[k]) {
			out[k] = v
		}
	}
	if res.SkipReason != "" {
		out["skipReason"] = res.SkipReason
	}
	for k := range base {
		if k == "packageRules" {
			continue
		}
		if _, ok := res.Config[k]; !ok {
			if _, ok := input[k]; !ok {
				out[k] = map[string]any{"__deleted": true}
			}
		}
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func diffKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !reflect.DeepEqual(a[k], b[k]) && !seen[k] {
			out, seen[k] = append(out, k), true
		}
	}
	for k := range b {
		if !reflect.DeepEqual(a[k], b[k]) && !seen[k] {
			out, seen[k] = append(out, k), true
		}
	}
	// A key present with a null value is not the same as an absent key.
	for k := range a {
		if _, ok := b[k]; !ok && !seen[k] {
			out, seen[k] = append(out, k), true
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok && !seen[k] {
			out, seen[k] = append(out, k), true
		}
	}
	sort.Strings(out)
	return out
}

func short(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
