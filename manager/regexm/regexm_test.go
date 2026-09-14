// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package regexm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
)

// nestedTemplateManager finds, in the fixture root's configuration, the
// custom manager whose packageNameTemplate nests Handlebars conditionals -
// the shape that justified hbs. Found by shape rather than by index, so
// the test reads the same on any root of the same shape.
func nestedTemplateManager(t *testing.T) (int, *model.CustomManager) {
	t.Helper()
	raw, err := os.ReadFile(fixture.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		CustomManagers []struct {
			MatchStrings        []string `json:"matchStrings"`
			PackageNameTemplate string   `json:"packageNameTemplate"`
			DatasourceTemplate  string   `json:"datasourceTemplate"`
			VersioningTemplate  string   `json:"versioningTemplate"`
			RegistryURLTemplate string   `json:"registryUrlTemplate"`
		} `json:"customManagers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for i, m := range cfg.CustomManagers {
		if strings.Count(m.PackageNameTemplate, "{{#if") >= 2 {
			return i, &model.CustomManager{
				Index: i, MatchStrings: m.MatchStrings, PackageNameTemplate: m.PackageNameTemplate,
				DatasourceTemplate: m.DatasourceTemplate, VersioningTemplate: m.VersioningTemplate,
				RegistryURLTemplate: m.RegistryURLTemplate,
			}
		}
	}
	t.Fatal("the configuration has no custom manager with a nested packageNameTemplate")
	return 0, nil
}

// The custom manager with the nested Handlebars conditional builds a
// composite packageName. The outputs compared against are the ones the
// extraction corpus recorded from the pinned container for the same
// composer.json, so this is parity against measurement rather than against
// a reading of the template.
func TestNestedTemplateReproducesTheCorpusPackageNames(t *testing.T) {
	index, cm := nestedTemplateManager(t)
	compared := 0
	for _, c := range fixture.Corpora(t) {
		if c.Tree == "" {
			continue
		}
		for _, e := range c.Entries(t, "regex") {
			if !strings.HasSuffix(e.PackageFile, "composer.json") {
				continue
			}
			var deps []struct {
				DepName     string `json:"depName"`
				PackageName string `json:"packageName"`
			}
			if err := json.Unmarshal(e.Deps, &deps); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{}
			for _, d := range deps {
				if strings.Contains(d.PackageName, ":") && strings.Contains(cm.MatchStrings[0], strings.SplitN(d.DepName, "/", 2)[0]+"/") {
					want[d.DepName] = d.PackageName
				}
			}
			if len(want) == 0 {
				continue
			}
			src := c.Read(t, e.PackageFile)
			res, err := New().Extract(context.Background(),
				extract.File{Path: e.PackageFile, Content: src}, extract.ManagerConfig{Custom: cm})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, d := range res.Deps {
				got[d.DepName] = d.PackageName
				// The offsets must bracket exactly the version.
				if slice := string(src[d.Locus.ValueStart:d.Locus.ValueEnd]); slice != d.CurrentValue {
					t.Errorf("%s: offsets point at %q, not at %q", d.DepName, slice, d.CurrentValue)
				}
			}
			for dep, pkg := range want {
				if got[dep] != pkg {
					t.Errorf("%s/%s: packageName for %s:\n got %q\nwant %q", c.Name, e.PackageFile, dep, got[dep], pkg)
				}
				compared++
			}
		}
	}
	if compared == 0 {
		t.Skipf("no capture with its files records custom manager #%d's package names", index)
	}
	t.Logf("%d templated package names agreed with the corpus", compared)
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
	cm := &model.CustomManager{
		Index:              20,
		MatchStrings:       []string{`"platform"\s*:\s*\{[^}]*?"php"\s*:\s*"(?<currentValue>(?<phpSeries>\d+\.\d+)(?:\.\d+)?)"`},
		DepNameTemplate:    "php-frankenphp-{{{phpSeries}}}",
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
	// The shape of the runner configuration's first custom manager: the
	// outer pattern narrows to a default: or *_image: line, the inner one
	// reads the image on it.
	cm := &model.CustomManager{
		Index: 0,
		MatchStrings: []string{
			`(?:[a-z0-9_-]+_image|default):[^\n]*`,
			`(?<depName>registry\.example\.test/devops/ci-mirrors/[a-z0-9._/-]+):(?<currentValue>[a-zA-Z0-9][a-zA-Z0-9._-]*)(?:@(?<currentDigest>sha256:[a-f0-9]{64}))?`,
		},
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
		"      default: registry.example.test/devops/ci-mirrors/trivy:v0.74.0\n" +
		"# see also registry.example.test/devops/ci-mirrors/other:v9.9.9\n"

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

// A match with a currentDigest group moves the digest span on a digest
// update, and value and digest together on a version move - the shape the
// image-signing templates carry (wolfi-base:latest@sha256:…). The value
// span alone was replaced before, "latest" over "latest", and nothing
// changed (2026-09-14).
func TestEditMovesTheDigestAMatchBound(t *testing.T) {
	cm := &model.CustomManager{
		Index:              3,
		MatchStrings:       []string{`image:\s*(?<depName>[^:\s]+):(?<currentValue>[^@\s]+)@(?<currentDigest>sha256:[a-f0-9]+)`},
		DatasourceTemplate: "docker",
	}
	src := "image: registry.example.org/base:latest@sha256:aaaa\n"
	f := extract.File{Path: "template.yml", Content: []byte(src)}
	res, err := New().Extract(context.Background(), f, extract.ManagerConfig{Custom: cm})
	if err != nil || len(res.Deps) != 1 || res.Deps[0].CurrentDigest != "sha256:aaaa" {
		t.Fatalf("extract: %v, %+v", err, res.Deps)
	}
	e, err := New().Edit(context.Background(), f, model.Update{Dep: res.Deps[0], NewValue: "latest", NewDigest: "sha256:bbbb", Type: model.UpdateDigest})
	if err != nil {
		t.Fatal(err)
	}
	if out := src[:e.Start] + e.New + src[e.End:]; out != "image: registry.example.org/base:latest@sha256:bbbb\n" {
		t.Errorf("digest move gave %q", out)
	}
	e, err = New().Edit(context.Background(), f, model.Update{Dep: res.Deps[0], NewValue: "2", NewDigest: "sha256:cccc", Type: model.UpdateMajor})
	if err != nil {
		t.Fatal(err)
	}
	if out := src[:e.Start] + e.New + src[e.End:]; out != "image: registry.example.org/base:2@sha256:cccc\n" {
		t.Errorf("value and digest gave %q", out)
	}
	if _, err := New().Edit(context.Background(), f, model.Update{Dep: res.Deps[0], NewValue: "2", Type: model.UpdateMajor}); err == nil {
		t.Error("a tag move without a digest for a pinned reference was accepted")
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
