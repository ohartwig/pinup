// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/versioning"
	"github.com/ohartwig/pinup/wire"
)

// applyWithdrawals moves a dependency off a version its repository has
// withdrawn (model.ReleaseSet.Withdrawn) the way an advisory does: the
// entry's ids become the dependency's advisories, marked withdrawn, and
// the replacement its vulnerability bound, so the planner takes the
// security fast path - its own branch, no release age, no schedule, no
// dashboard approval, no merge request limits.
//
// The bound is the replacement when the repository carries it, otherwise
// the newest release above the current version; a withdrawn version with
// nothing above it is a warning, the advisory stays for the plan to show.
// It runs after the advisory database was asked, and raises that bound,
// never lowers it. vulnerabilityAlerts.enabled: false turns it off like
// the database path; the withdrawn versions stay out of the releases.
func applyWithdrawals(cfg map[string]any, deps []model.Dependency, defaultVersioning func(string) string, releasesOf func(model.Dependency) *model.ReleaseSet) []model.Warning {
	var warns []model.Warning
	seen := map[string]bool{}
	on := true
	if va, ok := cfg["vulnerabilityAlerts"].(map[string]any); ok {
		if enabled, ok := va["enabled"].(bool); ok && !enabled {
			on = false
		}
	}
	schemes := wire.Versionings()
	for i := range deps {
		d := &deps[i]
		if d.SkipReason != "" && d.SkipReason != d.Disabled {
			continue
		}
		rs := releasesOf(*d)
		if rs == nil {
			continue
		}
		if rs.WithdrawnErr != "" && !seen[rs.WithdrawnErr] {
			seen[rs.WithdrawnErr] = true
			warns = append(warns, model.Warning{Stage: "lookup", Msg: fmt.Sprintf("%s: %s; planned without it", rs.Datasource, rs.WithdrawnErr)})
		}
		if rs.WithdrawnWarn != "" && !seen[rs.WithdrawnWarn] {
			seen[rs.WithdrawnWarn] = true
			warns = append(warns, model.Warning{Stage: "lookup", Msg: fmt.Sprintf("%s: %s; planned with it anyway", rs.Datasource, rs.WithdrawnWarn)})
		}
		if !on || len(rs.Withdrawn) == 0 {
			continue
		}
		current := d.CurrentValue
		if d.LockedVersion != "" {
			current = d.LockedVersion
		}
		var w *model.Withdrawal
		for k := range rs.Withdrawn {
			if rs.Withdrawn[k].Version == current {
				w = &rs.Withdrawn[k]
				break
			}
		}
		if w == nil {
			continue
		}
		scheme := d.Versioning
		if scheme == "" {
			scheme = defaultVersioning(d.Datasource)
		}
		v, err := schemes.Get(scheme)
		if err != nil {
			continue
		}
		// A stable current version moves to a stable one: the fast path
		// passes over prereleases for it too - unless a rule lets them in,
		// or the repository's own replacement is one. Then a prerelease at
		// or above the replacement is a way out as well: the replacement's
		// exact revision may have rolled out of the index while a later
		// one of the same build is there (crowdsec-firewall-bouncer
		// 0.0.37-r3 -> 0.0.38_rc1-r1, carried as -r2, 2026-10-11).
		rcReplacement := v.IsVersion(w.Replacement) && !v.IsStable(w.Replacement)
		bound := ""
		var newer []string
		for _, r := range rs.Releases {
			if r.Version == w.Replacement {
				bound = w.Replacement
			}
			if !v.IsVersion(r.Version) || !v.IsVersion(current) || v.Compare(r.Version, current) <= 0 {
				continue
			}
			if !v.IsStable(current) || v.IsStable(r.Version) || d.AllowUnstable ||
				(rcReplacement && v.Compare(r.Version, w.Replacement) >= 0) {
				newer = append(newer, r.Version)
			}
		}
		if bound == "" {
			if latest, ok := versioning.Latest(v, newer); ok {
				bound = latest
			}
		}
		ids := w.IDs
		if len(ids) == 0 {
			ids = []string{"withdrawn"}
		}
		for _, id := range ids {
			d.Advisories = append(d.Advisories, model.Advisory{
				ID: id, Summary: "withdrawn by its repository: " + w.Reason, Fixed: bound, Published: w.Date, Withdrawn: true,
			})
		}
		if bound == "" {
			warns = append(warns, model.Warning{Stage: "lookup", File: d.File,
				Msg: fmt.Sprintf("%s %s is withdrawn and no release above it exists yet", d.DepName, current)})
			continue
		}
		if d.VulnerabilityBound == "" || !v.IsValid(d.VulnerabilityBound) || v.Compare(bound, d.VulnerabilityBound) > 0 {
			d.VulnerabilityBound = bound
		}
	}
	return warns
}
