// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/apply"
	"github.com/ohartwig/pinup/cache"
	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/wire"
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
		// Every release carries a digest, as a gitlab tag's commit id
		// does: an edit must not append it to a reference that had none.
		rs.Releases = append(rs.Releases, model.Release{Version: v, Digest: "9b49336126056907f46fcc0f954acb62639c1fbe", Timestamp: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)})
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

func ciToolsOptions(t *testing.T, at time.Time) whatifOptions {
	t.Helper()
	return whatifOptions{
		Root: ciTools(t), ConfigPath: fixture.Config(t),
		RepoName: "devops/images/ci-tools", Now: at, Datasources: canned(),
	}
}

// ciTools is the estate's ci-tools checkout, the repository the canned
// answers above were written for; the test skips where the estate root
// does not name it or the checkout is not on this machine.
func ciTools(t *testing.T) string {
	t.Helper()
	for _, c := range fixture.Corpora(t) {
		if c.Name == "ci-tools" && c.Tree != "" {
			return c.Tree
		}
	}
	t.Skip("the ci-tools checkout is not present")
	return ""
}

// The first end-to-end check: run the real configuration over a real
// repository and compare what comes out against what the pinned Renovate
// container extracted from the same files.
//
// The repository lives outside this module, so the test skips when it is not
// checked out. CI must not require a sibling checkout; a developer who has one
// should be told when the two disagree.
// fileRule maps an index in default.json's own packageRules to its index in
// the resolved configuration, where the library's rules precede the file's.
// Rules are always reported in the resolved numbering, which is Renovate's.
func fileRule(t *testing.T, i int) int {
	t.Helper()
	e := fixture.Expect(t)
	return e.RulesResolved - e.RulesOwn + i
}

// TestWhatifAgreesWithTheCorpus runs the whole extraction over every
// repository the fixture root has a capture for - the run's discovery, the
// resolved managers, the custom regex managers, the lock files - and
// compares what comes out against what the pinned Renovate container
// extracted from the same files. A capture whose checkout is not on this
// machine is skipped; the public root's repositories are in the tree, so
// there every capture is compared.
func TestWhatifAgreesWithTheCorpus(t *testing.T) {
	ran := 0
	for _, c := range fixture.Corpora(t) {
		if c.Tree == "" {
			t.Logf("%s: checkout not present; skipped", c.Name)
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			root := c.Tree
			if _, err := os.Stat(filepath.Join(c.Tree, ".git")); err == nil {
				// The capture archived a commit; a working tree may carry
				// uncommitted changes the capture knows nothing about.
				root = archiveAt(t, c)
			}
			at := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
			plan, err := whatif(context.Background(), whatifOptions{
				Root: root, ConfigPath: c.Config, RepoName: "corpus/" + c.Name, Now: at, Datasources: canned(),
			})
			if err != nil {
				t.Fatal(err)
			}
			// A plan that found nothing would pass every comparison below.
			if plan.Stats.DepsExtracted == 0 {
				t.Fatal("the run extracted nothing; the pipeline is not connected")
			}
			t.Logf("%d dependencies in %d files", plan.Stats.DepsExtracted, plan.Stats.FilesDiscovered)
			compareWithCorpus(t, plan, c)
		})
	}
	if ran == 0 {
		t.Skip("no capture with its files on this machine")
	}
}

// archiveAt materialises the commit a capture archived from a checkout.
func archiveAt(t *testing.T, c fixture.Corpus) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", "git -C \"$1\" archive \"$2\" | tar -x -C \"$3\"", "sh", c.Tree, c.Commit, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("archiving %s at %s: %v\n%s", c.Name, c.Commit, err, out)
	}
	return dir
}

// compareWithCorpus holds a plan's extraction against a capture, keyed by
// file, name, value AND manager: the component pins are legitimately
// reported twice, by gitlabci (gitlab-tags) and by a custom regex manager
// (gitlab-releases), and the rules tell the two apart by manager.
func compareWithCorpus(t *testing.T, plan *model.Plan, c fixture.Corpus) {
	t.Helper()
	mine := map[string]bool{}
	for _, d := range plan.Deps {
		// A dependency extraction skipped in pinup's own words is a recorded
		// decision Renovate emits nothing for (a stage reference, a tag with
		// a variable in it). One skipped in Renovate's words - "local",
		// "unspecified-version" - or later, by the rules, the lookup or the
		// planner, was extracted just as Renovate extracted it, and stays
		// comparable.
		if d.SkipReason != "" && d.Disabled == "" && !skippedAfterExtraction(d.SkipReason) && !renovateSkipReason(d.SkipReason) {
			continue
		}
		mgr := d.Manager
		if d.CustomManager != model.NoCustomManager {
			mgr = "regex"
		}
		mine[d.File+"|"+d.DepName+"|"+d.CurrentValue+"|"+mgr] = true
	}

	raw, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus map[string][]struct {
		PackageFile string `json:"packageFile"`
		Deps        []struct {
			DepName      string `json:"depName"`
			CurrentValue string `json:"currentValue"`
			SkipReason   string `json:"skipReason"`
		} `json:"deps"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	theirs := map[string]string{} // key -> the manager Renovate used
	for mgr, files := range corpus {
		for _, f := range files {
			for _, d := range f.Deps {
				if d.DepName == "" {
					continue // skipped before it had a name: nothing to compare
				}
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
	sort.Strings(invented)
	if len(invented) != 0 {
		t.Errorf("pinup produced %d dependencies Renovate does not: %v", len(invented), invented)
	}

	// The other direction: everything Renovate found, pinup must find too.
	missingByManager := map[string]int{}
	var missing []string
	for k, mgr := range theirs {
		if _, ok := mine[k]; !ok {
			missingByManager[mgr]++
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	t.Logf("agreed on %d of %d; missing by manager: %v", len(mine)-len(invented), len(theirs), missingByManager)
	for mgr, n := range missingByManager {
		t.Errorf("%d dependencies Renovate found via %q are missing", n, mgr)
	}
	if len(missing) > 0 {
		t.Logf("missing: %v", missing)
	}
}

// The plan a run produces must satisfy its own schema, including the rule that
// every dependency either becomes an update or says why not.
func TestWhatifProducesAValidPlan(t *testing.T) {
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	plan, err := whatif(context.Background(), ciToolsOptions(t, at))
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

// skippedAfterExtraction recognises the skip reasons the rules, lookup and
// planner write. Listed here rather than matched loosely, so a new stage
// that skips with a new phrasing shows up as a test failure and gets added
// deliberately.
func skippedAfterExtraction(reason string) bool {
	for _, prefix := range []string{
		"lookup failed:", "no lookup was made", "up to date:", "the registry lists no releases",
		"current value", "none of the", "versioning:", "cannot write",
		"disabled by packageRules", "listed in ignoreDeps", "not the released package", "not the package ",
	} {
		if strings.HasPrefix(reason, prefix) {
			return true
		}
	}
	return false
}

// renovateSkipReason recognises the extraction skip reasons pinup records
// in Renovate's own words, for parity: the capture carries the dependency
// with its name and that reason, and so must the plan.
func renovateSkipReason(reason string) bool {
	switch reason {
	case "local", "unspecified-version", "invalid-dependency-specification", "unsupported-source":
		return true
	}
	return false
}

// The plan must carry real updates for the canned releases, each with a
// locus whose bytes are exactly the current value - the edit that apply will
// make is derived from nothing else.
func TestWhatifProposesUpdatesWithExactLoci(t *testing.T) {
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	plan, err := whatif(context.Background(), ciToolsOptions(t, at))
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
			body, err = os.ReadFile(ciTools(t) + "/" + u.Dep.File)
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
		// `@1` is a rolling major: the newer major is planned as
		// majorAvailable - reported, never written.
		if u.Type != model.UpdateMajorAvailable || u.NewValue != "2" {
			t.Errorf("lint-tools via gitlab-tags: got %s to %q, want majorAvailable to \"2\"", u.Type, u.NewValue)
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
		if !u.Blocked() || u.Blocks[0].Reason != model.BlockDisabled || u.Blocks[0].Org.Rule != fileRule(t, 46) {
			t.Errorf("release-tools 1 -> %s must be disabled by the file's packageRules[46] (resolved %d), got blocks %+v", u.NewValue, fileRule(t, 46), u.Blocks)
		}
		if u.SuppressedBy != model.BlockDisabled {
			t.Errorf("suppressedBy = %q", u.SuppressedBy)
		}
	}
	// A major on a gitlab-tags pin is held twice over: as a rolling major
	// by pinup itself, and by rule 18's dashboard approval - the rule was
	// written for a "major" and still fires on majorAvailable.
	for _, u := range lint {
		if u.Dep.Datasource != "gitlab-tags" {
			continue
		}
		reasons := map[model.BlockReason]model.Origin{}
		for _, b := range u.Blocks {
			reasons[b.Reason] = b.Org
		}
		if _, ok := reasons[model.BlockRollingMajor]; !ok {
			t.Errorf("lint-tools majorAvailable is not held as a rolling major: %+v", u.Blocks)
		}
		if org, ok := reasons[model.BlockDashboardApproval]; !ok || org.Rule != fileRule(t, 18) {
			t.Errorf("lint-tools major must wait for dashboard approval by the file's packageRules[18], got %+v", u.Blocks)
		}
		if u.SuppressedBy != model.BlockRollingMajor {
			t.Errorf("suppressedBy = %q, want the rolling-major hold first", u.SuppressedBy)
		}
	}
	if plan.Stats.UpdatesBlocked == 0 {
		t.Error("stats must count the blocked updates")
	}
}

// Every held update in the plan names its reason, its origin, and - for a
// time-based hold - when it thaws. A nearly empty cache is warned about.
func TestWhatifHoldsExplainThemselves(t *testing.T) {
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) // 14:00 in Berlin: the cron window is closed
	opts := ciToolsOptions(t, at)
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
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC) // window open, holds thawed
	plan, err := whatif(context.Background(), ciToolsOptions(t, at))
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
		// Live, the gitlab-tags commit id was once appended to a component
		// include - "1.63.3@9b49..." - by the digest-pinning path. Only a
		// pinDigest update may do that.
		if strings.Contains(e.New, "@") && !strings.Contains(e.Old, "@") {
			t.Errorf("edit pins a digest onto an unpinned reference: %+v", e)
		}
	}
	files := map[string][]byte{}
	for _, e := range all {
		if _, ok := files[e.File]; ok {
			continue
		}
		body, err := os.ReadFile(ciTools(t) + "/" + e.File)
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
	root := t.TempDir()
	for _, f := range []string{".gitlab-ci.yml", "Containerfile"} {
		body, err := os.ReadFile(ciTools(t) + "/" + f)
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
	opts := ciToolsOptions(t, at)
	opts.Root = root
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stats.DepsExtracted == 0 {
		t.Fatal("nothing extracted: the alias did not resolve the runner's managers")
	}
	// A dependency disabled before lookup is skipped, not looked up: the
	// repository's own rule comes after the runner's.
	disabled := 0
	for _, d := range plan.Deps {
		if strings.Contains(d.SkipReason, fmt.Sprintf("packageRules[%d]", fixture.Expect(t).RulesResolved)) {
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
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC)
	opts := ciToolsOptions(t, at)
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

// The advisory watch's targeted run: one external package by index key,
// every other dependency skipped by name, only that package looked up.
func TestWhatifPackageNarrowsToOnePackage(t *testing.T) {
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC)
	opts := ciToolsOptions(t, at)
	opts.Package = "github-releases|editorconfig-checker/editorconfig-checker"
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	planned, skippedByName := 0, 0
	for _, d := range plan.Deps {
		switch {
		case d.Datasource == "github-releases" && d.DepName == "editorconfig-checker/editorconfig-checker":
			// Looked up, and up to date in the canned answers: skipped by
			// the lookup, never by name.
			if strings.Contains(d.SkipReason, "not the package") {
				t.Errorf("the package was skipped by name: %s", d.SkipReason)
			}
			planned++
		case strings.Contains(d.SkipReason, "not the package github-releases|editorconfig-checker/editorconfig-checker"):
			skippedByName++
		case d.SkipReason == "":
			t.Errorf("%s was planned although it is not the package", d.DepName)
		}
	}
	if planned == 0 || skippedByName == 0 {
		t.Errorf("planned %d, %d skipped by name", planned, skippedByName)
	}
	if plan.Stats.LookupsIssued != 1 {
		t.Errorf("only the package is looked up: %d lookups", plan.Stats.LookupsIssued)
	}
}

func TestReleasedDebounce(t *testing.T) {
	path := t.TempDir() + "/released.json"
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	if seenRecently(path, "a/b@1", at) {
		t.Fatal("nothing seen yet")
	}
	if err := markSeen(path, "a/b@1", at); err != nil {
		t.Fatal(err)
	}
	if !seenRecently(path, "a/b@1", at.Add(30*time.Minute)) {
		t.Error("seen half an hour ago must count")
	}
	if seenRecently(path, "a/b@1", at.Add(2*time.Hour)) || seenRecently(path, "a/b@2", at) {
		t.Error("a later version or a later hour is a new run")
	}
	if err := markSeen(path, "c/d@1", at.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if seenRecently(path, "a/b@1", at.Add(48*time.Hour+time.Minute)) {
		t.Error("entries older than a day are pruned")
	}
}

// A dependency with a minor and a major proposed has two updates on two
// branches. Branches refer to updates by update key, not by dependency:
// before that, the minor's branch carried the major's edit as well, the two
// overlapped, and the branch went out with no edits at all. Found by the
// golden rendering of the estate repository, where vpc-5.x listed both.
func TestEachBranchCarriesOnlyItsOwnUpdate(t *testing.T) {
	src := "FROM alpine:3.20\n"
	dep := model.Dependency{Manager: "dockerfile", File: "Containerfile", DepName: "alpine", CurrentValue: "3.20",
		Datasource: "docker", CustomManager: model.NoCustomManager,
		Locus: model.Locus{ValueStart: 12, ValueEnd: 16, DigestStart: model.NoDigest, DigestEnd: model.NoDigest}}
	minor := model.Update{DepKey: dep.Key(), Dep: dep, NewValue: "3.21", NewVersion: "3.21", Type: model.UpdateMinor}
	major := model.Update{DepKey: dep.Key(), Dep: dep, NewValue: "4.0", NewVersion: "4.0", Type: model.UpdateMajor}
	if minor.Key() == major.Key() {
		t.Fatal("two updates of one dependency must have distinct keys")
	}
	updates := []model.Update{minor, major}
	contents := map[string][]byte{"Containerfile": []byte(src)}
	for _, tc := range []struct {
		branch string
		key    string
		want   string
	}{{"renovate/alpine-3.x", minor.Key(), "3.21"}, {"renovate/alpine-4.x", major.Key(), "4.0"}} {
		b := model.Branch{Name: tc.branch, UpdateKeys: []string{tc.key}}
		edits, warns := editsFor(context.Background(), b, updates, contents, config.Decoded{}, wire.Managers())
		if len(warns) != 0 || len(edits) != 1 || edits[0].New != tc.want {
			t.Errorf("%s: edits %+v warnings %+v", tc.branch, edits, warns)
		}
	}
}

// A repository without a configuration file runs with the recommended
// ignorePaths - a committed node_modules is not a package file - while a
// repository with one runs under exactly what it says (the runner's list,
// which does not ignore node_modules). Measured in the pinned container
// and on the estate's partner-a-jobs.
func TestARepositoryWithoutConfigIgnoresNodeModules(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(root+"/node_modules/dropzone", 0o755)
	os.WriteFile(root+"/package.json", []byte(`{"name":"root","dependencies":{"lodash":"4.17.20"}}`), 0o644)
	os.WriteFile(root+"/node_modules/dropzone/package.json", []byte(`{"name":"dropzone","version":"6.0.0","devDependencies":{"karma":"^6.1.0"}}`), 0o644)
	at := time.Date(2026, 9, 13, 14, 5, 0, 0, time.UTC)
	opts := ciToolsOptions(t, at)
	opts.Root = root
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, d := range plan.Deps {
		files[d.File] = true
	}
	if !files["package.json"] || files["node_modules/dropzone/package.json"] {
		t.Errorf("files extracted without a repository config: %v; want package.json alone", files)
	}

	os.WriteFile(root+"/renovate.json", []byte(`{"extends": ["local>devops/renovate-runner"]}`), 0o644)
	plan, err = whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	files = map[string]bool{}
	for _, d := range plan.Deps {
		files[d.File] = true
	}
	if !files["node_modules/dropzone/package.json"] {
		t.Errorf("files extracted with the runner's own ignorePaths: %v; want the vendored package file too", files)
	}
}

// A ticked dashboard box lifts exactly the hold it names: the block leaves
// the members, the branch is actionable, the plan records the dashboard as
// the origin. A hold the box does not name stays.
func TestADashboardBoxLiftsTheHoldItNames(t *testing.T) {
	at := time.Date(2026, 9, 13, 3, 5, 0, 0, time.UTC) // outside the 4-hourly window
	base, err := whatif(context.Background(), ciToolsOptions(t, at))
	if err != nil {
		t.Fatal(err)
	}
	var scheduled string
	for _, b := range base.Branches {
		if b.SuppressedBy == model.BlockSchedule {
			scheduled = b.Name
			break
		}
	}
	if scheduled == "" {
		t.Skip("no branch held by schedule at this moment in ci-tools")
	}
	opts := ciToolsOptions(t, at)
	opts.Checks = report.ParseChecks("- [x] <!-- unschedule-branch=" + scheduled + " -->x")
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var lifted *model.Branch
	for i := range plan.Branches {
		if plan.Branches[i].Name == scheduled {
			lifted = &plan.Branches[i]
		}
	}
	if lifted == nil || lifted.SuppressedBy != "" || len(lifted.Edits) == 0 {
		t.Fatalf("the ticked branch is still held or carries no edits: %+v", lifted)
	}
	if len(lifted.Prov) == 0 || lifted.Prov[len(lifted.Prov)-1].Source != "dashboard" {
		t.Errorf("the lift is not recorded on the branch: %+v", lifted.Prov)
	}
	keys := map[string]bool{}
	for _, k := range lifted.UpdateKeys {
		keys[k] = true
	}
	for _, u := range plan.Updates {
		if keys[u.Key()] {
			for _, blk := range u.Blocks {
				if blk.Reason == model.BlockSchedule {
					t.Errorf("a member still carries the schedule block: %+v", u.Blocks)
				}
			}
		}
	}
	// Every other scheduled branch stays held.
	for _, b := range plan.Branches {
		if b.Name != scheduled && b.SuppressedBy == "" {
			for _, ob := range base.Branches {
				if ob.Name == b.Name && ob.SuppressedBy == model.BlockSchedule {
					t.Errorf("%s was lifted without a box", b.Name)
				}
			}
		}
	}
}

// The runner project the repositories extend by alias comes from --config
// when that is a local> name, from PINUP_RUNNER_PROJECT otherwise, and is
// the estate's default when neither says.
func TestRunnerProjectComesFromConfigOrEnvironment(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		env     map[string]string
		cfgPath string
		want    string
	}{
		{nil, "/tmp/default.json", "devops/renovate-runner"},
		{map[string]string{"PINUP_RUNNER_PROJECT": "platform/bot"}, "/tmp/default.json", "platform/bot"},
		{map[string]string{"PINUP_RUNNER_PROJECT": "platform/bot"}, "local>tools/runner:release-fast.json", "tools/runner"},
		{nil, "local>tools/runner", "tools/runner"},
	} {
		if got := runnerProject(env(tc.env), tc.cfgPath); got != tc.want {
			t.Errorf("%v %s: got %q, want %q", tc.env, tc.cfgPath, got, tc.want)
		}
	}
	if got := runnerAliases("platform/bot"); len(got) != 3 || got[0] != "local>platform/bot" || got[2] != "local>platform/bot:default" {
		t.Errorf("aliases = %v", got)
	}
}
