// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/fake/fixture"
)

func TestFlattenNotationAndOrder(t *testing.T) {
	doc := map[string]any{
		"packageRules": []any{
			map[string]any{"automerge": true, "matchManagers": []any{"custom.regex"}},
			map[string]any{"enabled": false},
		},
		"labels":   []any{},
		"schedule": "at any time",
		"lock":     map[string]any{},
	}
	got := Flatten(doc)
	want := []string{
		`labels []`,
		`lock {}`,
		`packageRules[0].automerge true`,
		`packageRules[0].matchManagers[0] "custom.regex"`,
		`packageRules[1].enabled false`,
		`schedule "at any time"`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	if p := PointerOf("packageRules[25].matchManagers[0]"); p != "/packageRules/25/matchManagers/0" {
		t.Errorf("PointerOf = %q", p)
	}
}

// Swapping two rules must show up as every differing line of both, not as
// a reordering a tree differ would hide.
func TestDiffSeesARuleSwap(t *testing.T) {
	a := Flatten(map[string]any{"packageRules": []any{
		map[string]any{"automerge": true, "matchManagers": []any{"composer"}},
		map[string]any{"enabled": false, "matchDatasources": []any{"npm"}},
	}})
	b := Flatten(map[string]any{"packageRules": []any{
		map[string]any{"enabled": false, "matchDatasources": []any{"npm"}},
		map[string]any{"automerge": true, "matchManagers": []any{"composer"}},
	}})
	d := Diff(a, b)
	if len(d) != 8 {
		t.Fatalf("a swap of two two-key rules is 8 differing lines, got %d:\n%s", len(d), strings.Join(d, "\n"))
	}
	if len(Diff(a, a)) != 0 {
		t.Error("a document differs from itself")
	}
}

// DiffPaths is Diff by path: a changed value once, a removal and an
// addition each once, in a's order then b's, and nothing for equal documents.
func TestDiffPaths(t *testing.T) {
	for _, c := range []struct {
		name string
		a, b map[string]any
		want []string
	}{
		{"equal", map[string]any{"a": 1.0, "b": "x"}, map[string]any{"a": 1.0, "b": "x"}, nil},
		{"changed", map[string]any{"a": 1.0, "b": "x"}, map[string]any{"a": 2.0, "b": "x"}, []string{"a"}},
		{"removed", map[string]any{"a": 1.0, "b": "x"}, map[string]any{"b": "x"}, []string{"a"}},
		{"added", map[string]any{"b": "x"}, map[string]any{"a": 1.0, "b": "x"}, []string{"a"}},
		{"nested and ordered", map[string]any{"r": []any{"p", "q"}, "z": true}, map[string]any{"r": []any{"p"}, "y": 1.0},
			[]string{"r[1]", "z", "y"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := DiffPaths(Flatten(c.a), Flatten(c.b))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("DiffPaths = %q, want %q", got, c.want)
			}
		})
	}
}

// print-config parity: the configuration resolved here - defaults,
// presets, file - flattened, against what the pinned container printed for
// the same file, on every key but three. packageRules and customManagers
// are compared elsewhere, by effect (rules/parity_test) and by extraction
// (the corpus tests): the preset library is pinup's own and carries fewer
// rules than Renovate's, on purpose (config/preset/README.md). description
// is the library's wording. Keys that describe the capture run rather than
// the program are dropped, as measured and recorded in
// testdata/renovate/.../presets/README.md. Everything else must agree line
// for line, and the line count is asserted so an empty comparison cannot
// pass.
func TestPrintConfigParityWithTheCapturedResolution(t *testing.T) {
	r, _, err := ResolveFile(fixture.Config(t), preset.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	want := loadJSON(t, fixture.Captured(t, "resolved-options.json"))
	for k := range want {
		if runSpecific[k] {
			delete(want, k)
		}
	}
	mine := map[string]any{}
	for k, v := range r.Raw {
		if k != "packageRules" && k != "customManagers" && k != "description" {
			mine[k] = v
		}
	}
	for _, k := range []string{"packageRules", "customManagers", "description"} {
		if _, ok := want[k]; ok {
			t.Fatalf("resolved-options.json carries %s; the capture was not reduced", k)
		}
	}
	flatMine, theirs := Flatten(mine), Flatten(want)
	if len(theirs) < 400 {
		t.Fatalf("only %d lines captured; the snapshot was barely read", len(theirs))
	}
	d := Diff(flatMine, theirs)
	if len(d) != 0 {
		limit := min(len(d), 30)
		t.Errorf("%d lines differ from the captured resolution (first %d):\n%s", len(d), limit, strings.Join(d[:limit], "\n"))
	}
	t.Logf("%d flattened lines agree", len(theirs))
}

// runSpecific mirrors tools/defaultsgen: keys of the capture run, the
// platform session or a secret.
var runSpecific = map[string]bool{
	"branchList": true, "defaultBranch": true, "errors": true, "warnings": true,
	"repository": true, "repoFingerprint": true, "repoIsOnboarded": true, "renovateJsonPresent": true,
	"isFork": true, "hostRules": true, "token": true, "password": true, "username": true,
	"npmToken": true, "npmrc": true, "forkToken": true, "encrypted": true, "dryRun": true,
	"printConfig": true, "reportPath": true, "reportType": true, "reportFormatting": true,
	"privateKeyPath": true, "privateKeyPathOld": true, "gitAuthor": true, "gitUrl": true,
	"logLevelRemap": true, "writeDiscoveredRepos": true, "detectHostRulesFromEnv": true,
	"detectGlobalManagerConfig": true, "globalExtends": true, "onboardingBranch": true,
	"onboardingRebaseCheckbox": true, "persistRepoData": true, "repositoryCache": true,
	"repositoryCacheType": true, "useCloudMetadataServices": true, "mode": true,
	"inheritConfig": true, "inheritConfigFileName": true, "inheritConfigRepoName": true,
	"inheritConfigStrict": true, "platformCommit": true, "forkCreation": true, "forkOrg": true,
	"forkModeDisallowMaintainerEdits": true, "forkProcessing": true, "cloneSubmodules": true,
	"cloneSubmodulesFilter": true, "customizeDashboard": true,
	"deleteConfigFile": true, "deleteAdditionalConfigFile": true, "optimizeForDisabled": true,
	"expandCodeOwnersGroups": true, "filterUnavailableUsers": true, "azureWorkItemId": true,
	"azureWorkItemType": true, "bbAutoResolvePrTasks": true, "bbUseDefaultReviewers": true,
	"gitLabIgnoreApprovals": true, "milestone": true, "parentOrg": true,
}

func loadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
