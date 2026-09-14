// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Command defaultsgen builds config/defaults.json: Renovate's option
// defaults as the pinned container resolved them, taken from the captured
// --print-config output by subtracting everything the estate configuration
// and its presets set, and everything that describes the one run rather
// than the program. Nothing is copied from the Renovate source tree.
//
// Deterministic, and a test asserts the committed file equals its output.
//
// Usage: go run ./tools/defaultsgen
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

const (
	fullPath   = "testdata/estate/renovate-43.288.0/full-resolved.json"
	directPath = "testdata/estate/renovate-43.288.0/presets/default-resolved.json"
	outPath    = "config/defaults.json"
)

// runSpecific names keys that describe the capture run, the platform
// session or a secret, not a default any configuration inherits.
var runSpecific = map[string]bool{
	"branchList": true, "defaultBranch": true, "errors": true, "warnings": true,
	"repository": true, "repoFingerprint": true, "repoIsOnboarded": true, "renovateJsonPresent": true,
	"isFork": true, "hostRules": true, "token": true, "password": true, "username": true,
	"npmToken": true, "npmrc": true, "forkToken": true, "encrypted": true, "dryRun": true,
	"printConfig": true, "reportPath": true, "reportType": true, "reportFormatting": true,
	"privateKeyPath": true, "privateKeyPathOld": true, "gitAuthor": true, "gitUrl": true,
	"logLevelRemap": true, "writeDiscoveredRepos": true, "detectHostRulesFromEnv": true,
	"detectGlobalManagerConfig": true, "globalExtends": true, "onboardingBranch": true,
	"onboardingRebaseCheckbox": true, "persistRepoData": true, "repositoryCache": true,
	"repositoryCacheType": true, "useCloudMetadataServices": true, "mode": true,
	"inheritConfig": true, "inheritConfigFileName": true, "inheritConfigRepoName": true,
	"inheritConfigStrict": true, "platformCommit": true, "forkCreation": true, "forkOrg": true,
	"forkModeDisallowMaintainerEdits": true, "forkProcessing": true, "cloneSubmodules": true,
	"cloneSubmodulesFilter": true, "customizeDashboard": true,
	"deleteConfigFile": true, "deleteAdditionalConfigFile": true, "optimizeForDisabled": true,
	"expandCodeOwnersGroups": true, "filterUnavailableUsers": true, "azureWorkItemId": true,
	"azureWorkItemType": true, "bbAutoResolvePrTasks": true, "bbUseDefaultReviewers": true,
	"gitLabIgnoreApprovals": true, "milestone": true, "parentOrg": true,
}

func main() {
	b, err := Generate(fullPath, directPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "defaultsgen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "defaultsgen:", err)
		os.Exit(1)
	}
}

// Generate renders the defaults document.
func Generate(full, direct string) ([]byte, error) {
	var f, d map[string]any
	if err := load(full, &f); err != nil {
		return nil, err
	}
	if err := load(direct, &d); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range f {
		if runSpecific[k] {
			continue
		}
		set, isSet := d[k]
		if !isSet {
			out[k] = v
			continue
		}
		// An object the configuration sets partially - digest: {automerge:
		// true} over Renovate's digest defaults - is measured as a deep
		// merge: the default is what the configuration did not write.
		fm, fok := v.(map[string]any)
		dm, dok := set.(map[string]any)
		if !fok || !dok {
			continue
		}
		rest := map[string]any{}
		for kk, vv := range fm {
			if _, written := dm[kk]; !written {
				rest[kk] = vv
			}
		}
		if len(rest) > 0 {
			out[k] = rest
		}
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteString("{\n")
	fmt.Fprintf(&buf, "  \"source\": %q,\n", "executed in renovate/renovate:43.288.0: --print-config defaults, minus what the configuration sets and what describes the run")
	buf.WriteString("  \"defaults\": {\n")
	for i, k := range keys {
		v, _ := json.Marshal(out[k])
		fmt.Fprintf(&buf, "    %q: %s", k, v)
		if i < len(keys)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("  }\n}\n")
	return buf.Bytes(), nil
}

func load(path string, into *map[string]any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}
