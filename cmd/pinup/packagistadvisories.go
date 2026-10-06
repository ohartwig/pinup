// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/packagistadv"
	"github.com/ohartwig/pinup/versioning"
)

// advisorySources asks OSV about every dependency and, for composer
// packages, Packagist's security advisories as well - the list `composer
// audit` reads, which carries a TYPO3-CORE-SA within a day or two while
// OSV follows days to weeks later (packagistadv's package comment has the
// measurements). A Packagist advisory is merged into the OSV finding for the
// same query, so it takes exactly the path an OSV advisory takes:
// vulnerabilityAlerts, the security label, no soak, KEV by its CVE.
//
// Packagist that cannot be reached is a warning on the run, never a failed
// run; OSV's own answer stands.
type advisorySources struct {
	osv       advisoryChecker
	packagist *packagistadv.Client
	// private is the installation's own advisory feed
	// (PINUP_PRIVATE_ADVISORIES); nil or without sources asks nothing.
	private *osv.PrivateFeed
	// releases, when set, names a composer package's known releases, so a
	// fix is the lowest release the advisory no longer covers rather than
	// the bound its range names.
	releases func(pkg string) []string
}

// sourcesFor names the sources a query about datasource reaches: OSV for
// every datasource it has an ecosystem for, Packagist beside it for
// composer packages when the run reads Packagist.
func (s advisorySources) sourcesFor(datasource string) []string {
	var out []string
	if osv.Ecosystem(datasource) != "" || strings.HasPrefix(datasource, "custom.") {
		out = append(out, "osv")
	}
	if datasource == "packagist" && s.packagist != nil {
		out = append(out, "packagist")
	}
	if s.private.Asks(datasource) {
		out = append(out, osv.PrivateName)
	}
	return out
}

// asks reports whether any source is asked about q: OSV by its ecosystem,
// the private feed by its naming. A gitlab-* dependency has no OSV
// ecosystem; with a private feed it is asked all the same.
func (s advisorySources) asks(q osv.Query) bool {
	return osv.Asks(q) || s.private.Asks(q.Datasource)
}

// withReleases is the run's release knowledge for one call.
func (s advisorySources) withReleases(releases func(pkg string) []string) advisoryChecker {
	s.releases = releases
	return s
}

// releaseAware is a checker that can use release sets for its fixes.
type releaseAware interface {
	withReleases(func(pkg string) []string) advisoryChecker
}

func (s advisorySources) Check(ctx context.Context, vs versioning.Registry, queries []osv.Query) ([]osv.Finding, error) {
	findings, err := s.checkPublic(ctx, vs, queries)
	if err != nil {
		return findings, err
	}
	s.checkPrivate(ctx, vs, queries, findings)
	return findings, nil
}

// checkPrivate adds the private feed's advisories to the findings, as
// mergePackagist adds Packagist's: an advisory a finding already carries
// under one of its ids is not listed twice, and the bound is recomputed.
// A finding the feed answers is marked queried even when OSV has no
// ecosystem for it - a gitlab-* dependency the feed covers.
func (s advisorySources) checkPrivate(ctx context.Context, vs versioning.Registry, queries []osv.Query, findings []osv.Finding) {
	if s.private == nil || len(s.private.Sources) == 0 {
		return
	}
	var pq []osv.PrivateQuery
	var at []int
	for i, q := range queries {
		if s.private.Asks(q.Datasource) {
			pq = append(pq, osv.PrivateQuery{Datasource: q.Datasource, PackageName: q.PackageName, Version: q.Version, Versioning: q.Versioning})
			at = append(at, i)
		}
	}
	if len(pq) == 0 {
		return
	}
	answers, warns := s.private.Check(ctx, vs, pq)
	for k, i := range at {
		f := &findings[i]
		if k == 0 {
			f.Warnings = append(f.Warnings, warns...)
		}
		if f.Ecosystem == "" {
			f.Ecosystem = osv.PrivateName
		}
		known := map[string]bool{}
		for _, a := range f.Advisories {
			known[strings.ToUpper(a.ID)] = true
			for _, al := range a.Aliases {
				known[strings.ToUpper(al)] = true
			}
		}
		v, err := vs.Get(queries[i].Versioning)
		if err != nil {
			continue
		}
		for _, a := range answers[k] {
			carried := known[strings.ToUpper(a.ID)]
			for _, al := range a.Aliases {
				carried = carried || known[strings.ToUpper(al)]
			}
			if carried {
				continue
			}
			f.Advisories = append(f.Advisories, a)
			if a.Fixed != "" && (f.Bound == "" || v.Compare(a.Fixed, f.Bound) > 0) {
				f.Bound = a.Fixed
			}
		}
	}
}

// checkPublic asks OSV and, for composer packages, Packagist.
func (s advisorySources) checkPublic(ctx context.Context, vs versioning.Registry, queries []osv.Query) ([]osv.Finding, error) {
	findings, err := s.osv.Check(ctx, vs, queries)
	if err != nil || s.packagist == nil {
		return findings, err
	}
	var names []string
	for i, q := range queries {
		if packagistQueried(findings[i], q, vs) {
			names = append(names, q.PackageName)
		}
	}
	if len(names) == 0 {
		return findings, nil
	}
	byPkg, err := s.packagist.ForPackages(ctx, names)
	if err != nil {
		for i, q := range queries {
			if packagistQueried(findings[i], q, vs) {
				findings[i].Warnings = append(findings[i].Warnings, fmt.Sprintf("%v; composer packages are checked against OSV only", err))
				break
			}
		}
		return findings, nil
	}
	mergePackagist(findings, queries, byPkg, vs, s.releases)
	return findings, nil
}

// packagistQueried reports whether a query is one Packagist is asked about:
// a packagist dependency at a single version - a range such as "^1.2" is
// not asked, as OSV does not ask it either.
func packagistQueried(_ osv.Finding, q osv.Query, vs versioning.Registry) bool {
	if q.Datasource != "packagist" {
		return false
	}
	v, err := vs.Get(q.Versioning)
	return err == nil && v.IsVersion(q.Version)
}

// mergePackagist adds every Packagist advisory that covers a query's version
// to its finding, unless the finding already carries it from OSV under one
// of its ids (PKSA, CVE, GHSA), and recomputes the finding's bound.
func mergePackagist(findings []osv.Finding, queries []osv.Query, byPkg map[string][]packagistadv.Advisory, vs versioning.Registry, releases func(string) []string) {
	for i, q := range queries {
		f := &findings[i]
		if !packagistQueried(*f, q, vs) {
			continue
		}
		v, _ := vs.Get(q.Versioning)
		known := map[string]bool{}
		for _, a := range f.Advisories {
			known[strings.ToUpper(a.ID)] = true
			for _, al := range a.Aliases {
				known[strings.ToUpper(al)] = true
			}
		}
		var rs []string
		if releases != nil {
			rs = releases(q.PackageName)
		}
		for _, a := range byPkg[q.PackageName] {
			if !packagistadv.Affects(a, q.Version, v) {
				continue
			}
			dup := known[strings.ToUpper(a.ID)]
			for _, al := range a.Aliases() {
				dup = dup || known[strings.ToUpper(al)]
			}
			if dup {
				continue
			}
			f.Advisories = append(f.Advisories, osv.Advisory{
				ID: a.ID, Aliases: a.Aliases(), Summary: a.Title, Severity: strings.ToUpper(a.Severity),
				Published: a.Reported, Modified: a.Reported, Fixed: packagistadv.Fixed(a, q.Version, v, rs),
			})
			known[strings.ToUpper(a.ID)] = true
		}
		f.Bound = ""
		for _, a := range f.Advisories {
			if a.Fixed != "" && (f.Bound == "" || v.Compare(a.Fixed, f.Bound) > 0) {
				f.Bound = a.Fixed
			}
		}
	}
}
