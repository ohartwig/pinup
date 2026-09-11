// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// UpdateType classifies an update by what the version strings say. The names
// match Renovate's updateType so existing matchUpdateTypes rules keep working.
type UpdateType uint8

const (
	UpdateUnknown UpdateType = iota
	UpdateMajor
	UpdateMinor
	UpdatePatch
	UpdateDigest
	UpdatePin
	UpdatePinDigest
	UpdateRollback
	UpdateReplacement
	UpdateBump
	UpdateLockFileMaintenance
	// UpdateCompatibility is ours: a newer compatibility segment for the same
	// version, e.g. 22-alpine3.20 -> 22-alpine3.21. Off by default, and never
	// across a compatibility family.
	UpdateCompatibility
	// UpdateMajorAvailable is ours: a rolling major reference (@1) with a
	// newer major published. It is reported, never edited - adopting a major
	// is a deliberate act.
	UpdateMajorAvailable
)

// Renovate reports the update type Renovate would have used for the same
// change: majorAvailable is a major to every rule, template and per-type
// configuration object written for Renovate, and only the plan and the
// policy know the difference.
func (t UpdateType) Renovate() UpdateType {
	if t == UpdateMajorAvailable {
		return UpdateMajor
	}
	return t
}

var updateTypeNames = map[UpdateType]string{
	UpdateUnknown:             "unknown",
	UpdateMajor:               "major",
	UpdateMinor:               "minor",
	UpdatePatch:               "patch",
	UpdateDigest:              "digest",
	UpdatePin:                 "pin",
	UpdatePinDigest:           "pinDigest",
	UpdateRollback:            "rollback",
	UpdateReplacement:         "replacement",
	UpdateBump:                "bump",
	UpdateLockFileMaintenance: "lockFileMaintenance",
	UpdateCompatibility:       "compatibility",
	UpdateMajorAvailable:      "majorAvailable",
}

func (u UpdateType) String() string {
	if s, ok := updateTypeNames[u]; ok {
		return s
	}
	return "unknown"
}

func (u UpdateType) MarshalJSON() ([]byte, error) { return json.Marshal(u.String()) }

func (u *UpdateType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for k, v := range updateTypeNames {
		if v == s {
			*u = k
			return nil
		}
	}
	return fmt.Errorf("unknown updateType %q", s)
}

// ParseUpdateType is the inverse of String, for config parsing.
func ParseUpdateType(s string) (UpdateType, bool) {
	for k, v := range updateTypeNames {
		if v == s {
			return k, true
		}
	}
	return UpdateUnknown, false
}

// Risk is a severity ordering, used for both the declared and the effective
// label. It exists separately from UpdateType because the comparison
// "stricter of the two wins" needs an order, and UpdateType has none.
type Risk uint8

const (
	RiskUnknown Risk = iota // no analyzer ran; rules on it do not fire
	RiskPatch
	RiskMinor
	RiskMajor
	// RiskBreakingValues is what an analyzer reports when the version numbers
	// promise compatibility but the content does not - a removed or renamed
	// values key, say.
	RiskBreakingValues
)

var riskNames = map[Risk]string{
	RiskUnknown:        "unknown",
	RiskPatch:          "patch",
	RiskMinor:          "minor",
	RiskMajor:          "major",
	RiskBreakingValues: "breaking-values",
}

func (r Risk) String() string {
	if s, ok := riskNames[r]; ok {
		return s
	}
	return "unknown"
}

func (r Risk) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

func (r *Risk) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for k, v := range riskNames {
		if v == s {
			*r = k
			return nil
		}
	}
	return fmt.Errorf("unknown risk %q", s)
}

// Stricter returns the higher of two risks - the rule from the spec, made a
// function so no caller has to re-derive it.
//
// RiskUnknown is the zero value and loses to everything, which is why an
// effective label that was never computed cannot relax anything.
func Stricter(a, b Risk) Risk {
	if a > b {
		return a
	}
	return b
}

// BlockReason names why an update is not being acted on.
type BlockReason string

const (
	BlockMinimumReleaseAge BlockReason = "minimumReleaseAge"
	BlockSchedule          BlockReason = "schedule"
	BlockHourlyLimit       BlockReason = "hourlyLimit"
	BlockConcurrentLimit   BlockReason = "concurrentLimit"
	BlockDashboardApproval BlockReason = "dependencyDashboardApproval"
	BlockDisabled          BlockReason = "disabled"
	BlockAllowedVersions   BlockReason = "allowedVersions"
	BlockInternalChecks    BlockReason = "internalChecksFilter"
	// BlockPluginRequired holds an update this version of pinup can plan
	// but not carry out: a lock-file refresh needs the package manager in a
	// container, and until that plugin exists the plan says so rather than
	// pretending the branch was pushed.
	BlockPluginRequired BlockReason = "pluginRequired"
	// BlockRollingMajor holds a majorAvailable update: the current value is
	// a bare major (`@1`), GitLab's shorthand for the newest release of
	// that major, so a newer major is reported and never written - adopting
	// it is a decision with a rollout behind it, not a line a bot rewrites.
	BlockRollingMajor BlockReason = "rollingMajor"
	// BlockTaskRefused holds a branch whose post-upgrade command the
	// allowlist does not admit. Renovate skips the command and pushes the
	// branch anyway - which is how the estate lost every first-party lock
	// refresh for weeks without a red job; pinup holds the branch and
	// names the command instead.
	BlockTaskRefused BlockReason = "taskRefused"
)

// Block is one reason an update is held, with the rule that held it. Every
// held update carries at least one; that is what lets a merge-request body say
// what is pending and when it thaws.
type Block struct {
	Reason BlockReason `json:"reason"`
	// Until is the zero time for a block that is not time-based.
	Until time.Time `json:"until,omitzero"`
	Org   Origin    `json:"origin"`
	Note  string    `json:"note,omitempty"`
}

// TimeSource records where an update's release age came from. It is in the
// plan because a held update whose age was guessed is a different fact from
// one whose age was published.
type TimeSource string

const (
	TimeFromDatasource TimeSource = "datasource"
	TimeFromFirstSeen  TimeSource = "firstseen"
	TimeUnknown        TimeSource = "unknown"
)

// Update is one proposed change to one dependency.
type Update struct {
	DepKey string     `json:"depKey"`
	Dep    Dependency `json:"dep"`

	// NewValue is exactly the bytes written into Dep.Locus. NewVersion is the
	// version those bytes denote; for a range they differ.
	NewValue   string `json:"newValue"`
	NewVersion string `json:"newVersion,omitempty"`
	NewDigest  string `json:"newDigest,omitempty"`

	Type UpdateType `json:"updateType"`

	// Declared comes from the version strings, always. Effective comes from an
	// analyzer and stays RiskUnknown unless one ran.
	Declared  Risk `json:"declared"`
	Effective Risk `json:"effective"`

	ReleaseTime time.Time  `json:"releaseTime,omitzero"`
	TimeSource  TimeSource `json:"timeSource"`

	// SecurityFix marks an update planned to clear the dependency's
	// advisories: the lowest release at or above its vulnerability bound.
	// The vulnerabilityAlerts configuration overlays such an update - its
	// own branch, labels, no release age, no schedule.
	SecurityFix bool `json:"securityFix,omitempty"`

	// Blocks is empty for an update that will be acted on.
	Blocks []Block `json:"blocks,omitempty"`

	// SuppressedBy mirrors the first block's reason, or is empty. The shadow
	// comparator buckets on it: pinup plans entries that are not merge
	// requests yet, and Renovate's observable output contains none of them.
	SuppressedBy BlockReason `json:"suppressedBy,omitempty"`
}

// Blocked reports whether this update is held.
func (u Update) Blocked() bool { return len(u.Blocks) > 0 }

// AutomergeRisk is the risk an automerge decision must be taken against:
// the stricter of declared and effective, unless a rule trusts the effective
// label. Phase 1 leaves Effective at RiskUnknown throughout, which makes this
// return Declared - the safe reading.
func (u Update) AutomergeRisk(trustEffective bool) Risk {
	if trustEffective && u.Effective != RiskUnknown {
		return u.Effective
	}
	return Stricter(u.Declared, u.Effective)
}
