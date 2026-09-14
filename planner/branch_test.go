// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package planner

import (
	"testing"
	"time"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/versioning"
)

// resolvedConfig is the estate configuration resolved over the captured
// defaults: every template a branch name and a commit message come from.
func resolvedConfig(t *testing.T) map[string]any {
	t.Helper()
	r, _, err := config.ResolveFile(fixture.Config(t), preset.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	return r.Raw
}

// with returns the configuration with the keys a matching rule would have
// set. The rule is named at each call site.
func with(base map[string]any, kv map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(kv))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range kv {
		out[k] = v
	}
	return out
}

func upd(manager, ds, dep, current, value, version string, t model.UpdateType) model.Update {
	return model.Update{
		DepKey: dep + "|" + current,
		Dep: model.Dependency{
			Manager: manager, Datasource: ds, DepName: dep, PackageName: dep, CurrentValue: current,
			CustomManager: model.NoCustomManager, Versioning: "semver",
		},
		NewValue: value, NewVersion: version, Type: t,
	}
}

// Every case is a branch open on the estate on 2026-09-11
// (testdata/<root>/live/renovate-branches.json), with the title Renovate
// gave it.
func TestBranchNamesAndTitlesMatchTheEstate(t *testing.T) {
	base := resolvedConfig(t)
	vs := versioning.Registry{"semver": semverForTest{}}
	cases := []struct {
		name   string
		u      model.Update
		cfg    map[string]any
		branch string
		title  string
	}{
		{
			name:   "docker tag minor via gitlabci",
			u:      upd("gitlabci", "docker", "registry.ole-hartwig.eu/devops/images/code-signing", "1.2.0", "1.3.10", "1.3.10", model.UpdateMinor),
			cfg:    base,
			branch: "renovate/registry.ole-hartwig.eu-devops-images-code-signing-1.x",
			title:  "chore(deps): update registry.ole-hartwig.eu/devops/images/code-signing docker tag to v1.3.10",
		},
		{
			// The live list has no docker update that is provably a major
			// (a "-2.x" branch titled "to v2.4.0" reads as a minor within
			// 2.x); the major form stays unmeasured.
			name:   "docker tag minor via kustomize",
			u:      upd("kustomize", "docker", "quay.io/argoproj/argocd", "3.4.0", "3.5.2", "3.5.2", model.UpdateMinor),
			cfg:    base,
			branch: "renovate/quay.io-argoproj-argocd-3.x",
			title:  "chore(deps): update quay.io/argoproj/argocd docker tag to v3.5.2",
		},
		{
			name: "composer major in a group of one", // rules 20 (TYPO3 Core) and 49 (require -> fix)
			u:    upd("composer", "packagist", "typo3/cms-core", "^13.4", "^14.0", "14.0.0", model.UpdateMajor),
			cfg:  with(base, map[string]any{"groupName": "TYPO3 Core", "semanticCommitType": "fix"}),
			// A group of one is titled as its update; the branch is the group's.
			branch: "renovate/major-typo3-core",
			title:  "fix(deps): update dependency typo3/cms-core to v14",
		},
		{
			name:   "composer range, not a single version", // rule 49
			u:      upd("composer", "packagist", "php", "^8.4", "^8.5.10", "8.5.10", model.UpdateMinor),
			cfg:    with(base, map[string]any{"semanticCommitType": "fix"}),
			branch: "renovate/php-8.x",
			title:  "fix(deps): update dependency php to ^8.5.10",
		},
		{
			name:   "npm major",
			u:      upd("npm", "npm", "gulp", "^4.0.0", "^5.0.0", "5.0.0", model.UpdateMajor),
			cfg:    base,
			branch: "renovate/gulp-5.x",
			title:  "chore(deps): update dependency gulp to v5",
		},
		{
			name:   "go module minor",
			u:      upd("gomod", "go", "github.com/aws/aws-sdk-go-v2", "v1.46.0", "v1.47.0", "v1.47.0", model.UpdateMinor),
			cfg:    with(base, map[string]any{"groupName": "aws-sdk-go-v2 monorepo", "groupSlug": "aws-sdk-go-v2-monorepo", "semanticCommitType": "fix"}),
			branch: "renovate/aws-sdk-go-v2-monorepo",
			title:  "fix(deps): update module github.com/aws/aws-sdk-go-v2 to v1.47.0",
		},
		{
			name: "digest",
			u: model.Update{
				DepKey: "mobile", Type: model.UpdateDigest, NewDigest: "sha256:8b95e45abcdef0123456789",
				Dep: model.Dependency{Manager: "gomod", Datasource: "go", DepName: "golang.org/x/mobile", CustomManager: model.NoCustomManager},
			},
			cfg:    with(base, map[string]any{"semanticCommitType": "fix"}),
			branch: "renovate/golang.org-x-mobile-digest",
			title:  "fix(deps): update golang.org/x/mobile digest to 8b95e45",
		},
		{
			// Measured live: "renovate/registry.ole-hartwig.eu-devops-images-python-3.14",
			// "chore(deps): update registry.ole-hartwig.eu/devops/images/python:3.14 docker digest to 0c9dec5".
			name: "docker digest keeps the tag in branch and title",
			u: model.Update{
				DepKey: "python", Type: model.UpdateDigest, NewValue: "3.14", NewVersion: "3.14", NewDigest: "sha256:0c9dec5fffffffffffff",
				Dep: model.Dependency{Manager: "gitlabci", Datasource: "docker", DepName: "registry.ole-hartwig.eu/devops/images/python", CurrentValue: "3.14", CurrentDigest: "sha256:aaaa", CustomManager: model.NoCustomManager},
			},
			cfg:    base,
			branch: "renovate/registry.ole-hartwig.eu-devops-images-python-3.14",
			title:  "chore(deps): update registry.ole-hartwig.eu/devops/images/python:3.14 docker digest to 0c9dec5",
		},
		{
			// Measured live: "renovate/registry.ole-hartwig.eu-devops-ci-mirrors-wolfi-base-latest",
			// "... update registry.ole-hartwig.eu/devops/ci-mirrors/wolfi-base:latest docker digest to 65e1acb".
			name: "docker digest on latest",
			u: model.Update{
				DepKey: "wolfi", Type: model.UpdateDigest, NewValue: "latest", NewVersion: "latest", NewDigest: "sha256:65e1acbfffffffffffff",
				Dep: model.Dependency{Manager: "dockerfile", Datasource: "docker", DepName: "registry.ole-hartwig.eu/devops/ci-mirrors/wolfi-base", CurrentValue: "latest", CurrentDigest: "sha256:aaaa", CustomManager: model.NoCustomManager},
			},
			cfg:    base,
			branch: "renovate/registry.ole-hartwig.eu-devops-ci-mirrors-wolfi-base-latest",
			title:  "chore(deps): update registry.ole-hartwig.eu/devops/ci-mirrors/wolfi-base:latest docker digest to 65e1acb",
		},
		{
			name: "pin digest joins the pin group",
			u: model.Update{
				DepKey: "python", Type: model.UpdatePinDigest, NewDigest: "sha256:c6ead21fffffffffffff",
				Dep: model.Dependency{Manager: "dockerfile", Datasource: "docker", DepName: "docker.io/library/python", CurrentValue: "3.13", CustomManager: model.NoCustomManager},
			},
			cfg:    base,
			branch: "renovate/pin-dependencies",
			title:  "chore(deps): pin docker.io/library/python docker tag to c6ead21",
		},
		{
			name:   "lock file maintenance",
			u:      model.Update{DepKey: "lock", Type: model.UpdateLockFileMaintenance, Dep: model.Dependency{Manager: "composer", CustomManager: model.NoCustomManager}},
			cfg:    base,
			branch: "renovate/lock-file-maintenance",
			title:  "chore(deps): refresh lock file (transitive deps, e.g. guzzle)",
		},
	}
	for _, c := range cases {
		n, err := Name(c.u, c.cfg, vs)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if n.Branch != c.branch {
			t.Errorf("%s: branch %q, want %q", c.name, n.Branch, c.branch)
		}
		branches, err := Compose([]Named{n})
		if err != nil {
			t.Fatal(err)
		}
		if len(branches) != 1 || branches[0].Title != c.title {
			t.Errorf("%s: title %q, want %q", c.name, branches[0].Title, c.title)
		}
	}
}

// A group of several is titled as the group: "update ci components" when
// the members move to different versions, "update hocuspocus packages to
// v4" when they agree - and a group's majors travel on a major- branch.
func TestGroupTitles(t *testing.T) {
	base := resolvedConfig(t)
	vs := versioning.Registry{"semver": semverForTest{}}
	ci := with(base, map[string]any{"groupName": "CI components", "groupSlug": "ci-components"}) // rule 26
	a, _ := Name(upd("gitlabci", "gitlab-tags", "devops/ci-cd-components/lint-tools", "1", "1.34.0", "1.34.0", model.UpdateMinor), ci, vs)
	b, _ := Name(upd("gitlabci", "gitlab-tags", "devops/ci-cd-components/release-tools", "1", "1.21.0", "1.21.0", model.UpdateMinor), ci, vs)
	branches, err := Compose([]Named{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0].Name != "renovate/ci-components" || branches[0].Title != "chore(deps): update ci components" {
		t.Errorf("ci components group: %+v", branches)
	}
	if len(branches[0].UpdateKeys) != 2 || branches[0].GroupName != "CI components" {
		t.Errorf("group members %+v", branches[0])
	}

	hp := with(base, map[string]any{"groupName": "hocuspocus packages", "semanticCommitType": "fix"})
	c, _ := Name(upd("npm", "npm", "@hocuspocus/server", "^3.0.0", "^4.0.0", "4.0.0", model.UpdateMajor), hp, vs)
	d, _ := Name(upd("npm", "npm", "@hocuspocus/provider", "^3.0.0", "^4.0.0", "4.0.0", model.UpdateMajor), hp, vs)
	branches, _ = Compose([]Named{c, d})
	if len(branches) != 1 || branches[0].Name != "renovate/major-hocuspocus-packages" || branches[0].Title != "fix(deps): update hocuspocus packages to v4" {
		t.Errorf("hocuspocus major group: %+v", branches)
	}

	// A held update makes a branch marked with its reason and no edits -
	// the comparator sees what would have been pushed; the runner skips it.
	held := a
	held.Update.Blocks = []model.Block{{Reason: model.BlockSchedule}}
	branches, _ = Compose([]Named{held})
	if len(branches) != 1 || branches[0].SuppressedBy != model.BlockSchedule || len(branches[0].Edits) != 0 {
		t.Errorf("a held update must produce a suppressed branch, got %+v", branches)
	}
	// Held by its own age and actionable on one branch: the branch is
	// actionable and carries the actionable update only. (A schedule
	// would be the branch's own - TestABranchLevelHoldOnOneMemberHoldsTheGroup.)
	aged := a
	aged.Update.Blocks = []model.Block{{Reason: model.BlockMinimumReleaseAge}}
	branches, _ = Compose([]Named{aged, b})
	if len(branches) != 1 || branches[0].SuppressedBy != "" || len(branches[0].UpdateKeys) != 1 {
		t.Errorf("mixed branch: %+v", branches)
	}
}

func TestSanitizeAndSlug(t *testing.T) {
	for in, want := range map[string]string{
		"golang.org/x/mobile": "golang.org-x-mobile", "@scope/pkg": "scope-pkg",
		"https://github.com/crowdsecurity/hub":                   "https-github.com-crowdsecurity-hub",
		"registry.ole-hartwig.eu/devops/images/franken-php/base": "registry.ole-hartwig.eu-devops-images-franken-php-base",
	} {
		if got := sanitizeDepName(in); got != want {
			t.Errorf("sanitize %q = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Pin Dependencies": "pin-dependencies", "hocuspocus packages": "hocuspocus-packages", "TYPO3 Core": "typo3-core", "first-party composer packages": "first-party-composer-packages"} {
		if got := Slugify(in); got != want {
			t.Errorf("slug %q = %q, want %q", in, got, want)
		}
	}
	if got := cleanBranchName("renovate/a b/-c-"); got != "renovate/a-b/c" {
		t.Errorf("clean = %q", got)
	}
}

// semverForTest reads major/minor off a dotted version, with or without a
// leading v or a range operator; the branch templates need nothing more.
type semverForTest struct{ testScheme }

func (semverForTest) Major(s string) (int, bool) { return part(s, 0) }
func (semverForTest) Minor(s string) (int, bool) { return part(s, 1) }

func part(s string, i int) (int, bool) {
	s = trimRangeOp(s)
	parts := splitDots(s)
	if len(parts) <= i {
		return 0, false
	}
	n := 0
	for _, r := range parts[i] {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func trimRangeOp(s string) string {
	for len(s) > 0 && (s[0] == '^' || s[0] == '~' || s[0] == 'v' || s[0] == '>' || s[0] == '=') {
		s = s[1:]
	}
	return s
}

func splitDots(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '.' || r == '-' {
			out = append(out, cur)
			if r == '-' {
				return out
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

// One dependency read by two managers is one update for the title.
func TestGroupOfOneDependencyReadTwiceIsTitledAsOne(t *testing.T) {
	base := resolvedConfig(t)
	vs := versioning.Registry{"semver": semverForTest{}, "loose": semverForTest{}}
	ci := with(base, map[string]any{"groupName": "CI components", "groupSlug": "ci-components"})
	a, _ := Name(upd("gitlabci", "gitlab-tags", "devops/ci-cd-components/deploy-tools", "1.2.0", "1.3.0", "1.3.0", model.UpdateMinor), ci, vs)
	b, _ := Name(upd("regex", "gitlab-releases", "devops/ci-cd-components/deploy-tools", "1.2.0", "1.3.0", "1.3.0", model.UpdateMinor), ci, vs)
	branches, _ := Compose([]Named{a, b})
	if len(branches) != 1 || branches[0].Title != "chore(deps): update dependency devops/ci-cd-components/deploy-tools to v1.3.0" {
		t.Errorf("got %+v", branches)
	}
}

// A schedule or a dashboard approval is the branch's, not the member's:
// a group with one member behind a window is held whole (measured: a "pin
// dependencies" group of four images awaiting the throttle two of them
// carry). A release age is the member's own; that member stays off an
// actionable group.
func TestABranchLevelHoldOnOneMemberHoldsTheGroup(t *testing.T) {
	base := resolvedConfig(t)
	vs := versioning.Registry{"semver": semverForTest{}}
	ci := with(base, map[string]any{"groupName": "CI components", "groupSlug": "ci-components"})
	a, _ := Name(upd("gitlabci", "gitlab-tags", "devops/ci-cd-components/lint-tools", "1", "1.34.0", "1.34.0", model.UpdateMinor), ci, vs)
	b, _ := Name(upd("gitlabci", "gitlab-tags", "devops/ci-cd-components/release-tools", "1", "1.21.0", "1.21.0", model.UpdateMinor), ci, vs)
	b.Update.Blocks = []model.Block{{Reason: model.BlockSchedule, Until: now.Add(time.Hour)}}
	b.Update.SuppressedBy = model.BlockSchedule
	branches, err := Compose([]Named{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0].SuppressedBy != model.BlockSchedule || branches[0].HeldWith.Reason != model.BlockSchedule || len(branches[0].UpdateKeys) != 2 {
		t.Errorf("a scheduled member must hold the whole group: %+v", branches)
	}

	c := b
	c.Update.Blocks = []model.Block{{Reason: model.BlockMinimumReleaseAge}}
	c.Update.SuppressedBy = model.BlockMinimumReleaseAge
	branches, _ = Compose([]Named{a, c})
	if len(branches) != 1 || branches[0].SuppressedBy != "" || len(branches[0].UpdateKeys) != 1 {
		t.Errorf("an aged member stays off an actionable group: %+v", branches)
	}
}

// prBodyNotes are what a person wrote next to the rule for whoever reads
// the merge request: rendered as templates, and said once on a group,
// whichever members carry them. Measured in koh-infra's renovate.json.
func TestPrBodyNotesRenderOnceOntoTheBranch(t *testing.T) {
	base := resolvedConfig(t)
	vs := versioning.Registry{"semver": semverForTest{}}
	notes := []any{"**Raise `{{depName}}` in the same window.**", "Rollback is the previous pin."}
	cfg := with(base, map[string]any{"groupName": "k3s", "groupSlug": "k3s", "prBodyNotes": notes})
	a, err := Name(upd("regex", "github-releases", "k3s-io/k3s", "1.30.0", "1.31.0", "1.31.0", model.UpdateMinor), cfg, vs)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Name(upd("regex", "github-releases", "k3s-io/k3s", "1.30.0", "1.31.0", "1.31.0", model.UpdateMinor), with(cfg, map[string]any{"prBodyNotes": []any{"Rollback is the previous pin."}}), vs)
	if len(a.Notes) != 2 || a.Notes[0] != "**Raise `k3s-io/k3s` in the same window.**" {
		t.Fatalf("notes = %q", a.Notes)
	}
	branches, err := Compose([]Named{a, b})
	if err != nil {
		t.Fatal(err)
	}
	want := "**Raise `k3s-io/k3s` in the same window.**\n\nRollback is the previous pin."
	if len(branches) != 1 || branches[0].Body != want {
		t.Errorf("body = %q, want %q", branches[0].Body, want)
	}
	if _, err := Name(a.Update, with(cfg, map[string]any{"prBodyNotes": []any{"{{#if}}"}}), vs); err == nil {
		t.Error("a note that does not render must fail the naming, not vanish")
	}
}

// A branch that touches pipeline files alone is a ci commit, not a chore:
// the image is the same, and a chore would tag it again for nothing. One
// member outside the pipeline keeps the chore; a type a rule set stands.
func TestPipelineOnlyBranchesAreCiCommits(t *testing.T) {
	ci := func(file string) Named {
		return Named{Update: model.Update{Dep: model.Dependency{File: file}}}
	}
	for _, tc := range []struct {
		title   string
		members []Named
		want    string
	}{
		{"chore(deps): update ci components", []Named{ci(".gitlab-ci.yml"), ci(".gitlab/ci/build.yml")}, "ci(deps): update ci components"},
		{"chore: pin dependencies", []Named{ci("sub/.gitlab-ci.yml")}, "ci: pin dependencies"},
		{"chore(deps): pin dependencies", []Named{ci(".gitlab-ci.yml"), ci("Containerfile")}, "chore(deps): pin dependencies"},
		{"fix(deps): update dependency x to v2 [security]", []Named{ci(".gitlab-ci.yml")}, "fix(deps): update dependency x to v2 [security]"},
		{"chore(deps): update x", []Named{ci("templates/build.yml")}, "chore(deps): update x"},
		{"update x", []Named{ci(".gitlab-ci.yml")}, "update x"},
	} {
		if got := ciCommitType(tc.title, tc.members); got != tc.want {
			t.Errorf("%q over %d members: got %q, want %q", tc.title, len(tc.members), got, tc.want)
		}
	}
	// The runner asks again with what a branch actually commits: a task
	// that wrote outside the pipeline turns a ci title back into a chore.
	if got := CommitTypeFor("ci(deps): update x", []string{".gitlab-ci.yml", "recipe.yaml"}); got != "chore(deps): update x" {
		t.Errorf("a task's file outside the pipeline: %q", got)
	}
	if got := CommitTypeFor("ci(deps): update x", []string{".gitlab-ci.yml"}); got != "ci(deps): update x" {
		t.Errorf("pipeline only stays ci: %q", got)
	}
	// The platforms' own pipeline locations count; a product file does not.
	for _, f := range []string{".github/workflows/ci.yml", ".github/actions/setup/action.yml", ".forgejo/workflows/test.yml",
		".gitea/workflows/test.yml", ".woodpecker.yml", ".woodpecker/build.yml", ".drone.yml", ".circleci/config.yml",
		".buildkite/pipeline.yml", "azure-pipelines.yml", "bitbucket-pipelines.yml", ".travis.yml", "Jenkinsfile", "sub/.gitlab-ci.yml", "app/.github/workflows/x.yml"} {
		if !IsPipelineFile(f) {
			t.Errorf("%s is a pipeline file", f)
		}
	}
	for _, f := range []string{"Containerfile", "compose.yaml", "templates/build.yml", ".github/CODEOWNERS", ".github/ISSUE_TEMPLATE/bug.md", "charts/x/values.yaml", "gitlab-ci.yml"} {
		if IsPipelineFile(f) {
			t.Errorf("%s is not a pipeline file", f)
		}
	}
}
