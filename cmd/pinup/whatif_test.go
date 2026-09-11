// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/apply"
	"git.ole-hartwig.eu/pinup/pinup/cache"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// cannedDS answers lookups from a table and refuses everything else. The
// end-to-end test therefore touches no network at all: a dependency whose
// datasource or package is not in the table gets a lookup error, which the
// planner records as a skip reason, and the test can count those.
type cannedDS struct {
	name     string
	scheme   string
	releases map[string][]string
}

func (c cannedDS) Name() string              { return c.name }
func (c cannedDS) DefaultVersioning() string { return c.scheme }
func (c cannedDS) Releases(_ context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	vs, ok := c.releases[ref.PackageName]
	if !ok {
		return nil, fmt.Errorf("%s: no canned releases for %s", c.name, ref.PackageName)
	}
	rs := &model.ReleaseSet{PackageName: ref.PackageName, Datasource: c.name}
	for _, v := range vs {
		// Released a month before any plan time these tests use, so
		// minimumReleaseAge holds nothing and the policy under test is
		// the rules', not the clock's.
		rs.Releases = append(rs.Releases, model.Release{Version: v, Timestamp: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)})
	}
	return rs, nil
}

// canned is the lookup table for ci-tools: the component pins get a newer
// tag within their major and one beyond it, so both the "up to date within
// the rolling major" and the "a newer major exists" paths are exercised.
func canned() lookup.Registry {
	releases := map[string][]string{
		"devops/ci-cd-components/lint-tools":          {"1.33.59", "1.33.64", "2.0.0"},
		"devops/ci-cd-components/release-tools":       {"1.0.0", "1.2.0"},
		"devops/ci-cd-components/container-scanning":  {"3.0.0", "3.1.0"},
		"devops/ci-cd-components/supply-chain-verify": {"2.0.0", "2.4.1"},
	}
	// github-releases for the Containerfile's annotated tools: hadolint has
	// a patch to take, the rest are current.
	github := map[string][]string{
		"hadolint/hadolint":                         {"v2.15.1", "v2.15.2"},
		"openvex/vexctl":                            {"v0.4.4"},
		"fabpot/local-php-security-checker":         {"v2.1.3"},
		"editorconfig-checker/editorconfig-checker": {"v3.11.1"},
		"trufflesecurity/trufflehog":                {"v3.97.0"},
	}
	return lookup.Registry{
		"gitlab-tags":     cannedDS{name: "gitlab-tags", scheme: "semver", releases: releases},
		"gitlab-releases": cannedDS{name: "gitlab-releases", scheme: "semver", releases: releases},
		"github-releases": cannedDS{name: "github-releases", scheme: "semver", releases: github},
	}
}

func ciToolsOptions(at time.Time) whatifOptions {
	return whatifOptions{
		Root: ciToolsRepo, ConfigPath: "../../testdata/parity/config/default.json",
		RepoName: "devops/images/ci-tools", Now: at, Datasources: canned(),
	}
}

// The first end-to-end check: run the real configuration over a real
// repository and compare what comes out against what the pinned Renovate
// container extracted from the same files.
//
// The repository lives outside this module, so the test skips when it is not
// checked out. CI must not require a sibling checkout; a developer who has one
// should be told when the two disagree.
// fileRule maps an index in default.json's own packageRules to its index in
// the resolved configuration, where 722 preset rules precede the file's 48.
// Rules are always reported in the resolved numbering, which is Renovate's.
func fileRule(i int) int { return 722 + i }

const (
	ciToolsRepo   = "/Volumes/Samsung_X5/Projects/moselwal/devops/images/ci-tools"
	ciToolsCorpus = "../../testdata/parity/renovate-43.288.0/extract/ci-tools.json"
)

func TestWhatifAgainstARealRepository(t *testing.T) {
	if _, err := os.Stat(ciToolsRepo); err != nil {
		t.Skipf("the ci-tools checkout is not present: %v", err)
	}

	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	plan, err := whatif(context.Background(), ciToolsOptions(at))
	if err != nil {
		t.Fatal(err)
	}

	// A plan that found nothing would pass every comparison below.
	if plan.Stats.DepsExtracted == 0 {
		t.Fatal("the run extracted nothing; the pipeline is not connected")
	}
	t.Logf("%d dependencies in %d files", plan.Stats.DepsExtracted, plan.Stats.FilesDiscovered)

	// Keyed by file, name, value AND manager: the component pins are
	// legitimately reported twice, by gitlabci (gitlab-tags) and by the
	// estate's custom regex manager (gitlab-releases), and the rules tell
	// the two apart by manager.
	mine := map[string]bool{}
	for _, d := range plan.Deps {
		// A dependency extraction skipped is a recorded decision Renovate
		// emits nothing for. One skipped later - by lookup or by the
		// planner - was extracted just as Renovate extracted it, and stays
		// comparable.
		if d.SkipReason != "" && !skippedAfterExtraction(d.SkipReason) {
			continue
		}
		mgr := d.Manager
		if d.CustomManager != model.NoCustomManager {
			mgr = "regex"
		}
		mine[d.File+"|"+d.DepName+"|"+d.CurrentValue+"|"+mgr] = true
	}

	raw, err := os.ReadFile(ciToolsCorpus)
	if err != nil {
		t.Fatal(err)
	}
	var corpus map[string][]struct {
		PackageFile string `json:"packageFile"`
		Deps        []struct {
			DepName      string `json:"depName"`
			CurrentValue string `json:"currentValue"`
		} `json:"deps"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}

	theirs := map[string]string{} // key -> the manager Renovate used
	for mgr, files := range corpus {
		for _, f := range files {
			for _, d := range f.Deps {
				theirs[f.PackageFile+"|"+d.DepName+"|"+d.CurrentValue+"|"+mgr] = mgr
			}
		}
	}

	// The direction that matters most: pinup must never invent a dependency
	// Renovate does not see. A false positive writes to somebody's repository.
	var invented []string
	for k := range mine {
		if _, ok := theirs[k]; !ok {
			invented = append(invented, k)
		}
	}
	if len(invented) != 0 {
		t.Errorf("pinup produced %d dependencies Renovate does not: %v", len(invented), invented)
	}

	// The other direction: everything Renovate found, pinup must find too.
	// This expectation started life as "exactly 3 gitlabci dependencies still
	// missing" and was deleted when manager/gitlabci landed - which is how a
	// known gap is meant to behave.
	missingByManager := map[string]int{}
	for k, mgr := range theirs {
		if _, ok := mine[k]; !ok {
			missingByManager[mgr]++
		}
	}
	t.Logf("agreed on %d of %d; missing by manager: %v", len(mine), len(theirs), missingByManager)
	for mgr, n := range missingByManager {
		t.Errorf("%d dependencies Renovate found via %q are missing", n, mgr)
	}
}

// The plan a run produces must satisfy its own schema, including the rule that
// every dependency either becomes an update or says why not.
func TestWhatifProducesAValidPlan(t *testing.T) {
	if _, err := os.Stat(ciToolsRepo); err != nil {
		t.Skipf("the ci-tools checkout is not present: %v", err)
	}
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	plan, err := whatif(context.Background(), ciToolsOptions(at))
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("the plan is not valid: %v", err)
	}
	if plan.SchemaVersion != model.SchemaVersion {
		t.Errorf("schema version %d", plan.SchemaVersion)
	}
}

// skippedAfterExtraction recognises the skip reasons lookup and planner
// write. Listed here rather than matched loosely, so a new stage that skips
// with a new phrasing shows up as a test failure and gets added deliberately.
func skippedAfterExtraction(reason string) bool {
	for _, prefix := range []string{
		"lookup failed:", "no lookup was made", "up to date:", "the registry lists no releases",
		"current value", "none of the", "versioning:", "cannot write",
	} {
		if strings.HasPrefix(reason, prefix) {
			return true
		}
	}
	return false
}

// The plan must carry real updates for the canned releases, each with a
// locus whose bytes are exactly the current value - the edit that apply will
// make is derived from nothing else.
func TestWhatifProposesUpdatesWithExactLoci(t *testing.T) {
	if _, err := os.Stat(ciToolsRepo); err != nil {
		t.Skipf("the ci-tools checkout is not present: %v", err)
	}
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	plan, err := whatif(context.Background(), ciToolsOptions(at))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stats.LookupsIssued == 0 || plan.Stats.UpdatesFound == 0 {
		t.Fatalf("lookups=%d updates=%d: the lookup stage is not connected",
			plan.Stats.LookupsIssued, plan.Stats.UpdatesFound)
	}
	t.Logf("%d lookups, %d updates", plan.Stats.LookupsIssued, plan.Stats.UpdatesFound)

	files := map[string][]byte{}
	byDep := map[string][]model.Update{}
	for _, u := range plan.Updates {
		byDep[u.Dep.DepName] = append(byDep[u.Dep.DepName], u)
		body, ok := files[u.Dep.File]
		if !ok {
			body, err = os.ReadFile(ciToolsRepo + "/" + u.Dep.File)
			if err != nil {
				t.Fatal(err)
			}
			files[u.Dep.File] = body
		}
		l := u.Dep.Locus
		if got := string(body[l.ValueStart:l.ValueEnd]); got != u.Dep.CurrentValue {
			t.Errorf("%s %s: locus holds %q, current value is %q", u.Dep.File, u.Dep.DepName, got, u.Dep.CurrentValue)
		}
		if u.NewValue == u.Dep.CurrentValue {
			t.Errorf("%s: update to the same value %q", u.Dep.DepName, u.NewValue)
		}
	}

	// lint-tools is pinned @1 and 2.0.0 exists. Via gitlab-tags under
	// semver-partial that is a major update to "2", written in the pin's
	// own precision, and nothing within the major - "1" already covers
	// 1.33.64. Via gitlab-releases under loose it is 1 -> 1.33.64 and
	// 1 -> 2.0.0, both blocked by rules below.
	lint := byDep["devops/ci-cd-components/lint-tools"]
	if len(lint) == 0 {
		t.Fatal("no update for lint-tools although 2.0.0 is canned")
	}
	tags := 0
	for _, u := range lint {
		if u.Dep.Datasource != "gitlab-tags" {
			continue
		}
		tags++
		if u.Type != model.UpdateMajor || u.NewValue != "2" {
			t.Errorf("lint-tools via gitlab-tags: got %s to %q, want major to \"2\"", u.Type, u.NewValue)
		}
	}
	if tags != 1 {
		t.Errorf("want exactly one gitlab-tags update for lint-tools, got %d", tags)
	}
	// release-tools is pinned @1 and only 1.x exists: up to date via
	// gitlab-tags. The regex manager reads the same pin as gitlab-releases
	// under loose, where "1" is a version and 1.2.0 is newer - and rule 46
	// exists precisely to disable that reading. The update is held, not
	// deleted, and names the rule.
	for _, u := range byDep["devops/ci-cd-components/release-tools"] {
		if u.Dep.Datasource != "gitlab-releases" {
			t.Errorf("release-tools via %s: only the gitlab-releases reading may propose anything, got %+v", u.Dep.Datasource, u)
			continue
		}
		if !u.Blocked() || u.Blocks[0].Reason != model.BlockDisabled || u.Blocks[0].Org.Rule != fileRule(46) {
			t.Errorf("release-tools 1 -> %s must be disabled by the file's packageRules[46] (resolved %d), got blocks %+v", u.NewValue, fileRule(46), u.Blocks)
		}
		if u.SuppressedBy != model.BlockDisabled {
			t.Errorf("suppressedBy = %q", u.SuppressedBy)
		}
	}
	// A major on a gitlab-tags pin needs dashboard approval (rule 18).
	for _, u := range lint {
		if u.Dep.Datasource != "gitlab-tags" {
			continue
		}
		if !u.Blocked() || u.Blocks[0].Reason != model.BlockDashboardApproval || u.Blocks[0].Org.Rule != fileRule(18) {
			t.Errorf("lint-tools major must wait for dashboard approval by the file's packageRules[18], got %+v", u.Blocks)
		}
	}
	if plan.Stats.UpdatesBlocked == 0 {
		t.Error("stats must count the blocked updates")
	}
}

// Every held update in the plan names its reason, its origin, and - for a
// time-based hold - when it thaws. A nearly empty cache is warned about.
func TestWhatifHoldsExplainThemselves(t *testing.T) {
	if _, err := os.Stat(ciToolsRepo); err != nil {
		t.Skipf("the ci-tools checkout is not present: %v", err)
	}
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) // 14:00 in Berlin: the cron window is closed
	opts := ciToolsOptions(at)
	opts.Cache = newEmptyCache(t)
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, w := range plan.Warnings {
		if w.Stage == "cache" && strings.Contains(w.Msg, "first-seen") {
			warned = true
		}
	}
	if !warned {
		t.Error("an empty first-seen record must be warned about")
	}
	for _, u := range plan.Updates {
		if !u.Blocked() {
			continue
		}
		for _, b := range u.Blocks {
			switch b.Reason {
			case model.BlockDisabled, model.BlockDashboardApproval:
				if b.Org.Rule == model.NoRule {
					t.Errorf("%s: %s names no rule", u.DepKey, b.Reason)
				}
			case model.BlockMinimumReleaseAge:
				if u.TimeSource != model.TimeUnknown && b.Until.IsZero() {
					t.Errorf("%s: a hold on a known age must say when it thaws", u.DepKey)
				}
			case model.BlockSchedule:
				if b.Until.IsZero() || !strings.Contains(b.Note, "Europe/Berlin") {
					t.Errorf("%s: schedule hold %+v must name the thaw time and timezone", u.DepKey, b)
				}
			}
		}
		if u.SuppressedBy != u.Blocks[0].Reason {
			t.Errorf("%s: suppressedBy %q, first block %q", u.DepKey, u.SuppressedBy, u.Blocks[0].Reason)
		}
	}
}

// newEmptyCache is a fresh bbolt store in a temporary directory.
func newEmptyCache(t *testing.T) lookup.Cache {
	t.Helper()
	store, err := cache.Open(t.TempDir() + "/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// The edits a plan carries apply to a copy of the repository and change
// exactly the bytes of the values - nothing else in either file.
func TestWhatifEditsApplyByteExact(t *testing.T) {
	if _, err := os.Stat(ciToolsRepo); err != nil {
		t.Skipf("the ci-tools checkout is not present: %v", err)
	}
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC) // window open, holds thawed
	plan, err := whatif(context.Background(), ciToolsOptions(at))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Branches) == 0 {
		t.Fatal("no branches planned at an open window")
	}
	root := t.TempDir()
	var all []model.Edit
	names := map[string]bool{}
	for _, b := range plan.Branches {
		if b.SuppressedBy != "" {
			if len(b.Edits) != 0 {
				t.Errorf("held branch %s carries edits", b.Name)
			}
			continue
		}
		if len(b.Edits) == 0 {
			t.Errorf("branch %s carries no edits", b.Name)
		}
		all = append(all, b.Edits...)
		names[b.Name] = true
	}
	// hadolint 2.15.1 -> 2.15.2 via the annotated ARG: the branch is
	// named as Renovate names it, the edit is the version's bytes in the
	// Containerfile, and the v the tag carries is stripped by extractVersion.
	if !names["renovate/hadolint-hadolint-2.x"] {
		t.Errorf("expected renovate/hadolint-hadolint-2.x among %v", names)
	}
	for _, e := range all {
		if e.Old == "2.15.1" && (e.File != "Containerfile" || e.New != "2.15.2") {
			t.Errorf("hadolint edit %+v", e)
		}
	}
	files := map[string][]byte{}
	for _, e := range all {
		if _, ok := files[e.File]; ok {
			continue
		}
		body, err := os.ReadFile(ciToolsRepo + "/" + e.File)
		if err != nil {
			t.Fatal(err)
		}
		files[e.File] = body
		if err := os.WriteFile(root+"/"+e.File, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// All branches' edits at once: they must not overlap each other either.
	if cs := apply.Check(all); len(cs) != 0 {
		t.Fatalf("edits across branches overlap: %v", cs)
	}
	results, err := apply.WriteFiles(root, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(files) {
		t.Errorf("%d files written, %d expected", len(results), len(files))
	}
	for name, before := range files {
		after, _ := os.ReadFile(root + "/" + name)
		// Undo every edit by hand and expect the original back: the only
		// bytes that changed are the ones the plan named.
		restored := string(after)
		for _, e := range all {
			if e.File == name {
				restored = strings.Replace(restored, e.New, e.Old, 1)
			}
		}
		if restored != string(before) {
			t.Errorf("%s: bytes outside the edited values changed", name)
		}
	}
}

// A repository's own renovate.json is what runs, with the runner's file
// answering the local> alias: its rules come last and decide.
func TestRepositoryConfigExtendsTheRunnerFileByAlias(t *testing.T) {
	if _, err := os.Stat(ciToolsRepo); err != nil {
		t.Skipf("the ci-tools checkout is not present: %v", err)
	}
	root := t.TempDir()
	for _, f := range []string{".gitlab-ci.yml", "Containerfile"} {
		body, err := os.ReadFile(ciToolsRepo + "/" + f)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(root+"/"+f, body, 0o644)
	}
	os.WriteFile(root+"/renovate.json", []byte(`{
  "extends": ["local>devops/renovate-runner:default.json"],
  "packageRules": [{"description": "the fixture's own rule", "matchPackageNames": ["*"], "enabled": false}]
}`), 0o644)
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC)
	opts := ciToolsOptions(at)
	opts.Root = root
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stats.DepsExtracted == 0 {
		t.Fatal("nothing extracted: the alias did not resolve the runner's managers")
	}
	// A dependency disabled before lookup is skipped, not looked up: the
	// repository's own rule is the 771st, after the runner's 770.
	disabled := 0
	for _, d := range plan.Deps {
		if strings.Contains(d.SkipReason, "packageRules[770]") {
			disabled++
		}
	}
	if disabled == 0 {
		t.Errorf("no dependency names the repository's rule; skip reasons: %v", skipReasons(plan))
	}
	if plan.Stats.LookupsIssued != 0 || plan.Stats.BranchesPlanned != 0 {
		t.Errorf("a repository that disables everything looks nothing up and plans no branch: %+v", plan.Stats)
	}
}

func skipReasons(p *model.Plan) []string {
	var out []string
	for _, d := range p.Deps {
		out = append(out, d.SkipReason)
	}
	return out
}

// The fast lane plans one dependency: everything else is skipped by name,
// and the released package's lookups bypass the cache.
func TestWhatifReleasedNarrowsToTheReleasedPackage(t *testing.T) {
	if _, err := os.Stat(ciToolsRepo); err != nil {
		t.Skipf("the ci-tools checkout is not present: %v", err)
	}
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC)
	opts := ciToolsOptions(at)
	opts.Released = "devops/ci-cd-components/lint-tools"
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	planned, skippedByName := 0, 0
	for _, d := range plan.Deps {
		switch {
		case strings.Contains(d.DepName, "lint-tools"):
			if strings.Contains(d.SkipReason, "not the released") {
				t.Errorf("the released package was skipped: %s", d.SkipReason)
			}
			planned++
		case strings.Contains(d.SkipReason, "not the released package devops/ci-cd-components/lint-tools"):
			skippedByName++
		case d.SkipReason == "":
			t.Errorf("%s was planned although it is not the released package", d.DepName)
		}
	}
	if planned == 0 || skippedByName == 0 {
		t.Errorf("planned %d released deps, %d skipped by name", planned, skippedByName)
	}
	if plan.Stats.LookupsIssued != 2 {
		t.Errorf("only the released package is looked up (gitlab-tags and gitlab-releases): %d lookups", plan.Stats.LookupsIssued)
	}
}

func TestReleasedDebounce(t *testing.T) {
	path := t.TempDir() + "/released.json"
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	if seenRecently(path, "a/b@1", at) {
		t.Fatal("nothing seen yet")
	}
	markSeen(path, "a/b@1", at)
	if !seenRecently(path, "a/b@1", at.Add(30*time.Minute)) {
		t.Error("seen half an hour ago must count")
	}
	if seenRecently(path, "a/b@1", at.Add(2*time.Hour)) || seenRecently(path, "a/b@2", at) {
		t.Error("a later version or a later hour is a new run")
	}
	markSeen(path, "c/d@1", at.Add(48*time.Hour))
	if seenRecently(path, "a/b@1", at.Add(48*time.Hour+time.Minute)) {
		t.Error("entries older than a day are pruned")
	}
}
