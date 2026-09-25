// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ohartwig/pinup/manager/npmman"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/planner"
	npmver "github.com/ohartwig/pinup/versioning/npm"
)

// releaseAgeFloor is every release age a project's package managers are
// told to enforce, as a lower bound on pinup's own minimumReleaseAge for
// the npm dependencies it governs: npm's in .npmrc, pnpm's in
// pnpm-workspace.yaml (or, for pnpm 10, .npmrc), yarn's in .yarnrc.yml.
// The longest that does not exempt the package applies, and only when it
// is longer than pinup's: a repository asking for seven days where pinup's
// rules say three gets seven, one asking for less changes nothing.
//
// All of them count, whichever package manager the project uses. A setting
// the project wrote is what it asked for, and holding a release a few days
// longer than one tool would have is the direction that cannot hang or
// fail a lock refresh. It is the stricter of two labels again.
type releaseAgeFloor []ageRule

// ageRule is one release age and the file that set it.
type ageRule struct {
	age    npmman.ReleaseAge
	origin model.Origin
}

// readReleaseAgeFloor finds the settings governing manifest - each file in
// the manifest's directory or, as the package managers read a workspace
// member's, the nearest ancestor within the checkout - and returns those
// that set an age. A file pinup cannot read is a warning, not a silent
// absence of a floor.
func readReleaseAgeFloor(root, manifest string) (releaseAgeFloor, []model.Warning) {
	var floor releaseAgeFloor
	var warnings []model.Warning
	add := func(name string, ra npmman.ReleaseAge) {
		if ra.Age > 0 {
			floor = append(floor, ageRule{ra, model.Origin{Source: "file:" + filepath.ToSlash(name), Pointer: ra.Setting, Rule: model.NoRule}})
		}
	}
	if name, raw, ok := nearest(root, manifest, ".npmrc"); ok {
		for _, ra := range npmman.ReadNpmrc(raw) {
			add(name, ra)
		}
	}
	for _, f := range []struct {
		file string
		read func([]byte) (npmman.ReleaseAge, error)
	}{
		{"pnpm-workspace.yaml", npmman.ReadPnpmWorkspace},
		{".yarnrc.yml", npmman.ReadYarnrc},
	} {
		name, raw, ok := nearest(root, manifest, f.file)
		if !ok {
			continue
		}
		ra, err := f.read(raw)
		if err != nil {
			warnings = append(warnings, model.Warning{Stage: "extract", File: filepath.ToSlash(name),
				Msg: "release age not read, so not honoured: " + err.Error()})
			continue
		}
		add(name, ra)
	}
	return floor, warnings
}

// nearest is the file called base in manifest's directory or the closest
// ancestor that has one, by its path within the checkout.
func nearest(root, manifest, base string) (string, []byte, bool) {
	for d := filepath.Dir(manifest); ; d = filepath.Dir(d) {
		name := filepath.Join(d, base)
		if raw, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			return name, raw, true
		}
		if d == "." || d == "/" || d == "" {
			return "", nil, false
		}
	}
}

var npmScheme = npmver.New()

// forPackage is the rule that binds pkg at version: the longest age whose
// file does not exempt it. version may be empty when no single version is
// in question yet; an exemption narrowed to a range then does not apply.
func (f releaseAgeFloor) forPackage(pkg, version string) (ageRule, bool) {
	var best ageRule
	for _, r := range f {
		if r.age.Age > best.age.Age && !r.age.Exempts(pkg, version, npmScheme.Satisfies) {
			best = r
		}
	}
	return best, best.age.Age > 0
}

// releaseAgeFor is the rule that applies to one dependency: an npm one, in
// a directory whose project sets an age that does not exempt it.
func (r *whatifRun) releaseAgeFor(manager, file, depName, version string) (ageRule, bool) {
	if manager != "npm" {
		return ageRule{}, false
	}
	return r.releaseAges[filepath.Dir(file)].forPackage(depName, version)
}

// String is the age in the configuration's own notation: whole or
// fractional days from a day up, otherwise hours or minutes.
func (a ageRule) String() string {
	d := a.age.Age
	switch {
	case d >= 24*time.Hour:
		return strconv.FormatFloat(d.Hours()/24, 'f', -1, 64) + " days"
	case d%time.Hour == 0:
		return strconv.FormatFloat(d.Hours(), 'f', -1, 64) + " hours"
	default:
		return strconv.FormatFloat(d.Minutes(), 'f', -1, 64) + " minutes"
	}
}

// longer is the longer of an age as the configuration writes it and the
// rule's, in the configuration's own notation.
func (a ageRule) longer(age string) string {
	if cur, err := planner.ParseAge(age); err == nil && cur >= a.age.Age {
		return age
	}
	return a.String()
}

// raise lifts a policy's age to the rule's when the rule's is longer, and
// names the file the age came from - the plan's hold then says which file
// made the update wait, not which rule did not.
func (a ageRule) raise(p *planner.Policy) {
	if cur, err := planner.ParseAge(p.MinimumReleaseAge); err == nil && cur >= a.age.Age {
		return
	}
	p.MinimumReleaseAge = a.String()
	if p.Origins == nil {
		p.Origins = map[string]model.Origin{}
	}
	p.Origins["minimumReleaseAge"] = a.origin
}
