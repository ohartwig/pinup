// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package apply writes byte-range edits into files.
//
// Layer 2. It consumes model.Edit - a file, a byte range, the bytes there
// now and the bytes to put there - and nothing else: not a dependency, not
// an update, not a version. That is the invariant the plan rests on:
// nothing downstream of the plan can invent a change, and no file is ever
// re-serialised, so comments, key order, quoting and line endings outside
// the edited ranges are untouched by construction.
//
// Two edits on the same bytes of one file are a conflict, reported with
// both edits named and neither applied. composer.json matched by two
// managers is the real case.
package apply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// Conflict is two edits that claim the same bytes.
type Conflict struct {
	A, B model.Edit
}

func (c Conflict) Error() string {
	return fmt.Sprintf("%s: %s edits [%d:%d] and %s edits [%d:%d]; the ranges overlap and neither is applied",
		c.A.File, c.A.Manager, c.A.Start, c.A.End, c.B.Manager, c.B.Start, c.B.End)
}

// Check finds every conflicting pair among edits. It is the whole
// validation a set of edits needs before a write: with no overlap, edits
// commute, and applying them from the end of each file backwards keeps
// every recorded offset valid.
//
// Two edits that are the same edit - same bytes, same replacement - do not
// conflict: a component pin matched by the gitlabci manager and by the
// estate's own regex manager is one change asked for twice, and Renovate
// writes it once. Dedupe removes the repeat.
func Check(edits []model.Edit) []Conflict {
	edits = Dedupe(edits)
	var out []Conflict
	for i := range edits {
		for j := i + 1; j < len(edits); j++ {
			if edits[i].Overlaps(edits[j]) {
				out = append(out, Conflict{edits[i], edits[j]})
			}
		}
	}
	return out
}

// Dedupe drops edits identical to an earlier one in file, range, old and
// new bytes, keeping the first's manager.
func Dedupe(edits []model.Edit) []model.Edit {
	type key struct {
		file       string
		start, end int
		old, new   string
	}
	seen := map[key]bool{}
	out := make([]model.Edit, 0, len(edits))
	for _, e := range edits {
		k := key{e.File, e.Start, e.End, e.Old, e.New}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// Apply returns the file content with edits applied. Every edit's Old must
// be what the range holds; a mismatch means the file changed since the
// plan was made, and writing anyway would corrupt it.
func Apply(content []byte, edits []model.Edit) ([]byte, error) {
	if cs := Check(edits); len(cs) > 0 {
		return nil, cs[0]
	}
	sorted := Dedupe(edits)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start > sorted[j].Start })
	out := append([]byte(nil), content...)
	for _, e := range sorted {
		if e.Start < 0 || e.End > len(out) || e.Start > e.End {
			return nil, fmt.Errorf("%s: edit range [%d:%d] is outside the file (%d bytes)", e.File, e.Start, e.End, len(out))
		}
		if got := string(out[e.Start:e.End]); got != e.Old {
			return nil, fmt.Errorf("%s: bytes [%d:%d] are %q, the plan expected %q; the file changed since the plan was made",
				e.File, e.Start, e.End, got, e.Old)
		}
		out = append(out[:e.Start], append([]byte(e.New), out[e.End:]...)...)
	}
	return out, nil
}

// Result is what a write to one file did.
type Result struct {
	File         string
	Edits        int
	SHA256Before string
	SHA256After  string
}

// WriteFiles applies edits grouped by file under root. Every file is
// checked before any is written, so a conflict or a changed file leaves
// the tree exactly as it was. Files are written with their original mode.
func WriteFiles(root string, edits []model.Edit) ([]Result, error) {
	byFile := map[string][]model.Edit{}
	var files []string
	for _, e := range edits {
		if _, ok := byFile[e.File]; !ok {
			files = append(files, e.File)
		}
		byFile[e.File] = append(byFile[e.File], e)
	}
	sort.Strings(files)

	type pending struct {
		path    string
		mode    os.FileMode
		content []byte
		result  Result
	}
	var todo []pending
	for _, f := range files {
		path := filepath.Join(root, f)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		before, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		after, err := Apply(before, byFile[f])
		if err != nil {
			return nil, err
		}
		if bytes.Equal(before, after) {
			continue
		}
		todo = append(todo, pending{path, info.Mode(), after, Result{
			File: f, Edits: len(byFile[f]), SHA256Before: sum(before), SHA256After: sum(after),
		}})
	}
	var out []Result
	for _, p := range todo {
		if err := os.WriteFile(p.path, p.content, p.mode); err != nil {
			return out, err
		}
		out = append(out, p.result)
	}
	return out, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
