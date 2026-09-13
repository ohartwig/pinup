// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package toyaml_test

import (
	"testing"

	"github.com/ohartwig/pinup/config/toyaml"
	"github.com/ohartwig/pinup/yamlx"
)

// The runner's configuration round trip - converted, loaded, resolved to
// the same 771 rules - is asserted in cmd/pinup (TestMigrateToYAML): this
// package sits below config and may not import it.

// Order is kept, descriptions become comments above what they describe,
// and the scalars YAML would misread are quoted: "3.10" would be 3.1, "no"
// false, "1" a number; "^8.5" and "*.yaml" carry characters outside the
// plain alphabet; a preset name with a colon stays plain.
func TestOrderCommentsAndQuoting(t *testing.T) {
	src := []byte(`{
  "description": "top",
  "zebra": "3.10",
  "extends": ["config:recommended"],
  "packageRules": [
    {"description": ["first rule", "", "second paragraph"], "matchPackageNames": ["*.yaml", "^8.5", "no", "1", "on"], "enabled": false, "count": 3, "ratio": 0.5},
    {"matchDepNames": [], "labels": {}}
  ],
  "alpha": null
}`)
	out, err := toyaml.Convert(src, "x.json", toyaml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	want := `# top
zebra: "3.10"
extends:
  - config:recommended
packageRules:
  # first rule
  #
  # second paragraph
  - matchPackageNames:
      - "*.yaml"
      - "^8.5"
      - "no"
      - "1"
      - "on"
    enabled: false
    count: 3
    ratio: 0.5
  - matchDepNames: []
    labels: {}
alpha: null
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	raw := map[string]any{}
	if err := yamlx.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["zebra"] != "3.10" {
		t.Errorf("zebra = %v (%T), want the string 3.10", raw["zebra"], raw["zebra"])
	}
	rules := raw["packageRules"].([]any)
	names := rules[0].(map[string]any)["matchPackageNames"].([]any)
	if names[2] != "no" || names[3] != "1" || names[4] != "on" {
		t.Errorf("quoted scalars came back as %v", names)
	}
	if _, ok := raw["description"]; ok {
		t.Error("the top-level description is a comment, not a key")
	}
}

// JSONC comments and JSON5 names are read on the way in; YAML is refused,
// since it is what comes out.
func TestInputFormats(t *testing.T) {
	if _, err := toyaml.Convert([]byte("{\n  // a comment\n  \"a\": \"b\", // trailing\n}\n"), "x.jsonc", toyaml.Options{}); err != nil {
		t.Errorf("jsonc: %v", err)
	}
	out, err := toyaml.Convert([]byte("{ a: 'b', c: [1, 2,], }"), "x.json5", toyaml.Options{})
	if err != nil || string(out) != "a: b\nc:\n  - 1\n  - 2\n" {
		t.Errorf("json5: %q, %v", out, err)
	}
	if _, err := toyaml.Convert([]byte("a: b\n"), "x.yaml", toyaml.Options{}); err == nil {
		t.Error("yaml in must be refused")
	}
	if _, err := toyaml.Convert([]byte(`["not", "an", "object"]`), "x.json", toyaml.Options{}); err == nil {
		t.Error("a non-object document must be refused")
	}
}
