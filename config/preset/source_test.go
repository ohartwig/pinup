// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package preset

import (
	"context"
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
		"devops/renovate-runner":                     {"devops/renovate-runner", "default.json", ""},
		"devops/renovate-runner:default.json":        {"devops/renovate-runner", "default.json", ""},
		"devops/renovate-runner:release-fast":        {"devops/renovate-runner", "release-fast.json", ""},
		"moselwal/dev//config/renovate/default":      {"moselwal/dev", "config/renovate/default/default.json", ""},
		"moselwal/dev//config/renovate:default#main": {"moselwal/dev", "config/renovate/default.json", "main"},
	} {
		p, path, ref, err := ParseLocal(in)
		if err != nil || p != want[0] || path != want[1] || ref != want[2] {
			t.Errorf("ParseLocal(%q) = %q %q %q %v, want %v", in, p, path, ref, err, want)
		}
	}
	if _, _, _, err := ParseLocal("noslash"); err == nil {
		t.Error("a name without a project path must be refused")
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
	got, err := Resolve(map[string]any{"extends": []any{"local>moselwal/dev//config/renovate:default"}, "schedule": []any{"x"}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config["prConcurrentLimit"] != float64(5) || got.Config["dependencyDashboard"] != true {
		t.Errorf("resolved %v", got.Config)
	}
	if labels, _ := got.Config["labels"].([]any); len(labels) != 1 {
		t.Errorf("the alias must contribute the runner file's keys: %v", got.Config)
	}
	if strings.Join(got.Visited, ",") != "local>moselwal/dev//config/renovate:default,local>devops/renovate-runner,:dependencyDashboard" {
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
