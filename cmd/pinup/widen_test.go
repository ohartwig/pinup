// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/git"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/publish"
)

// groupFixture is the case of 2026-10-03, end to end and without a network:
// two first-party packages in one group, an unrelated image beside them,
// and an open group request carrying one member's update - from, the
// version on main, replaced by to on the request's branch. It returns the
// checkout, the remote and the fake platform.
func groupFixture(t *testing.T, from, to string) (work, remote string, pf *platformfake.Platform) {
	t.Helper()
	if err := git.Available(); err != nil {
		t.Skip(err)
	}
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
		"GIT_AUTHOR_NAME": "fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
		"GIT_COMMITTER_NAME": "fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid",
		"GIT_AUTHOR_DATE": "2026-10-03T17:25:00Z", "GIT_COMMITTER_DATE": "2026-10-03T17:25:00Z",
	} {
		t.Setenv(k, v)
	}
	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	mustGit(root, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	mustGit(root, "init", "--quiet", "--initial-branch=main", seed)
	containerfile := "FROM registry.example.org/moselwal/ole-hartwig-blog:3.27.0 AS blog\n" +
		"FROM registry.example.org/moselwal/only-ole-site:3.7.4 AS site\n" +
		"FROM alpine:3.20\n"
	os.WriteFile(filepath.Join(seed, "Containerfile"), []byte(containerfile), 0o644)
	mustGit(seed, "add", "Containerfile")
	mustGit(seed, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "init")
	mustGit(seed, "push", "--quiet", remote, "main")
	// The group request as the full scan left it: one member's update.
	mustGit(seed, "checkout", "--quiet", "-b", "pinup/first-party-composer")
	os.WriteFile(filepath.Join(seed, "Containerfile"), []byte(strings.Replace(containerfile, from, to, 1)), 0o644)
	mustGit(seed, "-c", "commit.gpgsign=false", "commit", "--quiet", "-am", "update first-party composer packages")
	mustGit(seed, "push", "--quiet", remote, "pinup/first-party-composer")
	work = filepath.Join(root, "work")
	if _, err := git.Clone(context.Background(), remote, work, git.CloneOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	pf = &platformfake.Platform{Verification: "verified", MRs: []publish.MergeRequest{{
		IID: 942, SourceBranch: "pinup/first-party-composer", TargetBranch: "main", State: "opened",
		Title: "chore(deps): update first-party composer packages",
	}}}
	return work, remote, pf
}

// groupConfig groups the two first-party packages and nothing else, and
// writes no dashboard: the narrowed run leaves it alone anyway.
func groupConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "default.json")
	os.WriteFile(path, []byte(`{
  "branchPrefix": "pinup/",
  "pinDigests": false,
  "packageRules": [
    {"matchPackageNames": ["registry.example.org/moselwal/ole-hartwig-blog", "registry.example.org/moselwal/only-ole-site"],
     "groupName": "first-party composer packages", "groupSlug": "first-party-composer"}
  ]
}`), 0o644)
	return path
}

func groupRun(t *testing.T, pf *platformfake.Platform, released string, releases map[string][]string) *runOptions {
	return &runOptions{
		cfgPath: groupConfig(t), platform: pf, now: time.Date(2026, 10, 3, 17, 25, 0, 0, time.UTC),
		identity:    git.Identity{Name: "fixture", Email: "fixture@example.invalid"},
		released:    released,
		datasources: lookup.Registry{"docker": cannedDocker{cannedDS{name: "docker", scheme: "docker", releases: releases}}},
	}
}

// The fast lane for one member of a group whose request is open writes the
// group branch with every member. Both directions were measured on
// 2026-10-03 in moselwal-websites: only-ole-site 3.7.5 was held behind the
// open request for ole-hartwig-blog 3.27.1 (!942), and an hour later
// ole-hartwig-blog 3.27.2 behind the open request for only-ole-site 3.7.6
// (!943); after each merge nothing ran until the four-hourly scan. The
// image outside the group stays the scheduled run's.
func TestNarrowedRunWidensToAnOpenGroup(t *testing.T) {
	for _, c := range []struct {
		name, released, from, to string
		releases                 map[string][]string
		want                     []string
	}{
		{"site released, blog open", "moselwal/only-ole-site", "ole-hartwig-blog:3.27.0", "ole-hartwig-blog:3.27.1",
			map[string][]string{"registry.example.org/moselwal/ole-hartwig-blog": {"3.27.0", "3.27.1"}, "registry.example.org/moselwal/only-ole-site": {"3.7.4", "3.7.5"}, "alpine": {"3.20", "3.21"}},
			[]string{"ole-hartwig-blog:3.27.1", "only-ole-site:3.7.5", "alpine:3.20\n"}},
		{"blog released, site open", "moselwal/ole-hartwig-blog", "only-ole-site:3.7.4", "only-ole-site:3.7.6",
			map[string][]string{"registry.example.org/moselwal/ole-hartwig-blog": {"3.27.0", "3.27.1", "3.27.2"}, "registry.example.org/moselwal/only-ole-site": {"3.7.4", "3.7.5", "3.7.6"}, "alpine": {"3.20", "3.21"}},
			[]string{"ole-hartwig-blog:3.27.2", "only-ole-site:3.7.6", "alpine:3.20\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			work, remote, pf := groupFixture(t, c.from, c.to)
			o := groupRun(t, pf, c.released, c.releases)
			report := filepath.Join(t.TempDir(), "plan.json")
			var out, errw strings.Builder
			if err := runProject(context.Background(), o, "", work, report, &out, &errw); err != nil {
				t.Fatalf("run: %v\n%s", err, errw.String())
			}
			got, err := exec.Command("git", "--git-dir", remote, "show", "pinup/first-party-composer:Containerfile").Output()
			if err != nil {
				t.Fatalf("the group branch: %v\n%s%s", err, out.String(), errw.String())
			}
			for _, want := range c.want {
				if !strings.Contains(string(got), want) {
					t.Errorf("the group branch lacks %q:\n%s\n%s%s", want, got, out.String(), errw.String())
				}
			}
			if len(pf.MRs) != 1 {
				t.Errorf("the open request is updated, not joined by another: %+v", pf.MRs)
			}
			raw, err := os.ReadFile(report)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := model.ReadPlan(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range plan.Deps {
				if d.DepName == "alpine" && !strings.HasPrefix(d.SkipReason, "not the released package") {
					t.Errorf("a dependency outside the group was planned: %+v", d)
				}
			}
			for _, b := range plan.Branches {
				if b.Name == "pinup/first-party-composer" && (b.SuppressedBy != "" || len(b.UpdateKeys) != 2) {
					t.Errorf("the group branch: %+v", b)
				}
			}
			if !strings.Contains(errw.String(), "widened to the open groups pinup/first-party-composer: 1 member updates, plan ") {
				t.Errorf("the widening is not reported:\n%s", errw.String())
			}
		})
	}
}

// A member that cannot be looked up keeps the hold: written from what the
// run knows, the branch would lose that member - the failure of 2026-09-26.
func TestNarrowedRunKeepsTheHoldWhenAMemberLookupFails(t *testing.T) {
	work, remote, pf := groupFixture(t, "ole-hartwig-blog:3.27.0", "ole-hartwig-blog:3.27.1")
	o := groupRun(t, pf, "moselwal/only-ole-site", map[string][]string{
		"registry.example.org/moselwal/only-ole-site": {"3.7.4", "3.7.5"},
		"alpine": {"3.20", "3.21"},
	})
	var out, errw strings.Builder
	if err := runProject(context.Background(), o, "", work, "", &out, &errw); err != nil {
		t.Fatalf("run: %v\n%s", err, errw.String())
	}
	got, err := exec.Command("git", "--git-dir", remote, "show", "pinup/first-party-composer:Containerfile").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "ole-hartwig-blog:3.27.1") || strings.Contains(string(got), "only-ole-site:3.7.5") {
		t.Errorf("the open group branch was rewritten:\n%s\n%s%s", got, out.String(), errw.String())
	}
	if !strings.Contains(errw.String(), "stay held (") || !strings.Contains(errw.String(), "a member's lookup failed") {
		t.Errorf("the kept hold is not reported:\n%s", errw.String())
	}
}

// A member's update that the rules name for another branch - its major,
// which separateMajorMinor puts on a branch of its own - is held: the
// widening plans the open branches whole and opens nothing else.
func TestNarrowedRunHoldsAMemberUpdateOutsideTheOpenBranch(t *testing.T) {
	work, _, pf := groupFixture(t, "ole-hartwig-blog:3.27.0", "ole-hartwig-blog:3.27.1")
	o := groupRun(t, pf, "moselwal/only-ole-site", map[string][]string{
		"registry.example.org/moselwal/ole-hartwig-blog": {"3.27.0", "3.27.1", "4.0.0"},
		"registry.example.org/moselwal/only-ole-site":    {"3.7.4", "3.7.5"},
		"alpine": {"3.20", "3.21"},
	})
	report := filepath.Join(t.TempDir(), "plan.json")
	var out, errw strings.Builder
	if err := runProject(context.Background(), o, "", work, report, &out, &errw); err != nil {
		t.Fatalf("run: %v\n%s", err, errw.String())
	}
	if len(pf.MRs) != 1 {
		t.Errorf("a branch outside the open group was opened: %+v\n%s", pf.MRs, out.String())
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := model.ReadPlan(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	held := false
	for _, u := range plan.Updates {
		if u.NewValue == "4.0.0" {
			held = u.SuppressedBy == model.BlockNarrowedRun
		}
	}
	if !held {
		t.Errorf("the member's major is not held as narrowedRun:\n%s", raw)
	}
}
