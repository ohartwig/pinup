// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The support table is derived from the code, not remembered: every
// configuration key the run reads by name - raw[...] in the config
// decoder, Raw[...]/cfg[...] in the pipeline, the case labels of the
// rules engine - must be classified. A key the code reads that the table
// does not know is what let the table fall two keys behind on 2026-09-13.
func TestKeySupportKnowsEveryKeyTheCodeReads(t *testing.T) {
	// Keys that live inside a nested object (a customManagers entry, a
	// customDatasources entry, a postUpgradeTasks object) are not
	// top-level or packageRules keys, and the table does not classify
	// them. Named here so the test says what it skips.
	nested := map[string]bool{
		"customType": true, "managerFilePatterns": true, "fileMatch": true, "matchStrings": true, "matchStringsStrategy": true,
		"depNameTemplate": true, "packageNameTemplate": true, "datasourceTemplate": true, "versioningTemplate": true,
		"currentValueTemplate": true, "extractVersionTemplate": true, "registryUrlTemplate": true, "depTypeTemplate": true,
		"defaultRegistryUrlTemplate": true, "transformTemplates": true, "format": true,
		"commands": true, "fileFilters": true, "executionMode": true,
	}
	reads := regexp.MustCompile(`\b(?:raw|Raw|cfg|c|single|rule|obj)\["([a-zA-Z$]+)"\]`)
	cases := regexp.MustCompile(`case ("match[A-Za-z]+"(?:,\s*"match[A-Za-z]+")*)`)
	found := map[string]string{}
	for _, dir := range []string{".", "../../planner", "../../rules", "../../config"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range reads.FindAllStringSubmatch(string(src), -1) {
				found[m[1]] = path
			}
			for _, m := range cases.FindAllStringSubmatch(string(src), -1) {
				for _, k := range strings.Split(m[1], ",") {
					found[strings.Trim(strings.TrimSpace(k), `"`)] = path
				}
			}
		}
	}
	if len(found) < 40 {
		t.Fatalf("only %d keys found in the code; the scan is broken", len(found))
	}
	var missing []string
	for k, where := range found {
		if nested[k] || len(k) < 2 {
			continue
		}
		if _, ok := keySupport[k]; !ok {
			missing = append(missing, k+" ("+where+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("keys the code reads that migrate's table does not classify:\n  %s", strings.Join(missing, "\n  "))
	}
}
