// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
)

var now = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

func dep(ds, name string) model.Dependency {
	return model.Dependency{Datasource: ds, DepName: name, PackageName: name, CustomManager: model.NoCustomManager}
}

func TestIndexRecordsAndAnswersEveryShapeTheEstateUses(t *testing.T) {
	x := NewIndex()
	x.Record("devops/images/ci-tools", &model.Plan{Deps: []model.Dependency{
		dep("gitlab-tags", "devops/ci-cd-components/lint-tools"),
		dep("docker", "registry.ole-hartwig.eu/devops/images/golang"),
	}}, now)
	x.Record("devops/koh-gitops", &model.Plan{Deps: []model.Dependency{
		dep("docker", "registry.ole-hartwig.eu/ai-ready-platform/platform/commerce/sources"),
		dep("docker", "registry.ole-hartwig.eu/ai-ready-platform/platform/deeper/than/one"),
	}}, now)
	x.Record("development/moselwal/moselwal-websites", &model.Plan{Deps: []model.Dependency{
		{Datasource: "gitlab-packages", DepName: "moselwal/dev", PackageName: "development/moselwal/dev:moselwal/dev", CustomManager: 10},
		dep("gitlab-tags", "devops/ci-cd-components/lint-tools"),
		{Datasource: "docker", DepName: "registry.ole-hartwig.eu/devops/images/golang", CustomManager: model.NoCustomManager, SkipReason: "disabled by packageRules[770]"},
	}}, now)

	for path, want := range map[string]string{
		"devops/ci-cd-components/lint-tools": "development/moselwal/moselwal-websites,devops/images/ci-tools",
		"devops/images/golang":               "development/moselwal/moselwal-websites,devops/images/ci-tools", // held deps count
		"development/moselwal/dev":           "development/moselwal/moselwal-websites",
		"devops/images/nothing":              "",
		// a nested registry repository of the project, one segment deep
		"ai-ready-platform/platform/commerce": "devops/koh-gitops",
		"ai-ready-platform/platform":          "", // a group is not a project
	} {
		if got := strings.Join(x.ConsumersOf(path), ","); got != want {
			t.Errorf("ConsumersOf(%q) = %q, want %q", path, got, want)
		}
	}

	// Re-recording a repository replaces its entries: the dependency it
	// dropped no longer lists it.
	x.Record("devops/images/ci-tools", &model.Plan{Deps: []model.Dependency{dep("gitlab-tags", "devops/ci-cd-components/lint-tools")}}, now.Add(time.Hour))
	if got := strings.Join(x.ConsumersOf("devops/images/golang"), ","); got != "development/moselwal/moselwal-websites" {
		t.Errorf("after re-record: %q", got)
	}
	if x.Repositories["devops/images/ci-tools"] != now.Add(time.Hour) {
		t.Error("the repository's index time was not updated")
	}
	// The repository's dependency list is replaced whole, with the
	// version the watch will ask about: the locked one where there is one.
	x.Record("devops/images/ci-tools", &model.Plan{Deps: []model.Dependency{
		{Datasource: "npm", DepName: "lodash", CurrentValue: "^4.17.0", LockedVersion: "4.17.20", Versioning: "npm", File: "package.json", CustomManager: model.NoCustomManager},
		{Datasource: "npm", DepName: "lodash", CurrentValue: "^4.17.0", LockedVersion: "4.17.20", Versioning: "npm", File: "package.json", CustomManager: model.NoCustomManager},
		dep("docker", "registry.ole-hartwig.eu/devops/images/golang"),
	}}, now.Add(2*time.Hour))
	got := x.Dependencies["devops/images/ci-tools"]
	if len(got) != 2 || got[1].PackageName != "lodash" || got[1].Version != "4.17.20" || got[1].Versioning != "npm" {
		t.Errorf("dependencies = %+v", got)
	}
	if _, ok := x.Dependencies["development/moselwal/moselwal-websites"]; !ok {
		t.Error("another repository's dependencies were dropped")
	}
}

func TestIndexRoundTripsAndAMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consumers.json")
	empty, err := LoadIndex(path)
	if err != nil || len(empty.Consumers) != 0 {
		t.Fatalf("missing file: %v %v", empty, err)
	}
	x := NewIndex()
	x.Record("a/b", &model.Plan{Deps: []model.Dependency{dep("docker", "x/y")}}, now)
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	y, err := LoadIndex(path)
	if err != nil || strings.Join(y.ConsumersOf("x/y"), ",") != "a/b" {
		t.Fatalf("round trip: %v %v", y, err)
	}
}

// TestIndexesMergeByWhoRecordedARepositoryLast: the partitions each write
// their own index. Merged, a repository comes from the index that recorded
// it most recently - its dependencies and its consumer entries - and one
// that only an older index knows is kept, never dropped.
func TestIndexesMergeByWhoRecordedARepositoryLast(t *testing.T) {
	dir := t.TempDir()
	older, newer := now, now.Add(time.Hour)

	a := NewIndex()
	a.Record("group/app", &model.Plan{Deps: []model.Dependency{
		{Datasource: "npm", DepName: "left-pad", PackageName: "left-pad", CurrentValue: "1.0.0", CustomManager: model.NoCustomManager},
	}}, older)
	a.Record("group/lib", &model.Plan{Deps: []model.Dependency{dep("docker", "x/y")}}, newer)
	if err := a.Save(filepath.Join(dir, "partition-a.json")); err != nil {
		t.Fatal(err)
	}
	b := NewIndex()
	b.Record("group/app", &model.Plan{Deps: []model.Dependency{
		{Datasource: "npm", DepName: "left-pad", PackageName: "left-pad", CurrentValue: "1.3.0", CustomManager: model.NoCustomManager},
		dep("docker", "x/y"),
	}}, newer)
	b.Record("group/lib", &model.Plan{Deps: []model.Dependency{dep("docker", "x/z")}}, older)
	b.Record("pinup/shadow-fixture", &model.Plan{Deps: []model.Dependency{dep("docker", "x/y")}}, older)
	if err := b.Save(filepath.Join(dir, "partition-b.json")); err != nil {
		t.Fatal(err)
	}

	for _, patterns := range [][]string{
		{filepath.Join(dir, "partition-*.json")},
		{filepath.Join(dir, "partition-b.json"), filepath.Join(dir, "partition-a.json")},
		{filepath.Join(dir, "partition-a.json"), filepath.Join(dir, "partition-b.json"), filepath.Join(dir, "absent.json")},
	} {
		m, err := LoadIndexes(patterns...)
		if err != nil {
			t.Fatalf("%v: %v", patterns, err)
		}
		if got := m.Dependencies["group/app"]; len(got) != 2 || got[0].Version != "1.3.0" && got[1].Version != "1.3.0" {
			t.Errorf("%v: group/app must come from b, the later record: %+v", patterns, got)
		}
		if got := m.Dependencies["group/lib"]; len(got) != 1 || got[0].PackageName != "x/y" {
			t.Errorf("%v: group/lib must come from a, the later record: %+v", patterns, got)
		}
		if got := strings.Join(m.ConsumersOf("x/y"), ","); got != "group/app,group/lib,pinup/shadow-fixture" {
			t.Errorf("%v: consumers of x/y: %q", patterns, got)
		}
		if len(m.ConsumersOf("x/z")) != 0 {
			t.Errorf("%v: b's stale group/lib entry survived the merge", patterns)
		}
		if !m.Repositories["group/app"].Equal(newer) || !m.Repositories["pinup/shadow-fixture"].Equal(older) || !m.GeneratedAt.Equal(newer) {
			t.Errorf("%v: timestamps %v %v", patterns, m.Repositories, m.GeneratedAt)
		}
	}
	for value, pattern := range map[string]bool{"one.json": false, "a,b": true, "idx/*.json": true} {
		if IsPattern(value) != pattern {
			t.Errorf("IsPattern(%q) = %v", value, !pattern)
		}
	}
}
