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
