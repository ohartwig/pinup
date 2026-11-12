// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/versioning"
)

// A bump of a direct dependency can be the only way a transitive package
// leaves a vulnerable version: on 2026-11-12 @vitest/mocker 4.1.11 in
// development/moselwal/collab-rte carried GHSA-qxg6-89mg-rqv8 and
// GHSA-xp26-3454-mjgp, both fixed in 4.1.12, and vitest 4.1.11 requires it
// at exactly 4.1.11 - no lock refresh could move it. vitest 4.1.12, which
// requires 4.1.12, came as a routine member of the npm group, waited for the
// group's four-hourly window and was titled like any other bump. The lock's
// advisory check (osvTransitiveAlerts) had the finding all along; nothing
// connected it to the bump.
//
// markTransitiveFixes connects them. For every bump of a dependency whose
// lock carries a transitive advisory, it asks the datasource what the new
// version requires and marks the update a security fix when that moves an
// advised package off its locked version onto a fixed one. The lock's tool
// is not run - the plan is made before any checkout is written - so the
// claim is checked after the refresh: the advised packages ride on the
// refresh as lifts, and the runner takes the [security] title back when
// none of them moved (runner.dropSecurityTitle).

// requirementsOf answers what one version of a dependency's package
// requires; lookup.Fetcher.Requirements in a run.
type requirementsOf func(d model.Dependency, version string) (map[string]string, error)

// markTransitiveFixes sets TransitiveFixes and SecurityFix on each update
// that moves a transitive package of its lock off an advisory, and returns
// a warning for each bump whose requirements could not be read. locks maps
// "manager|dir" to the lock beside a manifest, transitive the findings by
// "manager|lock path", as lockMaintenance reads them. A direct security
// fix, a lock refresh and an update without a new version are left alone.
func markTransitiveFixes(updates []model.Update, locks map[string]string, transitive map[string][]model.Advisory, requires requirementsOf, schemes versioning.Registry, defaultVersioning func(string) string) []model.Warning {
	if len(transitive) == 0 || requires == nil {
		return nil
	}
	var warns []model.Warning
	for i := range updates {
		u := &updates[i]
		if u.SecurityFix || u.Type == model.UpdateLockFileMaintenance || u.NewVersion == "" {
			continue
		}
		ds := lockDatasources[u.Dep.Manager]
		if ds == "" || u.Dep.Datasource != ds {
			continue
		}
		name := locks[u.Dep.Manager+"|"+filepath.Dir(u.Dep.File)]
		if name == "" {
			continue
		}
		found := transitive[u.Dep.Manager+"|"+filepath.Join(filepath.Dir(u.Dep.File), name)]
		if len(found) == 0 {
			continue
		}
		v, err := schemes.Get(defaultVersioning(ds))
		if err != nil {
			continue
		}
		req, err := requires(u.Dep, u.NewVersion)
		if err != nil {
			warns = append(warns, model.Warning{Stage: "lookup", File: u.Dep.File, Msg: fmt.Sprintf(
				"%s %s: requirements not read (%v); a transitive security fix this bump carries is not recognised", u.Dep.DepName, u.NewVersion, err)})
			continue
		}
		for _, a := range found {
			if movesToFix(v, req[a.Package], a.Installed, a.Fixed) {
				u.TransitiveFixes = append(u.TransitiveFixes, a)
			}
		}
		if len(u.TransitiveFixes) > 0 {
			u.SecurityFix = true
		}
	}
	return warns
}

// movesToFix reports whether a requirement of rng moves a package locked
// at installed onto a version at or above fixed. The installed version must
// fall outside the range - a range that still admits it leaves the lock
// where it is - and the range must admit a version at or above the fix
// that exists: the fix itself, or a version the range names. Every tool
// pinup refreshes a lock with takes the highest version a range admits for
// a package it has to move, so either is the floor of the outcome. A range
// the scheme cannot read (an alias, a URL, a workspace link) claims
// nothing.
func movesToFix(v versioning.Versioning, rng, installed, fixed string) bool {
	if rng == "" || fixed == "" || !v.IsValid(rng) || !v.IsVersion(installed) || !v.IsVersion(fixed) {
		return false
	}
	if v.Satisfies(installed, rng) {
		return false
	}
	for _, c := range append(versionTokens(rng), fixed) {
		if v.IsVersion(c) && v.Satisfies(c, rng) && v.Compare(c, fixed) >= 0 {
			return true
		}
	}
	return false
}

// versionTokens are the version-shaped words of a range: "^4.1.12" names
// 4.1.12, ">=2.6.3 <3" names 2.6.3 and 3. Operators and spaces separate
// them; a leading v stays, the scheme decides whether it reads one.
func versionTokens(rng string) []string {
	return strings.FieldsFunc(rng, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '+')
	})
}

// transitiveSecurityKeys are the vulnerabilityAlerts keys a bump that
// moves a transitive package takes: when it runs and how it is marked, as
// for a security lock refresh. Not the branch topic or the group - the
// bump stays where its rules put it, and a group that moves together
// (vitest and its @vitest/* family) stays together. Nor enabled or
// dependencyDashboardApproval: a rule that disabled the update or asked a
// person to approve it decided about this dependency, and a transitive
// advisory is no reason to overrule that.
var transitiveSecurityKeys = []string{"schedule", "minimumReleaseAge", "labels", "automerge", "prCreation", "commitMessageSuffix"}

func transitiveSecurityOverlay(cfg map[string]any) map[string]any {
	out := maps.Clone(cfg)
	va, ok := cfg["vulnerabilityAlerts"].(map[string]any)
	if !ok {
		return out
	}
	for _, k := range transitiveSecurityKeys {
		if v, ok := va[k]; ok {
			out[k] = v
		}
	}
	return out
}
