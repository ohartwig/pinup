// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/versioning"
)

type vector struct {
	Repo    string         `json:"repo"`
	Input   map[string]any `json:"input"`
	Matched []int          `json:"matched"`
	Delta   map[string]any `json:"delta"`
}

// loadVectors resolves the root's configuration here - defaults, pinup's
// own preset library, the file - as the base the vectors are applied to,
// and reads the vectors Renovate recorded against its own resolution of
// the same file. The two bases agree on every option (config's parity
// test); they differ in the preset rules, which is the point of the
// comparison below.
func loadVectors(t *testing.T) (map[string]any, []vector) {
	t.Helper()
	r, _, err := config.ResolveFile(fixture.Config(t), preset.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	base := r.Raw
	f, err := os.Open(fixture.Captured(t, "rules", "vectors.ndjson"))
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
// and the resulting delta must agree exactly - every key a rule wrote, by
// value - except the keys pinup does not read (unreadKeys). The fired-rule
// lists agree on the file's own rules, by index translated between the two
// numberings: Renovate's capture is the production shape, the file as the
// global configuration and as the repository's own - its rules at 0-48
// and again last, its 722 preset rules twice between - and this
// resolution puts pinup's library rules first and the file's last. A
// preset rule Renovate fired has no index here; the delta is what says
// whether the library carries its effect, and a library that lost one goes
// red on the keys it wrote (measured: the semantic commit types, the
// Node.js topic and versioning, the PHPStan and Symfony groups, the pinned
// digests and dev dependencies, the k3s, clamav and grafana versionings).
func TestResolvesEveryVectorAsRenovateDid(t *testing.T) {
	base, vs := loadVectors(t)
	// The count is pinned so an empty or truncated table cannot pass.
	if want := fixture.Expect(t).RuleVectors; len(vs) != want {
		t.Fatalf("%d vectors, expect.json pins %d; the table was recaptured or barely read", len(vs), want)
	}
	rulesRaw, _ := base["packageRules"].([]any)
	eng, err := Compile(rulesRaw, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range eng.Warnings {
		t.Logf("compile: %s", w)
	}
	own := fixture.Expect(t).RulesOwn
	presets := len(rulesRaw) - own
	theirTotal := renovateRuleCount(t)
	if theirTotal != 2*own+2*722 {
		t.Fatalf("Renovate's capture has %d rules, not the production shape of %d", theirTotal, 2*own+2*722)
	}

	matchedMismatch, deltaMismatch := 0, 0
	byKey := map[string]int{}
	for i, v := range vs {
		res := eng.Apply(base, subjectOf(v.Input))
		mineOwn := ownRules(res.Matched, func(i int) (int, bool) { return i - presets, i >= presets })
		theirOwn := ownRules(v.Matched, func(i int) (int, bool) {
			switch {
			case i < own:
				return i, true
			case i >= theirTotal-own:
				return i - (theirTotal - own), true
			}
			return 0, false
		})
		if !equalInts(mineOwn, theirOwn) {
			matchedMismatch++
			if matchedMismatch <= 5 {
				t.Errorf("vector %d %s %s (%s): fired own rules %v, Renovate fired %v",
					i, v.Repo, str(v.Input, "depName"), str(v.Input, "updateType"), mineOwn, theirOwn)
			}
			continue
		}
		mine := deltaOf(base, v.Input, res)
		theirs := map[string]any{}
		for k, val := range v.Delta {
			if !unreadKeys[k] {
				theirs[k] = val
			}
		}
		for k := range unreadKeys {
			delete(mine, k)
		}
		if !reflect.DeepEqual(mine, theirs) {
			deltaMismatch++
			for _, k := range diffKeys(mine, theirs) {
				byKey[k]++
				if byKey[k] <= 2 {
					t.Errorf("vector %d %s %s (%s) key %s: got %s, Renovate %s",
						i, v.Repo, str(v.Input, "depName"), str(v.Input, "updateType"), k, short(mine[k]), short(theirs[k]))
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

// unreadKeys are the keys a Renovate preset writes that pinup does not
// read, and whose presets the library therefore resolves to nothing:
// the merge-request body is rendered by report from the plan
// (prBodyColumns, prBodyDefinitions), compare links come from changelog
// (changelogUrl), abandonment detection is not implemented
// (abandonmentThreshold). Listed here so a new key a rule writes is a
// failure until it is either read or named.
var unreadKeys = map[string]bool{
	"prBodyColumns": true, "prBodyDefinitions": true, "changelogUrl": true, "abandonmentThreshold": true,
}

// renovateRuleCount reads how many rules the capture's base carried, from
// the vector file's header.
func renovateRuleCount(t *testing.T) int {
	t.Helper()
	f, err := os.Open(fixture.Captured(t, "rules", "vectors.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var hdr struct {
		Rules int `json:"rules"`
	}
	if err := json.NewDecoder(f).Decode(&hdr); err != nil {
		t.Fatal(err)
	}
	return hdr.Rules
}

// ownRules keeps the file's own rules in a fired list, each renumbered by
// ident to the file's own numbering, sorted and deduplicated - the
// production capture fires the file's rules twice.
func ownRules(matched []int, ident func(int) (int, bool)) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, i := range matched {
		if n, ok := ident(i); ok && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
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
