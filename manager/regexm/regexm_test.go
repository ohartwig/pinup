// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package regexm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/config/preset"
	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// The definitions below are copied from the captured default.json rather than
// invented, so a change upstream shows up as a test failure rather than as a
// difference nobody notices. loadDefinition reads one back out of the captured
// config to prove the copies are still faithful.
func loadDefinition(t *testing.T, index int) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/parity/config/default.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		CustomManagers []map[string]any `json:"customManagers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if index >= len(cfg.CustomManagers) {
		t.Fatalf("the captured config has %d custom managers, no #%d", len(cfg.CustomManagers), index)
	}
	return cfg.CustomManagers[index]
}

func firstMatchString(t *testing.T, def map[string]any) string {
	t.Helper()
	ms, _ := def["matchStrings"].([]any)
	if len(ms) == 0 {
		t.Fatal("definition has no matchStrings")
	}
	s, _ := ms[0].(string)
	return s
}

// Custom manager #10 builds a composite packageName through a triple-nested
// Handlebars conditional. These outputs are the ones the extraction corpus
// recorded from the real container, so this is parity against measurement
// rather than against my reading of the template.
func TestManager10CompositePackageNames(t *testing.T) {
	def := loadDefinition(t, 10)
	pattern := firstMatchString(t, def)
	tmpl, _ := def["packageNameTemplate"].(string)

	cm := &model.CustomManager{
		Index:               10,
		MatchStrings:        []string{pattern},
		PackageNameTemplate: tmpl,
		DatasourceTemplate:  "gitlab-packages",
		VersioningTemplate:  "composer",
		RegistryURLTemplate: "https://git.ole-hartwig.eu",
	}

	src := `{
  "require": {
    "moselwal/dev": "^5.0",
    "moselwal/fa4t3": "^1.0",
    "moselwal/content-provenance": "~0.9"
  }
}`
	res, err := New().Extract(context.Background(),
		extract.File{Path: "composer.json", Content: []byte(src)},
		extract.ManagerConfig{Custom: cm})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"moselwal/dev":                "development/moselwal/dev:moselwal/dev",
		"moselwal/fa4t3":              "development/moselwal/typo3-fathom-analytics:moselwal/fa4t3",
		"moselwal/content-provenance": "development/moselwal/content-provenance:moselwal/content-provenance",
	}
	got := map[string]string{}
	for _, d := range res.Deps {
		got[d.DepName] = d.PackageName
		if d.Datasource != "gitlab-packages" || d.Versioning != "composer" {
			t.Errorf("%s: datasource/versioning = %q/%q", d.DepName, d.Datasource, d.Versioning)
		}
		// The offsets must bracket exactly the version.
		if slice := src[d.Locus.ValueStart:d.Locus.ValueEnd]; slice != d.CurrentValue {
			t.Errorf("%s: offsets point at %q, not at %q", d.DepName, slice, d.CurrentValue)
		}
	}
	for dep, pkg := range want {
		if got[dep] != pkg {
			t.Errorf("packageName for %s:\n got %q\nwant %q", dep, got[dep], pkg)
		}
	}
}

// The mechanism that lets one definition replace a pair: an absent capture
// group leaves its templated field UNSET rather than empty.
//
// Three pairs in the estate config exist only because the upstream tool cannot
// express this - #14/#15, #16/#17 and #21/#22 differ by nothing but the
// presence of a registryUrl group.
func TestAnAbsentGroupLeavesTheFieldUnset(t *testing.T) {
	cm := &model.CustomManager{
		Index: 99,
		MatchStrings: []string{
			`depName=(?<depName>\S+)(?: registryUrl=(?<registryUrl>\S+))?\s+tag:\s*"(?<currentValue>[^"]+)"`,
		},
		RegistryURLTemplate: "{{{registryUrl}}}",
		DatasourceTemplate:  "docker",
	}

	for _, c := range []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "annotation carries a registry",
			src:  "# renovate: depName=img registryUrl=https://reg.example\ntag: \"1.2.3\"\n",
			want: []string{"https://reg.example"},
		},
		{
			name: "annotation omits it, so the field stays unset",
			src:  "# renovate: depName=img\ntag: \"1.2.3\"\n",
			want: nil,
		},
	} {
		res, err := New().Extract(context.Background(),
			extract.File{Path: "values.yaml", Content: []byte(c.src)},
			extract.ManagerConfig{Custom: cm})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(res.Deps) != 1 {
			t.Fatalf("%s: got %d deps, want 1", c.name, len(res.Deps))
		}
		d := res.Deps[0]
		if len(d.RegistryURLs) != len(c.want) {
			t.Errorf("%s: RegistryURLs = %v, want %v", c.name, d.RegistryURLs, c.want)
			continue
		}
		if len(c.want) > 0 && d.RegistryURLs[0] != c.want[0] {
			t.Errorf("%s: RegistryURLs = %v, want %v", c.name, d.RegistryURLs, c.want)
		}
		if c.want == nil && !d.Absent["registryUrl"] {
			t.Errorf("%s: the group is not recorded as absent", c.name)
		}
	}
}

// A nested named group feeds a template: #20 derives a depName from the PHP
// series inside the version it captured.
func TestNestedGroupFeedsATemplate(t *testing.T) {
	def := loadDefinition(t, 20)
	cm := &model.CustomManager{
		Index:              20,
		MatchStrings:       []string{firstMatchString(t, def)},
		DepNameTemplate:    def["depNameTemplate"].(string),
		DatasourceTemplate: "custom.wolfi",
	}
	src := `{"config":{"platform":{"php":"8.5.10"}}}`
	res, err := New().Extract(context.Background(),
		extract.File{Path: "composer.json", Content: []byte(src)},
		extract.ManagerConfig{Custom: cm})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deps) != 1 {
		t.Fatalf("got %d deps, want 1", len(res.Deps))
	}
	d := res.Deps[0]
	if d.DepName != "php-frankenphp-8.5" {
		t.Errorf("DepName = %q, want %q", d.DepName, "php-frankenphp-8.5")
	}
	if d.CurrentValue != "8.5.10" {
		t.Errorf("CurrentValue = %q, want %q", d.CurrentValue, "8.5.10")
	}
	if d.Captures["phpSeries"] != "8.5" {
		t.Errorf("the nested group was not captured: %v", d.Captures)
	}
}

// The recursive strategy: the outer pattern narrows a region so the inner one
// cannot match text that happens to look right elsewhere in the file.
func TestRecursiveStrategyConfinesTheInnerMatch(t *testing.T) {
	def := loadDefinition(t, 0)
	ms, _ := def["matchStrings"].([]any)
	cm := &model.CustomManager{
		Index:              0,
		MatchStrings:       []string{ms[0].(string), ms[1].(string)},
		MatchStrategy:      StrategyRecursive,
		DatasourceTemplate: "docker",
		VersioningTemplate: "docker",
	}

	// The first image sits inside a `default:` input and must be found. The
	// second is in a plain comment with no such key and must NOT be, which is
	// the entire point of the outer pattern.
	src := "spec:\n" +
		"  inputs:\n" +
		"    image-config:\n" +
		"      default: registry.ole-hartwig.eu/devops/ci-mirrors/trivy:v0.74.0\n" +
		"# see also registry.ole-hartwig.eu/devops/ci-mirrors/other:v9.9.9\n"

	res, err := New().Extract(context.Background(),
		extract.File{Path: ".gitlab-ci.yml", Content: []byte(src)},
		extract.ManagerConfig{Custom: cm})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deps) != 1 {
		var names []string
		for _, d := range res.Deps {
			names = append(names, d.DepName+"@"+d.CurrentValue)
		}
		t.Fatalf("got %d deps (%s), want 1 - the outer pattern should confine the inner one",
			len(res.Deps), strings.Join(names, ", "))
	}
	d := res.Deps[0]
	if d.CurrentValue != "v0.74.0" {
		t.Errorf("CurrentValue = %q, want %q", d.CurrentValue, "v0.74.0")
	}
	// Offsets are relative to the WHOLE file, not to the narrowed region.
	if slice := src[d.Locus.ValueStart:d.Locus.ValueEnd]; slice != d.CurrentValue {
		t.Errorf("offsets point at %q, not at %q - the region offset was not added back",
			slice, d.CurrentValue)
	}
}

func TestDigestIsCapturedWithItsOwnSpan(t *testing.T) {
	const digest = "sha256:b39029a467a389c506e0dab9ea75707b393467e653c876ff13d77fd4e35228a7"
	cm := &model.CustomManager{
		Index: 1,
		MatchStrings: []string{
			`(?<depName>[a-z./-]+):(?<currentValue>[\w.-]+)(?:@(?<currentDigest>sha256:[a-f0-9]{64}))?`,
		},
		DatasourceTemplate: "docker",
	}
	src := "FROM registry.example/img:v1.2.3@" + digest + "\n"
	res, err := New().Extract(context.Background(),
		extract.File{Path: "Containerfile", Content: []byte(src)},
		extract.ManagerConfig{Custom: cm})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deps) == 0 {
		t.Fatal("nothing extracted")
	}
	d := res.Deps[0]
	if d.CurrentDigest != digest {
		t.Errorf("CurrentDigest = %q", d.CurrentDigest)
	}
	if d.Locus.DigestStart == model.NoDigest {
		t.Fatal("the digest has no recorded span")
	}
	if slice := src[d.Locus.DigestStart:d.Locus.DigestEnd]; slice != digest {
		t.Errorf("the digest span points at %q", slice)
	}
}

func TestEditRefusesAChangedFile(t *testing.T) {
	cm := &model.CustomManager{
		Index:              2,
		MatchStrings:       []string{`tag:\s*"(?<currentValue>[^"]+)"`},
		DepNameTemplate:    "img",
		DatasourceTemplate: "docker",
	}
	src := "tag: \"1.2.3\"\n"
	f := extract.File{Path: "values.yaml", Content: []byte(src)}
	res, err := New().Extract(context.Background(), f, extract.ManagerConfig{Custom: cm})
	if err != nil || len(res.Deps) != 1 {
		t.Fatalf("extract: %v, %d deps", err, len(res.Deps))
	}
	up := model.Update{Dep: res.Deps[0], NewValue: "1.3.0"}

	e, err := New().Edit(context.Background(), f, up)
	if err != nil {
		t.Fatal(err)
	}
	out := src[:e.Start] + e.New + src[e.End:]
	if out != "tag: \"1.3.0\"\n" {
		t.Errorf("applying the edit gave %q", out)
	}

	changed := extract.File{Path: "values.yaml", Content: []byte("tag: \"9.9.9\"\n")}
	if _, err := New().Edit(context.Background(), changed, up); err == nil {
		t.Error("a changed file was accepted; writing anyway would corrupt it")
	}
}

// combination is not implemented, and says so rather than guessing at
// semantics nothing in the estate exercises.
func TestCombinationStrategyIsRefused(t *testing.T) {
	cm := &model.CustomManager{
		Index:         3,
		MatchStrings:  []string{`(?<currentValue>\d+)`},
		MatchStrategy: StrategyCombine,
	}
	_, err := New().Extract(context.Background(),
		extract.File{Path: "x", Content: []byte("1")}, extract.ManagerConfig{Custom: cm})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("err = %v, want a refusal naming the strategy", err)
	}
}

func TestAPatternThatDoesNotCompileIsAWarningNotAFailure(t *testing.T) {
	cm := &model.CustomManager{
		Index:        4,
		MatchStrings: []string{`(?<currentValue>\d+)`, `(?=lookahead)`},
	}
	res, err := New().Extract(context.Background(),
		extract.File{Path: "x", Content: []byte("42")}, extract.ManagerConfig{Custom: cm})
	if err != nil {
		t.Fatalf("one bad pattern failed the whole extraction: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Errorf("got %d warnings, want 1", len(res.Warnings))
	}
	if len(res.Deps) != 1 {
		t.Errorf("the good pattern contributed %d deps, want 1", len(res.Deps))
	}
}

// The customManagers:dockerfileVersions preset is what extracts a
// "# renovate:" annotated ARG in every estate configuration (the dockerfile
// manager reports FROM references only, measured). Its pattern stops the
// value at whitespace, so a trailing comment on the ARG line is not swallowed
// into the version - Docker itself would read the rest of the line as the
// value, and a version carrying a comment resolves against no datasource.
func TestDockerfileVersionsPresetStopsAtWhitespace(t *testing.T) {
	def := preset.Builtin().Presets["customManagers:dockerfileVersions"].Definition
	managers := def["customManagers"].([]any)
	pattern := managers[0].(map[string]any)["matchStrings"].([]any)[0].(string)
	cm := &model.CustomManager{Index: 0, MatchStrings: []string{pattern}}

	const src = "FROM alpine:3.20\n" +
		"# renovate: datasource=custom.wolfi depName=go-1.27 versioning=apk\n" +
		"ARG GO_APK_VERSION=1.27.0-r1\t# a trailing comment\n"
	res, err := New().Extract(context.Background(),
		extract.File{Path: "Containerfile", Content: []byte(src)}, extract.ManagerConfig{Custom: cm})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deps) != 1 {
		t.Fatalf("want one dependency, got %+v", res.Deps)
	}
	d := res.Deps[0]
	if d.DepName != "go-1.27" || d.Datasource != "custom.wolfi" || d.Versioning != "apk" {
		t.Errorf("fields: %+v", d)
	}
	if d.CurrentValue != "1.27.0-r1" {
		t.Errorf("CurrentValue = %q, want 1.27.0-r1", d.CurrentValue)
	}
	if got := src[d.Locus.ValueStart:d.Locus.ValueEnd]; got != d.CurrentValue {
		t.Errorf("the offsets point at %q, not at the value", got)
	}
}

// D.11: the estate's annotations say "# renovate:"; pinup reads "# pinup:"
// as the same marker without the configuration changing. The two spellings
// must extract byte-identical dependencies, offsets shifted by the length
// difference only. A pattern without the marker is left alone.
func TestPinupPrefixReadsLikeRenovatePrefix(t *testing.T) {
	cm := &model.CustomManager{
		Index: 1,
		MatchStrings: []string{
			`# renovate: datasource=(?<datasource>[a-z-]+) depName=(?<depName>\S+)\s+ARG \w+=(?<currentValue>\S+)`,
		},
	}
	extractFrom := func(src string) []model.Dependency {
		t.Helper()
		res, err := New().Extract(context.Background(),
			extract.File{Path: "Dockerfile", Content: []byte(src)},
			extract.ManagerConfig{Custom: cm})
		if err != nil {
			t.Fatal(err)
		}
		return res.Deps
	}
	legacy := extractFrom("# renovate: datasource=docker depName=alpine\nARG ALPINE=3.20\n")
	native := extractFrom("# pinup: datasource=docker depName=alpine\nARG ALPINE=3.20\n")
	if len(legacy) != 1 || len(native) != 1 {
		t.Fatalf("legacy %d deps, native %d deps, want 1 and 1", len(legacy), len(native))
	}
	l, n := legacy[0], native[0]
	if l.DepName != n.DepName || l.Datasource != n.Datasource || l.CurrentValue != n.CurrentValue {
		t.Errorf("prefixes extract differently:\n renovate %+v\n pinup    %+v", l, n)
	}
	const shift = len("renovate") - len("pinup")
	if n.Locus.ValueStart != l.Locus.ValueStart-shift || n.Locus.ValueEnd != l.Locus.ValueEnd-shift {
		t.Errorf("offsets: renovate [%d,%d), pinup [%d,%d), want a shift of %d",
			l.Locus.ValueStart, l.Locus.ValueEnd, n.Locus.ValueStart, n.Locus.ValueEnd, shift)
	}

	for _, tc := range []struct{ in, want string }{
		{`# renovate: datasource=(?<d>\S+)`, `# (?:renovate|pinup): datasource=(?<d>\S+)`},
		{`renovate:\s*datasource=custom\.x`, `(?:renovate|pinup):\s*datasource=custom\.x`},
		{`FROM (?<depName>\S+):(?<currentValue>\S+)`, `FROM (?<depName>\S+):(?<currentValue>\S+)`},
		{`renovate/renovate:(?<v>\S+)`, `renovate/(?:renovate|pinup):(?<v>\S+)`},
	} {
		if got := WidenPrefix(tc.in); got != tc.want {
			t.Errorf("WidenPrefix(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}
