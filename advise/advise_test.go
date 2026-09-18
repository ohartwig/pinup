// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package advise

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/versioning"
)

// base is a preset the cases extend: an ignore list, a schedule, a label
// and one rule, so a file can repeat, replace or shadow something.
var base = preset.Aliases{"p:base": {
	"ignorePaths":  []any{"**/vendor/**", "**/node_modules/**"},
	"schedule":     []any{"before 5am"},
	"labels":       []any{"dep"},
	"packageRules": []any{map[string]any{"matchDatasources": []any{"docker"}, "automerge": false}},
}}

// sources is the library the cases resolve against: the test preset over
// the builtin library.
var sources = preset.Chain{base, preset.Builtin()}

func load(t *testing.T, cfg string, plans ...*model.Plan) *Input {
	t.Helper()
	layer, err := config.Parse([]byte(cfg), "x.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := Load(layer, sources, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	in.Plans = plans
	in.Covers = func(m string) bool { return m == "npm" || m == "dockerfile" }
	in.Datasources = map[string]bool{"docker": true, "npm": true}
	return in
}

func plan(deps []model.Dependency, updates []model.Update, warnings ...model.Warning) *model.Plan {
	return &model.Plan{Repo: model.RepoRef{Path: "g/r"}, Deps: deps, Updates: updates, Warnings: warnings}
}

var dockerDep = model.Dependency{Manager: "dockerfile", CustomManager: model.NoCustomManager, DepName: "alpine", Datasource: "docker", CurrentValue: "3.20"}

// cases is the catalogue's proof: one firing configuration per check, with
// the pointer and the fix it must produce. The gate below asserts every
// check appears here.
var cases = []struct {
	name, cfg string
	plans     []*model.Plan
	want      string // the check ID
	pointer   string
	fix       *Fix
	absent    []string // IDs that must not fire on this configuration
}{
	{name: "inert preset the file names", cfg: `{"extends": ["config:recommended", "abandonments:recommended"]}`,
		want: "compat/preset-inert", pointer: "/extends/1", fix: &Fix{Pointer: "/extends/1", Op: OpRemove}},
	{name: "zero release age migrated", cfg: `{"minimumReleaseAge": "0"}`,
		want: "compat/migrated", pointer: "/minimumReleaseAge", fix: &Fix{Pointer: "/minimumReleaseAge", Op: OpSet}},
	{name: "a rule the engine cannot evaluate", cfg: `{"packageRules": [{"matchCategories": ["docker"], "enabled": false}]}`,
		want: "compat/rule-not-evaluable", pointer: "/packageRules/0"},
	{name: "a matcher the engine has never heard of", cfg: `{"packageRules": [{"matchNonesuch": ["x"]}]}`,
		want: "compat/rules-not-compilable", pointer: "/packageRules"},
	{name: "a key nothing reads", cfg: `{"nonesuchKey": 1}`,
		want: "compat/key-unsupported", pointer: "/nonesuchKey", fix: &Fix{Pointer: "/nonesuchKey", Op: OpRemove, Changes: []string{"/nonesuchKey"}}},
	{name: "a key honoured in part", cfg: `{"rebaseWhen": "auto"}`,
		want: "compat/key-partial", pointer: "/rebaseWhen"},
	{name: "a manager nothing implements", cfg: `{"enabledManagers": ["nonesuch"]}`,
		want: "compat/manager-unknown", pointer: "/enabledManagers", fix: &Fix{Pointer: "/enabledManagers/0", Op: OpRemove, Changes: []string{"/enabledManagers"}}},
	{name: "a datasource nothing serves", cfg: `{"packageRules": [{"matchDatasources": ["nonesuch"], "enabled": false}]}`,
		want: "compat/datasource-unknown", pointer: "/packageRules/0/matchDatasources/0"},
	{name: "a schedule that does not parse", cfg: `{"schedule": ["every blue moon"]}`,
		want: "compat/schedule-invalid", pointer: "/schedule"},
	{name: "a lookbehind", cfg: `{"customManagers": [{"customType": "regex", "managerFilePatterns": ["/x/"], "matchStrings": ["(?<=a)b"], "datasourceTemplate": "docker"}]}`,
		want: "compat/regex-not-re2", pointer: "/customManagers/0/matchStrings/0"},

	{name: "a label the preset already sets", cfg: `{"extends": ["p:base"], "labels": ["dep"]}`,
		want: "hygiene/redundant-inherited", pointer: "/labels", fix: &Fix{Pointer: "/labels", Op: OpRemove}},
	{name: "the builtin default spelled out", cfg: `{"prConcurrentLimit": 10}`,
		want: "hygiene/redundant-default", pointer: "/prConcurrentLimit", fix: &Fix{Pointer: "/prConcurrentLimit", Op: OpRemove}},
	{name: "a null over nothing", cfg: `{"minimumReleaseAge": null}`,
		want: "hygiene/null-clears-nothing", pointer: "/minimumReleaseAge", fix: &Fix{Pointer: "/minimumReleaseAge", Op: OpRemove, Changes: []string{"/minimumReleaseAge"}},
		absent: []string{"compat/migrated"}},
	{name: "at any time spelled out", cfg: `{"schedule": ["at any time"]}`,
		want: "hygiene/schedule-anytime-explicit", pointer: "/schedule", fix: &Fix{Pointer: "/schedule", Op: OpRemove, Changes: []string{"/schedule"}}},
	{name: "at any time over a real window is deliberate", cfg: `{"extends": ["p:base"], "schedule": ["at any time"]}`,
		want: "hygiene/schedule-anytime-explicit", pointer: "/schedule"},
	{name: "a preset extended twice", cfg: `{"extends": ["config:recommended", "config:recommended"]}`,
		want: "hygiene/extends-duplicate", pointer: "/extends/1", fix: &Fix{Pointer: "/extends/1", Op: OpRemove, Changes: concatChanged}},
	{name: "a preset another one already reaches", cfg: `{"extends": ["config:recommended", ":ignoreModulesAndTests"]}`,
		want: "hygiene/extends-transitive", pointer: "/extends/1", fix: &Fix{Pointer: "/extends/1", Op: OpRemove, Changes: concatChanged}},
	{name: "a path listed twice", cfg: `{"ignorePaths": ["**/a/**", "**/a/**"]}`,
		want: "hygiene/duplicate-entry", pointer: "/ignorePaths/1", fix: &Fix{Pointer: "/ignorePaths/1", Op: OpRemove, Changes: []string{"/ignorePaths"}}},
	{name: "a rule with no matcher", cfg: `{"packageRules": [{"enabled": false}]}`,
		want: "hygiene/rule-no-matchers", pointer: "/packageRules/0"},
	{name: "a rule that sets nothing", cfg: `{"packageRules": [{"matchDatasources": ["docker"], "description": "why"}]}`,
		want: "hygiene/rule-no-effect", pointer: "/packageRules/0", fix: &Fix{Pointer: "/packageRules/0", Op: OpRemove, Changes: []string{"/packageRules"}}},
	{name: "a rule a later one shadows", cfg: `{"packageRules": [{"matchDatasources": ["docker"], "enabled": false}, {"matchDatasources": ["docker"], "enabled": true}]}`,
		want: "hygiene/rule-duplicate-matchers", pointer: "/packageRules/0", fix: &Fix{Pointer: "/packageRules/0", Op: OpRemove, Changes: []string{"/packageRules"}}},
	{name: "two rules that could be one", cfg: `{"packageRules": [{"matchDatasources": ["docker"], "enabled": false}, {"matchDatasources": ["docker"], "pinDigests": true}]}`,
		want: "hygiene/rule-duplicate-matchers", pointer: "/packageRules/0"},
	{name: "a custom manager written twice", cfg: `{"customManagers": [{"customType": "regex", "managerFilePatterns": ["/x/"], "matchStrings": ["v(?<currentValue>\\d+)"], "depNameTemplate": "x", "datasourceTemplate": "docker"}, {"customType": "regex", "managerFilePatterns": ["/x/"], "matchStrings": ["v(?<currentValue>\\d+)"], "depNameTemplate": "x", "datasourceTemplate": "docker"}]}`,
		want: "hygiene/custom-manager-duplicate", pointer: "/customManagers/1", fix: &Fix{Pointer: "/customManagers/1", Op: OpRemove, Changes: []string{"/customManagers"}}},

	{name: "ignorePaths replacing the inherited list", cfg: `{"extends": ["p:base"], "ignorePaths": ["**/x/**"]}`,
		want: "perf/ignore-paths-replaced", pointer: "/ignorePaths",
		fix: &Fix{Pointer: "/ignorePaths", Op: OpSet, Value: []any{"**/vendor/**", "**/node_modules/**", "**/x/**"}, Changes: []string{"/ignorePaths"}}},
	{name: "a lock refresh with no window", cfg: `{"lockFileMaintenance": {"enabled": true}}`,
		want: "perf/lock-file-maintenance-unscheduled", pointer: "/lockFileMaintenance/schedule",
		fix: &Fix{Pointer: "/lockFileMaintenance/schedule", Op: OpSet, Value: []any{"before 5am on monday"}, Changes: []string{"/lockFileMaintenance/schedule"}}},
	{name: "no cap", cfg: `{"prHourlyLimit": 0}`,
		want: "perf/pr-limit-unbounded", pointer: "/prHourlyLimit"},
	{name: "a manager reading every file", cfg: `{"customManagers": [{"customType": "regex", "managerFilePatterns": ["/.*/"], "matchStrings": ["v(?<currentValue>\\d+)"], "depNameTemplate": "x", "datasourceTemplate": "docker"}]}`,
		want: "perf/file-pattern-catch-all", pointer: "/customManagers/0/managerFilePatterns/0"},

	{name: "an automerge over every update type", cfg: `{"packageRules": [{"matchDatasources": ["docker"], "automerge": true}]}`,
		want: "sec/automerge-major", pointer: "/packageRules/0/automerge",
		fix: &Fix{Pointer: "/packageRules/0/matchUpdateTypes", Op: OpSet, Value: safeUpdateTypes, Changes: []string{"/packageRules/0/matchUpdateTypes"}}},
	{name: "an automerge listing major", cfg: `{"packageRules": [{"matchDatasources": ["docker"], "automerge": true, "matchUpdateTypes": ["minor", "major"]}]}`,
		want: "sec/automerge-major", pointer: "/packageRules/0/automerge",
		fix: &Fix{Pointer: "/packageRules/0/matchUpdateTypes", Op: OpSet, Value: []any{"minor"}, Changes: []string{"/packageRules/0/matchUpdateTypes"}}},
	{name: "a top-level automerge", cfg: `{"automerge": true}`,
		want: "sec/automerge-major", pointer: "/automerge", fix: &Fix{Pointer: "/major", Op: OpSet, Value: map[string]any{"automerge": false}, Changes: []string{"/major"}}},
	{name: "a top-level automerge with a major object", cfg: `{"automerge": true, "major": {"minimumReleaseAge": "7 days"}}`,
		want: "sec/automerge-major", pointer: "/automerge", fix: &Fix{Pointer: "/major/automerge", Op: OpSet, Value: false, Changes: []string{"/major/automerge"}}},
	{name: "the analyzer trusted", cfg: `{"packageRules": [{"matchEffective": ["patch"], "matchUpdateTypes": ["minor"], "automerge": true, "trustEffective": true}]}`,
		want: "sec/trust-effective", pointer: "/packageRules/0/trustEffective", absent: []string{"sec/match-effective-without-trust"}},
	{name: "the analyzer matched but not trusted", cfg: `{"packageRules": [{"matchEffective": ["patch"], "matchUpdateTypes": ["minor"], "automerge": true}]}`,
		want: "sec/match-effective-without-trust", pointer: "/packageRules/0/matchEffective"},
	{name: "a plaintext registry", cfg: `{"registryUrls": ["http://registry.example/"]}`,
		want: "sec/registry-http", pointer: "/registryUrls/0", fix: &Fix{Pointer: "/registryUrls/0", Op: OpSet, Value: "https://registry.example/", Changes: []string{"/registryUrls/0"}}},
	{name: "advisories switched off", cfg: `{"osvVulnerabilityAlerts": false}`,
		want: "sec/vulnerability-alerts-off", pointer: "/osvVulnerabilityAlerts", fix: &Fix{Pointer: "/osvVulnerabilityAlerts", Op: OpSet, Value: true, Changes: []string{"/osvVulnerabilityAlerts"}}},
	{name: "images not pinned", cfg: `{"extends": ["config:recommended"], "enabledManagers": ["dockerfile"]}`,
		want: "sec/pin-digests-off", pointer: "/pinDigests", fix: &Fix{Pointer: "/extends", Op: OpAppend, Value: "docker:pinDigests", Changes: concatChanged}},
	{name: "tasks no allowlist admits", cfg: `{"postUpgradeTasks": {"commands": ["make"]}}`,
		want: "sec/post-upgrade-tasks-unallowed", pointer: "/postUpgradeTasks"},
	{name: "an allowlist admitting everything", cfg: `{"allowedCommands": [".*"]}`,
		want: "sec/allowed-commands-catch-all", pointer: "/allowedCommands/0"},
	{name: "no release age anywhere", cfg: `{}`,
		want: "sec/minimum-release-age-unset", pointer: "/minimumReleaseAge", fix: &Fix{Pointer: "/minimumReleaseAge", Op: OpSet, Value: "3 days", Changes: []string{"/minimumReleaseAge"}}},
	{name: "prereleases let in", cfg: `{"ignoreUnstable": false}`,
		want: "sec/ignore-unstable-false", pointer: "/ignoreUnstable", fix: &Fix{Pointer: "/ignoreUnstable", Op: OpSet, Value: true, Changes: []string{"/ignoreUnstable"}}},

	{name: "a rule no dependency reached", cfg: `{"packageRules": [{"matchDatasources": ["npm"], "enabled": false}]}`,
		plans: []*model.Plan{plan([]model.Dependency{dockerDep}, nil)},
		want:  "plan/rule-never-matched", pointer: "/packageRules/0"},
	{name: "a manager that found nothing", cfg: `{"enabledManagers": ["npm", "dockerfile"]}`,
		plans: []*model.Plan{plan([]model.Dependency{dockerDep}, nil)},
		want:  "plan/manager-idle", pointer: "/enabledManagers/0", fix: &Fix{Pointer: "/enabledManagers/0", Op: OpRemove, Changes: []string{"/enabledManagers"}}},
	{name: "a custom manager that found nothing", cfg: `{"customManagers": [{"customType": "regex", "managerFilePatterns": ["/x/"], "matchStrings": ["v(?<currentValue>\\d+)"], "depNameTemplate": "x", "datasourceTemplate": "docker"}]}`,
		plans: []*model.Plan{plan([]model.Dependency{dockerDep}, nil)},
		want:  "plan/custom-manager-idle", pointer: "/customManagers/0"},
	{name: "every update held by the schedule", cfg: `{"schedule": ["before 5am"]}`,
		plans: []*model.Plan{plan(nil, []model.Update{
			{Dep: dockerDep, Blocks: []model.Block{{Reason: model.BlockSchedule, Org: model.Origin{Rule: model.NoRule}}}},
			{Dep: dockerDep, Blocks: []model.Block{{Reason: model.BlockSchedule, Org: model.Origin{Rule: model.NoRule}}}},
		})},
		want: "plan/all-held", pointer: "/schedule"},
	{name: "the hourly cap holding updates", cfg: `{"prHourlyLimit": 2}`,
		plans: []*model.Plan{plan(nil, []model.Update{
			{Dep: dockerDep},
			{Dep: dockerDep, Blocks: []model.Block{{Reason: model.BlockHourlyLimit, Org: model.Origin{Rule: model.NoRule}}}},
		})},
		want: "plan/limit-holds", pointer: "/prHourlyLimit", absent: []string{"plan/all-held"}},
	{name: "a custom datasource failing", cfg: `{"customDatasources": {"koh": {"defaultRegistryUrlTemplate": "https://x/{{packageName}}", "format": "plain"}}}`,
		plans: []*model.Plan{plan(nil, nil, model.Warning{Stage: "lookup", Msg: "php via custom.koh: custom.koh: php: request failed"})},
		want:  "plan/datasource-failing", pointer: "/customDatasources/koh"},
}

func TestEachCheckFiresWhereItShould(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := load(t, c.cfg, c.plans...)
			findings, _ := Run(in, Catalogue())
			var hit []Finding
			for _, f := range findings {
				if f.ID == c.want && f.Pointer == c.pointer {
					hit = append(hit, f)
				}
				if slices.Contains(c.absent, f.ID) {
					t.Errorf("%s fired: %s", f.ID, f.Msg)
				}
			}
			if len(hit) != 1 {
				t.Fatalf("%s at %s: %d findings, want 1; all findings:\n%s", c.want, c.pointer, len(hit), describe(findings))
			}
			f := hit[0]
			if c.fix == nil {
				if f.Fix != nil {
					t.Errorf("unexpected fix %+v", *f.Fix)
				}
				return
			}
			if f.Fix == nil {
				t.Fatalf("no fix; want %+v", *c.fix)
			}
			if f.Fix.Pointer != c.fix.Pointer || f.Fix.Op != c.fix.Op || !reflect.DeepEqual(f.Fix.Value, c.fix.Value) || !slices.Equal(f.Fix.Changes, c.fix.Changes) {
				t.Errorf("fix = %+v, want %+v", *f.Fix, *c.fix)
			}
			if f.Msg == "" || f.Origin.Source == "" {
				t.Errorf("a finding without a message or an origin: %+v", f)
			}
		})
	}
}

func describe(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("  " + f.ID + " " + f.Pointer + ": " + f.Msg + "\n")
	}
	return b.String()
}

// The gate that proves the gates can fail: every check in the catalogue
// has a case above that makes it fire, and no case names a check that does
// not exist. Deleting a case turns this red.
func TestEveryCheckCanFire(t *testing.T) {
	ids := map[string]bool{}
	for _, c := range Catalogue() {
		if ids[c.ID] {
			t.Errorf("%s is in the catalogue twice", c.ID)
		}
		ids[c.ID] = true
	}
	fired := map[string]bool{}
	for _, c := range cases {
		if !ids[c.want] {
			t.Errorf("case %q wants %s, which is not in the catalogue", c.name, c.want)
		}
		fired[c.want] = true
	}
	for id := range ids {
		if !fired[id] {
			t.Errorf("%s has no case that makes it fire", id)
		}
	}
	if len(ids) < 30 {
		t.Errorf("only %d checks; the catalogue shrank", len(ids))
	}
}

// A clean configuration yields nothing: checks fire on something, not on
// everything.
func TestEveryCheckCanStaySilent(t *testing.T) {
	in := load(t, `{"extends": ["config:recommended"], "osvVulnerabilityAlerts": true, "minimumReleaseAge": "3 days"}`,
		plan([]model.Dependency{dockerDep}, nil))
	findings, skipped := Run(in, Catalogue())
	if len(findings) != 0 {
		t.Errorf("a clean configuration has findings:\n%s", describe(findings))
	}
	if len(skipped) != 0 {
		t.Errorf("with a plan nothing is skipped, got %v", skipped)
	}
	_, skipped = Run(load(t, `{}`), Catalogue())
	if len(skipped) != len(planChecks) {
		t.Errorf("without a plan every plan check is skipped: %v", skipped)
	}
}

// A fix edits the user's file and nothing else: its parent is in the
// document as written, and a remove names something that is there.
func TestFixesPointIntoTheFile(t *testing.T) {
	for _, c := range cases {
		in := load(t, c.cfg, c.plans...)
		findings, _ := Run(in, Catalogue())
		for _, f := range findings {
			if f.Fix == nil {
				continue
			}
			if strings.HasPrefix(f.Origin.Source, "preset:") && f.Frame == FrameFile {
				t.Errorf("%s: %s fixes a preset-owned value at %s", c.name, f.ID, f.Fix.Pointer)
			}
			segs := strings.Split(strings.TrimPrefix(f.Fix.Pointer, "/"), "/")
			parent, ok := walk(in.Layer.Raw, segs[:len(segs)-1])
			if !ok {
				t.Errorf("%s: %s fix at %s: parent is not in the file", c.name, f.ID, f.Fix.Pointer)
				continue
			}
			_, exists := walk(parent, segs[len(segs)-1:])
			switch f.Fix.Op {
			case OpRemove:
				if !exists {
					t.Errorf("%s: %s removes %s, which is not in the file", c.name, f.ID, f.Fix.Pointer)
				}
			case OpAppend:
				if _, isList := parent.(map[string]any)[segs[len(segs)-1]].([]any); !isList {
					t.Errorf("%s: %s appends to %s, which is not a list in the file", c.name, f.ID, f.Fix.Pointer)
				}
			}
		}
	}
}

func walk(v any, segs []string) (any, bool) {
	for _, s := range segs {
		switch x := v.(type) {
		case map[string]any:
			next, ok := x[s]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(s)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// Two rules from the preset stand before the file's three: the file's
// second rule is resolved index 3, and index 1 belongs to the preset.
func TestFrameMapping(t *testing.T) {
	src := preset.Chain{preset.Aliases{"p:two": {"packageRules": []any{
		map[string]any{"matchDatasources": []any{"npm"}, "enabled": false},
		map[string]any{"matchDatasources": []any{"docker"}, "enabled": false},
	}}}, preset.Builtin()}
	layer, err := config.Parse([]byte(`{"extends": ["p:two"], "packageRules": [{"matchManagers": ["a"]}, {"matchManagers": ["b"]}, {"matchManagers": ["c"]}]}`), "x.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := Load(layer, src, versioning.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	if got := in.base("packageRules"); got != 2 {
		t.Fatalf("base = %d, want 2", got)
	}
	if i, ok := in.ownIndex("packageRules", 3); !ok || i != 1 {
		t.Errorf("resolved 3 -> own %d, %v; want 1, true", i, ok)
	}
	if _, ok := in.ownIndex("packageRules", 1); ok {
		t.Error("resolved 1 is the preset's, not the file's")
	}
	if fp, ok := in.filePointer("/packageRules/4/matchManagers"); !ok || fp != "/packageRules/2/matchManagers" {
		t.Errorf("filePointer = %q, %v", fp, ok)
	}
	if _, ok := in.filePointer("/packageRules/0"); ok {
		t.Error("a preset's rule has a file pointer")
	}
	if _, ok := in.filePointer("/schedule"); ok {
		t.Error("a key the file does not set has a file pointer")
	}
}

// The report order is a function of the findings, not of the checks' order.
func TestSortIsStable(t *testing.T) {
	in := load(t, `{"automerge": true, "nonesuchKey": 1, "ignorePaths": ["a", "a"], "prHourlyLimit": 0}`)
	want, _ := Run(in, Catalogue())
	shuffled := slices.Clone(Catalogue())
	rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	got, _ := Run(in, shuffled)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order depends on the catalogue's order:\n%s\nvs\n%s", describe(got), describe(want))
	}
	if len(want) < 4 || want[0].Severity != Error {
		t.Errorf("errors first, got %s", describe(want))
	}
}
