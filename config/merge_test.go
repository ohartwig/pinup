// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

func layer(t *testing.T, source, doc string) Layer {
	t.Helper()
	raw := map[string]any{}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("%s: %v", source, err)
	}
	return Layer{Source: source, Raw: raw}
}

func TestMergeSemantics(t *testing.T) {
	for _, c := range []struct {
		name  string
		docs  []string
		check func(*testing.T, *Resolved)
	}{
		{
			name: "objects deep-merge",
			docs: []string{`{"a":{"x":1,"y":2}}`, `{"a":{"y":3,"z":4}}`},
			check: func(t *testing.T, r *Resolved) {
				want := map[string]any{"x": 1.0, "y": 3.0, "z": 4.0}
				if got := r.Raw["a"]; !reflect.DeepEqual(got, want) {
					t.Errorf("got %#v, want %#v", got, want)
				}
			},
		},
		{
			name: "arrays replace",
			docs: []string{`{"labels":["a","b"]}`, `{"labels":["c"]}`},
			check: func(t *testing.T, r *Resolved) {
				want := []any{"c"}
				if got := r.Raw["labels"]; !reflect.DeepEqual(got, want) {
					t.Errorf("got %#v, want %#v - arrays replace, they do not merge", got, want)
				}
			},
		},
		{
			name: "packageRules concatenate, in order",
			docs: []string{
				`{"packageRules":[{"id":"preset-1"},{"id":"preset-2"}]}`,
				`{"packageRules":[{"id":"repo-1"}]}`,
			},
			check: func(t *testing.T, r *Resolved) {
				rules, _ := r.Raw["packageRules"].([]any)
				if len(rules) != 3 {
					t.Fatalf("got %d rules, want 3 - packageRules concatenate", len(rules))
				}
				var ids []string
				for _, x := range rules {
					ids = append(ids, x.(map[string]any)["id"].(string))
				}
				if want := []string{"preset-1", "preset-2", "repo-1"}; !reflect.DeepEqual(ids, want) {
					t.Errorf("order %v, want %v - later layers append", ids, want)
				}
				// The appended rule must know the index it ended up at.
				o, ok := r.Winner("/packageRules/2")
				if !ok || o.Rule != 2 {
					t.Errorf("the repo rule records rule index %d, want 2", o.Rule)
				}
			},
		},
		{
			name: "an explicit null clears",
			docs: []string{`{"minimumReleaseAge":"24 hours"}`, `{"minimumReleaseAge":null}`},
			check: func(t *testing.T, r *Resolved) {
				if v, present := r.Raw["minimumReleaseAge"]; present {
					t.Errorf("value survives as %#v; null must clear it", v)
				}
			},
		},
		{
			name: "zero is not null",
			docs: []string{`{"minimumReleaseAge":"24 hours"}`, `{"minimumReleaseAge":"0"}`},
			check: func(t *testing.T, r *Resolved) {
				if got := r.Raw["minimumReleaseAge"]; got != "0" {
					t.Errorf(`got %#v, want "0" - "no hold" and "nothing inherited" are different answers`, got)
				}
			},
		},
		{
			name: "a nested object from one layer does not alias the layer it came from",
			docs: []string{`{"lockFileMaintenance":{"enabled":true}}`, `{"lockFileMaintenance":{"automerge":true}}`},
			check: func(t *testing.T, r *Resolved) {
				m := r.Raw["lockFileMaintenance"].(map[string]any)
				if m["enabled"] != true || m["automerge"] != true {
					t.Errorf("nested objects did not deep-merge: %#v", m)
				}
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var ls []Layer
			for i, d := range c.docs {
				ls = append(ls, layer(t, "file:l"+string(rune('0'+i)), d))
			}
			c.check(t, Merge(ls...))
		})
	}
}

// The acceptance check for the merge engine: four layers, one key set three
// times and cleared once, and the explanation must name all four in order.
func TestExplainNamesEveryLayerInOrder(t *testing.T) {
	r := Merge(
		layer(t, "builtin", `{"minimumReleaseAge":"7 days"}`),
		layer(t, "preset:config:recommended", `{"minimumReleaseAge":"3 days"}`),
		layer(t, "file:default.json", `{"minimumReleaseAge":"24 hours"}`),
		layer(t, "file:renovate.json", `{"minimumReleaseAge":null}`),
	)

	chain := r.Prov["/minimumReleaseAge"]
	if len(chain) != 4 {
		t.Fatalf("chain has %d entries, want 4 - the whole chain is kept, not just the winner", len(chain))
	}
	wantSources := []string{"builtin", "preset:config:recommended", "file:default.json", "file:renovate.json"}
	for i, o := range chain {
		if o.Source != wantSources[i] {
			t.Errorf("chain[%d] is %q, want %q", i, o.Source, wantSources[i])
		}
		if i > 0 && o.Order <= chain[i-1].Order {
			t.Errorf("chain[%d] has order %d, not after %d - application order is lost",
				i, o.Order, chain[i-1].Order)
		}
	}

	win, ok := r.Winner("/minimumReleaseAge")
	if !ok || win.Source != "file:renovate.json" {
		t.Errorf("winner is %q, want the clearing layer", win.Source)
	}

	out := r.Explain("/minimumReleaseAge")
	for _, s := range wantSources {
		if !strings.Contains(out, s) {
			t.Errorf("Explain output omits %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "> file:renovate.json") {
		t.Errorf("Explain does not mark the winner:\n%s", out)
	}

	// And the value really is gone, not merely explained away.
	if _, present := r.Raw["minimumReleaseAge"]; present {
		t.Error("the clearing layer explained itself but did not clear the value")
	}
}

func TestExplainSaysWhenNothingSetIt(t *testing.T) {
	r := Merge(layer(t, "builtin", `{"a":1}`))
	out := r.Explain("/nosuchkey")
	if !strings.Contains(out, "never set") {
		t.Errorf("an unset key should say so, not return an empty chain: %q", out)
	}
}

// Provenance must be complete: every leaf of the merged document has an
// origin. The count is asserted so a walk that found nothing cannot pass.
func TestEveryLeafOfTheRealConfigHasProvenance(t *testing.T) {
	l, err := LoadFile("../testdata/parity/config/default.json")
	if err != nil {
		t.Fatal(err)
	}
	r := Merge(l)

	leaves := Leaves(r.Raw, "")
	t.Logf("merged document has %d leaves, %d pointers recorded", len(leaves), len(r.Prov))
	// A leaf is a scalar or an array: arrays replace wholesale, so the array
	// is the unit that carries an origin, not its elements. packageRules are
	// the exception and are checked by index below.
	if len(leaves) < 70 {
		t.Fatalf("only %d leaves; the walk did not reach the config", len(leaves))
	}

	var missing []string
	for _, p := range leaves {
		if len(r.Prov[p]) == 0 {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		show := missing
		if len(show) > 10 {
			show = show[:10]
		}
		t.Errorf("%d leaves carry no provenance, e.g. %v", len(missing), show)
	}

	// Every packageRule must be locatable by its index.
	rules, _ := r.Raw["packageRules"].([]any)
	if len(rules) != 48 {
		t.Errorf("found %d packageRules, expected 48 - the captured config changed", len(rules))
	}
	for i := range rules {
		if _, ok := r.Winner("/packageRules/" + itoa(i)); !ok {
			t.Errorf("packageRules[%d] has no provenance", i)
		}
	}
	for _, o := range r.Prov["/packageRules/4"] {
		if o.Rule != 4 {
			t.Errorf("packageRules[4] records rule index %d, want 4", o.Rule)
		}
		if o.Rule == model.NoRule {
			t.Error("a rule origin must carry its index")
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
