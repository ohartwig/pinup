// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

// budgetPlan is a plan shaped like devops/rpm-packages' on 2026-11-12: a
// handful of boxes and thousands of components shipped by builds, one of
// them vulnerable.
func budgetPlan(components, approvals int) *model.Plan {
	plan := &model.Plan{PinupVersion: "v0.0.0"}
	for i := range approvals {
		name := fmt.Sprintf("pinup/major-%03d", i)
		plan.Branches = append(plan.Branches, model.Branch{Name: name, Title: "chore(deps): update dependency " + name, SuppressedBy: model.BlockDashboardApproval})
	}
	plan.Branches = append(plan.Branches, model.Branch{Name: "pinup/young", Title: "chore(deps): update young", SuppressedBy: model.BlockMinimumReleaseAge})
	plan.Deps = append(plan.Deps, model.Dependency{Manager: "gomod", File: "go.mod", DepName: "golang.org/x/net", CurrentValue: "v0.40.0", CustomManager: model.NoCustomManager})
	for i := range components {
		file := fmt.Sprintf("specs/tool-%d/go.bump", i%40)
		reason := "shipped by the build of " + file + ", not written in it (artifactManifests[0]): watched for advisories, not updated on its own"
		d := model.Dependency{Manager: model.ArtifactManager, File: file, DepName: fmt.Sprintf("example.com/module/number-%05d", i), CurrentValue: "v1.2.3",
			CustomManager: model.NoCustomManager, Artifact: &model.ArtifactRef{}, SkipReason: reason, Disabled: reason}
		if i == 7 {
			d.SkipReason = "vulnerable (GHSA-xxxx-yyyy-zzzz): no fixed version; shipped by the build, nothing to raise it to yet"
		}
		plan.Deps = append(plan.Deps, d)
	}
	return plan
}

// The same reason on five thousand lines is said once per file: at full
// detail a component's line is its name and version, and a component with
// something to say keeps saying it.
func TestDashboardSaysTheComponentReasonOnce(t *testing.T) {
	body := Dashboard(budgetPlan(200, 1), nil, nil, time.Unix(0, 0))
	if n := strings.Count(body, "not written in it"); n != 0 {
		t.Errorf("the component reason is repeated on %d lines", n)
	}
	for _, want := range []string{
		"specs/tool-0/go.bump (5), shipped by the build: watched for advisories, not updated on their own",
		"`example.com/module/number-00199 v1.2.3`\n",
		"number-00007 v1.2.3` — vulnerable (GHSA-xxxx-yyyy-zzzz)",
		"`golang.org/x/net v0.40.0`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// Over the budget the dashboard gives up detail in steps, and never a box:
// the components without anything to say first, then every dependency
// line, and the boxes stay whole at every step.
func TestDashboardWithinBudgetKeepsTheBoxes(t *testing.T) {
	plan := budgetPlan(20000, 3)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	full := DashboardWithin(plan, nil, nil, now, DashboardBudget{})
	if full.Detail != DetailFull || full.Untrimmed != len(full.Body) {
		t.Fatalf("no budget renders in full: %v", full.Detail)
	}
	notable := DashboardWithin(plan, nil, nil, now, DashboardBudget{Bytes: len(full.Body) - 1, FullList: "the plan report `reports/x.json`"})
	counts := DashboardWithin(plan, nil, nil, now, DashboardBudget{Bytes: 4000, FullList: "the plan report `reports/x.json`"})
	for _, c := range []struct {
		f    Fitted
		want Detail
		has  []string
		not  []string
	}{
		{notable, DetailNotable,
			[]string{"components shipped by a build are listed only where there is something to say", "`reports/x.json`", "number-00007 v1.2.3` — vulnerable", " - … and 499 more, nothing to say about them", "`golang.org/x/net v0.40.0`"},
			[]string{"number-00008 "}},
		{counts, DetailCounts,
			[]string{"managers and files with their counts only", " - specs/tool-0/go.bump (500)\n", " - go.mod (1)\n"},
			[]string{"number-00007", "golang.org/x/net"}},
	} {
		if c.f.Detail != c.want {
			t.Errorf("detail %v, want %v (%d bytes)", c.f.Detail, c.want, len(c.f.Body))
			continue
		}
		if c.f.Untrimmed != len(full.Body) || len(c.f.Body) >= len(full.Body) {
			t.Errorf("%v: %d bytes of %d, untrimmed %d", c.want, len(c.f.Body), len(full.Body), c.f.Untrimmed)
		}
		for _, w := range c.has {
			if !strings.Contains(c.f.Body, w) {
				t.Errorf("%v: missing %q", c.want, w)
			}
		}
		for _, w := range c.not {
			if strings.Contains(c.f.Body, w) {
				t.Errorf("%v: still lists %q", c.want, w)
			}
		}
		for _, box := range []string{"<!-- approve-branch=pinup/major-000 -->", "<!-- approve-branch=pinup/major-002 -->", "<!-- approve-all-pending-prs -->", "<!-- approvePr-branch=pinup/young -->"} {
			if !strings.Contains(c.f.Body, box) {
				t.Errorf("%v: lost the box %s", c.want, box)
			}
		}
		// Deterministic: the issue does not churn between runs over one plan.
		if again := DashboardWithin(plan, nil, nil, now, DashboardBudget{Bytes: len(full.Body) - 1, FullList: "the plan report `reports/x.json`"}); c.want == DetailNotable && again.Body != c.f.Body {
			t.Error("two renders of one plan differ")
		}
	}
	if len(counts.Body) > 4000 {
		t.Errorf("counts body %d bytes over the budget", len(counts.Body))
	}
}

// When the boxes alone exceed the budget, the body is cut at a whole line
// under it, says so and where the rest is, and keeps its footer. A box cut
// in half would name a branch that does not exist.
func TestDashboardCutAtAWholeLine(t *testing.T) {
	plan := budgetPlan(10, 400)
	f := DashboardWithin(plan, nil, nil, time.Unix(0, 0), DashboardBudget{Bytes: 5000, FullList: "X"})
	if f.Detail != DetailCut || len(f.Body) > 5000 {
		t.Fatalf("detail %v, %d bytes", f.Detail, len(f.Body))
	}
	if !strings.Contains(f.Body, "cut short to stay within the platform's size limit") || !strings.Contains(f.Body, "are in X.") || !strings.HasSuffix(f.Body, "UTC*\n") {
		t.Errorf("no note or footer:\n%s", f.Body[len(f.Body)-400:])
	}
	if strings.Contains(f.Body, "## Detected dependencies") {
		t.Error("the detected dependencies survived a cut")
	}
	for line := range strings.SplitSeq(f.Body, "\n") {
		if strings.Contains(line, "<!-- approve-branch=") && !strings.Contains(line, "-->chore(deps): update dependency pinup/major-") {
			t.Errorf("a box cut in half: %q", line)
		}
	}
	for _, c := range []string{"<!-- approve-branch=pinup/major-000 -->"} {
		if !strings.Contains(f.Body, c) {
			t.Errorf("the first box is gone: %s", c)
		}
	}
}

// The armed mark sits on the open request's line, which the budget never
// condenses: a dashboard cut to counts still says which request merges on
// its own, and stays within the budget saying it.
func TestDashboardWithinBudgetKeepsTheArmedMark(t *testing.T) {
	plan := budgetPlan(20000, 3)
	plan.Branches = append(plan.Branches, model.Branch{Name: "pinup/lock-file-maintenance", Title: "chore(deps): refresh lock file"})
	open := []publish.MergeRequest{{IID: 248, SourceBranch: "pinup/lock-file-maintenance", State: "opened", Automerge: true}}
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	const budget = 4500
	f := DashboardWithin(plan, nil, open, now, DashboardBudget{Bytes: budget})
	if f.Detail != DetailCounts || len(f.Body) > budget {
		t.Fatalf("detail %v, %d bytes; want counts within %d", f.Detail, len(f.Body), budget)
	}
	if !strings.Contains(f.Body, "(!248) — merges automatically when green\n") {
		t.Errorf("the armed mark is gone at counts:\n%s", f.Body)
	}
}
