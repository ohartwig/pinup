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
)

// npmrcFloor is a project's .npmrc release age, as a lower bound on pinup's
// own minimumReleaseAge for the npm dependencies it governs. The longer of
// the two applies: a repository asking for seven days where pinup's rules
// say three gets seven, one asking for less changes nothing. Packages the
// .npmrc exempts get pinup's rules alone - an estate's own packages stay as
// fast as its rules make them.
type npmrcFloor struct {
	age    npmman.ReleaseAge
	origin model.Origin
}

// readNpmrcFloor finds the .npmrc governing manifest - in its directory or,
// as npm reads a workspace's, the nearest ancestor within the checkout -
// and returns its release age when it sets one.
func readNpmrcFloor(root, manifest string) (npmrcFloor, bool) {
	for d := filepath.Dir(manifest); ; d = filepath.Dir(d) {
		name := filepath.Join(d, ".npmrc")
		if raw, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			ra := npmman.ReadReleaseAge(raw)
			if ra.Days <= 0 {
				return npmrcFloor{}, false
			}
			return npmrcFloor{age: ra, origin: model.Origin{Source: "file:" + filepath.ToSlash(name), Pointer: "/min-release-age", Rule: model.NoRule}}, true
		}
		if d == "." || d == "/" || d == "" {
			return npmrcFloor{}, false
		}
	}
}

// npmrcFloorFor is the floor that applies to one dependency: an npm one,
// under an .npmrc that sets an age, not exempted by name.
func (r *whatifRun) npmrcFloorFor(manager, file, depName string) (npmrcFloor, bool) {
	if manager != "npm" {
		return npmrcFloor{}, false
	}
	f, ok := r.npmrcAges[filepath.Dir(file)]
	if !ok || f.age.Excludes(depName) {
		return npmrcFloor{}, false
	}
	return f, true
}

func (f npmrcFloor) duration() time.Duration {
	return time.Duration(f.age.Days * float64(24*time.Hour))
}

func (f npmrcFloor) String() string {
	return strconv.FormatFloat(f.age.Days, 'f', -1, 64) + " days"
}

// longer is the longer of an age as the configuration writes it and the
// floor, in the configuration's own notation.
func (f npmrcFloor) longer(age string) string {
	if cur, err := planner.ParseAge(age); err == nil && cur >= f.duration() {
		return age
	}
	return f.String()
}

// raise lifts a policy's age to the floor when the floor is longer, and
// names the .npmrc as where the age came from - the plan's hold then says
// which file made the update wait, not which rule did not.
func (f npmrcFloor) raise(p *planner.Policy) {
	if cur, err := planner.ParseAge(p.MinimumReleaseAge); err == nil && cur >= f.duration() {
		return
	}
	p.MinimumReleaseAge = f.String()
	if p.Origins == nil {
		p.Origins = map[string]model.Origin{}
	}
	p.Origins["minimumReleaseAge"] = f.origin
}
