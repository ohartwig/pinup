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

import (
	"strconv"
	"time"
)

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
	// LockedVersion is the version a lock file pins this dependency to, when
	// a manager's caller supplies one; extraction from the manifest alone
	// leaves it empty.
	LockedVersion string `json:"lockedVersion,omitempty"`

	Datasource   string   `json:"datasource"`
	Versioning   string   `json:"versioning,omitempty"`
	RegistryURLs []string `json:"registryUrls,omitempty"`
	// SourceURL is where the package's source lives, as the datasource
	// reported it during lookup. Empty until then; the rules that match on
	// it (matchSourceUrls, 445 of the resolved rules) cannot fire before.
	SourceURL string `json:"sourceUrl,omitempty"`
	// AllowedVersions, when set by a rule, restricts the candidates: a range
	// in the dependency's scheme, or /regex/, or !/regex/.
	AllowedVersions string `json:"allowedVersions,omitempty"`
	// RangeStrategy is how a range current value moves: replace (a newer
	// version outside the range replaces it), bump (the range's floor is
	// raised even for a version it admits), update-lockfile (a version the
	// range admits moves only the lock), pin (the range becomes the one
	// version it resolves to). Empty means replace.
	RangeStrategy string `json:"rangeStrategy,omitempty"`
	// PinDigests asks for a reference without a digest to be pinned to
	// the digest its value resolves to (a pinDigest update). Rule 760 of
	// the estate's configuration sets it for every docker dependency.
	PinDigests bool `json:"pinDigests,omitempty"`
	// Analyze asks for the effective classification of this dependency's
	// updates, from a rule's `analyze: true`; off by default, since an
	// analyzer fetches both versions of the thing.
	Analyze bool `json:"analyze,omitempty"`
	// MinimumReleaseAge and InternalChecksFilter, from the pre-lookup rule
	// pass, let the planner choose among candidates by age: under
	// "strict" the newest release that already satisfies the age is the
	// candidate, under "flexible" the same with the newest as fallback,
	// under "none" (the default) the newest, held until it is old enough.
	MinimumReleaseAge    string `json:"minimumReleaseAge,omitempty"`
	InternalChecksFilter string `json:"internalChecksFilter,omitempty"`
	TimestampOptional    bool   `json:"timestampOptional,omitempty"`

	// ExtractVersion is a regex with a named group "version", applied to a
	// release before comparison.
	ExtractVersion string `json:"extractVersion,omitempty"`

	// Advisories are the security advisories that affect the current
	// version, as the advisory database answered; VulnerabilityBound is
	// the highest of their fix versions. A bound turns the dependency's
	// plan into a fix: the lowest release at or above it, on the fast
	// path, instead of the usual buckets. Empty when nothing affects the
	// current version or the datasource has no advisory ecosystem.
	Advisories         []Advisory `json:"advisories,omitempty"`
	VulnerabilityBound string     `json:"vulnerabilityBound,omitempty"`

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

	// Disabled is why this dependency is not updated on its own - an
	// indirect Go module, a rule that says enabled: false. Unlike a skip it
	// is still asked about advisories: a security fix reaches a disabled
	// dependency (measured: Renovate opens "update module golang.org/x/mod
	// to v0.40.0 [security]" for a `// indirect` requirement). Without one
	// the reason becomes the skip.
	Disabled string `json:"disabled,omitempty"`

	LockFiles []string `json:"lockFiles,omitempty"`
}

// Advisory is one security advisory affecting a dependency's current
// version. Fixed is the version that resolves it for the range the current
// version sits in, or empty when the advisory names none.
type Advisory struct {
	ID        string    `json:"id"`
	Aliases   []string  `json:"aliases,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	Fixed     string    `json:"fixed,omitempty"`
	Published time.Time `json:"published,omitzero"`
}

// NoCustomManager is the CustomManager value for a built-in manager.
const NoCustomManager = -1

// Key identifies a dependency within a plan, for cross-referencing from
// branches without repeating the whole record. The locus is part of it:
// the same include named three times in one file is three dependencies
// with three edits, and a key that did not tell them apart made every
// consumer of the plan dedupe by hand (measured: lint-tools in pinup's
// own .gitlab-ci.yml, 2026-09-13).
// The manager is part of it too: the estate's gitlabci manager and a
// custom regex manager read the same include on the same line and decide
// differently about it (one under semver-partial, one under semver).
func (d Dependency) Key() string {
	manager := d.Manager
	if d.CustomManager != NoCustomManager {
		manager += "#" + strconv.Itoa(d.CustomManager)
	}
	return d.File + "|" + manager + "|" + d.DepName + "|" + d.CurrentValue + "|" + strconv.Itoa(d.Locus.ValueStart)
}

// Release is one version a datasource offers.
type Release struct {
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
	// Timestamp is the zero time when the datasource supplies none. That is a
	// correctness-relevant distinction: it drives minimumReleaseAgeBehaviour
	// and the firstseen fallback.
	Timestamp time.Time `json:"timestamp,omitzero"`
	// FirstSeen is when this process's cache first observed the version -
	// the fallback age for a datasource that publishes no timestamp. It is
	// never earlier than the cache's own life, which is why a near-empty
	// cache must be warned about rather than trusted.
	FirstSeen   time.Time `json:"firstSeen,omitzero"`
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

// CustomDatasource is one `customDatasources` entry from the configuration:
// a URL template, a format, and JSONata transforms that shape the fetched
// document into {"releases": [{"version": ...}]}.
type CustomDatasource struct {
	Name string
	// DefaultRegistryURLTemplate renders with {{packageName}}.
	DefaultRegistryURLTemplate string
	// Format is "json" or "plain" (one version per line).
	Format             string
	TransformTemplates []string
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
