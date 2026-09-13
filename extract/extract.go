// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package extract declares the Manager interface and runs managers over the
// files discovery found.
//
// It is layer 2: it knows what a manager is and nothing about which managers
// exist. The implementations live under manager/ at layer 3 and are handed in
// by wire, which is the only package that knows both.
//
// The invariant this package exists to protect is format preservation. A
// Manager returns Edits - byte ranges plus replacements - and never a
// rewritten file. Comments, key order, indentation and line endings survive
// because nothing here is in a position to lose them: there is no
// serialization step to get wrong.
package extract

import (
	"context"
	"fmt"
	"sort"

	"github.com/ohartwig/pinup/model"
)

// File is a file as it was read, with its bytes untouched.
type File struct {
	// Path is relative to the repository root, slash-separated.
	Path string
	// Content is the exact bytes on disk. A manager reports offsets into
	// this, so it must not be normalised - no line-ending conversion, no
	// trailing-newline fixups.
	Content []byte
}

// ManagerConfig is the slice of resolved configuration a manager needs.
//
// It is deliberately narrow. A manager that could see the whole config would
// start making decisions that belong to the rules engine, and those decisions
// would then be invisible to print-config --explain.
type ManagerConfig struct {
	// FilePatterns overrides the manager's defaults, in the `/regex/` or glob
	// form the config uses.
	FilePatterns []string
	// Custom carries the definition of one custom manager - matchStrings,
	// templates and so on - for managers that are configured rather than
	// coded. It is nil for a built-in manager.
	Custom *model.CustomManager
	// RegistryURLs and Versioning are defaults a manager may stamp onto the
	// dependencies it produces when its own extraction does not name one.
	RegistryURLs []string
	Versioning   string
}

// Manager finds and rewrites dependency references in files.
type Manager interface {
	Name() string

	// FilePatterns returns the manager's default patterns, which the
	// configuration may override.
	FilePatterns() []string

	// Extract reads a file and reports what it found. It must not fail the
	// run for a file it cannot parse: a malformed file yields a warning and
	// no dependencies, because one bad file in a repository is not a reason
	// to stop managing the rest.
	Extract(ctx context.Context, f File, cfg ManagerConfig) (Result, error)

	// Edit produces the byte-range replacement for one update. It returns an
	// error rather than a best guess when the file no longer matches what
	// Extract saw - that means the file changed under us, and writing
	// anyway would corrupt it.
	Edit(ctx context.Context, f File, up model.Update) (model.Edit, error)

	// NeedsPlugin reports a toolchain this manager cannot run in-process,
	// or nil for a pure text manager.
	NeedsPlugin() *PluginRequirement
}

// Result is what one manager found in one file.
type Result struct {
	Deps []model.Dependency
	// Warnings are recorded in the plan rather than returned as errors. A
	// template that could not render or a group that did not participate is
	// worth seeing and is not worth stopping for.
	Warnings []model.Warning
	// LockFiles names files whose contents this manager's dependencies also
	// live in, so the planner knows a plugin will be needed.
	LockFiles []string
}

// PluginRequirement names a toolchain a manager needs but does not carry.
type PluginRequirement struct {
	// Capability is "apply", "resolve", "analyze" or "post-upgrade".
	Capability string
	// Toolchain is "npm", "composer" and so on - what the plugin image must
	// provide.
	Toolchain string
}

// Registry maps a manager name to its implementation. wire fills it.
type Registry map[string]Manager

// Get resolves a manager name, including the `custom.` prefix the
// configuration uses for configured managers.
func (r Registry) Get(name string) (Manager, error) {
	if m, ok := r[name]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("unknown manager %q", name)
}

// Names returns the registered manager names, sorted, so a diagnostic listing
// them is reproducible.
func (r Registry) Names() []string {
	out := make([]string, 0, len(r))
	for n := range r {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Run extracts from one file with one manager, turning a panic into a warning.
//
// A manager is given a regex from configuration and a file from a repository
// neither it nor we control. A panic there must cost that file, not the run -
// and it must be visible, because a manager that panics on a file is a defect
// even when the rest of the repository is fine.
func Run(ctx context.Context, m Manager, f File, cfg ManagerConfig) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = Result{Warnings: []model.Warning{{
				Stage: "extract",
				File:  f.Path,
				Msg:   fmt.Sprintf("manager %q panicked: %v", m.Name(), r),
			}}}
			err = nil
		}
	}()
	return m.Extract(ctx, f, cfg)
}

// SortDeps puts dependencies in the canonical order plan.json uses: by file,
// then by where they sit in it. Determinism here is what lets a golden plan be
// compared byte for byte.
func SortDeps(deps []model.Dependency) {
	sort.SliceStable(deps, func(i, j int) bool {
		if deps[i].File != deps[j].File {
			return deps[i].File < deps[j].File
		}
		if deps[i].Locus.ValueStart != deps[j].Locus.ValueStart {
			return deps[i].Locus.ValueStart < deps[j].Locus.ValueStart
		}
		return deps[i].DepName < deps[j].DepName
	})
}
