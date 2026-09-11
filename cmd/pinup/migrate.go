// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/config"
	"git.ole-hartwig.eu/pinup/pinup/config/preset"
	"git.ole-hartwig.eu/pinup/pinup/rules"
	"git.ole-hartwig.eu/pinup/pinup/wire"
)

// Support classifies a configuration key by what this version of pinup does
// with it. The table is the report: every key a configuration uses lands in
// exactly one class, and an unsupported one is named rather than dropped.
type support string

const (
	supported   support = "supported"
	partial     support = "partial"
	unsupported support = "unsupported"
)

// keySupport is the classification of top-level keys and packageRules keys.
// A key not listed is unsupported: an unknown key is one nothing reads.
var keySupport = map[string]support{
	// Resolution and matching.
	"extends": supported, "description": supported, "packageRules": supported, "customManagers": supported,
	"customDatasources": supported, "enabledManagers": supported, "ignorePaths": supported,
	"matchPackageNames": supported, "matchDepNames": supported, "matchDatasources": supported, "matchManagers": supported,
	"matchDepTypes": supported, "matchUpdateTypes": supported, "matchCurrentValue": supported, "matchCurrentVersion": partial,
	"matchFileNames": supported, "matchSourceUrls": partial, "matchJsonata": partial, "matchCategories": unsupported,
	// Decisions.
	"enabled": supported, "minimumReleaseAge": supported, "minimumReleaseAgeBehaviour": supported, "schedule": supported,
	"timezone": supported, "automerge": supported, "dependencyDashboardApproval": supported, "groupName": supported,
	"groupSlug": supported, "versioning": supported, "registryUrls": supported, "extractVersion": supported,
	"ignoreUnstable": supported, "prHourlyLimit": supported, "prConcurrentLimit": supported, "labels": supported,
	"ignoreDeps": supported, "allowedVersions": supported, "rangeStrategy": partial, "separateMajorMinor": supported,
	"separateMinorPatch": partial, "separateMultipleMajor": partial, "pinDigests": unsupported,
	// Publishing.
	"commitBody": partial, "prCreation": partial, "rebaseWhen": partial, "platformAutomerge": supported,
	"semanticCommitType": supported, "semanticCommitScope": supported, "commitMessageTopic": supported,
	"commitMessageExtra": supported, "commitMessageAction": supported, "branchPrefix": supported, "branchTopic": supported,
	// Not yet.
	"lockFileMaintenance": unsupported, "postUpgradeTasks": unsupported, "internalChecksFilter": unsupported,
	"vulnerabilityAlerts": unsupported, "osvVulnerabilityAlerts": unsupported, "dependencyDashboard": unsupported,
	"executionTimeout": partial, "$schema": supported,
}

// cmdMigrate resolves a configuration as a run would and reports, key by
// key, what pinup supports. It changes no file: the twenty renovate.json
// files leave the critical path through the runner alias, and a
// conversion nobody asked for would be a second config to keep true.
func cmdMigrate(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(errw)
	cfgPath := fs.String("config", "", "configuration file to classify (required)")
	asJSON := fs.Bool("json", false, "print the classification as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("migrate: --config is required")
	}
	layer, err := config.LoadFile(*cfgPath)
	if err != nil {
		return err
	}
	r, warnings, err := config.ResolveLayer(layer, preset.Builtin())
	if err != nil {
		return err
	}

	classes := map[support][]string{}
	seen := map[string]bool{}
	classify := func(where, key string) {
		id := key
		if where != "" {
			id = where + "." + key
		}
		if seen[id] {
			return
		}
		seen[id] = true
		cls, ok := keySupport[key]
		if !ok {
			cls = unsupported
		}
		classes[cls] = append(classes[cls], id)
	}
	for k := range layer.Raw {
		classify("", k)
	}
	if rulesRaw, ok := layer.Raw["packageRules"].([]any); ok {
		for _, rule := range rulesRaw {
			if obj, ok := rule.(map[string]any); ok {
				for k := range obj {
					classify("packageRules[]", k)
				}
			}
		}
	}

	// Managers and datasources the resolved configuration names.
	decoded, err := config.Decode(r.Raw)
	if err != nil {
		return err
	}
	managers := wire.Managers()
	var missingManagers []string
	for _, m := range decoded.EnabledManagers {
		if _, ok := managers[m]; !ok && m != "custom.regex" {
			missingManagers = append(missingManagers, m)
		}
	}
	sort.Strings(missingManagers)

	// Rules the engine cannot evaluate.
	var ruleWarnings []string
	if rulesRaw, ok := r.Raw["packageRules"].([]any); ok {
		if eng, err := rules.Compile(rulesRaw, wire.Versionings()); err != nil {
			ruleWarnings = append(ruleWarnings, err.Error())
		} else {
			ruleWarnings = eng.Warnings
		}
	}

	for _, cls := range []support{supported, partial, unsupported} {
		sort.Strings(classes[cls])
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"file": *cfgPath, "supported": classes[supported], "partial": classes[partial],
			"x-unsupported": classes[unsupported], "managersNotImplemented": missingManagers,
			"rulesNotEvaluable": ruleWarnings, "presetWarnings": warnings, "migrations": r.Migrations,
		})
	}
	fmt.Fprintf(out, "%s\n", *cfgPath)
	for _, cls := range []support{supported, partial, unsupported} {
		fmt.Fprintf(out, "\n%s (%d)\n", cls, len(classes[cls]))
		for _, k := range classes[cls] {
			fmt.Fprintf(out, "  %s\n", k)
		}
	}
	if len(missingManagers) > 0 {
		fmt.Fprintf(out, "\nmanagers enabled but not implemented (%d)\n  %s\n", len(missingManagers), strings.Join(missingManagers, "\n  "))
	}
	if len(ruleWarnings) > 0 {
		fmt.Fprintf(out, "\nrules the engine does not evaluate (%d)\n  %s\n", len(ruleWarnings), strings.Join(ruleWarnings, "\n  "))
	}
	if len(warnings) > 0 {
		fmt.Fprintf(out, "\npresets (%d)\n  %s\n", len(warnings), strings.Join(warnings, "\n  "))
	}
	if len(r.Migrations) > 0 {
		fmt.Fprintf(out, "\nmigrated (%d)\n  %s\n", len(r.Migrations), strings.Join(r.Migrations, "\n  "))
	}
	return nil
}
