// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

// D.12: every bare @N reference is classified, the ones with a newer major
// are notified, none is edited, and the denominator is the count found.
func TestRollingMajorsCountReferencesAndNotices(t *testing.T) {
	dep := func(name, cur string) model.Dependency {
		return model.Dependency{File: ".gitlab-ci.yml", DepName: name, CurrentValue: cur, CustomManager: model.NoCustomManager}
	}
	a := &model.Plan{Repo: model.RepoRef{Path: "devops/images/a"}, Deps: []model.Dependency{dep("devops/ci-cd-components/lint-tools", "1"), dep("devops/ci-cd-components/release-tools", "1"), dep("registry/x", "1.2.3")}}
	a.Updates = []model.Update{
		{DepKey: dep("devops/ci-cd-components/lint-tools", "1").Key(), Dep: a.Deps[0], NewValue: "2", NewVersion: "2.0.0", Type: model.UpdateMajorAvailable},
		{DepKey: dep("devops/ci-cd-components/lint-tools", "1").Key(), Dep: a.Deps[0], NewValue: "1.33.83", NewVersion: "1.33.83", Type: model.UpdateMinor},
	}
	b := &model.Plan{Repo: model.RepoRef{Path: "devops/images/b"}, Deps: []model.Dependency{dep("devops/ci-cd-components/lint-tools", "1")}}
	b.Updates = []model.Update{{DepKey: b.Deps[0].Key(), Dep: b.Deps[0], NewValue: "2", NewVersion: "2.1.0", Type: model.UpdateMajorAvailable}}

	refs, notices := RollingMajors([]*model.Plan{a, b})
	if len(refs) != 3 || notices != 2 {
		t.Fatalf("refs %d notices %d: %+v", len(refs), notices, refs)
	}
	body := RollingMajorIssue(refs, notices)
	for _, want := range []string{"3 across the estate", "the 2 that have", "## devops/ci-cd-components/lint-tools", "| devops/images/a |", "| devops/images/b |", "2.1.0", "1 references are on the newest major"} {
		if !strings.Contains(body, want) {
			t.Errorf("issue body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "release-tools") && !strings.Contains(body, "on the newest major") {
		t.Errorf("a current reference must not be listed as a notice")
	}
	if refs, notices := RollingMajors(nil); len(refs) != 0 || notices != 0 {
		t.Errorf("no plans: %v %d", refs, notices)
	}
}
