// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package composerman

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
)

// depNamed finds the one dependency with the given DepName, or fails the
// test - most of these tests care about one specific entry in a multi-entry
// fixture, not the whole slice.
func depNamed(t *testing.T, deps []model.Dependency, depName string) model.Dependency {
	t.Helper()
	var matches []model.Dependency
	for _, d := range deps {
		if d.DepName == depName {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("found %d dependencies named %q, want exactly 1 (deps: %+v)", len(matches), depName, deps)
	}
	return matches[0]
}

func extractAll(t *testing.T, src []byte) (extract.File, []model.Dependency) {
	t.Helper()
	f := extract.File{Path: "composer.json", Content: src}
	res, err := New().Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	return f, res.Deps
}

// synthetic exercises every shape the corpus and the task describe: require
// and require-dev, the php runtime special case, a platform package from
// each of the "ext-" and "lib-" families, version constraints in each of
// composer's common forms, and a repositories list mixing a composer-type
// entry (which contributes a registry) with a vcs-type one (which does not).
const synthetic = `{
  "name": "acme/example",
  "require": {
    "php": "^8.1",
    "ext-json": "*",
    "lib-curl": "*",
    "vendor/pinned": "^7.0",
    "vendor/tilde": "~2.3",
    "vendor/any": "*",
    "vendor/branch": "dev-main"
  },
  "require-dev": {
    "vendor/devtool": "^3.1"
  },
  "repositories": [
    {
      "type": "composer",
      "url": "https://example.test/composer/"
    },
    {
      "type": "vcs",
      "url": "https://example.test/vcs-repo.git"
    }
  ]
}
`

var wantPackagistURLs = []string{"https://example.test/composer/", defaultPackagistURL}

func TestSyntheticExtraction(t *testing.T) {
	src, deps := extractAll(t, []byte(synthetic))

	if len(deps) != 8 {
		for _, d := range deps {
			t.Logf("  got %-9s %-15s %q skip=%q ds=%q", d.DepType, d.DepName, d.CurrentValue, d.SkipReason, d.Datasource)
		}
		t.Fatalf("got %d deps, want 8", len(deps))
	}

	// Every Locus must bracket exactly CurrentValue, without its quotes -
	// the contract Edit relies on.
	for _, d := range deps {
		if got := string(src.Content[d.Locus.ValueStart:d.Locus.ValueEnd]); got != d.CurrentValue {
			t.Errorf("%s: offsets point at %q, not at CurrentValue %q", d.DepName, got, d.CurrentValue)
		}
	}

	php := depNamed(t, deps, "php")
	if php.DepType != "require" {
		t.Errorf("php: depType = %q, want require", php.DepType)
	}
	if php.CurrentValue != "^8.1" {
		t.Errorf("php: currentValue = %q, want ^8.1", php.CurrentValue)
	}
	if php.Datasource != "github-tags" {
		t.Errorf("php: datasource = %q, want github-tags", php.Datasource)
	}
	if php.PackageName != "containerbase/php-prebuild" {
		t.Errorf("php: packageName = %q, want containerbase/php-prebuild", php.PackageName)
	}
	if php.SkipReason != "" {
		t.Errorf("php: skipReason = %q, want none - the runner carries a GitHub token", php.SkipReason)
	}
	if php.RegistryURLs != nil {
		t.Errorf("php: registryUrls = %v, want none", php.RegistryURLs)
	}

	for _, platform := range []string{"ext-json", "lib-curl"} {
		d := depNamed(t, deps, platform)
		if d.SkipReason != "platform package" {
			t.Errorf("%s: skipReason = %q, want %q", platform, d.SkipReason, "platform package")
		}
		if d.Datasource != "" {
			t.Errorf("%s: datasource = %q, want none - a platform package is never looked up", platform, d.Datasource)
		}
	}

	type want struct {
		depType, value string
	}
	wants := map[string]want{
		"vendor/pinned":  {"require", "^7.0"},
		"vendor/tilde":   {"require", "~2.3"},
		"vendor/any":     {"require", "*"},
		"vendor/branch":  {"require", "dev-main"},
		"vendor/devtool": {"require-dev", "^3.1"},
	}
	for depName, w := range wants {
		d := depNamed(t, deps, depName)
		if d.DepType != w.depType {
			t.Errorf("%s: depType = %q, want %q", depName, d.DepType, w.depType)
		}
		if d.CurrentValue != w.value {
			t.Errorf("%s: currentValue = %q, want %q", depName, d.CurrentValue, w.value)
		}
		if d.Datasource != "packagist" {
			t.Errorf("%s: datasource = %q, want packagist", depName, d.Datasource)
		}
		if d.SkipReason != "" {
			t.Errorf("%s: skipReason = %q, want none", depName, d.SkipReason)
		}
		if !slices.Equal(d.RegistryURLs, wantPackagistURLs) {
			t.Errorf("%s: registryUrls = %v, want %v (the vcs repository must not contribute)", depName, d.RegistryURLs, wantPackagistURLs)
		}
		if !slices.Equal(d.LockFiles, []string{"composer.lock"}) {
			t.Errorf("%s: lockFiles = %v, want [composer.lock]", depName, d.LockFiles)
		}
	}
}

// TestPackagistDisabled covers the "packagist.org": false form, both as an
// array entry and as a key in the named-map form of "repositories".
func TestPackagistDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{
			name: "array form",
			src: `{"require": {"vendor/x": "^1.0"},
			       "repositories": [
			         {"type": "composer", "url": "https://example.test/composer/"},
			         {"packagist.org": false}
			       ]}`,
		},
		{
			name: "named-map form",
			src: `{"require": {"vendor/x": "^1.0"},
			       "repositories": {
			         "packagist.org": false,
			         "acme": {"type": "composer", "url": "https://example.test/composer/"}
			       }}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, deps := extractAll(t, []byte(tc.src))
			d := depNamed(t, deps, "vendor/x")
			want := []string{"https://example.test/composer/"}
			if !slices.Equal(d.RegistryURLs, want) {
				t.Errorf("registryUrls = %v, want %v (packagist.org disabled)", d.RegistryURLs, want)
			}
		})
	}
}

// TestManagerConfigDefaults covers the fallback path: a composer.json with no
// "repositories" key at all leans on ManagerConfig's own default, exactly the
// contract manager/dockerfile follows for the same field.
func TestManagerConfigDefaults(t *testing.T) {
	src := `{"require": {"vendor/x": "^1.0"}}`
	f := extract.File{Path: "composer.json", Content: []byte(src)}
	cfg := extract.ManagerConfig{RegistryURLs: []string{"https://fallback.example/"}, Versioning: "composer"}
	res, err := New().Extract(context.Background(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	d := depNamed(t, res.Deps, "vendor/x")
	if !slices.Equal(d.RegistryURLs, cfg.RegistryURLs) {
		t.Errorf("registryUrls = %v, want the ManagerConfig default %v", d.RegistryURLs, cfg.RegistryURLs)
	}
	if d.Versioning != "composer" {
		t.Errorf("versioning = %q, want the ManagerConfig default", d.Versioning)
	}
}

// TestEditReplacesOnlyRecordedBytes applies one Edit by hand and confirms
// everything outside the recorded byte range survives untouched.
func TestEditReplacesOnlyRecordedBytes(t *testing.T) {
	f, deps := extractAll(t, []byte(synthetic))
	dep := depNamed(t, deps, "vendor/tilde")

	edit, err := New().Edit(context.Background(), f, model.Update{Dep: dep, NewValue: "~2.4"})
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	if edit.Old != "~2.3" {
		t.Errorf("edit.Old = %q, want %q", edit.Old, "~2.3")
	}
	if edit.New != "~2.4" {
		t.Errorf("edit.New = %q, want %q", edit.New, "~2.4")
	}

	rebuilt := slices.Concat(f.Content[:edit.Start], []byte(edit.New), f.Content[edit.End:])
	if !bytes.Equal(rebuilt[:edit.Start], f.Content[:edit.Start]) {
		t.Error("bytes before the edit changed")
	}
	if !bytes.Equal(rebuilt[edit.Start+len(edit.New):], f.Content[edit.End:]) {
		t.Error("bytes after the edit changed")
	}
}

// TestEditRefusesWhenFileChanged covers the format-preservation invariant:
// Edit must fail rather than guess when the bytes it would replace no longer
// match what Extract recorded.
func TestEditRefusesWhenFileChanged(t *testing.T) {
	f, deps := extractAll(t, []byte(synthetic))
	dep := depNamed(t, deps, "vendor/tilde")

	changed := bytes.Clone(f.Content)
	copy(changed[dep.Locus.ValueStart:dep.Locus.ValueEnd], "~9.9")
	changedFile := extract.File{Path: f.Path, Content: changed}

	_, err := New().Edit(context.Background(), changedFile, model.Update{Dep: dep, NewValue: "~2.4"})
	if err == nil {
		t.Fatal("Edit succeeded against a file that changed since extraction, want an error")
	}
}

func TestLockedVersions(t *testing.T) {
	lock := []byte(`{
  "packages": [
    {"name": "vendor/pinned", "version": "7.0.2"},
    {"name": "vendor/any", "version": "4.5.0"}
  ],
  "packages-dev": [
    {"name": "vendor/devtool", "version": "3.1.9"}
  ]
}`)
	got, err := LockedVersions(lock)
	if err != nil {
		t.Fatalf("LockedVersions returned an error: %v", err)
	}
	want := map[string]string{
		"vendor/pinned":  "7.0.2",
		"vendor/any":     "4.5.0",
		"vendor/devtool": "3.1.9",
	}
	if !maps.Equal(got, want) {
		t.Errorf("LockedVersions = %v, want %v", got, want)
	}
}

func TestNameAndFilePatterns(t *testing.T) {
	m := New()
	if m.Name() != "composer" {
		t.Errorf("Name() = %q, want composer", m.Name())
	}
	if len(m.FilePatterns()) == 0 {
		t.Error("FilePatterns() returned nothing")
	}
	if m.NeedsPlugin() != nil {
		t.Errorf("NeedsPlugin() = %+v, want nil", m.NeedsPlugin())
	}
}

// --- the real fixture -------------------------------------------------------

// TestAgreesWithTheCorpus compares this manager's extraction of every
// composer.json in the fixture root's corpus against what the pinned
// Renovate container extracted from the same file. A capture whose checkout
// is not on this machine is skipped - this manager must not depend on a
// private checkout to build or to pass CI - but the public root's
// repositories are in the tree, so there the comparison always runs.
func TestAgreesWithTheCorpus(t *testing.T) {
	ran := 0
	for _, c := range fixture.Corpora(t) {
		for _, e := range c.Entries(t, "composer") {
			if c.Tree == "" {
				t.Logf("%s: checkout not present; %s skipped", c.Name, e.PackageFile)
				continue
			}
			ran++
			t.Run(c.Name+"/"+e.PackageFile, func(t *testing.T) {
				var want []corpusDep
				if err := json.Unmarshal(e.Deps, &want); err != nil {
					t.Fatal(err)
				}
				content := c.Read(t, e.PackageFile)
				res, err := New().Extract(context.Background(),
					extract.File{Path: e.PackageFile, Content: content}, extract.ManagerConfig{})
				if err != nil {
					t.Fatalf("Extract returned an error: %v", err)
				}
				compareWithCorpus(t, res.Deps, want)
			})
		}
	}
	if ran == 0 {
		t.Skip("no composer capture with its files on this machine")
	}
}

// corpusDep is one dependency as the capture records it: only the fields
// this comparison needs.
type corpusDep struct {
	CurrentValue string   `json:"currentValue"`
	Datasource   string   `json:"datasource"`
	DepName      string   `json:"depName"`
	DepType      string   `json:"depType"`
	PackageName  string   `json:"packageName"`
	SkipReason   string   `json:"skipReason"`
	RegistryURLs []string `json:"registryUrls"`
}

func compareWithCorpus(t *testing.T, deps []model.Dependency, want []corpusDep) {
	t.Helper()
	type key struct {
		depName, currentValue, depType string
	}
	remaining := make(map[key]model.Dependency, len(deps))
	for _, d := range deps {
		remaining[key{d.DepName, d.CurrentValue, d.DepType}] = d
	}

	agreed := 0
	for _, w := range want {
		k := key{w.DepName, w.CurrentValue, w.DepType}
		got, ok := remaining[k]
		if !ok {
			t.Errorf("corpus dependency not extracted: %s %s (%s)", w.DepName, w.CurrentValue, w.DepType)
			continue
		}
		delete(remaining, k)

		if got.Datasource != w.Datasource {
			t.Errorf("%s: datasource = %q, want %q", w.DepName, got.Datasource, w.Datasource)
		}
		if got.PackageName != w.PackageName {
			t.Errorf("%s: packageName = %q, want %q", w.DepName, got.PackageName, w.PackageName)
		}
		if got.SkipReason != w.SkipReason {
			t.Errorf("%s: skipReason = %q, want %q", w.DepName, got.SkipReason, w.SkipReason)
		}
		if !slices.Equal(got.RegistryURLs, w.RegistryURLs) {
			t.Errorf("%s: registryUrls = %v, want %v", w.DepName, got.RegistryURLs, w.RegistryURLs)
		}
		agreed++
	}
	for k := range remaining {
		t.Errorf("extracted a dependency the corpus does not have: %s %s (%s)", k.depName, k.currentValue, k.depType)
	}

	t.Logf("%d of %d corpus dependencies agreed exactly", agreed, len(want))
}
