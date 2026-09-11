// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

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
	plan, err := whatif(context.Background(), ciToolsRepo,
		"../../testdata/parity/config/default.json", "devops/images/ci-tools", at)
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
		// A skipped dependency is a recorded decision, and Renovate emits
		// nothing for those - so only the actionable ones are comparable.
		if d.SkipReason != "" && !strings.HasPrefix(d.SkipReason, "lookup is not wired") {
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
	plan, err := whatif(context.Background(), ciToolsRepo,
		"../../testdata/parity/config/default.json", "devops/images/ci-tools", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("the plan is not valid: %v", err)
	}
	for _, d := range plan.Deps {
		if d.SkipReason == "" {
			t.Errorf("%s: %s has neither an update nor a skip reason", d.File, d.DepName)
		}
	}
	if plan.SchemaVersion != model.SchemaVersion {
		t.Errorf("schema version %d", plan.SchemaVersion)
	}
}
