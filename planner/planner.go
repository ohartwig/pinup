// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package planner turns a dependency plus its known releases into updates.
//
// Layer 2. It knows the versioning vocabulary and the plan model, and nothing
// about where releases came from or which datasources exist: the caller hands
// in a function that answers "what releases does this dependency have".
//
// The stage's one hard rule is the plan invariant: every dependency leaves
// here with either at least one update or a SkipReason. A dependency with
// neither is a silent "nothing happened", and model.Plan.Validate refuses
// it - so this package cannot produce such a plan by accident, only by
// writing the wrong string into SkipReason, which a test can read.
package planner

import (
	"fmt"
	"strings"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/re2x"
	"github.com/ohartwig/pinup/versioning"
)

// Request is everything the planner needs for one repository.
type Request struct {
	Deps []model.Dependency

	// Releases answers for one dependency. nil means the dependency was not
	// looked up at all, which is recorded as a skip, not as "up to date".
	Releases func(model.Dependency) *model.ReleaseSet

	Versionings versioning.Registry

	// DefaultVersioning names the scheme for a datasource when the
	// dependency itself carries none. It may return "" for an unknown
	// datasource; the planner then falls back to semver and says so.
	DefaultVersioning func(datasource string) string

	// Digest answers the content digest of one version of a dependency,
	// for references pinned by digest: every update of such a reference
	// carries the digest of the value it writes, and a tag whose digest
	// moved is itself an update. nil means digests cannot be learned, and
	// a digest-pinned reference is then skipped rather than half-moved.
	Digest func(d model.Dependency, version string) (string, error)

	// Now is the plan's clock. Required; the planner never reads time.Now.
	Now time.Time
}

// Result is the planner's output. Deps is the input with SkipReasons filled
// in; Updates carries one entry per proposed change.
type Result struct {
	Deps     []model.Dependency
	Updates  []model.Update
	Warnings []model.Warning
}

// Plan classifies every dependency.
//
// The version-selection policy is Renovate's default one, measured rather
// than assumed wherever the differential harness has a vector for it:
//
//   - candidates are releases the scheme can parse that sort above current
//   - an unstable candidate is skipped unless current is itself unstable
//   - majors are separated from everything else (separateMajorMinor), minors
//     and patches are one bucket (separateMinorPatch off), and only the
//     highest major is offered (separateMultipleMajor off)
//   - compatibility moves are not offered; they are a distinct, opt-in type
//
// Rules (allowedVersions, enabled, the rest) are applied by a later stage
// once the rules engine exists. This stage knows the defaults only.
func Plan(req Request) Result {
	var res Result
	res.Deps = make([]model.Dependency, 0, len(req.Deps))
	for _, d := range req.Deps {
		ups, skip, warn := planOne(req, &d)
		if warn != nil {
			res.Warnings = append(res.Warnings, *warn)
		}
		if skip != "" {
			d.SkipReason = skip
		}
		res.Deps = append(res.Deps, d)
		if n := streamNotice(req, &d); n != nil {
			ups = append(ups, *n)
		}
		// Two buckets can name one value - a minor and a "major" of a 0.x
		// range written the same way. One update per value: the plan
		// refuses a key that appears twice, and it was a whole
		// repository's run that failed on it (2026-09-14).
		seen := map[string]bool{}
		for _, u := range ups {
			if seen[u.Key()] {
				continue
			}
			seen[u.Key()] = true
			res.Updates = append(res.Updates, u)
		}
	}
	return res
}

// streamNotice is the majorAvailable update for a dependency whose index
// carries a higher series of the same family (kubectl-1.37 beside
// kubectl-1.36, valkey-9.1-cli beside valkey-cli). The series is the
// package name; nothing in the dependency's own releases can show the
// next one, and a series the distribution retired ages in silence -
// measured 2026-09-18: Wolfi's mariadb-11.8-client stood at 11.8.3-r0
// against thirteen CVEs whose fix was named and never built. Reported
// like a rolling major, never written: moving a series is a decision.
func streamNotice(req Request, d *model.Dependency) *model.Update {
	rs := req.Releases(*d)
	if rs == nil || rs.NewerStream == nil || len(rs.NewerStream.Versions) == 0 {
		return nil
	}
	scheme := d.Versioning
	if scheme == "" && req.DefaultVersioning != nil {
		scheme = req.DefaultVersioning(d.Datasource)
	}
	if scheme == "" {
		scheme = "semver"
	}
	v, err := req.Versionings.Get(scheme)
	if err != nil {
		return nil
	}
	top := func(vs []string) string {
		best := ""
		for _, c := range vs {
			if v.IsVersion(c) && (best == "" || v.Compare(c, best) > 0) {
				best = c
			}
		}
		return best
	}
	newest := top(rs.NewerStream.Versions)
	if newest == "" {
		return nil
	}
	// A name without a series is usually the family's main line, and a
	// series beside it a pinned older one: libxml2-utils at 2.15 beside
	// libxml2-2.13-utils. The sibling is newer only if its versions are -
	// valkey-cli at 8.1 beside valkey-9.1-cli, npm 12.0.2-r0 beside npm-12
	// 12.0.2-r3.
	own := make([]string, 0, len(rs.Releases))
	for _, r := range rs.Releases {
		own = append(own, r.Version)
	}
	if cur := top(own); cur != "" && v.Compare(newest, cur) <= 0 {
		return nil
	}
	return &model.Update{
		DepKey: d.Key(), Dep: *d, NewValue: newest, NewVersion: newest,
		Type: model.UpdateMajorAvailable, Declared: declaredRisk(model.UpdateMajorAvailable),
		TimeSource: model.TimeUnknown, Stream: rs.NewerStream.Package,
	}
}

// planOne decides one dependency. It writes back the versioning scheme it
// resolved, so the plan names the scheme every comparison used rather than
// leaving "" for "whatever the datasource implied".
func planOne(req Request, d *model.Dependency) ([]model.Update, string, *model.Warning) {
	if d.SkipReason != "" {
		return nil, d.SkipReason, nil
	}
	rs := req.Releases(*d)
	if rs == nil {
		return nil, "no lookup was made for this dependency", nil
	}
	if rs.Err != "" {
		return nil, "lookup failed: " + rs.Err, nil
	}
	p := &planning{req: req, d: d, rs: rs}
	if skip, warn := p.resolveScheme(); skip != "" {
		return nil, skip, warn
	}
	if ups, skip, warn, done := p.digestOnly(); done {
		return ups, skip, warn
	}
	if len(rs.Releases) == 0 {
		return nil, "the registry lists no releases", nil
	}
	if skip := p.resolveBase(); skip != "" {
		return nil, skip, nil
	}
	c, skip, warn := p.candidates()
	if skip != "" {
		return nil, skip, warn
	}
	if p.seen == 0 {
		return nil, fmt.Sprintf("none of the %d releases is a valid %s version", len(rs.Releases), p.scheme), nil
	}
	if p.isRange && p.strategy == versioning.StrategyPin && d.VulnerabilityBound == "" {
		// The range becomes the one version it resolves to; nothing else
		// is offered on this run. Renovate types it "pin" and groups it
		// under pin-dependencies. A security fix takes precedence and
		// pins to the fix instead.
		return pinRange(d, p.cur, p.base, byVersionOf(rs, p.v))
	}
	if d.VulnerabilityBound != "" {
		return p.securityFix()
	}
	ups, unchanged, skip := p.ordinary(c)
	if skip != "" {
		return nil, skip, nil
	}
	if d.CurrentDigest != "" && versioning.GoPseudoCommit(d.CurrentValue) == "" {
		return p.withDigests(ups)
	}
	if len(ups) == 0 {
		return nil, p.upToDate(unchanged), nil
	}
	return ups, "", nil
}

// planning is the state one dependency's decision accumulates: the scheme
// and the releases it is measured against, the value as written and the
// version it stands for, and what the range strategy makes of the two.
type planning struct {
	req    Request
	d      *model.Dependency
	rs     *model.ReleaseSet
	v      versioning.Versioning
	scheme string
	// cur is the value as written; base the version it is compared from -
	// the value itself for a pin, the highest admitted release (or the
	// lock's version) for a range; floor the lowest release a range
	// admits, what a bump of the range is measured from.
	cur, base, floor string
	isRange          bool
	baseStable       bool
	strategy         versioning.RangeStrategy
	// seen counts the releases the scheme could read at all.
	seen int
}

// candidateSet is what the bucket pass produced: the newest-per-bucket
// choices are made from majors and others; byVersion carries each
// candidate's release record.
type candidateSet struct {
	majors, others []string
	byVersion      map[string]model.Release
	// ageWaived is set when internalChecksFilter "flexible" offered a
	// release that is not old enough because none was.
	ageWaived bool
}

// resolveScheme names the versioning every comparison uses, writes it and
// the source URL back onto the dependency, and applies extractVersion to
// the releases.
func (p *planning) resolveScheme() (string, *model.Warning) {
	d, req := p.d, p.req
	scheme := d.Versioning
	if scheme == "" && req.DefaultVersioning != nil {
		scheme = req.DefaultVersioning(d.Datasource)
	}
	if scheme == "" {
		scheme = "semver"
	}
	v, err := req.Versionings.Get(scheme)
	if err != nil {
		return fmt.Sprintf("versioning: %v", err), &model.Warning{
			Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: %v", d.DepName, err),
		}
	}
	d.Versioning = scheme
	p.v, p.scheme = v, scheme
	// The source URL is a lookup result. Writing it back is what lets the
	// per-update rule pass see it - the pre-lookup pass could not.
	if d.SourceURL == "" {
		d.SourceURL = p.rs.SourceURL
	}

	// extractVersion rewrites every release's version before anything is
	// compared: "^v?(?<version>.+)$" turns the tag v3.11.3 into 3.11.3. A
	// release the pattern does not match is not a release of this
	// dependency and is dropped.
	if d.ExtractVersion != "" {
		extracted, err := extractVersions(p.rs, d.ExtractVersion)
		if err != nil {
			return fmt.Sprintf("extractVersion: %v", err), &model.Warning{
				Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: extractVersion %q: %v", d.DepName, d.ExtractVersion, err),
			}
		}
		p.rs = extracted
	}
	p.cur = d.CurrentValue
	p.strategy = rangeStrategy(d.RangeStrategy)
	return "", nil
}

// digestOnly handles the references whose decision is about a digest, not
// a version: a tag to be pinned, a bare digest, a tag that is no version.
// done reports that it decided.
func (p *planning) digestOnly() (ups []model.Update, skip string, warn *model.Warning, done bool) {
	d, req, v, cur := p.d, p.req, p.v, p.cur
	if d.PinDigests && d.CurrentDigest == "" && cur != "" && d.Manager != "custom.regex" && !unsatisfiedRange(v, cur, p.rs) {
		// A valid range no release satisfies is skipped as an invalid
		// value, pin and all. Measured in the pinned container over one
		// job file: registry.ole-hartwig.eu/devops/images/node:24 - "24"
		// a range under the node versioning, the image's releases all
		// 2.x - is "invalid-value" with no update, while node:24 and
		// node:24-alpine from the hub, node:2.1.6 from the estate and
		// python:3.14 under docker all get their pinDigest. An invalid
		// value (latest, 24-alpine) is pinned; a range nothing satisfies
		// is not.
		// A custom regex match is not pinned. Measured across the estate's
		// open "pin dependencies" branches: Renovate pins the FROM lines
		// and the job images the dockerfile and gitlabci managers read,
		// never a value a regex manager matched - with or without an
		// optional currentDigest group in the pattern (build-tools'
		// bash v5.3 has one and sits unpinned beside a pinned FROM).
		// pinDigests: the reference names a tag and no digest; the digest
		// the tag resolves to is pinned onto it, as its own update type,
		// before any version moves. Measured: "renovate/pin-dependencies",
		// "pin docker.io/library/python docker tag to c6ead21".
		ups, skip, warn = pinDigest(req, d, cur)
		return ups, skip, warn, true
	}
	if cur == "" && d.CurrentDigest != "" {
		// A reference by digest alone - `image@sha256:…` - has no version
		// to compare; what can move is the digest behind the tag it
		// implies. Renovate reads that tag as "latest" when none is
		// written (documented, not measured here).
		ups, skip, warn = digestRefresh(req, d, "latest")
		return ups, skip, warn, true
	}
	if !v.IsValid(cur) {
		if d.CurrentDigest != "" {
			// `latest@sha256:…`: the tag is not a version and never
			// moves, but the digest behind it does. Measured: Renovate
			// opens "update …/wolfi-base:latest docker digest to 65e1acb".
			ups, skip, warn = digestRefresh(req, d, cur)
			return ups, skip, warn, true
		}
		return nil, fmt.Sprintf("current value %q is not a valid %s version", cur, p.scheme), nil, true
	}
	return nil, "", nil, false
}

// resolveBase fixes the version candidates are compared from.
//
// A pin is compared against releases directly. A range - "^8.5", or "1"
// under semver-partial, which is how a rolling major is written - is
// first resolved to the highest release it admits, and that release is
// what candidates are compared against. Measured: semver-partial says
// isGreaterThan("2.0.0", "1") is false, so comparing against the range
// itself would find nothing newer, ever.
func (p *planning) resolveBase() string {
	d, v, cur := p.d, p.v, p.cur
	p.isRange = !v.IsVersion(cur)
	p.base = cur
	if p.isRange {
		var admitted []string
		for _, r := range p.rs.Releases {
			if !v.IsVersion(r.Version) {
				continue
			}
			p.seen++
			if v.Satisfies(r.Version, cur) {
				admitted = append(admitted, r.Version)
			}
		}
		if p.seen == 0 {
			return fmt.Sprintf("none of the %d releases is a valid %s version", len(p.rs.Releases), p.scheme)
		}
		best, ok := versioning.Latest(v, admitted)
		if !ok {
			return fmt.Sprintf("no release satisfies %q", cur)
		}
		p.base = best
		p.floor = versioning.Sort(v, admitted)[0]
		// With a lock file the current version is what the lock pins,
		// not the highest the range admits: under bump and
		// update-lockfile every release above the lock is an update,
		// inside the range or not. Renovate reads the current version
		// the same way (lockedVersion first).
		if p.strategy != versioning.StrategyReplace && d.LockedVersion != "" && v.IsVersion(d.LockedVersion) {
			p.base = d.LockedVersion
		}
	}
	p.baseStable = v.IsStable(p.base)
	return ""
}

// candidates buckets the releases above the base by whether they cross a
// major. Within each bucket the latest wins; Latest tolerates the docker
// scheme's partial order.
//
// internalChecksFilter: under "strict" a release younger than the
// minimum age is no candidate, so the newest old-enough one is offered
// now rather than the newest held until it ages - a package releasing
// daily is updated to its three-day-old state each day instead of
// never. "flexible" falls back to the unfiltered choice when nothing
// is old enough. "none", the default, offers the newest and lets the
// policy hold it.
func (p *planning) candidates() (candidateSet, string, *model.Warning) {
	d, req, v, cur, base := p.d, p.req, p.v, p.cur, p.base
	ageFilter := time.Duration(0)
	if d.InternalChecksFilter == "strict" || d.InternalChecksFilter == "flexible" {
		if age, err := ParseAge(d.MinimumReleaseAge); err == nil && age > 0 {
			ageFilter = age
		}
	}
	var c candidateSet
	var youngMajors, youngOthers []string // filtered out by age, kept for flexible
	c.byVersion = map[string]model.Release{}
	for _, r := range p.rs.Releases {
		cand := r.Version
		if !v.IsVersion(cand) {
			continue
		}
		if !p.isRange {
			p.seen++
		}
		if v.Compare(cand, base) <= 0 {
			continue
		}
		if p.isRange && v.Satisfies(cand, cur) && p.strategy == versioning.StrategyReplace {
			// Already admitted by the range: under replace, writing it
			// back would change nothing in the file, and an update that
			// changes nothing is not an update. Bump raises the floor
			// anyway; update-lockfile moves the lock.
			continue
		}
		if !versioning.IsCompatible(v, cand, base) {
			continue
		}
		if p.baseStable && !v.IsStable(cand) {
			continue
		}
		if r.Deprecated {
			// ignoreDeprecated defaults to true: a release its registry
			// marks deprecated is not a candidate. Measured on lodash,
			// where the deprecated 4.18.0 is passed over for 4.18.1.
			continue
		}
		if d.AllowedVersions != "" {
			ok, err := allowed(v, d.AllowedVersions, cand)
			if err != nil {
				return c, fmt.Sprintf("allowedVersions %q: %v", d.AllowedVersions, err), &model.Warning{
					Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: allowedVersions %q: %v", d.DepName, d.AllowedVersions, err),
				}
			}
			if !ok {
				continue
			}
		}
		young := ageFilter > 0 && !oldEnough(r, ageFilter, req.Now, d.TimestampOptional)
		switch versioning.UpdateType(v, base, cand) {
		case model.UpdateMajor:
			if young {
				youngMajors = append(youngMajors, cand)
			} else {
				c.majors = append(c.majors, cand)
			}
		case model.UpdateMinor, model.UpdatePatch:
			if young {
				youngOthers = append(youngOthers, cand)
			} else {
				c.others = append(c.others, cand)
			}
		default:
			// Unknown (different family, same version), rollback,
			// compatibility: not offered by default.
			continue
		}
		c.byVersion[cand] = r
	}
	// Nothing old enough in a bucket: strict offers the newest anyway
	// and the policy holds it as pending (measured on Renovate's
	// dashboard: "Pending Status Checks" for a docker tag whose age is
	// unknown); flexible offers it and waives the age.
	if ageFilter > 0 {
		if len(c.majors) == 0 && len(youngMajors) > 0 {
			c.majors = youngMajors
			c.ageWaived = d.InternalChecksFilter == "flexible"
		}
		if len(c.others) == 0 && len(youngOthers) > 0 {
			c.others = youngOthers
			c.ageWaived = c.ageWaived || d.InternalChecksFilter == "flexible"
		}
	}
	return c, "", nil
}

// securityFix is the fast path. Measured: with advisories against the
// current version, Renovate's one update is the LOWEST released,
// non-deprecated version at or above the highest fix version - lodash
// 4.17.20 goes to 4.18.1 (4.18.0 is deprecated), guzzle 7.4.4 to 7.15.2 -
// on the vulnerabilityAlerts branch, whatever the usual buckets would
// have offered.
func (p *planning) securityFix() ([]model.Update, string, *model.Warning) {
	d, v, cur, base := p.d, p.v, p.cur, p.base
	if !v.IsValid(d.VulnerabilityBound) {
		return nil, fmt.Sprintf("vulnerability bound %q is not a valid %s version", d.VulnerabilityBound, p.scheme), &model.Warning{
			Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: advisory fix version %q is not a %s version", d.DepName, d.VulnerabilityBound, p.scheme),
		}
	}
	// The fix is searched among every release, not only those above
	// the base: a range without a lock is taken to run its lowest
	// admitted release, and the fix may sit inside the range (minimist
	// ^1.2.5 is vulnerable at 1.2.5 and fixed at 1.2.6).
	all := byVersionOf(p.rs, v)
	fix, ok := "", false
	for c, r := range all {
		if v.Compare(c, d.VulnerabilityBound) < 0 || r.Deprecated || (p.baseStable && !v.IsStable(c)) {
			continue
		}
		if !ok || v.Compare(c, fix) < 0 {
			fix, ok = c, true
		}
	}
	if !ok {
		ids := make([]string, 0, len(d.Advisories))
		for _, a := range d.Advisories {
			ids = append(ids, a.ID)
		}
		return nil, fmt.Sprintf("vulnerable (%s): no release at or above the fix version %s", strings.Join(ids, ", "), d.VulnerabilityBound), &model.Warning{
			Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s %s is affected by %s and no release at or above %s exists", d.DepName, cur, strings.Join(ids, ", "), d.VulnerabilityBound),
		}
	}
	// Typed from the version in use: the lock's, or a range's lowest
	// admitted release - the one the advisory was asked about.
	from := base
	if p.isRange && p.floor != "" && (d.LockedVersion == "" || !v.IsVersion(d.LockedVersion)) {
		from = p.floor
	}
	u, skip := buildUpdate(v, d, cur, from, fix, all[fix], p.scheme)
	if strings.HasPrefix(skip, unchangedPrefix) {
		return nil, fmt.Sprintf("up to date: %q written as a %s value already admits the fix %s", cur, p.scheme, fix), nil
	}
	if skip != "" {
		return nil, skip, nil
	}
	u.SecurityFix = true
	return []model.Update{u}, "", nil
}

// ordinary builds the updates the buckets offer: the bump of a range's
// floor, then the newest of each bucket. unchanged names a target the
// scheme keeps the value as written for; skip is a reason to plan nothing.
func (p *planning) ordinary(c candidateSet) (ups []model.Update, unchanged, skip string) {
	d, v, cur, base := p.d, p.v, p.cur, p.base
	if p.isRange && p.strategy == versioning.StrategyBump && base != d.LockedVersion && p.floor != "" {
		// Bump raises the range to the highest release it admits even
		// when nothing newer exists: "^8.5" becomes "^8.5.10". Measured:
		// "update dependency php to ^8.5.10" on renovate/php-8.x, typed
		// from the range's floor. A lock makes the lock the base instead
		// and the ordinary buckets cover it.
		if u, skip := buildUpdate(v, d, cur, p.floor, base, byVersionOf(p.rs, v)[base], p.scheme); skip == "" {
			ups = append(ups, u)
		} else if !strings.HasPrefix(skip, unchangedPrefix) {
			return nil, "", skip
		}
	}
	for _, bucket := range [][]string{c.others, c.majors} {
		target, ok := versioning.Latest(v, bucket)
		if !ok {
			continue
		}
		u, skip := buildUpdate(v, d, cur, base, target, c.byVersion[target], p.scheme)
		if c.ageWaived {
			u.AgeWaived = true
		}
		if skip != "" {
			if strings.HasPrefix(skip, unchangedPrefix) {
				// The scheme keeps the value as written - a go directive
				// "1.27.0" already admits 1.27.1 - so there is nothing
				// to write, and an update that changes nothing is not an
				// update. Measured: Renovate plans none for `go 1.27.0`
				// with 1.27.1 out.
				unchanged = target
				continue
			}
			return nil, "", skip
		}
		ups = append(ups, u)
	}
	return ups, unchanged, ""
}

// withDigests completes the updates of a digest-pinned reference. Value
// and digest move together, or not at all: a runtime pulls by digest and
// ignores the tag, so `newtag@olddigest` would claim a version it does
// not run. A Go pseudo-version is the exception (its commit is spelled
// inside the value) and never reaches here.
func (p *planning) withDigests(ups []model.Update) ([]model.Update, string, *model.Warning) {
	d, req := p.d, p.req
	kept := ups[:0]
	var warn *model.Warning
	for _, u := range ups {
		if u.NewDigest != "" {
			kept = append(kept, u)
			continue
		}
		// The registry knows the tag as the file spells it, not the version
		// the scheme reads off it: v2.28.0 under semver is 2.28.0, and
		// asking for that tag is a 404 (measured 2026-09-15: ntfy in
		// koh-gitops, "minor to v2.28.0 not planned").
		digest, err := lookupDigest(req, *d, u.NewValue)
		if err != nil {
			warn = &model.Warning{Stage: "plan", File: d.File,
				Msg: fmt.Sprintf("%s: %s to %s not planned: %v", d.DepName, u.Type, u.NewValue, err)}
			continue
		}
		u.NewDigest = digest
		kept = append(kept, u)
	}
	ups = kept
	if len(ups) > 0 {
		return ups, "", warn
	}
	// The tag stays; the digest behind it may not have.
	refresh, skip, rwarn := digestRefresh(req, d, p.base)
	if warn != nil {
		// A version update was dropped for want of its digest; that,
		// not "up to date", is what the dependency reports.
		if len(refresh) == 0 {
			skip = warn.Msg
		}
		return refresh, skip, warn
	}
	return refresh, skip, rwarn
}

// upToDate is the reason for planning nothing when nothing is newer.
func (p *planning) upToDate(unchanged string) string {
	if p.isRange {
		return fmt.Sprintf("up to date: %q admits %s, and none of %d releases is newer", p.cur, p.base, p.seen)
	}
	if unchanged != "" {
		return fmt.Sprintf("up to date: %q written as a %s value already admits %s", p.cur, p.scheme, unchanged)
	}
	return fmt.Sprintf("up to date: none of %d releases is newer than %s", p.seen, p.cur)
}

// allowed applies an allowedVersions constraint to one candidate: /regex/
// matches the version string, !/regex/ must not match, anything else is a
// range the candidate must satisfy under the dependency's scheme.
func allowed(v versioning.Versioning, constraint, candidate string) (bool, error) {
	negate := strings.HasPrefix(constraint, "!")
	body := strings.TrimPrefix(constraint, "!")
	if strings.HasPrefix(body, "/") && strings.LastIndex(body, "/") > 0 {
		end := strings.LastIndex(body, "/")
		expr, flags := body[1:end], body[end+1:]
		if flags == "i" {
			expr = "(?i)" + expr
		} else if flags != "" {
			return false, fmt.Errorf("unsupported regex flag %q", flags)
		}
		re, err := re2x.Compile(expr)
		if err != nil {
			return false, err
		}
		_, matched := re.Find(candidate)
		return matched != negate, nil
	}
	if negate {
		return false, fmt.Errorf("negation is only supported for a /regex/")
	}
	if !v.IsValid(constraint) {
		return false, fmt.Errorf("not a valid %s range", v.Name())
	}
	return v.Satisfies(candidate, constraint), nil
}

// extractVersions applies an extractVersion pattern to a release set,
// returning a copy with the rewritten versions.
func extractVersions(rs *model.ReleaseSet, pattern string) (*model.ReleaseSet, error) {
	re, err := re2x.Compile(pattern)
	if err != nil {
		return nil, err
	}
	hasGroup := false
	for _, n := range re.Names() {
		if n == "version" {
			hasGroup = true
		}
	}
	if !hasGroup {
		return nil, fmt.Errorf("pattern has no (?<version>...) group")
	}
	out := *rs
	out.Releases = make([]model.Release, 0, len(rs.Releases))
	for _, r := range rs.Releases {
		m, ok := re.Find(r.Version)
		if !ok {
			continue
		}
		v, present := m.Get("version")
		if !present || v == "" {
			continue
		}
		r.Version = v
		out.Releases = append(out.Releases, r)
	}
	return &out, nil
}

// unchangedPrefix marks buildUpdate's answer for a target the scheme would
// write back as the current value.
const unchangedPrefix = "unchanged:"

// buildUpdate turns one chosen target release into an update, or names why
// it cannot: the scheme refuses to write it, or writing it changes nothing.
func buildUpdate(v versioning.Versioning, d *model.Dependency, cur, base, target string, rel model.Release, scheme string) (model.Update, string) {
	strategy := rangeStrategy(d.RangeStrategy)
	lockOnly := false
	if strategy == versioning.StrategyUpdateLockfile && !v.IsVersion(cur) && v.Satisfies(target, cur) {
		// The range admits the target: the manifest keeps its range and
		// the lock moves. Renovate titles it like a version update. With
		// no lock there is nothing to move and nothing happens - measured:
		// typo3/cms-core "^14.0" in a library without composer.lock gets
		// neither a security fix nor an update from Renovate, advisories
		// against 14.0.0 notwithstanding.
		if d.LockedVersion == "" {
			return model.Update{}, unchangedPrefix + target
		}
		lockOnly = true
	}
	newValue := cur
	if !lockOnly {
		var err error
		newValue, err = v.NewValue(cur, target, strategy)
		if err != nil {
			return model.Update{}, fmt.Sprintf("cannot write %s as a %s value: %v", target, scheme, err)
		}
		if newValue == cur {
			return model.Update{}, unchangedPrefix + target
		}
	}
	t := versioning.UpdateType(v, base, target)
	if versioning.GoPseudoCommit(cur) != "" && versioning.GoPseudoCommit(target) != "" {
		// Commit to commit: a Go pseudo-version names no release, and
		// the move between two is a digest move. Measured: "update
		// golang.org/x/mobile digest to 8b95e45" on the branch
		// golang.org-x-mobile-digest (development/s3mail/ios!20).
		t = model.UpdateDigest
	}
	if t == model.UpdateMajor && isRollingMajor(cur) {
		// `@1`-style pins float within their major by design (measured
		// across 44 image repositories: 55 % of all commits on main were
		// component bumps before the estate switched to them). A newer
		// major is the one thing such a pin cannot see on its own, so it
		// is planned as a notification, never as an edit.
		t = model.UpdateMajorAvailable
	}
	u := model.Update{
		DepKey:     d.Key(),
		Dep:        *d,
		NewValue:   newValue,
		NewVersion: target,
		NewDigest:  rel.Digest,
		Type:       t,
		Declared:   declaredRisk(t),
		TimeSource: model.TimeUnknown,
		LockOnly:   lockOnly,
	}
	switch {
	case !rel.Timestamp.IsZero():
		u.ReleaseTime = rel.Timestamp
		u.TimeSource = model.TimeFromDatasource
	case !rel.FirstSeen.IsZero():
		u.ReleaseTime = rel.FirstSeen
		u.TimeSource = model.TimeFromFirstSeen
	}
	return u, ""
}

// rangeStrategy maps the configuration's word onto the scheme strategy.
// Renovate's "auto" and "widen" are read as replace: auto is what every
// manager here resolves to without a lock, and widen (`^1 || ^2`) is a
// form no configuration in the estate asks for.
func rangeStrategy(s string) versioning.RangeStrategy {
	switch s {
	case "bump":
		return versioning.StrategyBump
	case "update-lockfile":
		return versioning.StrategyUpdateLockfile
	case "pin":
		return versioning.StrategyPin
	}
	return versioning.StrategyReplace
}

// byVersionOf indexes the valid releases by version.
func byVersionOf(rs *model.ReleaseSet, v versioning.Versioning) map[string]model.Release {
	out := map[string]model.Release{}
	for _, r := range rs.Releases {
		if v.IsVersion(r.Version) {
			out[r.Version] = r
		}
	}
	return out
}

// pinRange plans the one update a range gets under the pin strategy: the
// version it resolves to - the lock's, else the highest it admits -
// written exactly.
func pinRange(d *model.Dependency, cur, resolved string, byVersion map[string]model.Release) ([]model.Update, string, *model.Warning) {
	if resolved == cur {
		return nil, "", nil
	}
	rel := byVersion[resolved]
	u := model.Update{
		DepKey: d.Key(), Dep: *d, NewValue: resolved, NewVersion: resolved, NewDigest: rel.Digest,
		Type: model.UpdatePin, Declared: model.RiskUnknown, TimeSource: model.TimeUnknown,
	}
	return []model.Update{u}, "", nil
}

// lookupDigest asks the request for a digest, naming the gap when it has
// no way to answer.
func lookupDigest(req Request, d model.Dependency, version string) (string, error) {
	if req.Digest == nil {
		return "", fmt.Errorf("no digest source for %s", d.Datasource)
	}
	return req.Digest(d, version)
}

// pinDigest plans the pinDigest update for a reference that names a tag
// and no digest.
func pinDigest(req Request, d *model.Dependency, tag string) ([]model.Update, string, *model.Warning) {
	digest, err := lookupDigest(req, *d, tag)
	if err != nil {
		return nil, fmt.Sprintf("digest of %s unknown: %v", tag, err), &model.Warning{
			Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: digest of %s: %v", d.DepName, tag, err),
		}
	}
	return []model.Update{{
		DepKey:     d.Key(),
		Dep:        *d,
		NewValue:   tag,
		NewVersion: tag,
		NewDigest:  digest,
		Type:       model.UpdatePinDigest,
		Declared:   model.RiskUnknown,
		TimeSource: model.TimeUnknown,
	}}, "", nil
}

// digestRefresh plans the one update a digest-pinned reference has when its
// value is current: the digest the tag points at now, if it moved.
func digestRefresh(req Request, d *model.Dependency, tag string) ([]model.Update, string, *model.Warning) {
	digest, err := lookupDigest(req, *d, tag)
	if err != nil {
		return nil, fmt.Sprintf("digest of %s unknown: %v", tag, err), &model.Warning{
			Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: digest of %s: %v", d.DepName, tag, err),
		}
	}
	if digest == d.CurrentDigest {
		return nil, fmt.Sprintf("up to date: %s still resolves to the pinned digest", tag), nil
	}
	return []model.Update{{
		DepKey:     d.Key(),
		Dep:        *d,
		NewValue:   d.CurrentValue,
		NewVersion: d.CurrentValue,
		NewDigest:  digest,
		Type:       model.UpdateDigest,
		Declared:   declaredRisk(model.UpdateDigest),
		TimeSource: model.TimeUnknown,
	}}, "", nil
}

// oldEnough is whether a release has reached the minimum age at now, by
// its published timestamp or, failing that, by when this cache first saw
// it; a release with neither is old enough only under timestamp-optional.
func oldEnough(r model.Release, age time.Duration, now time.Time, timestampOptional bool) bool {
	switch {
	case !r.Timestamp.IsZero():
		return !now.Before(r.Timestamp.Add(age))
	case !r.FirstSeen.IsZero():
		return !now.Before(r.FirstSeen.Add(age))
	}
	return timestampOptional
}

// unsatisfiedRange is whether cur is a valid range under v that none of
// the releases satisfies.
func unsatisfiedRange(v versioning.Versioning, cur string, rs *model.ReleaseSet) bool {
	if !v.IsValid(cur) || v.IsVersion(cur) {
		return false
	}
	for _, r := range rs.Releases {
		if v.IsVersion(r.Version) && v.Satisfies(r.Version, cur) {
			return false
		}
	}
	return true
}

// isRollingMajor is whether a current value is a bare major: digits and
// nothing else. `1` is; `1.0`, `v1` and `^1` are not.
func isRollingMajor(cur string) bool {
	if cur == "" {
		return false
	}
	for _, c := range cur {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// declaredRisk maps an update type onto the risk order. Digest and pin moves
// carry no version semantics and stay unknown, which loses to everything.
func declaredRisk(t model.UpdateType) model.Risk {
	switch t {
	case model.UpdateMajor, model.UpdateMajorAvailable:
		return model.RiskMajor
	case model.UpdateMinor:
		return model.RiskMinor
	case model.UpdatePatch:
		return model.RiskPatch
	}
	return model.RiskUnknown
}
