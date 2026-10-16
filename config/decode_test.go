// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/fake/fixture"
	"testing"
)

// Decoding the real configuration is the only test that matters here: a
// synthetic one would only prove the decoder agrees with itself.
func TestDecodeTheRealConfig(t *testing.T) {
	// With the presets expanded, as a run sees it: the 25 custom managers
	// the file declares plus the two its extends contribute, which come
	// first (customManagers:dockerfileVersions, :gitlabPipelineVersions).
	d, r, warnings, err := DecodeFile(fixture.Config(t), preset.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	// The two inert presets the file names; the third, reached through
	// config:recommended, is the library's own and stays quiet.
	if len(warnings) != 2 {
		t.Errorf("want the two inert-preset warnings, got %v", warnings)
	}
	want := fixture.Expect(t)
	rules, _ := r.Raw["packageRules"].([]any)
	if len(rules) != want.RulesResolved {
		t.Errorf("resolved %d rules, want %d", len(rules), want.RulesResolved)
	}
	if len(d.CustomManagers) != 27 {
		t.Errorf("decoded %d custom managers, expected 27", len(d.CustomManagers))
	}
	// The file's definitions start at 2; the offset is read off provenance
	// rather than assumed.
	const own = 2
	if o, ok := r.Winner("/customManagers/1"); !ok || !strings.HasPrefix(o.Source, "preset:") {
		t.Errorf("customManagers[1] origin = %+v, want a preset", o)
	}
	if o, ok := r.Winner("/customManagers/2"); !ok || !strings.HasSuffix(o.Source, "default.json") {
		t.Errorf("customManagers[2] origin = %+v, want the file", o)
	}
	// Provenance: the file's first own rule follows the library's, and
	// names the file.
	first := len(rules) - want.RulesOwn
	if o, ok := r.Winner(fmt.Sprintf("/packageRules/%d", first)); !ok || o.Rule != first || !strings.HasSuffix(o.Source, "default.json") {
		t.Errorf("packageRules[%d] origin = %+v", first, o)
	}
	if o, ok := r.Winner("/packageRules/0"); !ok || o.Source != "preset::semanticPrefixFixDepsChoreOthers" {
		t.Errorf("packageRules[0] origin = %+v", o)
	}

	// Without presets, the file on its own - which is only meaningful for a
	// file that extends nothing, and this one does.
	if _, _, _, err := DecodeFile(fixture.Config(t), nil); err == nil {
		t.Error("a file with extends and no preset source must fail, not resolve to less")
	}
	if len(d.EnabledManagers) != 10 {
		t.Errorf("decoded %d enabled managers, expected 10", len(d.EnabledManagers))
	}
	if d.Timezone != "Europe/Berlin" {
		t.Errorf("timezone = %q", d.Timezone)
	}
	if len(d.IgnorePaths) != 1 {
		t.Errorf("ignorePaths = %v, expected one entry", d.IgnorePaths)
	}

	// Every definition must carry file patterns, or discovery would offer it
	// nothing and the manager would look like it found nothing.
	for _, cm := range d.CustomManagers {
		if len(cm.FilePatterns) == 0 {
			t.Errorf("custom manager %d has no file patterns", cm.Index)
		}
		if len(cm.MatchStrings) == 0 {
			t.Errorf("custom manager %d has no matchStrings", cm.Index)
		}
		if got := d.FilePatterns[CustomManagerName(cm.Index)]; len(got) == 0 {
			t.Errorf("custom manager %d is not registered for discovery", cm.Index)
		}
	}

	// Spot-checks against definitions this project has read closely, in the
	// file's own numbering.
	if got := d.CustomManagers[own+0].MatchStrategy; got != "recursive" {
		t.Errorf("definition 0 strategy = %q, want recursive", got)
	}
	if got := d.CustomManagers[own+10].PackageNameTemplate; got == "" {
		t.Error("definition 10 lost its packageNameTemplate")
	}
	if got := d.CustomManagers[own+20].DepNameTemplate; got != "php-frankenphp-{{{phpSeries}}}" {
		t.Errorf("definition 20 depNameTemplate = %q", got)
	}
}

// description is a string in some entries and a list in others. Both must
// decode rather than one of them panicking at load.
func TestStringSliceAcceptsBothShapes(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int
	}{
		{nil, 0},
		{"one", 1},
		{[]any{"a", "b"}, 2},
		{[]any{}, 0},
	} {
		if got := len(stringSlice(c.in)); got != c.want {
			t.Errorf("stringSlice(%#v) has %d entries, want %d", c.in, got, c.want)
		}
	}
}

// An artifactManifests entry is refused, not half-read, when it names no
// files, an unknown format, no source or two, or a pin without its two
// halves; a complete one is decoded with its discovery key.
func TestDecodeArtifactManifests(t *testing.T) {
	for _, c := range []struct {
		name  string
		entry string
		want  string
	}{
		{"no files", `{"format": "cyclonedx", "path": "sbom.json"}`, "names no matchFileNames"},
		{"unknown format", `{"matchFileNames": ["*.yaml"], "format": "syft", "path": "sbom.json"}`, `format "syft"`},
		{"no source", `{"matchFileNames": ["*.yaml"], "format": "spdx"}`, "exactly one of path, url and artifact, has 0"},
		{"two sources", `{"matchFileNames": ["*.yaml"], "format": "spdx", "path": "a", "url": "https://x/a"}`, "has 2"},
		{"artifact without job", `{"matchFileNames": ["*.yaml"], "format": "trivy", "artifact": {"path": "a.json"}}`, "needs job and path"},
		{"half a pin", `{"matchFileNames": ["*.yaml"], "format": "go-modules", "path": "mods.txt", "pin": {"after": "x"}}`, "pin needs after and line"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal([]byte(`{"artifactManifests": [`+c.entry+`]}`), &raw); err != nil {
				t.Fatal(err)
			}
			_, err := Decode(raw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"artifactManifests": [{"description": "the apk's modules", "matchFileNames": ["/^[^/]+\\.yaml$/"], "format": "trivy",
		"artifact": {"project": "devops/wolfi-packages", "ref": "main", "job": "audit:published:aarch64", "path": "scan-reports/{{{fileStem}}}.trivy.json"},
		"datasources": ["go"], "pin": {"after": "\\n(?<indent>[ \\t]+)x@v1", "line": "{{{indent}}}{{{depName}}}@{{{newValue}}}", "datasources": ["go"]}}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	d, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ArtifactManifests) != 1 {
		t.Fatalf("manifests = %+v", d.ArtifactManifests)
	}
	m := d.ArtifactManifests[0]
	if m.Index != 0 || m.Format != "trivy" || m.Artifact == nil || m.Artifact.Project != "devops/wolfi-packages" || m.Artifact.Job != "audit:published:aarch64" ||
		m.Pin == nil || !m.Pin.Covers("go") || m.Pin.Covers("npm") || len(m.Datasources) != 1 || len(m.FilePatterns) != 1 {
		t.Errorf("manifest = %+v, artifact %+v, pin %+v", m, m.Artifact, m.Pin)
	}
	if got := d.FilePatterns[ArtifactManifestName(0)]; len(got) != 1 || got[0] != `/^[^/]+\.yaml$/` {
		t.Errorf("file patterns under %s = %v", ArtifactManifestName(0), got)
	}
}
