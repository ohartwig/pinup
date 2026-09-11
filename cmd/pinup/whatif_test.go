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
		rs.Releases = append(rs.Releases, model.Release{Version: v})
	}
	return rs, nil
}

// canned is the lookup table for ci-tools: the component pins get a newer
// tag within their major and one beyond it, so both the "up to date within
// the rolling major" and the "a newer major exists" paths are exercised.
func canned() lookup.Registry {
	return lookup.Registry{
		"gitlab-tags": cannedDS{name: "gitlab-tags", scheme: "semver", releases: map[string][]string{
			"devops/ci-cd-components/lint-tools":          {"1.33.59", "1.33.64", "2.0.0"},
			"devops/ci-cd-components/release-tools":       {"1.0.0", "1.2.0"},
			"devops/ci-cd-components/container-scanning":  {"3.0.0", "3.1.0"},
			"devops/ci-cd-components/supply-chain-verify": {"2.0.0", "2.4.1"},
		}},
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

	mine := map[string]bool{}
	for _, d := range plan.Deps {
		// A dependency extraction skipped is a recorded decision Renovate
		// emits nothing for. One skipped later - by lookup or by the
		// planner - was extracted just as Renovate extracted it, and stays
		// comparable.
		if d.SkipReason != "" && !skippedAfterExtraction(d.SkipReason) {
			continue
		}
		mine[d.File+"|"+d.DepName+"|"+d.CurrentValue] = true
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
				theirs[f.PackageFile+"|"+d.DepName+"|"+d.CurrentValue] = mgr
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
		if !mine[k] {
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

	// lint-tools is pinned @1 and 2.0.0 exists: a major update to "2",
	// written in the pin's own precision, and nothing within the major -
	// "1" already covers 1.33.64.
	lint := byDep["devops/ci-cd-components/lint-tools"]
	if len(lint) == 0 {
		t.Fatal("no update for lint-tools although 2.0.0 is canned")
	}
	for _, u := range lint {
		if u.Type != model.UpdateMajor || u.NewValue != "2" {
			t.Errorf("lint-tools: got %s to %q, want major to \"2\"", u.Type, u.NewValue)
		}
	}
	// release-tools is pinned @1 and only 1.x exists: up to date.
	if rt := byDep["devops/ci-cd-components/release-tools"]; len(rt) != 0 {
		t.Errorf("release-tools @1 with only 1.x released must not update, got %+v", rt)
	}
}
