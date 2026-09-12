// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

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

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/re2x"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
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
		res.Updates = append(res.Updates, ups...)
	}
	return res
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

	scheme := d.Versioning
	if scheme == "" && req.DefaultVersioning != nil {
		scheme = req.DefaultVersioning(d.Datasource)
	}
	if scheme == "" {
		scheme = "semver"
	}
	v, err := req.Versionings.Get(scheme)
	if err != nil {
		return nil, fmt.Sprintf("versioning: %v", err), &model.Warning{
			Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: %v", d.DepName, err),
		}
	}
	d.Versioning = scheme
	// The source URL is a lookup result. Writing it back is what lets the
	// per-update rule pass see it - the pre-lookup pass could not.
	if d.SourceURL == "" {
		d.SourceURL = rs.SourceURL
	}

	// extractVersion rewrites every release's version before anything is
	// compared: "^v?(?<version>.+)$" turns the tag v3.11.3 into 3.11.3. A
	// release the pattern does not match is not a release of this
	// dependency and is dropped.
	if d.ExtractVersion != "" {
		extracted, err := extractVersions(rs, d.ExtractVersion)
		if err != nil {
			return nil, fmt.Sprintf("extractVersion: %v", err), &model.Warning{
				Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: extractVersion %q: %v", d.DepName, d.ExtractVersion, err),
			}
		}
		rs = extracted
	}

	cur := d.CurrentValue
	if d.PinDigests && d.CurrentDigest == "" && cur != "" && d.Manager != "custom.regex" && scheme != "node" {
		// Under the node versioning nothing is pinned: measured in the
		// pinned container over one job file, node:24 is skipped as an
		// invalid value and node:24-alpine gets no update at all, while
		// python:3.14 under docker gets its pinDigest in the same run.
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
		return pinDigest(req, d, cur)
	}
	if cur == "" && d.CurrentDigest != "" {
		// A reference by digest alone - `image@sha256:…` - has no version
		// to compare; what can move is the digest behind the tag it
		// implies. Renovate reads that tag as "latest" when none is
		// written (documented, not measured here).
		return digestRefresh(req, d, "latest")
	}
	if !v.IsValid(cur) {
		if d.CurrentDigest != "" {
			// `latest@sha256:…`: the tag is not a version and never
			// moves, but the digest behind it does. Measured: Renovate
			// opens "update …/wolfi-base:latest docker digest to 65e1acb".
			return digestRefresh(req, d, cur)
		}
		return nil, fmt.Sprintf("current value %q is not a valid %s version", cur, scheme), nil
	}
	if len(rs.Releases) == 0 {
		return nil, "the registry lists no releases", nil
	}

	// A pin is compared against releases directly. A range - "^8.5", or "1"
	// under semver-partial, which is how a rolling major is written - is
	// first resolved to the highest release it admits, and that release is
	// what candidates are compared against. Measured: semver-partial says
	// isGreaterThan("2.0.0", "1") is false, so comparing against the range
	// itself would find nothing newer, ever.
	isRange := !v.IsVersion(cur)
	base := cur
	seen := 0
	strategy := rangeStrategy(d.RangeStrategy)
	// floor is the lowest release a range admits - what a bump of the
	// range itself is measured from.
	floor := ""
	if isRange {
		var admitted []string
		for _, r := range rs.Releases {
			if !v.IsVersion(r.Version) {
				continue
			}
			seen++
			if v.Satisfies(r.Version, cur) {
				admitted = append(admitted, r.Version)
			}
		}
		if seen == 0 {
			return nil, fmt.Sprintf("none of the %d releases is a valid %s version", len(rs.Releases), scheme), nil
		}
		best, ok := versioning.Latest(v, admitted)
		if !ok {
			return nil, fmt.Sprintf("no release satisfies %q", cur), nil
		}
		base = best
		floor = versioning.Sort(v, admitted)[0]
		// With a lock file the current version is what the lock pins,
		// not the highest the range admits: under bump and
		// update-lockfile every release above the lock is an update,
		// inside the range or not. Renovate reads the current version
		// the same way (lockedVersion first).
		if strategy != versioning.StrategyReplace && d.LockedVersion != "" && v.IsVersion(d.LockedVersion) {
			base = d.LockedVersion
		}
	}
	baseStable := v.IsStable(base)

	// Bucket candidates by whether they cross a major. Within each bucket
	// the latest wins; Latest tolerates the docker scheme's partial order.
	var majors, others []string
	byVersion := map[string]model.Release{}
	for _, r := range rs.Releases {
		c := r.Version
		if !v.IsVersion(c) {
			continue
		}
		if !isRange {
			seen++
		}
		if v.Compare(c, base) <= 0 {
			continue
		}
		if isRange && v.Satisfies(c, cur) && strategy == versioning.StrategyReplace {
			// Already admitted by the range: under replace, writing it
			// back would change nothing in the file, and an update that
			// changes nothing is not an update. Bump raises the floor
			// anyway; update-lockfile moves the lock.
			continue
		}
		if !versioning.IsCompatible(v, c, base) {
			continue
		}
		if baseStable && !v.IsStable(c) {
			continue
		}
		if r.Deprecated {
			// ignoreDeprecated defaults to true: a release its registry
			// marks deprecated is not a candidate. Measured on lodash,
			// where the deprecated 4.18.0 is passed over for 4.18.1.
			continue
		}
		if d.AllowedVersions != "" {
			ok, err := allowed(v, d.AllowedVersions, c)
			if err != nil {
				return nil, fmt.Sprintf("allowedVersions %q: %v", d.AllowedVersions, err), &model.Warning{
					Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: allowedVersions %q: %v", d.DepName, d.AllowedVersions, err),
				}
			}
			if !ok {
				continue
			}
		}
		switch versioning.UpdateType(v, base, c) {
		case model.UpdateMajor:
			majors = append(majors, c)
		case model.UpdateMinor, model.UpdatePatch:
			others = append(others, c)
		default:
			// Unknown (different family, same version), rollback,
			// compatibility: not offered by default.
			continue
		}
		byVersion[c] = r
	}
	if seen == 0 {
		return nil, fmt.Sprintf("none of the %d releases is a valid %s version", len(rs.Releases), scheme), nil
	}

	if isRange && strategy == versioning.StrategyPin && d.VulnerabilityBound == "" {
		// The range becomes the one version it resolves to; nothing else
		// is offered on this run. Renovate types it "pin" and groups it
		// under pin-dependencies. A security fix takes precedence and
		// pins to the fix instead.
		return pinRange(d, cur, base, byVersionOf(rs, v))
	}
	if d.VulnerabilityBound != "" {
		// The fast path. Measured: with advisories against the current
		// version, Renovate's one update is the LOWEST released,
		// non-deprecated version at or above the highest fix version -
		// lodash 4.17.20 goes to 4.18.1 (4.18.0 is deprecated), guzzle
		// 7.4.4 to 7.15.2 - on the vulnerabilityAlerts branch, whatever
		// the usual buckets would have offered.
		if !v.IsValid(d.VulnerabilityBound) {
			return nil, fmt.Sprintf("vulnerability bound %q is not a valid %s version", d.VulnerabilityBound, scheme), &model.Warning{
				Stage: "plan", File: d.File, Msg: fmt.Sprintf("%s: advisory fix version %q is not a %s version", d.DepName, d.VulnerabilityBound, scheme),
			}
		}
		// The fix is searched among every release, not only those above
		// the base: a range without a lock is taken to run its lowest
		// admitted release, and the fix may sit inside the range (minimist
		// ^1.2.5 is vulnerable at 1.2.5 and fixed at 1.2.6).
		all := byVersionOf(rs, v)
		fix, ok := "", false
		for c, r := range all {
			if v.Compare(c, d.VulnerabilityBound) < 0 || r.Deprecated || (baseStable && !v.IsStable(c)) {
				continue
			}
			if !ok || v.Compare(c, fix) < 0 {
				fix, ok = c, true
			}
		}
		byVersion = all
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
		if isRange && floor != "" && (d.LockedVersion == "" || !v.IsVersion(d.LockedVersion)) {
			from = floor
		}
		u, skip := buildUpdate(v, d, cur, from, fix, byVersion[fix], scheme)
		if strings.HasPrefix(skip, unchangedPrefix) {
			return nil, fmt.Sprintf("up to date: %q written as a %s value already admits the fix %s", cur, scheme, fix), nil
		}
		if skip != "" {
			return nil, skip, nil
		}
		u.SecurityFix = true
		return []model.Update{u}, "", nil
	}

	var ups []model.Update
	unchanged := ""
	if isRange && strategy == versioning.StrategyBump && base != d.LockedVersion && floor != "" {
		// Bump raises the range to the highest release it admits even
		// when nothing newer exists: "^8.5" becomes "^8.5.10". Measured:
		// "update dependency php to ^8.5.10" on renovate/php-8.x, typed
		// from the range's floor. A lock makes the lock the base instead
		// and the ordinary buckets cover it.
		if u, skip := buildUpdate(v, d, cur, floor, base, byVersionOf(rs, v)[base], scheme); skip == "" {
			ups = append(ups, u)
		} else if !strings.HasPrefix(skip, unchangedPrefix) {
			return nil, skip, nil
		}
	}
	for _, bucket := range [][]string{others, majors} {
		target, ok := versioning.Latest(v, bucket)
		if !ok {
			continue
		}
		u, skip := buildUpdate(v, d, cur, base, target, byVersion[target], scheme)
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
			return nil, skip, nil
		}
		ups = append(ups, u)
	}
	if d.CurrentDigest != "" && versioning.GoPseudoCommit(d.CurrentValue) == "" {
		// A digest-pinned reference moves value and digest together, or
		// not at all: a runtime pulls by digest and ignores the tag, so
		// `newtag@olddigest` would claim a version it does not run. A Go
		// pseudo-version is the exception: its commit is spelled inside
		// the value, and a move to a tagged release leaves it behind.
		kept := ups[:0]
		var warn *model.Warning
		for _, u := range ups {
			if u.NewDigest != "" {
				kept = append(kept, u)
				continue
			}
			digest, err := lookupDigest(req, *d, u.NewVersion)
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
		refresh, skip, rwarn := digestRefresh(req, d, base)
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
	if len(ups) == 0 {
		if isRange {
			return nil, fmt.Sprintf("up to date: %q admits %s, and none of %d releases is newer", cur, base, seen), nil
		}
		if unchanged != "" {
			return nil, fmt.Sprintf("up to date: %q written as a %s value already admits %s", cur, scheme, unchanged), nil
		}
		return nil, fmt.Sprintf("up to date: none of %d releases is newer than %s", seen, cur), nil
	}
	return ups, "", nil
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
