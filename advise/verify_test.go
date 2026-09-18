// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"strings"
	"testing"
)

// The gate passes a rewrite that changes exactly what its fixes declared,
// and refuses everything else. The refusals are the point: a rewrite that
// flips a neighbour, a "preserving" fix that dropped a key, a duplicated
// preset nobody declared, a declared file index that does not map to the
// resolved index that changed, a rewrite that does not parse.
func TestVerifyIsTheGate(t *testing.T) {
	twoRules := `{"extends": ["p:base"], "packageRules": [{"matchDatasources": ["npm"], "enabled": false}, {"matchDatasources": ["docker"], "enabled": false}]}`
	for _, c := range []struct {
		name, before, after string
		fixes               []Fix
		refused             string // "" when the rewrite passes
	}{
		{"a declared set", `{"packageRules": [{"matchDatasources": ["docker"], "automerge": true}]}`,
			`{"packageRules": [{"matchDatasources": ["docker"], "automerge": true, "matchUpdateTypes": ["minor"]}]}`,
			[]Fix{{Pointer: "/packageRules/0/matchUpdateTypes", Op: OpSet, Changes: []string{"/packageRules/0/matchUpdateTypes"}}}, ""},
		{"a preserving removal of a repeated key", `{"extends": ["p:base"], "labels": ["dep"]}`, `{"extends": ["p:base"]}`,
			[]Fix{{Pointer: "/labels", Op: OpRemove}}, ""},
		{"a whole rule removed shifts the rest", twoRules,
			`{"extends": ["p:base"], "packageRules": [{"matchDatasources": ["docker"], "enabled": false}]}`,
			[]Fix{{Pointer: "/packageRules/0", Op: OpRemove, Changes: []string{"/packageRules/0"}}}, ""},
		{"a zero written as the null it migrates to", `{"minimumReleaseAge": "0"}`, `{"minimumReleaseAge": null}`,
			[]Fix{{Pointer: "/minimumReleaseAge", Op: OpSet}}, ""},
		{"a rule's only key removed leaves an empty rule", `{"packageRules": [{"nonesuch": 1}]}`, `{"packageRules": [{}]}`,
			[]Fix{{Pointer: "/packageRules/0/nonesuch", Op: OpRemove, Changes: []string{"/packageRules/0/nonesuch"}}}, ""},

		{"an undeclared neighbour flipped", `{"schedule": ["before 5am"], "automerge": false}`, `{"schedule": ["before 6am"], "automerge": true}`,
			[]Fix{{Pointer: "/schedule", Op: OpSet, Changes: []string{"/schedule"}}}, "automerge changed (false became true)"},
		{"a preserving fix that dropped a neighbour", `{"prHourlyLimit": 5, "prConcurrentLimit": 3}`, `{"prHourlyLimit": 5}`,
			[]Fix{{Pointer: "/prHourlyLimit", Op: OpSet}}, "prConcurrentLimit changed (3 became 10)"},
		{"a rewrite that does not parse", `{"a": 1}`, `{"a": `,
			[]Fix{{Pointer: "/a", Op: OpRemove, Changes: []string{"/a"}}}, "does not parse"},
		{"a duplicated preset nobody declared", `{"extends": ["p:base"]}`, `{"extends": ["p:base", "p:base"]}`,
			[]Fix{{Pointer: "/extends", Op: OpAppend}}, "packageRules[1]"},
		{"a declared file index that is not the one that changed", twoRules,
			`{"extends": ["p:base"], "packageRules": [{"matchDatasources": ["npm"], "enabled": false}, {"matchDatasources": ["docker"], "enabled": true}]}`,
			[]Fix{{Pointer: "/packageRules/0/enabled", Op: OpSet, Changes: []string{"/packageRules/0/enabled"}}}, "packageRules[2].enabled changed"},
		{"a change under a removed rule's floor is admitted, above it is not", twoRules,
			`{"extends": ["p:base"], "packageRules": [{"matchDatasources": ["npm"], "enabled": false}, {"matchDatasources": ["docker"], "enabled": false}], "labels": ["x"]}`,
			[]Fix{{Pointer: "/packageRules/1", Op: OpRemove, Changes: []string{"/packageRules/1"}}}, "labels[0] changed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := Verify([]byte(c.before), []byte(c.after), "x.json", sources, c.fixes)
			if c.refused == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("passed; want a refusal naming %q", c.refused)
			}
			if !strings.Contains(err.Error(), c.refused) {
				t.Errorf("refusal = %q, want it to name %q", err, c.refused)
			}
		})
	}
}
