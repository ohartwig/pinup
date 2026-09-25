// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"testing"
	"testing/fstest"

	"github.com/ohartwig/pinup/model"
)

// The scan is only worth reading if it stays quiet where a pin is held or
// meant to be left alone. Each case is one way a version literal is not a
// gap; the last proves the same literal is reported once nothing excuses it.
func TestUnmanagedPinLeavesHeldPinsAlone(t *testing.T) {
	mirrored := model.Dependency{Manager: "regex", File: "mirrors.yml", CustomManager: 0, DepName: "docker.io/semgrep/semgrep", Datasource: "docker", CurrentValue: "1.174.0", Locus: model.Locus{Line: 9}}
	tracked := model.Dependency{Manager: "gitlabci", File: ".gitlab-ci.yml", CustomManager: model.NoCustomManager, DepName: "x/helm", Datasource: "docker", CurrentValue: "3.19.0", Locus: model.Locus{Line: 2}}
	for _, c := range []struct {
		name, file, body, cfg string
		want                  int
	}{
		{"on a line the plan holds", ".gitlab-ci.yml", "variables:\n  HELM_VERSION: 3.18.0\n", `{}`, 0},
		{"a value the plan holds for the file", ".gitlab-ci.yml", "a: 1\nb: 2\n  HELM_VERSION: 3.19.0\n", `{}`, 0},
		{"under an annotation", "ci.yml", "# renovate: datasource=docker depName=x/helm\nHELM_VERSION: 3.18.0\n", `{}`, 0},
		{"marked on the line above", "ci.yml", "# pinup: coverage-ignore\nHELM_VERSION: 3.18.0\n", `{}`, 0},
		{"marked on the line", "ci.yml", "HELM_VERSION: 3.18.0 # pinup: coverage-ignore\n", `{}`, 0},
		{"a file marked whole", "ci.yml", "# pinup: coverage-ignore-file\nx:\n  HELM_VERSION: 3.18.0\n", `{}`, 0},
		{"a comment", "ci.yml", "# HELM_VERSION: 3.18.0\n", `{}`, 0},
		{"a test file", "tests/ci.yml", "HELM_VERSION: 3.18.0\n", `{}`, 0},
		{"prose", "README.md", "image: smallstep/step-ca:0.28.1\n", `{}`, 0},
		{"an ignored path", "legacy/ci.yml", "HELM_VERSION: 3.18.0\n", `{"ignorePaths": ["legacy/**"]}`, 0},
		{"a lowercase key is not a pin variable", "ci.yml", "helm_version: 3.18.0\n", `{}`, 0},
		{"the configuration", "renovate.json", `{"description": "x"}` + "\nHELM_VERSION: 3.18.0\n", `{}`, 0},
		{"a sentence naming an image", "ci.py", "    values.yaml stood at devops/images/ksops:2.1.6, a Git tag with\n", `{}`, 0},
		{"a line, not a version", "Containerfile", "ARG PHP_VERSION=8.5\n", `{}`, 0},
		{"an image on a line tag", "compose.yaml", "    image: valkey/valkey:8.1-alpine3.22\n", `{}`, 0},
		{"a command naming an image", "lefthook.yml", "run: docker run --rm -v \"$PWD:/check:ro\" mstruebing/editorconfig-checker:v3.7.0 ec {staged_files}\n", `{}`, 1},
		{"a dependency the plan found, raised since", "mirrors.yml", "x: 1\n  - SRC: [\"docker.io/semgrep/semgrep:1.178.0\"]\n", `{}`, 0},
		{"another image in that file", "mirrors.yml", "  - SRC: [\"docker.io/rancher/system-upgrade-controller:v0.20.2\"]\n", `{}`, 1},
		{"nothing excuses it", "ci.yml", "x:\n  HELM_VERSION: 3.18.0\n", `{}`, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := load(t, c.cfg, plan([]model.Dependency{tracked, mirrored}, nil))
			in.Repo = fstest.MapFS{c.file: {Data: []byte(c.body)}}
			if got := len(unmanagedPin(in)); got != c.want {
				t.Errorf("%d findings, want %d: %+v", got, c.want, unmanagedPin(in))
			}
		})
	}
}
