// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"github.com/ohartwig/pinup/fake/fixture"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The captured parity snapshot is the target pinup's own resolution is
// measured against. These tests do not compare pinup to it yet - the rule
// engine does not exist - they establish that the snapshot is trustworthy and
// pin the numbers that decide how much work the preset rebuild actually is.
//
// The snapshot is produced by executing a pinned Renovate container and
// recording its output. Nothing is copied from the Renovate source tree; see
// tools/capture/README.md.

func snapshotDir(t *testing.T) string {
	t.Helper()
	return fixture.Captured(t)
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// A snapshot that does not say what produced it cannot be trusted a month
// later, and a missing field is exactly the kind of gap that goes unnoticed
// until the numbers disagree with reality.
func TestSnapshotProvenanceIsComplete(t *testing.T) {
	dir := snapshotDir(t)
	prov := readJSON[map[string]any](t, filepath.Join(dir, "provenance.json"))

	for _, k := range []string{
		"renovateVersion", "imageRef", "imageDigest", "configSha256",
		"configSourceCommit", "capturedAt", "capturedBy", "tz", "method",
	} {
		v, ok := prov[k]
		if !ok || v == "" {
			t.Errorf("provenance.json is missing %q", k)
		}
	}

	// The snapshot must describe the config actually checked in beside it,
	// or the two drifted and every comparison below is against the wrong
	// target.
	raw, err := os.ReadFile(fixture.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(mustRead(t, fixture.Config(t)+".sha256")))
	if got, _ := prov["configSha256"].(string); got != want {
		t.Errorf("snapshot was captured against a different config\n  snapshot: %s\n  on disk:  %s", got, want)
	}
	_ = raw

	// The directory name must match the version it claims.
	if v, _ := prov["renovateVersion"].(string); !strings.HasSuffix(dir, v) {
		t.Errorf("snapshot directory %q does not match renovateVersion %q", dir, v)
	}
}

// What the eleven presets actually contribute. These numbers are the scope of
// the preset rebuild, and they are pinned so a recapture that moves them shows
// up as a reviewed change rather than a quiet one.
func TestPresetExpansionScope(t *testing.T) {
	dir := snapshotDir(t)
	full := readJSON[map[string]any](t, filepath.Join(dir, "full-resolved.json"))
	rawCfg := readJSON[map[string]any](t, fixture.Config(t))

	rules, _ := full["packageRules"].([]any)
	written, _ := rawCfg["packageRules"].([]any)
	t.Logf("packageRules: %d written, %d after preset expansion", len(written), len(rules))

	want := fixture.Expect(t)
	if len(written) != want.RulesOwn {
		t.Errorf("the config writes %d rules, expected %d", len(written), want.RulesOwn)
	}
	if len(rules) != want.RulesResolved {
		t.Errorf("resolution yields %d rules, expected %d - the preset content moved", len(rules), want.RulesResolved)
	}
	if len(full) != want.ResolvedKeys {
		t.Errorf("resolved config has %d top-level keys, expected %d", len(full), want.ResolvedKeys)
	}

	visited := readJSON[map[string][]string](t, filepath.Join(dir, "visited-presets.json"))
	if n := len(visited["unmerged"]); n != want.Presets {
		t.Errorf("%d top-level presets visited, expected %d", n, want.Presets)
	}
}

// The matchers the resolved rules use, which is a wider surface than the
// hand-written config suggests. This test exists to keep that visible: the
// spec's first reading of the config found six matchers, and resolution needs
// four more.
func TestResolvedRulesNeedMatchersTheWrittenConfigDoesNot(t *testing.T) {
	dir := snapshotDir(t)
	full := readJSON[map[string]any](t, filepath.Join(dir, "full-resolved.json"))
	rules, _ := full["packageRules"].([]any)

	counts := map[string]int{}
	for _, r := range rules {
		m, _ := r.(map[string]any)
		for k := range m {
			if strings.HasPrefix(k, "match") {
				counts[k]++
			}
		}
	}

	// Implemented, or planned for phase 0.
	for _, k := range []string{"matchPackageNames", "matchDatasources", "matchUpdateTypes", "matchManagers"} {
		if counts[k] == 0 {
			t.Errorf("%s does not appear in the resolved rules; the snapshot looks wrong", k)
		}
	}

	// Not yet implemented. Each is load-bearing for preset-contributed rules,
	// and the counts say how much.
	for _, c := range []struct {
		key  string
		want int
	}{
		{"matchSourceUrls", 445},
		{"matchCurrentVersion", 110},
		{"matchDepTypes", 7},
		{"matchJsonata", 5},
	} {
		if counts[c.key] != c.want {
			t.Errorf("%s used by %d rules, expected %d", c.key, counts[c.key], c.want)
		}
	}

	// The bound that makes the rebuild tractable: almost every rule using an
	// unimplemented matcher is keyed to a named upstream package, so it can
	// only fire if this estate depends on that exact package.
	unimplemented := map[string]bool{
		"matchSourceUrls": true, "matchCurrentVersion": true,
		"matchJsonata": true, "matchDepTypes": true,
	}
	var affected, named int
	for _, r := range rules {
		m, _ := r.(map[string]any)
		hit := false
		for k := range m {
			if unimplemented[k] {
				hit = true
			}
		}
		if !hit {
			continue
		}
		affected++
		if m["matchPackageNames"] != nil || m["matchSourceUrls"] != nil {
			named++
		}
	}
	t.Logf("rules needing an unimplemented matcher: %d, of which %d are keyed to named packages", affected, named)
	if affected-named > 20 {
		t.Errorf("%d rules using an unimplemented matcher are generic and could fire for anything; "+
			"the rebuild is no longer bounded by the estate's dependency set", affected-named)
	}
}

// --------------------------------------------------------------------------
// The extraction corpus: real dependency vectors from three real repositories,
// captured by running the pinned container over copies of them.
//
// Until the managers exist this pins the target rather than comparing against
// it. Once manager/regexm lands, these vectors are what it must reproduce.
// --------------------------------------------------------------------------

type corpusDep struct {
	DepName       string `json:"depName"`
	PackageName   string `json:"packageName"`
	CurrentValue  string `json:"currentValue"`
	CurrentDigest string `json:"currentDigest"`
	Datasource    string `json:"datasource"`
	Versioning    string `json:"versioning"`
	SkipReason    string `json:"skipReason"`
}

type corpusFile struct {
	PackageFile string      `json:"packageFile"`
	Deps        []corpusDep `json:"deps"`
}

func loadCorpus(t *testing.T) map[string]map[string][]corpusFile {
	t.Helper()
	dir := filepath.Join(snapshotDir(t), "extract")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no extraction corpus: %v", err)
	}
	out := map[string]map[string][]corpusFile{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		repo := strings.TrimSuffix(e.Name(), ".json")
		out[repo] = readJSON[map[string][]corpusFile](t, filepath.Join(dir, e.Name()))
	}
	return out
}

func corpusDeps(t *testing.T) []corpusDep {
	t.Helper()
	var all []corpusDep
	for _, managers := range loadCorpus(t) {
		for _, files := range managers {
			for _, f := range files {
				all = append(all, f.Deps...)
			}
		}
	}
	return all
}

func TestCorpusIsRepresentative(t *testing.T) {
	deps := corpusDeps(t)
	t.Logf("%d dependency vectors from %d repositories", len(deps), len(loadCorpus(t)))
	// 350 from seven estate repositories, plus 14 from the synthetic tree
	// under testdata/renovate/synthetic that exercises the two enabled
	// managers no estate repository uses yet (terraform-version, kustomize).
	// ... plus 10 from the synthetic gomod tree captured with gomod.json,
	// the one manager the runner never enables and pinup needs for itself.
	if want := fixture.Expect(t).CorpusDeps; len(deps) != want {
		t.Errorf("corpus has %d vectors, expected %d - it was recaptured", len(deps), want)
	}

	seen := map[string]bool{}
	for _, d := range deps {
		seen["ds:"+d.Datasource] = true
		if d.Versioning != "" {
			seen["ver:"+d.Versioning] = true
		}
	}
	// The corpus earns its place by covering datasources a hand-written
	// fixture would not have thought to include.
	for _, want := range []string{
		"ds:docker", "ds:gitlab-tags", "ds:gitlab-releases", "ds:gitlab-packages",
		"ds:github-tags", "ds:github-releases", "ds:packagist", "ds:npm",
		"ds:terraform-provider", "ds:custom.wolfi", "ds:git-tags", "ds:helm", "ds:go", "ds:golang-version",
		"ver:composer", "ver:loose", "ver:semver-partial", "ver:docker", "ver:hashicorp", "ver:go-mod-directive",
	} {
		if !seen[want] {
			t.Errorf("corpus does not cover %s", want)
		}
	}
}

// The custom manager with the nested Handlebars conditional builds a
// composite packageName, "<project path>:<depName>" on the gitlab-packages
// datasource. The corpus records the real outputs - what hbs and
// manager/regexm together must reproduce (manager/regexm compares them) -
// and this test pins that the capture carries them at all: at least one,
// every one in the template's shape, and the count as expect.json says.
func TestCorpusPinsTheTemplatedPackageNames(t *testing.T) {
	n := 0
	for _, d := range corpusDeps(t) {
		if d.Datasource != "gitlab-packages" || d.PackageName == "" {
			continue
		}
		n++
		if project, dep, ok := strings.Cut(d.PackageName, ":"); !ok || dep != d.DepName || project == "" {
			t.Errorf("packageName %q for %s is not <project>:<depName>", d.PackageName, d.DepName)
		}
	}
	if want := fixture.Expect(t).TemplatedNames; n != want {
		t.Errorf("the corpus carries %d templated package names, expect.json pins %d", n, want)
	}
}

// A defect in the runner configuration, found by running the corpus rather
// than by reading the config.
//
// The first-party composer manager matches `"<vendor>/x": "y"` anywhere in
// composer.json, with no notion of which block it is in. composer's
// `suggest` maps a package name to a human description, so the manager
// reads those descriptions as version constraints and emits dependencies
// whose currentValue is prose.
//
// They cannot resolve, so today this is noise rather than damage. The test
// exists so the noise is recorded rather than tolerated: expect.json pins
// how many the root's corpus carries, and a configuration that was fixed
// moves the pin to zero in the same commit, which is the point.
func TestCorpusRecordsTheSuggestBlockOverMatch(t *testing.T) {
	// Discriminating prose from a constraint needs care, and the obvious rule
	// is wrong. "contains a space" flags `^0.3 || ^0.4 || ^0.5` - twenty-one
	// perfectly valid composer OR-ranges in moselwal-websites - and asserting
	// those are a defect would have been worse than not testing at all.
	//
	// A composer constraint is digits and the operators . ^ ~ > < = * | -,
	// plus `dev-<branch>` and a leading v. So: a word of three or more
	// letters, outside a dev- prefix, is prose.
	word := regexp.MustCompile(`[A-Za-z]{3,}`)
	isProse := func(v string) bool {
		if strings.HasPrefix(v, "dev-") {
			return false
		}
		return word.MatchString(v)
	}

	var prose []corpusDep
	for _, d := range corpusDeps(t) {
		switch d.Datasource {
		case "gitlab-packages", "packagist":
			if isProse(d.CurrentValue) {
				prose = append(prose, d)
			}
		}
	}
	if want := fixture.Expect(t).SuggestOverMatches; len(prose) != want {
		t.Errorf("found %d dependencies whose currentValue is prose, expect.json pins %d "+
			"(the composer `suggest` over-match)", len(prose), want)
	}
	for _, d := range prose {
		t.Logf("  over-match: %s = %q", d.DepName, d.CurrentValue)
	}
}

// --------------------------------------------------------------------------
// Versioning behaviour tables. Until versioning/* exists these assert that the
// captured tables are usable and pin the semantics that are easy to get wrong.
// --------------------------------------------------------------------------

type verRow struct {
	In      any `json:"in"`
	A       any `json:"a"`
	B       any `json:"b"`
	Version any `json:"version"`
	Range   any `json:"range"`
	Out     any `json:"out"`
}

type verTable struct {
	Module        string   `json:"module"`
	Source        string   `json:"source"`
	IsValid       []verRow `json:"isValid"`
	IsStable      []verRow `json:"isStable"`
	IsGreaterThan []verRow `json:"isGreaterThan"`
	Matches       []verRow `json:"matches"`
}

func verTableFor(t *testing.T, module string) verTable {
	t.Helper()
	return readJSON[verTable](t, fixture.SharedCaptured(t, "versioning", module+".json"))
}

func TestVersioningTablesAreUsable(t *testing.T) {
	var total int
	for _, m := range []string{
		"semver", "docker", "composer", "loose", "npm", "go", "apk",
		"semver-partial", "semver-coerced", "hashicorp", "regex-alpine",
		"go-mod-directive", "node",
	} {
		tbl := verTableFor(t, m)
		n := len(tbl.IsValid) + len(tbl.IsStable) + len(tbl.IsGreaterThan) + len(tbl.Matches)
		if n == 0 {
			t.Errorf("%s: table is empty", m)
		}
		if !strings.Contains(tbl.Source, "renovate/renovate:43.288.0") {
			t.Errorf("%s: table does not record what produced it (%q)", m, tbl.Source)
		}
		total += n
	}
	// Counts only the four operations this struct decodes, not every row in
	// the files - the tables carry getMajor, equals, sortVersions,
	// getSatisfyingVersion and getNewValue too, which versioning/* will read
	// when it exists.
	t.Logf("%d rows across 13 schemes, in the four operations decoded here", total)
	if total < 4000 {
		t.Errorf("only %d rows; the capture is thinner than it should be", total)
	}
}

// The compatibility segment orders in REVERSE lexicographic order. An earlier
// version of this test asserted only that the two directions disagree and
// concluded there was no ordering - which is backwards, since disagreeing
// directions are what an ordering is. It now asserts the actual rule.
func TestDockerCompatibilityOrdersInReverse(t *testing.T) {
	tbl := verTableFor(t, "docker")
	get := func(a, b string) (bool, bool) {
		for _, r := range tbl.IsGreaterThan {
			if r.A == a && r.B == b {
				v, ok := r.Out.(bool)
				return v, ok
			}
		}
		return false, false
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.0-alpha", true},
		{"22-alpine3.20", "22-alpine3.21", true},
		{"22-alpine3.21", "22-alpine3.20", false},
		{"22-alpine3.21", "22-bookworm", true},
		{"22-bookworm", "22-alpine3.21", false},
	} {
		got, ok := get(c.a, c.b)
		if !ok {
			t.Errorf("pair (%q,%q) missing from the captured table", c.a, c.b)
			continue
		}
		if got != c.want {
			t.Errorf("captured isGreaterThan(%q,%q) = %v, want %v - "+
				"if Renovate changed the suffix ordering, versioning/docker must change with it",
				c.a, c.b, got, c.want)
		}
	}
}

// Eight corpus vectors use semver-partial, and its matching rule is not the
// obvious one.
func TestSemverPartialMatching(t *testing.T) {
	tbl := verTableFor(t, "semver-partial")
	got := map[string]any{}
	for _, r := range tbl.Matches {
		if r.Range == "1" {
			got[r.Version.(string)] = r.Out
		}
	}
	if got["1.10.17"] != true {
		t.Errorf(`matches("1.10.17", "1") = %v, want true`, got["1.10.17"])
	}
	if got["1.22"] != false {
		t.Errorf(`matches("1.22", "1") = %v, want false - a two-part version is not comparable`, got["1.22"])
	}
}
