// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package planner

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/semverx"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// testScheme is a small semver on top of the L0 parser. The planner is L2
// and may not import the L3 schemes, which is the right constraint: what is
// under test here is the selection policy, not any scheme's parsing.
type testScheme struct {
	// family, when true, makes the scheme behave like docker's compatibility
	// rule: a candidate must share the current value's suffix and precision.
	family bool
	// partial, when true, reads a bare major ("1") as the range every 1.x.y
	// satisfies - the rolling-pin shape semver-partial gives the estate.
	partial bool
}

func (testScheme) Name() string { return "test" }

func split(s string) (semverx.Version, string, bool) {
	body, suffix, _ := strings.Cut(s, "-")
	parts := strings.Split(body, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	v, ok := semverx.Parse(strings.Join(parts, "."))
	return v, suffix, ok
}

func (t testScheme) IsValid(s string) bool { _, _, ok := split(s); return ok }
func (t testScheme) IsVersion(s string) bool {
	return t.IsValid(s) && !(t.partial && !strings.Contains(s, "."))
}
func (t testScheme) IsStable(s string) bool {
	_, suffix, ok := split(s)
	if t.family {
		return ok
	}
	return ok && suffix == ""
}
func (t testScheme) Major(s string) (int, bool) { v, _, ok := split(s); return v.Major, ok }
func (t testScheme) Minor(s string) (int, bool) { v, _, ok := split(s); return v.Minor, ok }
func (t testScheme) Patch(s string) (int, bool) { v, _, ok := split(s); return v.Patch, ok }
func (t testScheme) Compare(a, b string) int {
	va, sa, _ := split(a)
	vb, sb, _ := split(b)
	if c := semverx.CompareVersions(va, vb); c != 0 || t.family {
		return c
	}
	// A plain release beats its prereleases.
	switch {
	case sa == sb:
		return 0
	case sa == "":
		return 1
	case sb == "":
		return -1
	}
	return strings.Compare(sa, sb)
}
func (t testScheme) Equal(a, b string) bool { return t.Compare(a, b) == 0 }
func (t testScheme) Satisfies(v, rng string) bool {
	if t.partial && !strings.Contains(rng, ".") {
		maj, _ := t.Major(v)
		want, _ := t.Major(rng)
		return maj == want
	}
	return t.Equal(v, rng)
}
func (t testScheme) NewValue(current, target string, _ versioning.RangeStrategy) (string, error) {
	if t.partial && !strings.Contains(current, ".") {
		maj, _ := t.Major(target)
		return fmt.Sprint(maj), nil
	}
	return target, nil
}
func (t testScheme) IsCompatible(candidate, current string) bool {
	_, sc, ok := split(candidate)
	_, sr, ok2 := split(current)
	if !ok || !ok2 {
		return false
	}
	if !t.family {
		return true
	}
	return sc == sr && strings.Count(candidate, ".") == strings.Count(current, ".")
}

var now = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

func registry() versioning.Registry {
	return versioning.Registry{
		"semver": testScheme{}, "docker": testScheme{family: true}, "semver-partial": testScheme{partial: true},
	}
}

func dep(name, current, scheme string) model.Dependency {
	return model.Dependency{
		Manager: "gitlabci", File: ".gitlab-ci.yml", DepName: name,
		CurrentValue: current, Datasource: "gitlab-tags", Versioning: scheme,
		CustomManager: model.NoCustomManager,
	}
}

func releases(vs ...string) *model.ReleaseSet {
	rs := &model.ReleaseSet{PackageName: "p", Datasource: "gitlab-tags"}
	for _, v := range vs {
		rs.Releases = append(rs.Releases, model.Release{Version: v})
	}
	return rs
}

func plan(t *testing.T, d model.Dependency, rs *model.ReleaseSet) Result {
	t.Helper()
	res := Plan(Request{
		Deps:        []model.Dependency{d},
		Releases:    func(model.Dependency) *model.ReleaseSet { return rs },
		Versionings: registry(),
		Now:         now,
	})
	// The invariant this package exists to uphold, checked on every case.
	p := &model.Plan{SchemaVersion: model.SchemaVersion, Deps: res.Deps, Updates: res.Updates}
	if err := p.Validate(); err != nil {
		t.Fatalf("planner produced an invalid plan: %v", err)
	}
	return res
}

func TestSeparatesMajorFromMinorAndOffersOnlyTheHighestMajor(t *testing.T) {
	res := plan(t, dep("lint", "1.33.59", "semver"),
		releases("1.33.58", "1.33.60", "1.34.0", "2.0.0", "2.1.0", "3.0.0"))
	if len(res.Updates) != 2 {
		t.Fatalf("want a minor and a major update, got %d: %+v", len(res.Updates), res.Updates)
	}
	minor, major := res.Updates[0], res.Updates[1]
	if minor.Type != model.UpdateMinor || minor.NewValue != "1.34.0" {
		t.Errorf("minor bucket: got %s to %s, want minor to 1.34.0", minor.Type, minor.NewValue)
	}
	if major.Type != model.UpdateMajor || major.NewValue != "3.0.0" {
		t.Errorf("major bucket: got %s to %s, want major to 3.0.0 (only the highest major)", major.Type, major.NewValue)
	}
	if minor.Declared != model.RiskMinor || major.Declared != model.RiskMajor {
		t.Errorf("declared risk: minor=%s major=%s", minor.Declared, major.Declared)
	}
	if res.Deps[0].SkipReason != "" {
		t.Errorf("a dependency with updates must carry no skip reason, got %q", res.Deps[0].SkipReason)
	}
}

func TestPatchFoldsIntoMinorBucket(t *testing.T) {
	res := plan(t, dep("lint", "1.33.59", "semver"), releases("1.33.60", "1.33.64"))
	if len(res.Updates) != 1 || res.Updates[0].Type != model.UpdatePatch || res.Updates[0].NewValue != "1.33.64" {
		t.Fatalf("want one patch update to 1.33.64, got %+v", res.Updates)
	}
}

func TestUpToDateIsASkipReasonNotSilence(t *testing.T) {
	res := plan(t, dep("lint", "1.33.64", "semver"), releases("1.33.59", "1.33.64"))
	if len(res.Updates) != 0 {
		t.Fatalf("no update expected, got %+v", res.Updates)
	}
	if !strings.HasPrefix(res.Deps[0].SkipReason, "up to date") {
		t.Errorf("skip reason %q should say up to date", res.Deps[0].SkipReason)
	}
}

func TestUnstableCandidatesAreSkippedForAStableCurrent(t *testing.T) {
	res := plan(t, dep("lint", "1.0.0", "semver"), releases("1.1.0-rc.1", "2.0.0-beta"))
	if len(res.Updates) != 0 {
		t.Fatalf("prereleases must not be offered to a stable pin, got %+v", res.Updates)
	}
	res = plan(t, dep("lint", "1.1.0-rc.1", "semver"), releases("1.1.0-rc.2", "1.1.0"))
	if len(res.Updates) != 1 || res.Updates[0].NewValue != "1.1.0" {
		t.Fatalf("an unstable current may move to the latest, got %+v", res.Updates)
	}
}

func TestDockerKeepsFamilyAndPrecision(t *testing.T) {
	d := dep("node", "22-alpine3.20", "docker")
	res := plan(t, d, releases("22-alpine3.21", "22-bookworm", "23-alpine3.20", "23.1-alpine3.20", "24-alpine3.20"))
	if len(res.Updates) != 1 {
		t.Fatalf("want exactly one update (the major within the family), got %+v", res.Updates)
	}
	u := res.Updates[0]
	if u.NewValue != "24-alpine3.20" || u.Type != model.UpdateMajor {
		t.Errorf("got %s to %q, want major to 24-alpine3.20", u.Type, u.NewValue)
	}
}

func TestLookupFailureAndMissingLookupAreDistinctSkips(t *testing.T) {
	res := plan(t, dep("x", "1.0.0", "semver"), &model.ReleaseSet{Err: "connection refused"})
	if got := res.Deps[0].SkipReason; !strings.Contains(got, "lookup failed") || !strings.Contains(got, "connection refused") {
		t.Errorf("failed lookup skip reason: %q", got)
	}
	res = plan(t, dep("x", "1.0.0", "semver"), nil)
	if got := res.Deps[0].SkipReason; !strings.Contains(got, "no lookup") {
		t.Errorf("missing lookup skip reason: %q", got)
	}
}

func TestInvalidCurrentAndUnknownSchemeAreExplained(t *testing.T) {
	res := plan(t, dep("x", "latest", "semver"), releases("1.0.0"))
	if got := res.Deps[0].SkipReason; !strings.Contains(got, `"latest" is not a valid semver`) {
		t.Errorf("invalid current: %q", got)
	}
	res = plan(t, dep("x", "1.0.0", "no-such-scheme"), releases("1.0.0"))
	if got := res.Deps[0].SkipReason; !strings.Contains(got, "no-such-scheme") {
		t.Errorf("unknown scheme: %q", got)
	}
	if len(res.Warnings) != 1 {
		t.Errorf("an unknown scheme is a config problem and must warn, got %d warnings", len(res.Warnings))
	}
}

func TestDefaultVersioningComesFromTheDatasource(t *testing.T) {
	d := dep("x", "1.0.0", "")
	res := Plan(Request{
		Deps:              []model.Dependency{d},
		Releases:          func(model.Dependency) *model.ReleaseSet { return releases("1.1.0") },
		Versionings:       registry(),
		DefaultVersioning: func(ds string) string { return "semver" },
		Now:               now,
	})
	if len(res.Updates) != 1 {
		t.Fatalf("want one update via the datasource default scheme, got %+v", res.Updates)
	}
}

func TestReleaseTimeIsRecordedWithItsSource(t *testing.T) {
	rs := releases("1.1.0")
	rs.Releases[0].Timestamp = now.Add(-48 * time.Hour)
	res := plan(t, dep("x", "1.0.0", "semver"), rs)
	if u := res.Updates[0]; u.TimeSource != model.TimeFromDatasource || u.ReleaseTime.IsZero() {
		t.Errorf("timestamped release: source=%s time=%v", u.TimeSource, u.ReleaseTime)
	}
	res = plan(t, dep("x", "1.0.0", "semver"), releases("1.1.0"))
	if u := res.Updates[0]; u.TimeSource != model.TimeUnknown {
		t.Errorf("untimestamped release must say unknown, got %s", u.TimeSource)
	}
}

// A rolling major pin "1" is a range: releases it admits are not updates,
// and a release beyond it is a major written in the pin's own precision.
func TestRollingMajorPinIsResolvedAsARange(t *testing.T) {
	res := plan(t, dep("lint", "1", "semver-partial"), releases("1.33.59", "1.33.64"))
	if len(res.Updates) != 0 {
		t.Fatalf("1.33.64 is admitted by \"1\"; no update expected, got %+v", res.Updates)
	}
	if got := res.Deps[0].SkipReason; !strings.Contains(got, "admits 1.33.64") {
		t.Errorf("skip reason should name the release the pin resolves to: %q", got)
	}

	res = plan(t, dep("lint", "1", "semver-partial"), releases("1.33.59", "1.33.64", "2.0.0", "2.3.0"))
	if len(res.Updates) != 1 {
		t.Fatalf("want exactly one major update, got %+v", res.Updates)
	}
	u := res.Updates[0]
	if u.Type != model.UpdateMajor || u.NewValue != "2" || u.NewVersion != "2.3.0" {
		t.Errorf("got %s to value %q version %q; want major to \"2\" (2.3.0)", u.Type, u.NewValue, u.NewVersion)
	}

	res = plan(t, dep("lint", "9", "semver-partial"), releases("1.0.0"))
	if got := res.Deps[0].SkipReason; !strings.Contains(got, `no release satisfies "9"`) {
		t.Errorf("a range nothing satisfies must say so: %q", got)
	}
}

// extractVersion rewrites release versions before comparison and drops
// releases it does not match; the plan records the scheme that was used.
func TestExtractVersionAndRecordedScheme(t *testing.T) {
	d := dep("editorconfig-checker/editorconfig-checker", "3.11.1", "")
	d.ExtractVersion = `^v?(?<version>.+)$`
	res := Plan(Request{
		Deps:              []model.Dependency{d},
		Releases:          func(model.Dependency) *model.ReleaseSet { return releases("v3.11.2", "v3.11.3", "nightly-2026") },
		Versionings:       registry(),
		DefaultVersioning: func(string) string { return "semver" },
		Now:               now,
	})
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "3.11.3" || res.Updates[0].NewValue != "3.11.3" {
		t.Fatalf("want one update to 3.11.3 with the v stripped, got %+v", res.Updates)
	}
	if res.Deps[0].Versioning != "semver" || res.Updates[0].Dep.Versioning != "semver" {
		t.Errorf("the resolved scheme must be written back: dep=%q update=%q", res.Deps[0].Versioning, res.Updates[0].Dep.Versioning)
	}
	d.ExtractVersion = `^v(.+)$` // no named group
	res = Plan(Request{Deps: []model.Dependency{d}, Releases: func(model.Dependency) *model.ReleaseSet { return releases("v1.0.0") }, Versionings: registry(), Now: now})
	if !strings.Contains(res.Deps[0].SkipReason, "version") || len(res.Warnings) != 1 {
		t.Errorf("a pattern without a version group must be refused: skip=%q warnings=%v", res.Deps[0].SkipReason, res.Warnings)
	}
}

// allowedVersions narrows the candidates: a range, a regex, a negated regex.
func TestAllowedVersionsNarrowsCandidates(t *testing.T) {
	d := dep("x", "1.0.0", "semver")
	d.AllowedVersions = "!/^2\\./"
	res := plan(t, d, releases("1.1.0", "2.0.0", "2.1.0"))
	if len(res.Updates) != 1 || res.Updates[0].NewValue != "1.1.0" {
		t.Errorf("negated regex: %+v", res.Updates)
	}
	d.AllowedVersions = "/^2\\.1/"
	res = plan(t, d, releases("1.1.0", "2.0.0", "2.1.0"))
	if len(res.Updates) != 1 || res.Updates[0].NewValue != "2.1.0" {
		t.Errorf("regex: %+v", res.Updates)
	}
	d.AllowedVersions = "1.1.0" // the test scheme's Satisfies is equality
	res = plan(t, d, releases("1.1.0", "2.0.0"))
	if len(res.Updates) != 1 || res.Updates[0].NewValue != "1.1.0" {
		t.Errorf("range: %+v", res.Updates)
	}
	d.AllowedVersions = "!1.1.0"
	res = plan(t, d, releases("1.1.0"))
	if !strings.Contains(res.Deps[0].SkipReason, "allowedVersions") || len(res.Warnings) != 1 {
		t.Errorf("a negated range is refused: %q %v", res.Deps[0].SkipReason, res.Warnings)
	}
}
