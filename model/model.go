// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package model holds the typed data the pipeline stages pass to one another,
// and the on-disk shape of plan.json.
//
// It is layer 0: pure data, standard library only, no knowledge of any stage.
//
// Two of the repository's invariants are carried by the types here rather than
// by anyone remembering them:
//
//   - An Update that does not happen carries its Blocks, so a plan states why
//     nothing happens. Absence is never expressed as absence.
//   - Apply consumes Edits - byte ranges plus replacements - not Dependencies,
//     so nothing downstream of the planner can invent a change, and format
//     preservation stops being a rule someone has to follow.
package model

import "time"

// SchemaVersion is the version of the plan.json contract. Bump it when a
// change would make an older reader wrong, not merely incomplete.
const SchemaVersion = 1

// Origin says where a resolved value came from. Provenance is kept as a chain
// rather than a winner: print-config --explain prints every place that set a
// key and which one won, which is what makes a rule-ordering surprise
// diagnosable instead of repeatable.
type Origin struct {
	// Source is "file:<path>", "preset:<name>", "env:<var>", "cli" or
	// "builtin".
	Source string `json:"source"`
	// Pointer is an RFC 6901 JSON pointer into that source.
	Pointer string `json:"pointer,omitempty"`
	// Rule is the index into the flattened packageRules array, or -1.
	Rule int `json:"rule"`
	// Order is a monotonic application counter: the tiebreak, and the audit
	// trail.
	Order int `json:"order"`
}

// NoRule is the Rule value for a value that did not come from a packageRule.
const NoRule = -1

// RepoRef identifies one repository.
type RepoRef struct {
	Path   string `json:"path"`             // "pinup/pinup"
	Host   string `json:"host,omitempty"`   // "git.ole-hartwig.eu"
	Branch string `json:"branch,omitempty"` // the base branch
	SHA    string `json:"sha,omitempty"`    // the base commit
}

// Locus is what makes a text manager format-preserving: byte offsets into the
// file exactly as it was read. Nothing is ever re-serialized.
type Locus struct {
	ValueStart int `json:"valueStart"`
	ValueEnd   int `json:"valueEnd"`
	// DigestStart and DigestEnd are -1 when the digest group did not
	// participate in the match.
	DigestStart int `json:"digestStart"`
	DigestEnd   int `json:"digestEnd"`
	// Line is for humans reading the report; offsets are what edits use.
	Line int `json:"line,omitempty"`
}

// NoDigest is the Locus offset for an absent digest.
const NoDigest = -1

// Dependency is one reference to something versioned, found in one file.
type Dependency struct {
	Manager string `json:"manager"`
	File    string `json:"file"`
	// CustomManager is the index of the custom manager that produced this
	// dependency, or -1 for a built-in manager.
	CustomManager int `json:"customManager"`

	DepName     string `json:"depName"`
	PackageName string `json:"packageName,omitempty"`
	DepType     string `json:"depType,omitempty"`

	CurrentValue  string `json:"currentValue"`
	CurrentDigest string `json:"currentDigest,omitempty"`

	Datasource   string   `json:"datasource"`
	Versioning   string   `json:"versioning,omitempty"`
	RegistryURLs []string `json:"registryUrls,omitempty"`
	// ExtractVersion is a regex with a named group "version", applied to a
	// release before comparison.
	ExtractVersion string `json:"extractVersion,omitempty"`

	// Captures holds every named regex group the manager matched, so
	// templates can reference groups this package has never heard of.
	Captures map[string]string `json:"captures,omitempty"`
	// Absent names the groups that did not participate. Present-but-empty and
	// absent are different: an absent group leaves a templated field unset
	// rather than empty, and that difference is what lets one manager replace
	// the pairs Renovate needs.
	Absent map[string]bool `json:"absent,omitempty"`

	Locus Locus `json:"locus"`

	// SkipReason is set when this dependency yields no update. A dependency
	// with neither an update nor a skip reason is a bug, and the golden tests
	// assert against it.
	SkipReason string `json:"skipReason,omitempty"`

	LockFiles []string `json:"lockFiles,omitempty"`
}

// NoCustomManager is the CustomManager value for a built-in manager.
const NoCustomManager = -1

// Key identifies a dependency within a plan, for cross-referencing from
// branches without repeating the whole record.
func (d Dependency) Key() string {
	return d.File + "|" + d.DepName + "|" + d.CurrentValue
}

// Release is one version a datasource offers.
type Release struct {
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
	// Timestamp is the zero time when the datasource supplies none. That is a
	// correctness-relevant distinction: it drives minimumReleaseAgeBehaviour
	// and the firstseen fallback.
	Timestamp   time.Time `json:"timestamp,omitzero"`
	Deprecated  bool      `json:"deprecated,omitempty"`
	RegistryURL string    `json:"registryUrl,omitempty"`
	SourceURL   string    `json:"sourceUrl,omitempty"`
}

// ReleaseSet is what a datasource returns for one package.
type ReleaseSet struct {
	PackageName string    `json:"packageName"`
	Datasource  string    `json:"datasource"`
	RegistryURL string    `json:"registryUrl,omitempty"`
	Releases    []Release `json:"releases"`
	SourceURL   string    `json:"sourceUrl,omitempty"`
	FetchedAt   time.Time `json:"fetchedAt,omitzero"`
	FromCache   bool      `json:"fromCache,omitempty"`
	// Err records a soft failure. A datasource that fails degrades to a plan
	// warning; it never fails the run for the whole repository.
	Err string `json:"err,omitempty"`
}

// CustomManager is one `customManagers` entry from the configuration.
type CustomManager struct {
	// Index is the position in the flattened customManagers array. It travels
	// onto every dependency this manager produces, so a plan can say which
	// definition found a thing.
	Index int

	FilePatterns  []string
	MatchStrings  []string
	MatchStrategy string // "any" (default) or "recursive"; "combination" is refused

	DepNameTemplate        string
	PackageNameTemplate    string
	DatasourceTemplate     string
	VersioningTemplate     string
	RegistryURLTemplate    string
	ExtractVersionTemplate string
	DepTypeTemplate        string
	CurrentValueTemplate   string

	Description []string
}
