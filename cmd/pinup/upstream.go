// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohartwig/pinup/cvelist"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/planner"
	"github.com/ohartwig/pinup/versioning"
	"github.com/ohartwig/pinup/wire"
)

// upstreamSources answer, for a dependency that follows a repository's
// tags (github-tags, github-releases, git-tags), what no package ecosystem
// can: whether the version in use is affected by a known vulnerability
// and which release fixes it. Such a dependency has no OSV ecosystem, so
// checkAdvisories never asks about it, and the container scanner judges the
// package built from it against its distribution's advisories only. Two
// sources speak for it:
//
//   - OSV's GIT ecosystem, asked by repository URL and tag
//     (osvVulnerabilityAlerts). A tag is fixed by the lowest newer tag OSV
//     no longer lists - and only where OSV analysed the record after that
//     tag existed: a tag younger than the record is absent because OSV has
//     not looked, not because it is unaffected.
//   - The release notes of the newer releases (releaseNotesAdvisories). A
//     CVE id a note names counts once the CVE List confirms it: published,
//     naming the product, and published after the version in use was
//     released. The note alone is never enough - whoever controls a release
//     writes its notes, and the release age exists to hold exactly such a
//     release.
//
// A finding is written on the dependency as checkAdvisories writes OSV's:
// its advisories and the highest fix as the vulnerability bound, so the
// planner takes the security fast path. Every failure is a warning and
// means no fast path, never a held or failed update.
type upstreamSources struct {
	osv   upstreamOSV
	notes releaseLister
	cves  cveReader
}

// upstreamOSV is the part of osv.Client the GIT lookup uses.
type upstreamOSV interface {
	TagAdvisories(ctx context.Context, repo string, tags []string) ([][]osv.TagHit, error)
	Record(ctx context.Context, id string, modified time.Time) (osv.Advisory, error)
}

// releaseLister is the part of changelog.Fetcher the release notes come
// from.
type releaseLister interface {
	Releases(ctx context.Context, sourceURL string, v versioning.Versioning, version func(tag string) (string, bool), current string) ([]model.ReleaseNote, error)
}

// cveReader is the part of cvelist.Client a confirmation reads.
type cveReader interface {
	Get(ctx context.Context, id string) (cvelist.Record, error)
}

// upstreamDatasources are the datasources whose releases are a
// repository's tags. gitlab-* are left out: on this estate they are our
// own projects, which publish no advisories of their own.
var upstreamDatasources = map[string]bool{"github-tags": true, "github-releases": true, "git-tags": true}

// upstreamCandidates bounds how many newer releases one dependency asks
// about: the nearest ones, where a fix for the version in use lands.
const upstreamCandidates = 20

// noteCVEWindow is how much older than the release that names it a CVE
// record may be. A fix's record is published with the release or after
// it (ClamAV 1.5.5: the tag at 16:09 UTC, Cisco's record at 16:30); a
// record days older that a new release cites is not that release's news,
// and is what a forged release would carry to skip the release age - an
// active product nearly always has some recent CVE to cite. The CVE
// record's affected versions cannot stand in: Cisco's ClamAV records list
// Cisco Secure Endpoint's versions, not ClamAV's.
const noteCVEWindow = 72 * time.Hour

// Source names a dependency's coverage records for the two lookups.
const (
	sourceOSVGit       = "osv-git"
	sourceReleaseNotes = "release-notes"
)

// upstreamCandidate is a newer release of the dependency: its tag, the
// version the planner reads from it, and since when it is known to exist.
// latest is the later of the forge's timestamp and pinup's own first
// sighting: a tag's commit date is whatever its author wrote, the first
// sighting is not, so a backdated tag cannot pass for an old release.
type upstreamCandidate struct {
	tag, version string
	seen, latest time.Time
}

// checkUpstreamAdvisories runs both lookups over deps, after the advisory
// database and before the withdrawal lists: it raises a bound
// checkAdvisories set, never lowers it.
func checkUpstreamAdvisories(ctx context.Context, src *upstreamSources, cfg map[string]any, deps []model.Dependency, coverage *model.AdvisoryCoverage, defaultVersioning func(string) string, releasesOf func(model.Dependency) *model.ReleaseSet) []model.Warning {
	if src == nil {
		return nil
	}
	if va, ok := cfg["vulnerabilityAlerts"].(map[string]any); ok {
		if enabled, ok := va["enabled"].(bool); ok && !enabled {
			return nil
		}
	}
	askOSV, _ := cfg["osvVulnerabilityAlerts"].(bool)
	askNotes, _ := cfg["releaseNotesAdvisories"].(bool)
	askOSV = askOSV && src.osv != nil
	askNotes = askNotes && src.notes != nil && src.cves != nil
	if !askOSV && !askNotes {
		return nil
	}
	var warns []model.Warning
	seen := map[string]bool{}
	warn := func(d *model.Dependency, msg string) {
		if !seen[msg] {
			seen[msg] = true
			warns = append(warns, model.Warning{Stage: "lookup", File: d.File, Msg: msg})
		}
	}
	schemes := wire.Versionings()
	for i := range deps {
		d := &deps[i]
		if !upstreamDatasources[d.Datasource] || (d.SkipReason != "" && d.SkipReason != d.Disabled) {
			continue
		}
		rs := releasesOf(*d)
		if rs == nil || rs.Err != "" {
			continue
		}
		scheme := cmp.Or(d.Versioning, defaultVersioning(d.Datasource))
		v, err := schemes.Get(scheme)
		if err != nil {
			continue
		}
		extract, err := planner.VersionExtractor(d.ExtractVersion)
		if err != nil {
			continue // the planner reports the pattern
		}
		current := cmp.Or(d.LockedVersion, d.CurrentValue)
		if !v.IsVersion(current) {
			continue
		}
		cur, cands := upstreamTags(v, d, rs, extract, current)
		if cur.tag == "" || len(cands) == 0 {
			// Nothing to move to, or the version in use is not among the
			// tags: there is no fix to find.
			continue
		}
		var found []model.Advisory
		var sources []string
		if askOSV {
			if repo := osv.RepoURL(cmp.Or(rs.SourceURL, d.SourceURL, gitTagsURL(d))); repo != "" {
				adv, err := gitAdvisories(ctx, src.osv, repo, cur, cands)
				if err != nil {
					warn(d, fmt.Sprintf("%s: advisory database (GIT %s): %v; planned without its advisories", d.DepName, repo, err))
				} else {
					sources = append(sources, sourceOSVGit)
					found = append(found, adv...)
				}
			}
		}
		if askNotes && d.Datasource != "git-tags" {
			if sourceURL := cmp.Or(rs.SourceURL, d.SourceURL); sourceURL != "" {
				adv, ws, err := noteAdvisories(ctx, src, v, extract, sourceURL, productName(d), current, cur, cands)
				for _, w := range ws {
					warn(d, d.DepName+": "+w)
				}
				if err != nil {
					warn(d, fmt.Sprintf("%s: release notes: %v; planned without the advisories they name", d.DepName, err))
				} else {
					sources = append(sources, sourceReleaseNotes)
					found = append(found, adv...)
				}
			}
		}
		if len(sources) > 0 {
			coverUpstream(coverage, d, sources)
		}
		bound := d.VulnerabilityBound
		for _, a := range found {
			if knownAdvisory(d.Advisories, a) {
				continue
			}
			d.Advisories = append(d.Advisories, a)
			if a.Fixed != "" && (bound == "" || !v.IsValid(bound) || v.Compare(a.Fixed, bound) > 0) {
				bound = a.Fixed
			}
		}
		d.VulnerabilityBound = bound
	}
	return warns
}

// upstreamTags finds the tag of the version in use and the newer releases
// a fix could be in, nearest first. A stable version looks at stable
// releases only, unless a rule lets prereleases in - the same choice the
// planner's fast path makes.
func upstreamTags(v versioning.Versioning, d *model.Dependency, rs *model.ReleaseSet, extract func(string) (string, bool), current string) (cur upstreamCandidate, cands []upstreamCandidate) {
	for _, r := range rs.Releases {
		ver, ok := extract(r.Version)
		if !ok || !v.IsVersion(ver) {
			continue
		}
		seen := r.Timestamp
		if seen.IsZero() {
			seen = r.FirstSeen
		}
		latest := seen
		if r.FirstSeen.After(latest) {
			latest = r.FirstSeen
		}
		switch c := v.Compare(ver, current); {
		case c == 0 && cur.tag == "":
			cur = upstreamCandidate{tag: r.Version, version: ver, seen: seen, latest: latest}
		case c > 0 && !r.Deprecated && (!v.IsStable(current) || v.IsStable(ver) || d.AllowUnstable):
			cands = append(cands, upstreamCandidate{tag: r.Version, version: ver, seen: seen, latest: latest})
		}
	}
	slices.SortFunc(cands, func(a, b upstreamCandidate) int { return v.Compare(a.version, b.version) })
	if len(cands) > upstreamCandidates {
		cands = cands[:upstreamCandidates]
	}
	return cur, cands
}

// gitAdvisories asks OSV's GIT ecosystem about the tag in use and the
// newer ones in one batch. An advisory listing the tag in use is fixed by
// the lowest newer tag OSV does not list, provided OSV analysed the record
// after it knew that tag; with no such tag the advisory is recorded without
// a fix, which plans nothing.
func gitAdvisories(ctx context.Context, client upstreamOSV, repo string, cur upstreamCandidate, cands []upstreamCandidate) ([]model.Advisory, error) {
	tags := make([]string, 0, 1+len(cands))
	tags = append(tags, cur.tag)
	for _, c := range cands {
		tags = append(tags, c.tag)
	}
	hits, err := client.TagAdvisories(ctx, repo, tags)
	if err != nil {
		return nil, err
	}
	if len(hits) != len(tags) {
		return nil, fmt.Errorf("answered %d of %d tags", len(hits), len(tags))
	}
	var out []model.Advisory
	for _, h := range hits[0] {
		fixed := ""
		for k, c := range cands {
			if slices.ContainsFunc(hits[k+1], func(o osv.TagHit) bool { return o.ID == h.ID }) {
				continue // still affected
			}
			if c.seen.IsZero() || h.Modified.IsZero() || h.Modified.Before(c.seen) {
				continue // OSV has not looked since this tag was made
			}
			fixed = c.version
			break
		}
		a := model.Advisory{ID: h.ID, Fixed: fixed}
		// The record's details are for the plan and the merge request;
		// without them the id still stands.
		if rec, err := client.Record(ctx, h.ID, h.Modified); err == nil {
			a.Aliases, a.Summary, a.Severity, a.Published = rec.Aliases, rec.Summary, rec.Severity, rec.Published
		}
		out = append(out, a)
	}
	return out, nil
}

// noteAdvisories reads the newer releases' notes for CVE ids and keeps the
// ones the CVE List confirms. A CVE is fixed by the lowest release whose
// note names it. It is not taken when a note at or below the version in
// use names it already - the fix is in - or when the record was published
// before the version in use was released: an old CVE id written into a
// new note is not news about this version, and is what a forged note
// would carry to skip the release age. For the same reason the record may
// be at most noteCVEWindow older than the release that names it. A version
// in use whose release date is unknown confirms nothing.
func noteAdvisories(ctx context.Context, src *upstreamSources, v versioning.Versioning, extract func(string) (string, bool), sourceURL, product, current string, cur upstreamCandidate, cands []upstreamCandidate) ([]model.Advisory, []string, error) {
	notes, err := src.notes.Releases(ctx, sourceURL, v, extract, current)
	if err != nil && notes == nil {
		return nil, nil, err
	}
	byTag := map[string]model.ReleaseNote{}
	named := map[string]bool{}
	for _, n := range notes {
		byTag[n.Version] = n
		if ver, ok := extract(n.Version); ok && v.IsVersion(ver) && v.Compare(ver, current) <= 0 {
			for _, id := range cvelist.IDs(n.Body) {
				named[id] = true
			}
		}
	}
	released := cur.seen
	if n, ok := byTag[cur.tag]; ok && !n.Published.IsZero() && (released.IsZero() || n.Published.Before(released)) {
		released = n.Published
	}
	// A cache write that failed leaves the notes in hand: said, and used.
	var warns []string
	if err != nil {
		warns = append(warns, err.Error())
	}
	if released.IsZero() || product == "" {
		return nil, warns, nil
	}
	var out []model.Advisory
	done := map[string]bool{}
	for _, c := range cands {
		n, ok := byTag[c.tag]
		if !ok {
			continue
		}
		// The release's own time: the latest of the tag, pinup's first
		// sighting and the forge's publication of the release.
		releasedAt := c.latest
		if n.Published.After(releasedAt) {
			releasedAt = n.Published
		}
		for _, id := range cvelist.IDs(n.Body) {
			if named[id] || done[id] {
				continue
			}
			done[id] = true
			rec, err := src.cves.Get(ctx, id)
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s named by %s's notes not confirmed: %v", id, c.tag, err))
				continue
			}
			if rec.State != cvelist.Published || !rec.Mentions(product) || rec.Published.IsZero() || rec.Published.Before(released) {
				continue
			}
			if !releasedAt.IsZero() && rec.Published.Before(releasedAt.Add(-noteCVEWindow)) {
				continue // older than the release can explain
			}
			out = append(out, model.Advisory{ID: id, Summary: rec.Title, Severity: rec.Severity, Fixed: c.version, Published: rec.Published})
		}
	}
	return out, warns, nil
}

// productName is the name a CVE record calls the dependency's product: the
// repository's own name, "clamav" for Cisco-Talos/clamav.
func productName(d *model.Dependency) string {
	name := cmp.Or(d.PackageName, d.DepName)
	name = strings.TrimSuffix(strings.TrimRight(name, "/"), ".git")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// gitTagsURL is a git-tags dependency's repository: its package name is
// the clone URL.
func gitTagsURL(d *model.Dependency) string {
	if d.Datasource != "git-tags" {
		return ""
	}
	return cmp.Or(d.PackageName, d.DepName)
}

// knownAdvisory reports whether a is among have already, under its id or
// an alias - OSV's GO-… record and its CVE, the CVE a note names and OSV's
// record for it.
func knownAdvisory(have []model.Advisory, a model.Advisory) bool {
	ids := append([]string{a.ID}, a.Aliases...)
	for _, h := range have {
		for _, id := range ids {
			if strings.EqualFold(h.ID, id) || slices.ContainsFunc(h.Aliases, func(x string) bool { return strings.EqualFold(x, id) }) {
				return true
			}
		}
	}
	return false
}

// coverUpstream records that an upstream source was asked about d: a
// dependency checkAdvisories counted as not covered moves to covered.
func coverUpstream(c *model.AdvisoryCoverage, d *model.Dependency, sources []string) {
	if d.AdvisoryCoverage != nil && d.AdvisoryCoverage.State == model.CoveredByAdvisories {
		for _, s := range sources {
			if !slices.Contains(d.AdvisoryCoverage.Sources, s) {
				d.AdvisoryCoverage.Sources = append(d.AdvisoryCoverage.Sources, s)
			}
		}
		return
	}
	if c != nil && d.AdvisoryCoverage != nil && d.AdvisoryCoverage.State == model.NotCovered {
		if c.NotCovered[d.Datasource]--; c.NotCovered[d.Datasource] <= 0 {
			delete(c.NotCovered, d.Datasource)
		}
		if r := d.AdvisoryCoverage.Reason; r != "" {
			if c.Reasons[r]--; c.Reasons[r] <= 0 {
				delete(c.Reasons, r)
			}
		}
	}
	if c != nil {
		c.Asked++
	}
	d.AdvisoryCoverage = &model.DependencyCoverage{State: model.CoveredByAdvisories, Sources: slices.Clone(sources)}
}
