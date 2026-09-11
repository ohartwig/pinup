// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package planner

import (
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/config"
	"git.ole-hartwig.eu/pinup/pinup/config/preset"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// resolvedConfig is the estate configuration resolved over the captured
// defaults: every template a branch name and a commit message come from.
func resolvedConfig(t *testing.T) map[string]any {
	t.Helper()
	r, _, err := config.ResolveFile("../testdata/parity/config/default.json", preset.Builtin())
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
// (testdata/parity/live/renovate-branches.json), with the title Renovate
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

	// Held updates make no branch.
	held := a
	held.Update.Blocks = []model.Block{{Reason: model.BlockSchedule}}
	branches, _ = Compose([]Named{held})
	if len(branches) != 0 {
		t.Errorf("a held update must not produce a branch, got %+v", branches)
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
