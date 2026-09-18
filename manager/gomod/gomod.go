// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package gomod extracts and rewrites dependency references in go.mod files -
// Renovate's `gomod` manager.
//
// Measured across the extraction corpus
// (testdata/<root>/renovate-43.288.0/extract/gomod.json, key "gomod", 10
// dependencies over the synthetic tree's two go.mod files):
//
//	go 1.27.0                              depName go, depType golang
//	                                        datasource golang-version
//	                                        versioning go-mod-directive
//	                                        currentValue verbatim ("1.27.0")
//
//	toolchain go1.27.1                     depName go, depType toolchain
//	                                        datasource golang-version
//	                                        currentValue "1.27.1" - the "go"
//	                                        prefix is stripped from the
//	                                        value AND from its Locus, so
//	                                        Edit rewrites only the number.
//	                                        No versioning is set: the
//	                                        datasource's own default applies.
//
//	require x v1.0.0                       depType require, datasource go
//	  (block or single-line)                packageName and depName are both
//	                                        the module path; currentValue is
//	                                        the version, quotes never present.
//
//	require x v1.0.0 // indirect            depType indirect, and - because
//	                                        Renovate marks it enabled:false -
//	                                        Disabled "indirect dependency":
//	                                        extraction records it, and only
//	                                        a security fix moves it.
//
//	replace old => new vX                  one dependency: depName and
//	                                        packageName are the RIGHT-hand
//	                                        module path, currentValue is the
//	                                        right-hand version. A replace
//	                                        whose right-hand side is a local
//	                                        path ("=> ../local") is not in
//	                                        the corpus; it yields nothing,
//	                                        which is the natural reading of
//	                                        Terraform's own local-module case
//	                                        (manager/terraform) rather than a
//	                                        guess made up for this report. A
//	                                        module replace with no version at
//	                                        all is likewise not in the corpus
//	                                        (Go's own grammar only allows
//	                                        that for a local right-hand side)
//	                                        and is treated the same way.
//
//	exclude x v1.0.0                       yields nothing.
//	tool x/cmd/y                           yields nothing.
//	module x                               yields nothing.
//
// No JSON or module-graph library is used: this package hand-rolls a small
// line scanner over go.mod's line-oriented grammar - directives, one
// `require ( ... )` block, `//` line comments - the same shape
// manager/tfversion and manager/terraform use for their own text formats.
// Nothing is re-serialized: every dependency reports a model.Locus, the byte
// span of its version, and Edit routes through extract.EditRef so that byte
// range and nothing else is ever rewritten.
package gomod

import (
	"context"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/versioning"
)

// name is both the registry key (extract.Registry) and model.Dependency.Manager.
const name = "gomod"

// goVersioning is the scheme registered for the `go` directive - the module's
// minimum toolchain, read as a caret floor. The toolchain directive carries
// no versioning of its own; the datasource's default applies there instead.
const goVersioning = "go-mod-directive"

// Manager implements extract.Manager for go.mod files. It carries no state:
// every call is a pure function of the file it is given.
type Manager struct{}

// New returns a ready-to-use Manager.
func New() *Manager { return &Manager{} }

// Name implements extract.Manager.
func (m *Manager) Name() string { return name }

// FilePatterns implements extract.Manager.
func (m *Manager) FilePatterns() []string { return []string{"**/go.mod"} }

// NeedsPlugin implements extract.Manager. `go mod tidy` after an update is a
// later task's concern; extraction and the edit it produces are plain text
// scanning that needs no toolchain of its own.
func (m *Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Extract implements extract.Manager. A line this scanner does not recognise
// simply yields no dependency for that line - the same "warn, don't stop"
// contract every text manager in this estate follows for a single bad line.
func (m *Manager) Extract(_ context.Context, f extract.File, _ extract.ManagerConfig) (extract.Result, error) {
	var deps []model.Dependency
	inRequireBlock := false

	for _, ln := range splitLines(f.Content) {
		code, comment := splitComment(ln.text)
		trimmed := strings.TrimSpace(code)

		if inRequireBlock {
			if trimmed == ")" {
				inRequireBlock = false
				continue
			}
			if trimmed == "" {
				continue
			}
			if d, ok := requireDep(f.Path, ln, fieldsWithOffsets(code), comment); ok {
				deps = append(deps, d)
			}
			continue
		}

		if trimmed == "" {
			continue
		}
		toks := fieldsWithOffsets(code)
		if len(toks) == 0 {
			continue
		}

		switch toks[0].text {
		case "go":
			if len(toks) >= 2 {
				deps = append(deps, goDirectiveDep(f.Path, ln, toks[1]))
			}
		case "toolchain":
			if len(toks) >= 2 {
				deps = append(deps, toolchainDep(f.Path, ln, toks[1]))
			}
		case "require":
			switch {
			case len(toks) >= 2 && toks[1].text == "(":
				inRequireBlock = true
			case len(toks) >= 3:
				if d, ok := requireDep(f.Path, ln, toks[1:], comment); ok {
					deps = append(deps, d)
				}
			}
		case "replace":
			if d, ok := replaceDep(f.Path, ln, toks[1:]); ok {
				deps = append(deps, d)
			}
		case "module", "exclude", "tool":
			// Measured: none of these yield a dependency.
		}
	}

	extract.SortDeps(deps)
	// go.sum is the lock: every module update needs its hashes refreshed,
	// which is `go mod tidy` on the branch, and the planner learns from
	// LockFiles that the go toolchain will be needed for this file. The
	// go directive and toolchain carry no lock entry and stay without.
	for i := range deps {
		if deps[i].Datasource == "go" {
			deps[i].LockFiles = []string{"go.sum"}
		}
	}
	return extract.Result{Deps: deps, LockFiles: []string{"go.sum"}}, nil
}

// LockedVersions is what go.sum says about versions in use: nothing. go.sum
// is a lock in the sense that it must be refreshed when go.mod moves, not
// in the sense that it names the version in use - go.mod does that itself,
// and go.sum may carry hashes for versions no longer required until the
// next tidy. An empty, non-nil answer marks the lock as present without
// overriding a single current value.
func LockedVersions(_ []byte) (map[string]string, error) {
	return map[string]string{}, nil
}

// goDirectiveDep builds the dependency for the module's `go` directive.
func goDirectiveDep(file string, ln line, v tok) model.Dependency {
	return model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       "go",
		DepType:       "golang",
		Datasource:    "golang-version",
		Versioning:    goVersioning,
		CurrentValue:  v.text,
		Locus:         valueLocus(ln, v.start, v.end),
	}
}

// toolchainDep builds the dependency for the `toolchain` directive. The value
// is always written as "go" followed by a version (e.g. "go1.27.1"); the "go"
// prefix is stripped from both CurrentValue and the Locus, so an Edit moves
// only the number and never touches the literal "go".
func toolchainDep(file string, ln line, v tok) model.Dependency {
	raw := v.text
	version := strings.TrimPrefix(raw, "go")
	prefixLen := len(raw) - len(version)
	return model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       "go",
		DepType:       "toolchain",
		Datasource:    "golang-version",
		CurrentValue:  version,
		Locus:         valueLocus(ln, v.start+prefixLen, v.end),
	}
}

// requireDep builds the dependency for one `require` entry, whether it came
// from inside a `require ( ... )` block (toks is [path, version]) or a
// single-line `require path version` (toks is the same [path, version], the
// leading "require" token already stripped by the caller).
//
// A trailing "// indirect" comment - checked loosely, by substring, since an
// indirect comment in the wild sometimes carries extra text after a
// semicolon - marks the dependency depType "indirect" and marks it
// Disabled: Renovate records it as enabled:false, and extraction records
// that fact rather than acting on it.
func requireDep(file string, ln line, toks []tok, comment string) (model.Dependency, bool) {
	if len(toks) < 2 {
		return model.Dependency{}, false
	}
	path, version := toks[0], toks[1]

	depType := "require"
	disabled := ""
	if strings.Contains(comment, "indirect") {
		depType = "indirect"
		disabled = "indirect dependency"
	}

	return model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       path.text,
		PackageName:   path.text,
		DepType:       depType,
		Datasource:    "go",
		CurrentValue:  version.text,
		// A pseudo-version pins a commit; the commit is the digest and
		// the version around it is how go.mod spells it. The locus stays
		// on the whole value: a move rewrites the pseudo-version, there
		// is no separate digest field to edit.
		CurrentDigest: versioning.GoPseudoCommit(version.text),
		Disabled:      disabled,
		Locus:         valueLocus(ln, version.start, version.end),
	}, true
}

// replaceDep builds the dependency for a `replace old-path [old-version] =>
// new-path [new-version]` line. Only the right-hand side is reported: the
// left-hand module is not itself a dependency, it is the one being replaced.
//
// A right-hand side with no version is, by Go's own module grammar, a local
// filesystem path (the only case a version may be omitted) - not in the
// corpus, and reported as nothing here rather than guessed at, the same
// convention manager/terraform uses for a local module source.
func replaceDep(file string, ln line, toks []tok) (model.Dependency, bool) {
	arrow := -1
	for i, t := range toks {
		if t.text == "=>" {
			arrow = i
			break
		}
	}
	if arrow < 0 {
		return model.Dependency{}, false
	}
	right := toks[arrow+1:]
	if len(right) < 2 {
		// Either nothing follows "=>", or the right-hand side has no
		// version - a local replacement, which Go's grammar is the only
		// shape that allows.
		return model.Dependency{}, false
	}
	newPath, newVersion := right[0], right[1]
	if strings.HasPrefix(newPath.text, "./") || strings.HasPrefix(newPath.text, "../") {
		return model.Dependency{}, false
	}

	return model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       newPath.text,
		PackageName:   newPath.text,
		DepType:       "replace",
		Datasource:    "go",
		CurrentValue:  newVersion.text,
		Locus:         valueLocus(ln, newVersion.start, newVersion.end),
	}, true
}

// valueLocus turns a token's line-relative span into the model.Locus that
// brackets it in the whole file: no digest group, this manager never reports
// one, and the line the value sits on for a human-readable report.
func valueLocus(ln line, start, end int) model.Locus {
	return model.Locus{
		ValueStart: ln.start + start, ValueEnd: ln.start + end,
		DigestStart: model.NoDigest, DigestEnd: model.NoDigest,
		Line: ln.number,
	}
}

// Edit implements extract.Manager. go.mod values carry no digest field - a
// pseudo-version's commit is part of the value - so this is exactly
// extract.EditRef's plain value-replacement case: it refuses when the bytes
// it recorded at extraction time no longer match what is on disk now, rather
// than guessing.
func (m *Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	return extract.EditRef(name, f, up)
}
