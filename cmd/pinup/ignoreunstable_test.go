// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/wire"
)

// ignoreUnstable reaches the dependency. It was listed as supported and
// read by nothing: the planner skipped prereleases for every stable pin
// whatever a rule said. The resolved default (true) keeps them out, a
// rule's false lets them in for the packages it names, and a later rule
// writing true again wins.
func TestIgnoreUnstableReachesTheDependency(t *testing.T) {
	engine, err := rules.Compile([]any{
		map[string]any{"matchPackageNames": []any{"crowdsec-firewall-bouncer", "lint"}, "ignoreUnstable": false},
		map[string]any{"matchPackageNames": []any{"lint"}, "ignoreUnstable": true},
	}, wire.Versionings())
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"ignoreUnstable": true}
	for _, c := range []struct {
		name string
		want bool
	}{
		{"crowdsec-firewall-bouncer", true},
		{"lint", false},
		{"unmatched", false},
	} {
		d := applyDepRules(engine, base, model.Dependency{DepName: c.name, Datasource: "custom.koh-apk", CurrentValue: "1.0.0-r0"})
		if d.AllowUnstable != c.want {
			t.Errorf("%s: AllowUnstable %v, want %v", c.name, d.AllowUnstable, c.want)
		}
	}
}
