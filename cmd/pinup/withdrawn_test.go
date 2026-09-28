// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

// A dependency on a withdrawn version gets the entry's ids as advisories,
// marked withdrawn, and the replacement as its vulnerability bound - the
// planner's security fast path from there. Without the replacement the
// newest stable release above takes its place; a database bound is raised,
// never lowered; vulnerabilityAlerts.enabled: false turns it off; a
// version nobody withdrew is left alone (koh wolfi-packages, 2026-09-28).
func TestWithdrawnVersionsAreMovedOffLikeAnAdvisory(t *testing.T) {
	rs := func(replacement string, versions ...string) *model.ReleaseSet {
		r := &model.ReleaseSet{Datasource: "custom.koh-apk", Withdrawn: []model.Withdrawal{{
			Version: "1.19.2-r6", IDs: []string{"CVE-2026-33997", "CVE-2026-34040"}, Replacement: replacement, Reason: "fixable HIGH",
		}}}
		for _, v := range versions {
			r.Releases = append(r.Releases, model.Release{Version: v})
		}
		return r
	}
	dep := func(cur string) model.Dependency {
		return model.Dependency{DepName: "grafana-alloy", Datasource: "custom.koh-apk", CurrentValue: cur, Versioning: "apk"}
	}
	apk := func(string) string { return "apk" }
	for _, c := range []struct {
		name  string
		cfg   map[string]any
		dep   model.Dependency
		rs    *model.ReleaseSet
		bound string
		ids   int
	}{
		{"replacement carried", map[string]any{}, dep("1.19.2-r6"), rs("1.20.0-r0", "1.20.0-r0", "1.21.0-r0"), "1.20.0-r0", 2},
		{"replacement absent: newest stable above", map[string]any{}, dep("1.19.2-r6"), rs("1.20.0-r0", "1.20.1-r0", "1.21.0-r0", "1.22.0_rc1-r0"), "1.21.0-r0", 2},
		{"nothing above: advisory, no bound", map[string]any{}, dep("1.19.2-r6"), rs("1.20.0-r0"), "", 2},
		{"not withdrawn", map[string]any{}, dep("1.19.1-r0"), rs("1.20.0-r0", "1.20.0-r0"), "", 0},
		{"alerts off", map[string]any{"vulnerabilityAlerts": map[string]any{"enabled": false}}, dep("1.19.2-r6"), rs("1.20.0-r0", "1.20.0-r0"), "", 0},
	} {
		deps := []model.Dependency{c.dep}
		warns := applyWithdrawals(c.cfg, deps, apk, func(model.Dependency) *model.ReleaseSet { return c.rs })
		d := deps[0]
		if d.VulnerabilityBound != c.bound || len(d.Advisories) != c.ids {
			t.Errorf("%s: bound %q advisories %d, want %q and %d", c.name, d.VulnerabilityBound, len(d.Advisories), c.bound, c.ids)
		}
		for _, a := range d.Advisories {
			if !a.Withdrawn || a.Fixed != c.bound {
				t.Errorf("%s: advisory %+v not marked withdrawn with the bound", c.name, a)
			}
		}
		if c.ids > 0 && !strings.HasPrefix(d.WithdrawnNote(), "withdrawn: CVE-2026-33997, CVE-2026-34040") {
			t.Errorf("%s: note %q", c.name, d.WithdrawnNote())
		}
		if c.name == "nothing above: advisory, no bound" && len(warns) != 1 {
			t.Errorf("%s: warnings %v", c.name, warns)
		}
	}

	// A database bound above the replacement stays; one below is raised.
	for _, c := range []struct{ osv, want string }{{"1.21.0-r0", "1.21.0-r0"}, {"1.19.9-r0", "1.20.0-r0"}} {
		d := dep("1.19.2-r6")
		d.VulnerabilityBound = c.osv
		deps := []model.Dependency{d}
		applyWithdrawals(map[string]any{}, deps, apk, func(model.Dependency) *model.ReleaseSet { return rs("1.20.0-r0", "1.20.0-r0", "1.21.0-r0") })
		if deps[0].VulnerabilityBound != c.want {
			t.Errorf("database bound %s: got %s, want %s", c.osv, deps[0].VulnerabilityBound, c.want)
		}
	}

	// An unreadable list is one warning per message, not one per dependency.
	broken := &model.ReleaseSet{Datasource: "custom.koh-apk", WithdrawnErr: "withdrawal list x: 500"}
	deps := []model.Dependency{dep("1.0.0-r0"), dep("1.0.0-r0")}
	if warns := applyWithdrawals(map[string]any{}, deps, apk, func(model.Dependency) *model.ReleaseSet { return broken }); len(warns) != 1 {
		t.Errorf("warnings for an unreadable list: %v", warns)
	}
}
