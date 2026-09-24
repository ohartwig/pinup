// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package npmman

import (
	"path"
	"strconv"
	"strings"
)

// ReleaseAge is what a project's .npmrc says about how old a version must
// be before npm installs it: min-release-age in days (npm >= 11.10), and
// the packages min-release-age-exclude exempts.
//
// pinup reads it because npm enforces it and pinup must not propose past
// it. A version younger than the floor, pinned exactly in package.json,
// does not make npm fail: npm 11.17 and 12.1 both resolve in a loop and
// never return (measured 2026-09-23 on development/sales-agent-ai, eslint
// 10.11.0 at 5.6 days under min-release-age=7: killed after two minutes,
// thousands of reads of the same packument; the same bump without the
// setting done in three seconds). pinup's own age for npm was three days,
// so every version between three and seven days old hung a lock refresh
// for its full 45-minute timeout, every four hours.
type ReleaseAge struct {
	Days    float64
	Exclude []string
}

// ReadReleaseAge reads min-release-age and min-release-age-exclude out of an
// .npmrc. The exclusions may be listed once with commas, or repeated, with
// or without npm's `[]` array suffix. A value in quotes is unquoted.
func ReadReleaseAge(npmrc []byte) ReleaseAge {
	var ra ReleaseAge
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
				ra.Days = days
			}
		case "min-release-age-exclude":
			for p := range strings.SplitSeq(value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					ra.Exclude = append(ra.Exclude, p)
				}
			}
		}
	}
	return ra
}

// Excludes reports whether the .npmrc exempts the package: an exact name, or
// a pattern such as "@moselwal/*".
func (ra ReleaseAge) Excludes(pkg string) bool {
	for _, p := range ra.Exclude {
		if p == pkg {
			return true
		}
		if ok, err := path.Match(p, pkg); err == nil && ok {
			return true
		}
	}
	return false
}
