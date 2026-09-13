// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package model

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"
)

// Edit is a byte-range replacement in one file. Apply consumes these and
// nothing else, which is what keeps text managers format-preserving: the bytes
// outside the range are copied through untouched, so comments, key order and
// line endings survive.
type Edit struct {
	File         string `json:"file"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
	Old          string `json:"old"`
	New          string `json:"new"`
	Manager      string `json:"manager"`
	SHA256Before string `json:"sha256Before,omitempty"`
	SHA256After  string `json:"sha256After,omitempty"`
}

// Overlaps reports whether two edits touch the same bytes of the same file.
// Two managers matching one value is a real case - composer.json is matched by
// both the composer manager and a custom manager - and the applier must report
// it rather than write twice.
func (e Edit) Overlaps(o Edit) bool {
	return e.File == o.File && e.Start < o.End && o.Start < e.End
}

// ExecutionMode is how often a post-upgrade task runs.
type ExecutionMode string

const (
	// ExecUpdate runs the command once per update, scoped by FileFilters.
	ExecUpdate ExecutionMode = "update"
	// ExecBranch runs it once per branch, after all edits are applied.
	ExecBranch ExecutionMode = "branch"
)

// Task is a post-upgrade command, already expanded and already matched against
// the allowlist. It is never handed to a shell.
type Task struct {
	// Kind is "lock-refresh" for the toolchain run pinup itself asks for
	// when a manifest with a lock file changes, or "post-upgrade" for a
	// command the configuration's postUpgradeTasks names.
	Kind string `json:"kind"`
	// Manager is the manager whose files the task works on.
	Manager string `json:"manager,omitempty"`
	// Dir is the directory the command runs in, relative to the checkout.
	Dir string `json:"dir,omitempty"`
	// Command is the argv, split. There is no shell, so no quoting rules and
	// no injection surface.
	Command       []string      `json:"command"`
	ExecutionMode ExecutionMode `json:"executionMode"`
	FileFilters   []string      `json:"fileFilters,omitempty"`
	// AllowedBy is the index of the allowlist pattern that admitted this
	// command, or -1 for a lock refresh pinup composed itself. A command
	// that ran because the allowlist was too loose is then visibly
	// different from one that matched the intended entry.
	AllowedBy int    `json:"allowedBy"`
	Origin    Origin `json:"origin"`
}

// Task kinds.
const (
	TaskLockRefresh = "lock-refresh"
	TaskPostUpgrade = "post-upgrade"
)

// Window is a resolved schedule window, rendered into the plan so a reader can
// see when a held branch thaws.
type Window struct {
	Expr     string    `json:"expr"`
	Kind     string    `json:"kind"` // "natural" or "cron"
	Timezone string    `json:"timezone,omitempty"`
	Active   bool      `json:"active"`
	NextOpen time.Time `json:"nextOpen,omitzero"`
}

// ExistingBranch describes a branch already on the platform, so the plan can
// say it is being adopted rather than created. Branch naming compatibility is
// what makes adoption possible at cutover.
type ExistingBranch struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	MRIID   int    `json:"mrIid,omitempty"`
	MRState string `json:"mrState,omitempty"`
}

// Branch is one merge request's worth of work.
type Branch struct {
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	GroupName string `json:"groupName,omitempty"`

	UpdateKeys []string `json:"updateKeys"`
	Edits      []Edit   `json:"edits,omitempty"`
	Tasks      []Task   `json:"tasks,omitempty"`

	Automerge     bool     `json:"automerge"`
	AutomergeType string   `json:"automergeType,omitempty"`
	Labels        []string `json:"labels,omitempty"`

	// SuppressedBy is set on a branch the run will not push: every update
	// on it is held, and this is the first reason. The shadow comparator
	// buckets on it, since Renovate's merge requests contain nothing that
	// is held. A branch with it set carries no edits.
	SuppressedBy BlockReason `json:"suppressedBy,omitempty"`
	// HeldWith is the block a member brought onto the whole branch - a
	// schedule window or a dashboard approval is the branch's, not the
	// member's - and the members that were actionable on their own carry
	// it too, with this note.
	HeldWith Block `json:"heldWith,omitzero"`

	Schedule Window          `json:"schedule"`
	Existing *ExistingBranch `json:"existing,omitempty"`

	Prov []Origin `json:"provenance,omitempty"`
}

// Warning is a soft failure recorded in the plan. A datasource that could not
// be reached, an unknown manager, an unsupported template construct: none of
// these fail the run, all of them are visible.
type Warning struct {
	Stage string `json:"stage"`
	File  string `json:"file,omitempty"`
	Msg   string `json:"msg"`
}

// Stats are denominators. They are in the schema because a report that says
// "0 differences" over 0 dependencies looks exactly like one over 3000, and
// the estate has paid for that confusion before.
type Stats struct {
	FilesDiscovered  int `json:"filesDiscovered"`
	DepsExtracted    int `json:"depsExtracted"`
	LookupsIssued    int `json:"lookupsIssued"`
	LookupsFromCache int `json:"lookupsFromCache"`
	UpdatesFound     int `json:"updatesFound"`
	UpdatesBlocked   int `json:"updatesBlocked"`
	BranchesPlanned  int `json:"branchesPlanned"`
}

// Plan is the primary artefact. Every run produces one before any write.
type Plan struct {
	SchemaVersion int       `json:"schemaVersion"`
	PinupVersion  string    `json:"pinupVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	// ConfigSHA256 lets the shadow comparator refuse to compare a stale
	// artefact against a fresh run.
	ConfigSHA256 string `json:"configSha256,omitempty"`
	Partition    string `json:"partition,omitempty"`

	Repo RepoRef `json:"repo"`

	// Limits are the run-wide caps the configuration set; the runner
	// enforces them and records what they held.
	Limits Limits `json:"limits,omitzero"`
	// Dashboard is whether the configuration asks for the dashboard issue
	// and what it is titled: dependencyDashboard and
	// dependencyDashboardTitle, resolved.
	Dashboard Dashboard `json:"dashboard,omitzero"`

	Deps     []Dependency `json:"deps"`
	Updates  []Update     `json:"updates"`
	Branches []Branch     `json:"branches"`
	Warnings []Warning    `json:"warnings,omitempty"`
	Stats    Stats        `json:"stats"`
}

// Limits are the run-wide caps from the configuration. Zero means none.
// Dashboard is the resolved dashboard configuration.
type Dashboard struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title,omitempty"`
}

type Limits struct {
	PRHourlyLimit     int `json:"prHourlyLimit"`
	PRConcurrentLimit int `json:"prConcurrentLimit"`
}

// Sort puts a plan in canonical order. Determinism is asserted rather than
// hoped for: the same repository planned twice in one process must produce the
// same bytes, which catches map-iteration order that a single run passes by
// luck.
func (p *Plan) Sort() {
	slices.SortStableFunc(p.Deps, func(a, b Dependency) int {
		if c := cmp.Compare(a.File, b.File); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Locus.ValueStart, b.Locus.ValueStart); c != 0 {
			return c
		}
		return cmp.Compare(a.DepName, b.DepName)
	})
	slices.SortStableFunc(p.Updates, func(a, b Update) int {
		return cmp.Compare(a.Key(), b.Key())
	})
	slices.SortStableFunc(p.Branches, func(a, b Branch) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for i := range p.Branches {
		slices.Sort(p.Branches[i].UpdateKeys)
		slices.SortStableFunc(p.Branches[i].Edits, func(a, b Edit) int {
			if c := cmp.Compare(a.File, b.File); c != 0 {
				return c
			}
			return cmp.Compare(a.Start, b.Start)
		})
	}
	slices.SortStableFunc(p.Warnings, func(a, b Warning) int {
		if c := cmp.Compare(a.Stage, b.Stage); c != 0 {
			return c
		}
		return cmp.Compare(a.Msg, b.Msg)
	})
}

// Validate enforces the invariants a plan must satisfy to be believable.
//
// The important one is the last: a dependency that yields no update must say
// why. A plan full of silent absences is the shape of a check that stopped
// checking, and the golden tests would not notice.
func (p *Plan) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion %d, want %d", p.SchemaVersion, SchemaVersion)
	}
	updated := make(map[string]bool, len(p.Updates))
	known := make(map[string]bool, len(p.Updates))
	for _, u := range p.Updates {
		if u.DepKey == "" {
			return fmt.Errorf("update for %q has no depKey", u.Dep.DepName)
		}
		updated[u.DepKey] = true
		known[u.Key()] = true
		if u.Blocked() && u.SuppressedBy == "" {
			return fmt.Errorf("update %s is blocked but names no suppressedBy", u.DepKey)
		}
	}
	for _, d := range p.Deps {
		if !updated[d.Key()] && d.SkipReason == "" {
			return fmt.Errorf("dependency %s produced neither an update nor a skipReason", d.Key())
		}
	}
	for _, b := range p.Branches {
		if len(b.UpdateKeys) == 0 {
			return fmt.Errorf("branch %s carries no updates", b.Name)
		}
		for _, k := range b.UpdateKeys {
			if !known[k] {
				return fmt.Errorf("branch %s references unknown update %s", b.Name, k)
			}
		}
	}
	return nil
}

// WritePlan renders a plan canonically: two-space indent, a trailing newline,
// sorted. Byte-identical output for identical input is the contract the golden
// tests rest on.
func WritePlan(w io.Writer, p *Plan) error {
	p.Sort()
	// Empty lists are written as [], never null: a reader that ranges over
	// "updates": null sees nothing either way, but a schema check and a
	// human do not, and the two spellings would make two identical plans
	// differ by bytes.
	if p.Deps == nil {
		p.Deps = []Dependency{}
	}
	if p.Updates == nil {
		p.Updates = []Update{}
	}
	if p.Branches == nil {
		p.Branches = []Branch{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// Plan content is data, never markup; escaping < and > would make the
	// golden files unreadable and diff badly.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// ReadPlan parses a plan and checks its invariants.
func ReadPlan(r io.Reader) (*Plan, error) {
	var p Plan
	dec := json.NewDecoder(r)
	// An unknown field is a schema drift, and silently dropping it is how a
	// reader and a writer stop agreeing without anyone noticing.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
