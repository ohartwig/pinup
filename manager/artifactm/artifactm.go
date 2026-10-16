// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package artifactm is the manager behind a component an artifact manifest
// named: a module or package the build of a file shipped without a line in
// that file (docs/artifact-manifests.md).
//
// It extracts nothing. The manifest stage reads the components and records
// them as dependencies of the file whose build the manifest describes; the
// one thing this manager does is the edit - the line a configured pin
// writes when an advisory names a fix. There is no value to replace, so the
// edit is an insertion: after the line the pin's `after` pattern last
// matches, the pin's `line` template rendered for the update.
//
// The pattern and the template travel on the dependency (model.ArtifactRef),
// and the place is computed here against the file as the edit is asked for:
// a file whose anchor is gone is refused, as every other manager refuses a
// file that changed under the run, rather than patched at a remembered
// offset that now points somewhere else.
package artifactm

import (
	"context"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/hbs"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/re2x"
)

// Manager edits the pins of artifact-manifest components.
type Manager struct{}

// New returns the manager.
func New() *Manager { return &Manager{} }

func (*Manager) Name() string { return model.ArtifactManager }

// FilePatterns returns nothing: the manifest stage decides which files
// have manifests, and discovery must not hand this manager any file.
func (*Manager) FilePatterns() []string { return nil }

func (*Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Extract finds nothing in a file: components come from manifests, not
// from the file's text.
func (*Manager) Extract(context.Context, extract.File, extract.ManagerConfig) (extract.Result, error) {
	return extract.Result{}, nil
}

// Insertion is where a pin goes and what it says, computed for one file
// and one update.
type Insertion struct {
	// Offset is the byte offset the new text is inserted at: the end of
	// the line the anchor's last match ends on, before its line break (or
	// the end of the file).
	Offset int
	// Text is what is inserted: a line break and the rendered line.
	Text string
}

// Place computes the insertion of a pin into content: the last match of
// after marks the line, indent is the match's `indent` group or the
// indentation of that line, and line renders with indent, depName,
// packageName, newValue, newVersion and the match's groups.
func Place(content []byte, after, line string, up model.Update) (Insertion, error) {
	if after == "" || line == "" {
		return Insertion{}, fmt.Errorf("%s: %s: no pin configured for %s (artifactManifests[%d].pin)", model.ArtifactManager, up.Dep.File, up.Dep.DepName, manifestOf(up.Dep))
	}
	re, err := re2x.Compile(after)
	if err != nil {
		return Insertion{}, fmt.Errorf("%s: %s: pin.after does not compile: %v", model.ArtifactManager, up.Dep.File, err)
	}
	src := string(content)
	matches := re.FindAll(src)
	if len(matches) == 0 {
		return Insertion{}, fmt.Errorf("%s: %s: pin.after %q matches nothing; the file changed since the manifest was read, or the pattern names a line the file never had", model.ArtifactManager, up.Dep.File, after)
	}
	last := matches[len(matches)-1]
	_, end, ok := last.Whole()
	if !ok {
		return Insertion{}, fmt.Errorf("%s: %s: pin.after matched nothing to anchor on", model.ArtifactManager, up.Dep.File)
	}
	// The line the match ends on: back to its start for the indentation,
	// forward to its end for the offset. A match that ends exactly on a
	// line break belongs to the line before it.
	lineEnd := end
	if lineEnd > 0 && src[lineEnd-1] == '\n' {
		lineEnd--
	}
	if i := strings.IndexByte(src[lineEnd:], '\n'); i >= 0 {
		lineEnd += i
	} else {
		lineEnd = len(src)
	}
	lineStart := strings.LastIndexByte(src[:lineEnd], '\n') + 1
	indent := src[lineStart : lineStart+len(src[lineStart:lineEnd])-len(strings.TrimLeft(src[lineStart:lineEnd], " \t"))]

	env := hbs.MapEnv{Values: map[string]string{}, Absent: map[string]bool{}}
	for _, name := range last.Names() {
		if v, ok := last.Get(name); ok {
			env.Values[name] = v
		} else {
			env.Absent[name] = true
		}
	}
	if v, ok := last.Get("indent"); ok {
		indent = v
	}
	env.Values["indent"] = indent
	env.Values["depName"] = up.Dep.DepName
	env.Values["packageName"] = firstOf(up.Dep.PackageName, up.Dep.DepName)
	env.Values["newValue"] = up.NewValue
	env.Values["newVersion"] = firstOf(up.NewVersion, up.NewValue)
	delete(env.Absent, "indent")
	rendered, set, err := hbs.RenderString(line, env)
	if err != nil {
		return Insertion{}, fmt.Errorf("%s: %s: pin.line: %v", model.ArtifactManager, up.Dep.File, err)
	}
	if !set || strings.TrimSpace(rendered) == "" {
		return Insertion{}, fmt.Errorf("%s: %s: pin.line %q rendered nothing for %s", model.ArtifactManager, up.Dep.File, line, up.Dep.DepName)
	}
	if strings.Contains(rendered, "\n") {
		return Insertion{}, fmt.Errorf("%s: %s: pin.line rendered more than one line for %s; one pin is one line", model.ArtifactManager, up.Dep.File, up.Dep.DepName)
	}
	return Insertion{Offset: lineEnd, Text: "\n" + rendered}, nil
}

// Edit inserts the pin for an update: the configured line after the
// anchor's last match, as a zero-width edit at that offset.
func (*Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	a := up.Dep.Artifact
	if a == nil {
		return model.Edit{}, fmt.Errorf("%s: %s: %s was not read from an artifact manifest", model.ArtifactManager, f.Path, up.Dep.DepName)
	}
	if up.NewValue == "" {
		return model.Edit{}, fmt.Errorf("%s: %s: update for %s names no value to pin", model.ArtifactManager, f.Path, up.Dep.DepName)
	}
	ins, err := Place(f.Content, a.After, a.Line, up)
	if err != nil {
		return model.Edit{}, err
	}
	return model.Edit{File: f.Path, Start: ins.Offset, End: ins.Offset, Old: "", New: ins.Text, Manager: model.ArtifactManager}, nil
}

func manifestOf(d model.Dependency) int {
	if d.Artifact == nil {
		return -1
	}
	return d.Artifact.Manifest
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
