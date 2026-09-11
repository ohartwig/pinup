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
	if !v.IsValid(cur) {
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
		if isRange && v.Satisfies(c, cur) {
			// Already admitted by the range: writing it back would change
			// nothing in the file, and an update that changes nothing is
			// not an update.
			continue
		}
		if !versioning.IsCompatible(v, c, base) {
			continue
		}
		if baseStable && !v.IsStable(c) {
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

	var ups []model.Update
	for _, bucket := range [][]string{others, majors} {
		target, ok := versioning.Latest(v, bucket)
		if !ok {
			continue
		}
		newValue, err := v.NewValue(cur, target, versioning.StrategyAuto)
		if err != nil {
			return nil, fmt.Sprintf("cannot write %s as a %s value: %v", target, scheme, err), nil
		}
		t := versioning.UpdateType(v, base, target)
		if t == model.UpdateMajor && isRollingMajor(cur) {
			// `@1`-style pins float within their major by design (measured
			// across 44 image repositories: 55 % of all commits on main
			// were component bumps before the estate switched to them).
			// A newer major is the one thing such a pin cannot see on its
			// own, so it is planned as a notification, never as an edit.
			t = model.UpdateMajorAvailable
		}
		rel := byVersion[target]
		u := model.Update{
			DepKey:     d.Key(),
			Dep:        *d,
			NewValue:   newValue,
			NewVersion: target,
			NewDigest:  rel.Digest,
			Type:       t,
			Declared:   declaredRisk(t),
			TimeSource: model.TimeUnknown,
		}
		switch {
		case !rel.Timestamp.IsZero():
			u.ReleaseTime = rel.Timestamp
			u.TimeSource = model.TimeFromDatasource
		case !rel.FirstSeen.IsZero():
			u.ReleaseTime = rel.FirstSeen
			u.TimeSource = model.TimeFromFirstSeen
		}
		ups = append(ups, u)
	}
	if len(ups) == 0 {
		if isRange {
			return nil, fmt.Sprintf("up to date: %q admits %s, and none of %d releases is newer", cur, base, seen), nil
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
