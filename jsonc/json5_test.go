// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package jsonc

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestFromJSON5QuotesKeysAndRewritesSingleQuotes(t *testing.T) {
	src := []byte(`{
  $schema: "https://x/schema.json",
  extends: ['local>a//b', "c"], // trailing comment
  description: 'it\'s "quoted"',
  packageRules: [
    { matchManagers: ["custom.regex"], automerge: true, minimumReleaseAge: null, count: 3, },
  ],
  /* block */ nested: { deep_key: 'v' },
}`)
	var got map[string]any
	if err := json.Unmarshal(FromJSON5(src), &got); err != nil {
		t.Fatalf("%v\n%s", err, FromJSON5(src))
	}
	want := map[string]any{
		"$schema":     "https://x/schema.json",
		"extends":     []any{"local>a//b", "c"},
		"description": `it's "quoted"`,
		"packageRules": []any{map[string]any{
			"matchManagers": []any{"custom.regex"}, "automerge": true, "minimumReleaseAge": nil, "count": float64(3),
		}},
		"nested": map[string]any{"deep_key": "v"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
	// Plain JSON is a fixed point: nothing to quote, nothing to strip.
	plain := []byte(`{"a": "b:c", "true": true, "n": null}`)
	if string(FromJSON5(plain)) != string(plain) {
		t.Errorf("plain JSON changed: %s", FromJSON5(plain))
	}
}

// The one .json5 in the estate has to decode; its shape is the reason the
// rewrite exists at all.
func TestTheEstateJSON5Decodes(t *testing.T) {
	src, err := os.ReadFile("/Volumes/Samsung_X5/Projects/moselwal/deploy/moselwal-websites-deploy/renovate.json5")
	if err != nil {
		t.Skipf("estate checkout not present: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(FromJSON5(src), &got); err != nil {
		t.Fatal(err)
	}
	rules, _ := got["packageRules"].([]any)
	if len(rules) != 2 || got["$schema"] == nil {
		t.Errorf("decoded %d rules, $schema %v", len(rules), got["$schema"])
	}
}
