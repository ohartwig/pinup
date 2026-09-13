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
