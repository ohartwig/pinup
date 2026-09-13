// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
)

// The overview folds every plan into one table per datasource: a version
// in use lists its repositories, a proposal its target, a hold its reason,
// and the header counts add up.
func TestEstateFoldsPlansByDependency(t *testing.T) {
	dep := func(repo, name, current string) (model.Plan, model.Dependency) {
		d := model.Dependency{Manager: "gitlabci", File: ".gitlab-ci.yml", DepName: name, Datasource: "docker", CurrentValue: current, CustomManager: model.NoCustomManager}
		return model.Plan{Repo: model.RepoRef{Path: repo}, Deps: []model.Dependency{d}}, d
	}
	a, da := dep("g/a", "registry/python", "3.13")
	b, _ := dep("g/b", "registry/python", "3.13")
	c, dc := dep("g/c", "registry/python", "3.14")
	a.Updates = []model.Update{{DepKey: da.Key(), Dep: da, NewValue: "3.14", NewVersion: "3.14", Type: model.UpdateMinor}}
	b.Updates = []model.Update{{DepKey: da.Key(), Dep: da, NewValue: "3.14", NewVersion: "3.14", Type: model.UpdateMinor, Blocks: []model.Block{{Reason: model.BlockSchedule}}, SuppressedBy: model.BlockSchedule}}
	_ = dc
	e := EstateOf([]*model.Plan{&a, &b, &c})
	if e.Repos != 3 || e.Deps != 1 || e.Uses != 3 || e.Current != 1 || e.Behind != 1 || e.Held != 1 {
		t.Errorf("counts: %+v", e)
	}
	list := e.ByDatasource["docker"]
	if len(list) != 1 || len(list[0].Uses) != 2 {
		t.Fatalf("docker: %+v", list)
	}
	u313 := list[0].Uses[0]
	if u313.Current != "3.13" || len(u313.Repos) != 2 || u313.Repos[0].Proposed != "3.14" || u313.Repos[1].Held != model.BlockSchedule {
		t.Errorf("3.13 use: %+v", u313)
	}
	md := EstateMarkdown(e, "test", time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC))
	for _, want := range []string{"| 3 | 1 | 3 | 1 | 1 | 1 | 0 |", "<details><summary>docker (1 dependencies, 2 versions in use)</summary>", "`registry/python` | `3.13` | g/a → `3.14`; g/b → `3.14` (schedule) |", "| `3.14` | g/c |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q\n%s", want, md)
		}
	}
}
