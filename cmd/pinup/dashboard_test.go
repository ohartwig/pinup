// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/platform/github"
	"github.com/ohartwig/pinup/platform/gitlab"
	"github.com/ohartwig/pinup/publish"
)

// A dashboard the platform refuses is not a warning line in a green job:
// it is an error line, a warning in the plan report, and exit status 3,
// which the runner's jobs show as passed with a warning. The project's
// other work stands - the refusal is counted, not returned.
func TestRefusedDashboardIsSeen(t *testing.T) {
	plan := &model.Plan{Dashboard: model.Dashboard{Enabled: true, Title: "pinup Dashboard"}}
	o := &runOptions{now: time.Now()}
	if err := dashboardFailures(o); err != nil {
		t.Fatalf("nothing failed yet: %v", err)
	}
	pf := &platformfake.Platform{IssueErr: errors.New(`gitlab: status 400 for project "group/app": {"description":["is too long (1.12 MiB). The maximum size is 1 MiB."]}`)}
	var errw strings.Builder
	writeDashboard(t.Context(), o, pf, publish.Project{Path: "group/app"}, plan, nil, "", "", io.Discard, &errw)
	if !strings.Contains(errw.String(), "error: group/app: dashboard not written: gitlab: status 400") {
		t.Errorf("log: %q", errw.String())
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Stage != "publish" || !strings.Contains(plan.Warnings[0].Msg, "is too long") {
		t.Errorf("plan warnings: %+v", plan.Warnings)
	}
	e, ok := errors.AsType[*exitError](dashboardFailures(o))
	if !ok || e.code != exitDashboard || exitDashboard != 3 {
		t.Fatalf("exit: %v", dashboardFailures(o))
	}
	// A written dashboard counts nothing.
	ok2 := &runOptions{now: time.Now()}
	writeDashboard(t.Context(), ok2, &platformfake.Platform{}, publish.Project{Path: "group/app"}, &model.Plan{Dashboard: plan.Dashboard}, nil, "", "", io.Discard, io.Discard)
	if err := dashboardFailures(ok2); err != nil {
		t.Errorf("a written dashboard failed the run: %v", err)
	}
}

// The budget sits under each platform's limit: GitLab's 1 MiB description,
// GitHub's 65536-character body.
func TestDashboardBudgetPerPlatform(t *testing.T) {
	if b := dashboardBudget(gitlab.Name); b >= 1<<20 || b < 800<<10 {
		t.Errorf("gitlab budget %d", b)
	}
	if b := dashboardBudget(github.Name); b >= 65536 || b < 50000 {
		t.Errorf("github budget %d", b)
	}
	if got := fullListAt("https://git.example/g/runner/-/jobs/7", "reports/%s.json", "devops/rpm-packages"); got != "the plan report `reports/devops-rpm-packages.json` in the [job's artifacts](https://git.example/g/runner/-/jobs/7/artifacts/browse)" {
		t.Errorf("pointer: %s", got)
	}
	if got := fullListAt("", "plan.json", ""); got != "the plan report `plan.json`" {
		t.Errorf("pointer outside CI: %s", got)
	}
}

// The dashboard written is the one within the budget, and the log says
// when it was condensed. GitHub's budget is the tight one.
func TestPublishedDashboardFitsTheBudget(t *testing.T) {
	plan := &model.Plan{Dashboard: model.Dashboard{Enabled: true, Title: "pinup Dashboard"}}
	for i := range 2000 {
		plan.Deps = append(plan.Deps, model.Dependency{Manager: "gomod", File: "go.mod", DepName: fmt.Sprintf("example.com/a/rather/long/module/path/number-%05d", i), CurrentValue: "v1.0.0", CustomManager: model.NoCustomManager})
	}
	pf := &githubFake{}
	var errw strings.Builder
	if err := publishDashboard(t.Context(), &runOptions{now: time.Now()}, pf, publish.Project{Path: "o/r"}, plan, nil, "", "", io.Discard, &errw); err != nil {
		t.Fatal(err)
	}
	body := pf.IssueBodies["pinup Dashboard"]
	if len(body) == 0 || len(body) > dashboardBudget(github.Name) {
		t.Errorf("body %d bytes", len(body))
	}
	if !strings.Contains(errw.String(), "over the budget of 60000; written with detected dependencies counts only") {
		t.Errorf("log: %q", errw.String())
	}
}

// githubFake is the fake platform under GitHub's name.
type githubFake struct{ platformfake.Platform }

func (*githubFake) Name() string { return github.Name }
