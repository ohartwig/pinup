// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package npmman

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ohartwig/pinup/yamlx"
)

// ReleaseAge is one package manager's rule on how old a version must be
// before it installs it, as a project writes it down, and the packages the
// rule leaves alone.
//
// pinup reads these because the package managers enforce them and pinup
// must not propose past them. What each does with a version younger than
// its age, pinned exactly in package.json (measured 2026-09-24 against
// @types/node 26.6.2 at 5.8 days under a seven-day age):
//
//   - npm 11.17 and 12.1 (.npmrc min-release-age) resolve in a loop and never
//     return. On development/sales-agent-ai that hung a lock refresh for its
//     full 45-minute timeout, every four hours, for every version between
//     pinup's three days and the project's seven.
//   - pnpm 10.34 and 11.27 (pnpm-workspace.yaml minimumReleaseAge, and in
//     pnpm 10 also .npmrc minimum-release-age) fail within two seconds:
//     ERR_PNPM_NO_MATURE_MATCHING_VERSION.
//   - yarn 4.14 (.yarnrc.yml npmMinimalAgeGate) fails at once: YN0016, "all
//     versions satisfying ... are quarantined".
//
// Failing fast is kinder than looping, but a lock refresh that fails is
// still a branch that lands nowhere. A range is different in all three: it
// resolves to the newest version old enough.
//
// None of them has an age by default (pnpm 11.27 and yarn 4.14 install a
// version ten hours old without one), so no setting means no floor.
type ReleaseAge struct {
	Age time.Duration
	// Setting is where the age is written, as a pointer into its file:
	// "/min-release-age", "/minimumReleaseAge", "/npmMinimalAgeGate".
	Setting string
	Exempt  []Exemption
}

// Exemption is one package, or pattern of packages, a release age leaves
// alone. Range, when set, narrows it to the versions in that npm range:
// pnpm writes exact versions ("pkg@1.2.3 || 1.2.4"), yarn a range
// ("pkg@^2"). Both read as npm ranges.
type Exemption struct {
	Pattern, Range string
}

func exemption(s string) Exemption {
	if name, rng, ok := strings.CutLast(s, "@"); ok && name != "" {
		return Exemption{Pattern: name, Range: strings.TrimSpace(rng)}
	}
	return Exemption{Pattern: s}
}

// ReadNpmrc reads the release ages an .npmrc sets: npm's min-release-age,
// in days, with min-release-age-exclude; and pnpm 10's minimum-release-age,
// in minutes, with minimum-release-age-exclude. pnpm 11 no longer reads the
// latter from .npmrc, but a project that wrote it asked for it. An exclude
// may be repeated, with or without npm's `[]` array suffix; npm's also
// splits at commas, pnpm's does not (a comma list exempted nothing in pnpm
// 10.34). A value in quotes is unquoted.
func ReadNpmrc(npmrc []byte) []ReleaseAge {
	npm := ReleaseAge{Setting: "/min-release-age"}
	pnpm := ReleaseAge{Setting: "/minimum-release-age"}
	for line := range strings.Lines(string(npmrc)) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSuffix(strings.TrimSpace(key), "[]")
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "min-release-age":
			if days, err := strconv.ParseFloat(value, 64); err == nil && days > 0 {
				npm.Age = time.Duration(days * float64(24*time.Hour))
			}
		case "min-release-age-exclude":
			for p := range strings.SplitSeq(value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					npm.Exempt = append(npm.Exempt, Exemption{Pattern: p})
				}
			}
		case "minimum-release-age":
			if minutes, err := strconv.ParseFloat(value, 64); err == nil && minutes > 0 {
				pnpm.Age = time.Duration(minutes * float64(time.Minute))
			}
		case "minimum-release-age-exclude":
			if value != "" {
				pnpm.Exempt = append(pnpm.Exempt, exemption(value))
			}
		}
	}
	var out []ReleaseAge
	for _, ra := range []ReleaseAge{npm, pnpm} {
		if ra.Age > 0 {
			out = append(out, ra)
		}
	}
	return out
}

// ReadPnpmWorkspace reads minimumReleaseAge, in minutes, and
// minimumReleaseAgeExclude out of a pnpm-workspace.yaml. A zero Age means
// the file sets none; an error means it sets one pinup cannot read.
func ReadPnpmWorkspace(raw []byte) (ReleaseAge, error) {
	ra := ReleaseAge{Setting: "/minimumReleaseAge"}
	var doc map[string]any
	if err := yamlx.Unmarshal(raw, &doc); err != nil {
		return ra, err
	}
	v, ok := doc["minimumReleaseAge"]
	if !ok || v == nil {
		return ra, nil
	}
	minutes, err := number(v)
	if err != nil {
		return ra, fmt.Errorf("minimumReleaseAge: %w", err)
	}
	ra.Age = time.Duration(minutes * float64(time.Minute))
	ra.Exempt, err = exemptions(doc["minimumReleaseAgeExclude"])
	if err != nil {
		return ra, fmt.Errorf("minimumReleaseAgeExclude: %w", err)
	}
	return ra, nil
}

// ReadYarnrc reads npmMinimalAgeGate and npmPreapprovedPackages out of a
// .yarnrc.yml. The gate is minutes as a bare number, or a number with a
// unit - "7d", "140h", "1w" all measured to mean what they say in yarn
// 4.14. A zero Age means the file sets none.
func ReadYarnrc(raw []byte) (ReleaseAge, error) {
	ra := ReleaseAge{Setting: "/npmMinimalAgeGate"}
	var doc map[string]any
	if err := yamlx.Unmarshal(raw, &doc); err != nil {
		return ra, err
	}
	v, ok := doc["npmMinimalAgeGate"]
	if !ok || v == nil {
		return ra, nil
	}
	age, err := yarnDuration(v)
	if err != nil {
		return ra, fmt.Errorf("npmMinimalAgeGate: %w", err)
	}
	ra.Age = age
	ra.Exempt, err = exemptions(doc["npmPreapprovedPackages"])
	if err != nil {
		return ra, fmt.Errorf("npmPreapprovedPackages: %w", err)
	}
	return ra, nil
}

func number(v any) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case float64:
		return n, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(n), 64)
	}
	return 0, fmt.Errorf("%v is not a number", v)
}

var yarnUnits = map[string]time.Duration{
	"ms": time.Millisecond, "s": time.Second, "m": time.Minute,
	"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

func yarnDuration(v any) (time.Duration, error) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		num := strings.TrimRight(s, "abcdefghijklmnopqrstuvwxyz")
		if unit := s[len(num):]; unit != "" {
			per, ok := yarnUnits[unit]
			n, err := strconv.ParseFloat(num, 64)
			if !ok || err != nil {
				return 0, fmt.Errorf("%q is not a duration", s)
			}
			return time.Duration(n * float64(per)), nil
		}
	}
	minutes, err := number(v)
	if err != nil {
		return 0, err
	}
	return time.Duration(minutes * float64(time.Minute)), nil
}

func exemptions(v any) ([]Exemption, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%v is not a list", v)
	}
	var out []Exemption
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("%v is not a package name", e)
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, exemption(s))
		}
	}
	return out, nil
}

// Exempts reports whether the rule leaves pkg at version alone: an exact
// name or a pattern such as "@moselwal/*", and - for an exemption narrowed
// to a range - a version in it. With no version known, a narrowed
// exemption exempts nothing: pinup cannot tell, and the stricter reading
// is the one that cannot hang a refresh. satisfies is the npm range check.
func (ra ReleaseAge) Exempts(pkg, version string, satisfies func(version, rng string) bool) bool {
	for _, e := range ra.Exempt {
		if e.Pattern != pkg {
			if ok, err := path.Match(e.Pattern, pkg); err != nil || !ok {
				continue
			}
		}
		if e.Range == "" || (version != "" && satisfies(version, e.Range)) {
			return true
		}
	}
	return false
}
