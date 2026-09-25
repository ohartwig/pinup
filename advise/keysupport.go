// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package advise analyses a resolved configuration and reports what to
// change: findings with a severity, a pointer, the origin that wrote the
// value and, where the file owns it, a fix. It decides nothing about a
// repository - the plan does that - it reads the configuration the way a
// run resolves it and says what the run will make of it.
package advise

// Support classifies a configuration key by what this version of pinup does
// with it. The table is the report: every key a configuration uses lands in
// exactly one class, and an unsupported one is named rather than dropped.
type Support string

const (
	Supported   Support = "supported"
	Partial     Support = "partial"
	Unsupported Support = "unsupported"
)

// KeySupport is the classification of top-level keys and packageRules keys.
// A key not listed is unsupported: an unknown key is one nothing reads.
var KeySupport = map[string]Support{
	// Resolution and matching.
	"extends": Supported, "description": Supported, "packageRules": Supported, "customManagers": Supported,
	"customDatasources": Supported, "enabledManagers": Supported, "ignorePaths": Supported,
	"matchPackageNames": Supported, "matchDepNames": Supported, "matchDatasources": Supported, "matchManagers": Supported,
	"matchDepTypes": Supported, "matchUpdateTypes": Supported, "matchCurrentValue": Supported, "matchCurrentVersion": Partial,
	"matchFileNames": Supported, "matchSourceUrls": Partial, "matchJsonata": Partial, "matchCategories": Unsupported,
	"matchEffective": Supported, "analyze": Supported, "trustEffective": Supported,
	// Decisions.
	"enabled": Supported, "minimumReleaseAge": Supported, "minimumReleaseAgeBehaviour": Supported, "schedule": Supported,
	"timezone": Supported, "automerge": Supported, "automergeDirect": Supported, "dependencyDashboardApproval": Supported, "groupName": Supported,
	"groupSlug": Supported, "versioning": Supported, "registryUrls": Supported, "extractVersion": Supported,
	"ignoreUnstable": Supported, "prHourlyLimit": Supported, "prConcurrentLimit": Supported, "prConcurrentLimitIgnoreLabels": Supported, "labels": Supported,
	"ignoreDeps": Supported, "allowedVersions": Supported, "rangeStrategy": Supported, "separateMajorMinor": Supported,
	"separateMinorPatch": Partial, "separateMultipleMajor": Partial, "pinDigests": Supported,
	"lockFileMaintenance": Supported, "osvVulnerabilityAlerts": Supported, "vulnerabilityAlerts": Supported,
	"postUpgradeTasks": Supported, "allowedCommands": Supported,
	// postUpdateOptions: gomodTidy is what the gomod lock refresh does anyway; the other options are unread.
	"postUpdateOptions": Partial, "prBodyDefinitions": Supported, "prBodyNotes": Supported, "addLabels": Supported,
	// Release notes come from the forge, never a third-party service; "off" per rule is honoured.
	"fetchChangeLogs": Supported, "internalChecksFilter": Supported, "dependencyDashboard": Supported,
	"dependencyDashboardTitle": Supported,
	// Publishing.
	"commitBody": Supported, "prCreation": Partial, "rebaseWhen": Partial, "platformAutomerge": Supported,
	"semanticCommitType": Supported, "semanticCommitScope": Supported, "commitMessageTopic": Supported,
	"commitMessageExtra": Supported, "commitMessageAction": Supported, "branchPrefix": Supported, "branchPrefixOld": Supported, "branchTopic": Supported,
	"additionalBranchPrefix": Supported, "commitMessagePrefix": Supported, "commitMessageSuffix": Supported,
	"commitMessageLowerCase": Supported, "semanticCommits": Supported,
	// The per-update-type objects the planner overlays (planner.Overlay).
	"major": Supported, "minor": Supported, "patch": Supported, "pin": Supported, "digest": Supported,
	"pinDigest": Supported, "rollback": Supported, "replacement": Supported,
	// Read into the template variables, decided by separateMajorMinor alone.
	"separateMultipleMinor": Partial,
	"executionTimeout":      Partial, "$schema": Supported,
}
