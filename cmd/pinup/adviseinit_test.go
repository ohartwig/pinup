// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/model"
)

func plant(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func customDeps(plan *model.Plan) []string {
	var out []string
	for _, d := range plan.Deps {
		if d.CustomManager != model.NoCustomManager {
			out = append(out, d.File+" "+d.DepName+" "+d.CurrentValue)
		}
	}
	slices.Sort(out)
	return out
}

// The whole path a new user takes: the proposal names the managers the
// tree has, reads the annotation already there, and once the annotation
// it suggests for the compose image is written, that pin is a dependency
// too. A proposal whose suggestion did not match its own manager would
// leave the user exactly where they started.
func TestAdviseInitProposesWhatTheTreeNeeds(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	root := plant(t, map[string]string{
		"Dockerfile":   "FROM docker.io/library/alpine:3.20.3\n",
		"compose.yaml": "services:\n  ca:\n    image: smallstep/step-ca:0.29.0\n",
		"ci/tools.yml": "variables:\n  # renovate: datasource=github-releases depName=helm/helm\n  HELM_VERSION: \"3.20.1\"\n",
	})
	p, err := proposeConfig(context.Background(), root, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Config["enabledManagers"]; !reflect.DeepEqual(got, []any{"dockerfile", "custom.regex"}) {
		t.Errorf("enabledManagers = %v", got)
	}
	want := []model.Unmanaged{{File: "compose.yaml", Line: 3, Value: "0.29.0"}}
	if !slices.Equal(p.Unmanaged, want) {
		t.Errorf("unmanaged = %+v, want %+v", p.Unmanaged, want)
	}
	if !slices.Equal(p.Annotated, []string{"ci/tools.yml"}) {
		t.Errorf("annotated = %v", p.Annotated)
	}
	suggestion := p.Annotations["compose.yaml:3"]
	if suggestion != "# renovate: datasource=docker depName=smallstep/step-ca" {
		t.Errorf("suggestion = %q", suggestion)
	}

	// Under the proposal the existing annotation is read.
	plan, err := offlinePlan(context.Background(), root, p.Config, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := customDeps(plan); !slices.Equal(got, []string{"ci/tools.yml helm/helm 3.20.1"}) {
		t.Errorf("custom deps under the proposal = %v", got)
	}
	// And with the suggested annotation written, the compose pin is too,
	// and the scan has nothing left to report.
	compose := "services:\n  ca:\n    " + suggestion + "\n    image: smallstep/step-ca:0.29.0\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err = offlinePlan(context.Background(), root, p.Config, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := customDeps(plan); !slices.Equal(got, []string{"ci/tools.yml helm/helm 3.20.1", "compose.yaml smallstep/step-ca 0.29.0"}) {
		t.Errorf("custom deps after annotating = %v", got)
	}
	if len(plan.Unmanaged) != 0 {
		t.Errorf("still unmanaged after annotating: %v", plan.Unmanaged)
	}
}

// What --init prints is the file a user saves: it parses as the
// configuration it describes, extends first, and a tree with nothing to
// manage still gets the base, not an empty enabledManagers that would
// switch every manager off.
func TestAdviseInitWritesAFileThatParses(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		files map[string]string
		keys  []string
	}{
		{"a tree with a Dockerfile", map[string]string{"Dockerfile": "FROM docker.io/library/alpine:3.20.3\n"},
			[]string{"extends", "minimumReleaseAge", "osvVulnerabilityAlerts", "enabledManagers"}},
		{"a tree with nothing to manage", map[string]string{"README.md": "hello\n"},
			[]string{"extends", "minimumReleaseAge", "osvVulnerabilityAlerts"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := proposeConfig(context.Background(), plant(t, c.files), now)
			if err != nil {
				t.Fatal(err)
			}
			body, err := writeProposal(p, now)
			if err != nil {
				t.Fatal(err)
			}
			layer, err := config.Parse(body, ".pinup.jsonc")
			if err != nil {
				t.Fatalf("the proposal does not parse: %v\n%s", err, body)
			}
			if !reflect.DeepEqual(layer.Raw, p.Config) {
				t.Errorf("parsed %v, proposed %v", layer.Raw, p.Config)
			}
			text := string(body)
			last := -1
			for _, k := range c.keys {
				i := strings.Index(text, `"`+k+`"`)
				if i < last {
					t.Errorf("%s out of order in\n%s", k, text)
				}
				last = i
			}
			if len(layer.Raw) != len(c.keys) {
				t.Errorf("keys = %v, want %v", layer.Raw, c.keys)
			}
		})
	}
}

// --init writes a first configuration, never over one, and refuses the
// flags that belong to advising on an existing file.
func TestAdviseInitRefusesToOverwrite(t *testing.T) {
	root := plant(t, map[string]string{"Dockerfile": "FROM docker.io/library/alpine:3.20.3\n"})
	out := filepath.Join(root, ".pinup.jsonc")
	var stdout, stderr strings.Builder
	if err := run([]string{"advise", "--init", "--repo", root, "--out", out}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("nothing written: %v", err)
	}
	if err := run([]string{"advise", "--init", "--repo", root, "--out", out}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("a second --init over the file: %v", err)
	}
	if err := run([]string{"advise", "--init", "--repo", root, "--config", out}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "--init") {
		t.Errorf("--init with --config: %v", err)
	}
}

// The gate can fail: a proposal advise warns about is refused, named, and
// never printed. ignoreUnstable: false stands in for a bad proposal - one
// the base never makes, and the check that catches it is a warning.
func TestAdviseInitRefusesAProposalAdviseWarnsAbout(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	root := plant(t, map[string]string{"Dockerfile": "FROM docker.io/library/alpine:3.20.3\n"})
	if err := verifyProposal(context.Background(), root, initBase(), now); err != nil {
		t.Fatalf("the base fails its own advice: %v", err)
	}
	bad := initBase()
	bad["ignoreUnstable"] = false
	err := verifyProposal(context.Background(), root, bad, now)
	if err == nil || !strings.Contains(err.Error(), "sec/ignore-unstable-false") {
		t.Errorf("a proposal with a warning passed the gate: %v", err)
	}
}
