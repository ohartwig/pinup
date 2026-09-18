// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package preset

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeReader struct {
	files map[string]string // "project|path|ref"
	calls []string
}

func (f *fakeReader) ReadFile(_ context.Context, project, path, ref string) ([]byte, error) {
	key := project + "|" + path + "|" + ref
	f.calls = append(f.calls, key)
	if s, ok := f.files[key]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("404")
}

func TestParseLocal(t *testing.T) {
	for in, want := range map[string][3]string{
		"devops/renovate-runner":                {"devops/renovate-runner", "default.json", ""},
		"devops/renovate-runner:default.json":   {"devops/renovate-runner", "default.json", ""},
		"devops/renovate-runner:release-fast":   {"devops/renovate-runner", "release-fast.json", ""},
		"moselwal/dev//config/renovate/default": {"moselwal/dev", "config/renovate/default.json", ""},
		"a/b//sub/dir":                          {"a/b", "sub/dir.json", ""},
		"a/b//x/y#main":                         {"a/b", "x/y.json", "main"},
		"a/b//top":                              {"a/b", "top.json", ""},
		"a/b:c/d":                               {"a/b", "c/d.json", ""},
	} {
		p, path, ref, err := ParseLocal(in)
		if err != nil || p != want[0] || path != want[1] || ref != want[2] {
			t.Errorf("ParseLocal(%q) = %q %q %q %v, want %v", in, p, path, ref, err, want)
		}
	}
	if _, _, _, err := ParseLocal("noslash"); err == nil {
		t.Error("a name without a project path must be refused")
	}
	// Measured: Renovate refuses "prohibited sub-preset".
	if _, _, _, err := ParseLocal("a/b//sub/dir:name"); err == nil {
		t.Error("a //path combined with a :name must be refused")
	}
}

func TestChainAliasRemoteBuiltin(t *testing.T) {
	global := map[string]any{"extends": []any{":dependencyDashboard"}, "labels": []any{"renovate"}}
	reader := &fakeReader{files: map[string]string{
		"moselwal/dev|config/renovate/default.json|": `{ // comment
  "extends": ["local>devops/renovate-runner"], "prConcurrentLimit": 5 /* five */ }`,
	}}
	src := Chain{
		Aliases{"local>devops/renovate-runner": global, "local>devops/renovate-runner:default.json": global},
		Remote{Reader: reader},
		Builtin(),
	}
	got, err := Resolve(map[string]any{"extends": []any{"local>moselwal/dev//config/renovate/default"}, "schedule": []any{"x"}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config["prConcurrentLimit"] != float64(5) || got.Config["dependencyDashboard"] != true {
		t.Errorf("resolved %v", got.Config)
	}
	if labels, _ := got.Config["labels"].([]any); len(labels) != 1 {
		t.Errorf("the alias must contribute the runner file's keys: %v", got.Config)
	}
	if strings.Join(got.Visited, ",") != "local>moselwal/dev//config/renovate/default,local>devops/renovate-runner,:dependencyDashboard" {
		t.Errorf("visited %v", got.Visited)
	}
	if len(reader.calls) != 1 {
		t.Errorf("the alias must not be fetched: calls %v", reader.calls)
	}
	// An unreachable local> preset is an error naming it.
	_, err = Resolve(map[string]any{"extends": []any{"local>nowhere/nothing"}}, src)
	if err == nil || !strings.Contains(err.Error(), "nowhere/nothing") {
		t.Errorf("want an error naming the preset, got %v", err)
	}
}

// A preset that spells its extension is fetched under that name and parsed
// by it, through the parser the caller hands in; without one, JSON with
// comments as before. No probing: "local>a/b" is a/b's default.json.
func TestRemoteParsesByTheNameItWasGiven(t *testing.T) {
	reader := &fakeReader{files: map[string]string{
		"pinup/runner|default.yaml|": "prConcurrentLimit: 7\nlabels:\n  - pinup\n",
		"pinup/runner|default.json|": `{"prConcurrentLimit": 3}`,
	}}
	parsed := []string{}
	parse := func(raw []byte, name string) (map[string]any, error) {
		parsed = append(parsed, name)
		if strings.HasSuffix(name, ".yaml") {
			return map[string]any{"prConcurrentLimit": float64(7), "labels": []any{"pinup"}}, nil
		}
		var doc map[string]any
		return doc, json.Unmarshal(raw, &doc)
	}
	for _, c := range []struct {
		name string
		want float64
	}{
		{"local>pinup/runner:default.yaml", 7},
		{"local>pinup/runner:default.json", 3},
		{"local>pinup/runner", 3},
	} {
		doc, _, ok, err := Remote{Reader: reader, Parse: parse}.Get(c.name)
		if err != nil || !ok || doc["prConcurrentLimit"] != c.want {
			t.Errorf("%s: %v %v %v", c.name, doc, ok, err)
		}
	}
	if strings.Join(parsed, ",") != "default.yaml,default.json,default.json" {
		t.Errorf("parsed by name: %v", parsed)
	}
	// Without a parser the YAML is read as JSON, and that is an error
	// naming the file, not an empty preset.
	if _, _, _, err := (Remote{Reader: reader}).Get("local>pinup/runner:default.yaml"); err == nil {
		t.Error("YAML without a parser must fail loudly")
	}
	for _, name := range []string{"default.yaml", "default.yml", "default.jsonc", "default.json5", "default.json"} {
		if !HasConfigExtension(name) {
			t.Errorf("%s: not recognised", name)
		}
	}
	if HasConfigExtension("default") || HasConfigExtension("default.txt") {
		t.Error("a bare or foreign name has no configuration extension")
	}
}
