// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package osv

import (
	"time"

	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// The wire shapes below are OSV's documented protocol
// (https://ossf.github.io/osv-schema/, https://google.github.io/osv.dev/api/):
// POST /v1/querybatch and GET /v1/vulns/{id}. Only the fields this package
// needs are declared; encoding/json/v2 ignores the rest, the same way the
// real advisories carry far more (details, references, database_specific,
// schema_version, ...) than any consumer needs.

// pkgRef identifies a package the way both the querybatch request and an
// advisory's "affected" entries do.
type pkgRef struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

// batchQuery is one entry of a querybatch request.
type batchQuery struct {
	Package pkgRef `json:"package"`
	Version string `json:"version"`
}

type batchRequest struct {
	Queries []batchQuery `json:"queries"`
}

// vulnRef is what querybatch answers for one query: the id and modified
// timestamp of each advisory that applies, and nothing else - the full
// record is fetched separately, and only once per distinct (id, modified).
type vulnRef struct {
	ID       string `json:"id"`
	Modified string `json:"modified"`
}

// batchResult is empty (no "vulns" key at all) for a package with no
// advisories - Vulns is nil rather than an empty slice in that case, which
// is exactly what omitting the field decodes to.
type batchResult struct {
	Vulns []vulnRef `json:"vulns,omitempty"`
}

type batchResponse struct {
	Results []batchResult `json:"results"`
}

// severityEntry is one entry of an advisory's "severity" array.
type severityEntry struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

// event is one point in a range's timeline. Exactly one of its fields is set
// per the OSV schema; "limit" exists in the schema too but this package has
// no use for it and does not decode it.
type event struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed"`
	LastAffected string `json:"last_affected"`
}

// rangeEntry is one of an affected entry's ranges. Type is "SEMVER",
// "ECOSYSTEM" or "GIT"; GIT ranges name commits, not versions, and are
// ignored by walkRange's caller.
type rangeEntry struct {
	Type   string  `json:"type"`
	Events []event `json:"events"`
}

// affected is one entry of an advisory's "affected" array. Only entries
// whose Package matches the one queried count - the same advisory commonly
// lists several packages (a library and its re-published forks) with
// different ranges each.
type affected struct {
	Package  pkgRef       `json:"package"`
	Ranges   []rangeEntry `json:"ranges"`
	Versions []string     `json:"versions"`
}

// vulnDoc is the advisory document GET /v1/vulns/{id} answers.
type vulnDoc struct {
	ID        string          `json:"id"`
	Summary   string          `json:"summary"`
	Aliases   []string        `json:"aliases"`
	Modified  string          `json:"modified"`
	Published string          `json:"published"`
	Severity  []severityEntry `json:"severity"`
	Affected  []affected      `json:"affected"`
}

// evaluateAdvisory reports whether doc affects version under scheme, for the
// package and ecosystem actually queried, and if so the Advisory to record -
// including Fixed, the lowest version of the range that contains version and
// no longer has it, when the advisory names one.
func evaluateAdvisory(doc *vulnDoc, packageName, ecosystem, version string, scheme versioning.Versioning) (Advisory, bool) {
	affectedAtAll, fixed := false, ""
	for _, a := range doc.Affected {
		if a.Package.Name != packageName || a.Package.Ecosystem != ecosystem {
			continue
		}
		for _, v := range a.Versions {
			if v == version {
				affectedAtAll = true
			}
		}
		for _, r := range a.Ranges {
			if r.Type != "SEMVER" && r.Type != "ECOSYSTEM" {
				continue // GIT ranges name commits, not versions; anything
				// else is unrecognised and treated the same way: ignored.
			}
			if hit, f := walkRange(r, version, scheme); hit {
				affectedAtAll = true
				if fixed == "" {
					fixed = f
				}
			}
		}
	}
	if !affectedAtAll {
		return Advisory{}, false
	}

	adv := Advisory{
		ID:      doc.ID,
		Aliases: append([]string(nil), doc.Aliases...),
		Summary: doc.Summary,
		Fixed:   fixed,
	}
	if len(doc.Severity) > 0 {
		adv.Severity = doc.Severity[0].Score
	}
	if t, err := time.Parse(time.RFC3339, doc.Published); err == nil {
		adv.Published = t
	}
	if t, err := time.Parse(time.RFC3339, doc.Modified); err == nil {
		adv.Modified = t
	}
	return adv, true
}

// walkRange walks one range's events in order and reports whether version
// falls inside it, and the fix version for the segment that contains it.
//
// The events form alternating segments: an "introduced" opens one, a
// "fixed" or "last_affected" closes it. "introduced": "0" means from the
// beginning of time - it is treated as always true rather than compared
// through scheme, since a scheme's own ordering is not guaranteed to place
// the literal string "0" before every real version.
func walkRange(r rangeEntry, version string, scheme versioning.Versioning) (hit bool, fixed string) {
	inRange := false
	for _, ev := range r.Events {
		switch {
		case ev.Introduced != "":
			inRange = ev.Introduced == "0" || scheme.Compare(version, ev.Introduced) >= 0
		case ev.Fixed != "":
			if inRange && scheme.Compare(version, ev.Fixed) < 0 {
				return true, ev.Fixed
			}
			inRange = false
		case ev.LastAffected != "":
			if inRange && scheme.Compare(version, ev.LastAffected) <= 0 {
				return true, ""
			}
			inRange = false
		}
	}
	return false, ""
}
