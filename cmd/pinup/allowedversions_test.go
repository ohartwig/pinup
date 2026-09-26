// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/wire"
)

// The rule that sets allowedVersions travels with it, so the planner can
// name who holds a dependency back; a later rule that writes the key again
// is the one named.
func TestAllowedVersionsCarriesItsRule(t *testing.T) {
	engine, err := rules.Compile([]any{
		map[string]any{"matchPackageNames": []any{"php"}, "allowedVersions": "<9"},
		map[string]any{"matchPackageNames": []any{"other"}, "allowedVersions": "<2"},
		map[string]any{"matchPackageNames": []any{"php"}, "allowedVersions": "/^8\\.2\\./"},
	}, wire.Versionings())
	if err != nil {
		t.Fatal(err)
	}
	d := applyDepRules(engine, map[string]any{}, model.Dependency{DepName: "php", Datasource: "docker", CurrentValue: "8.2.30"})
	if d.AllowedVersions != `/^8\.2\./` || d.AllowedVersionsBy != "packageRules[2]" {
		t.Errorf("allowedVersions %q by %q; want /^8\\.2\\./ by packageRules[2]", d.AllowedVersions, d.AllowedVersionsBy)
	}
	d = applyDepRules(engine, map[string]any{}, model.Dependency{DepName: "unmatched", Datasource: "docker", CurrentValue: "1.0.0"})
	if d.AllowedVersions != "" || d.AllowedVersionsBy != "" {
		t.Errorf("no rule, yet allowedVersions %q by %q", d.AllowedVersions, d.AllowedVersionsBy)
	}
}

// A rule can move where a dependency is looked up. composer's php resolves
// against the upstream PHP release by default; the estate points it at the
// Wolfi package its images pin, one rule per PHP line, with the apk revision
// cut off. extractVersion in a rule was reported as supported and never
// applied until then.
func TestPackageRuleOverridesTheLookup(t *testing.T) {
	engine, err := rules.Compile([]any{
		map[string]any{"matchManagers": []any{"composer"}, "matchDepNames": []any{"php"}, "matchCurrentValue": "/^[~^>=]*8\\.5\\./",
			"overrideDatasource": "custom.wolfi", "overridePackageName": "php-frankenphp-8.5", "extractVersion": "^(?<version>\\d+\\.\\d+\\.\\d+)"},
	}, wire.Versionings())
	if err != nil {
		t.Fatal(err)
	}
	php := model.Dependency{Manager: "composer", CustomManager: model.NoCustomManager, DepName: "php", PackageName: "containerbase/php-prebuild", Datasource: "github-tags",
		CurrentValue: "^8.5.10", RegistryURLs: []string{"https://example.test"}}
	got := applyDepRules(engine, map[string]any{}, php)
	if got.Datasource != "custom.wolfi" || got.PackageName != "php-frankenphp-8.5" || got.ExtractVersion == "" || got.RegistryURLs != nil {
		t.Errorf("php looked up at %s/%s extract %q urls %v; want custom.wolfi/php-frankenphp-8.5 with the revision cut, no urls",
			got.Datasource, got.PackageName, got.ExtractVersion, got.RegistryURLs)
	}
	other := php
	other.CurrentValue = "^8.4.20"
	if got := applyDepRules(engine, map[string]any{}, other); got.Datasource != "github-tags" || got.PackageName != "containerbase/php-prebuild" {
		t.Errorf("a php on another line moved to %s/%s", got.Datasource, got.PackageName)
	}
}
