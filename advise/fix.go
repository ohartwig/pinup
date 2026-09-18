// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/jsonc"
)

// Applied is one fix written into the text, with where and what.
type Applied struct {
	Fix  Fix    `json:"fix"`
	Line int    `json:"line"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new,omitempty"`
}

// Skipped is one fix left alone, and why.
type Skipped struct {
	Fix    Fix    `json:"fix"`
	Reason string `json:"reason"`
}

// Apply rewrites src - the configuration file named by name, so the format
// is known - with every fix it can apply, byte for byte, comments and order
// kept. It writes nothing; the caller decides where the result goes, after
// Verify has passed it.
//
// Fixes are applied from the highest pointer down, so removing
// /packageRules/3 never moves /packageRules/2 under a later fix. Two fixes
// at one pointer are both skipped; a fix under a pointer another removes is
// skipped. YAML is not edited in this version: every fix on a .yaml file is
// skipped with the line to write by hand.
func Apply(src []byte, name string, fixes []Fix) ([]byte, []Applied, []Skipped, error) {
	var applied []Applied
	var skipped []Skipped
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		for _, f := range fixes {
			skipped = append(skipped, Skipped{Fix: f, Reason: "YAML is not rewritten; apply by hand: " + manual(f)})
		}
		return src, nil, skipped, nil
	case ".json", ".jsonc", ".json5", "":
	default:
		return nil, nil, nil, fmt.Errorf("%s: no rewriter for %q files", name, filepath.Ext(name))
	}

	ordered := slices.Clone(fixes)
	slices.SortStableFunc(ordered, func(a, b Fix) int { return comparePointers(b.Pointer, a.Pointer) })
	at := map[string]int{}
	var removed []string
	for _, f := range ordered {
		at[f.Pointer]++
		if f.Op == OpRemove {
			removed = append(removed, f.Pointer)
		}
	}
	unit := jsonc.Indent(src)
	out := src
	for _, f := range ordered {
		if at[f.Pointer] > 1 {
			skipped = append(skipped, Skipped{Fix: f, Reason: "conflicts with another fix at the same pointer"})
			continue
		}
		if i := slices.IndexFunc(removed, func(r string) bool { return r != f.Pointer && under(f.Pointer, r) }); i >= 0 {
			skipped = append(skipped, Skipped{Fix: f, Reason: "shadowed by the removal of " + removed[i]})
			continue
		}
		var (
			next []byte
			edit jsonc.Edit
			err  error
		)
		switch f.Op {
		case OpSet:
			next, edit, err = jsonc.Set(out, f.Pointer, encode(f.Value, unit))
		case OpRemove:
			next, edit, err = jsonc.Remove(out, f.Pointer)
		case OpAppend:
			next, edit, err = jsonc.Append(out, f.Pointer, encode(f.Value, unit))
		default:
			err = fmt.Errorf("unknown op %q", f.Op)
		}
		if err != nil {
			skipped = append(skipped, Skipped{Fix: f, Reason: err.Error()})
			continue
		}
		applied = append(applied, Applied{Fix: f, Line: bytes.Count(out[:edit.Start], []byte("\n")) + 1, Old: strings.TrimSpace(edit.Old), New: strings.TrimSpace(edit.New)})
		out = next
	}
	return out, applied, skipped, nil
}

// under reports whether p is at or below parent.
func under(p, parent string) bool {
	return p == parent || strings.HasPrefix(p, parent+"/")
}

// manual is the fix as a line to write by hand.
func manual(f Fix) string {
	switch f.Op {
	case OpRemove:
		return "remove " + f.Pointer
	case OpAppend:
		return fmt.Sprintf("append %s to %s", encode(f.Value, "  "), f.Pointer)
	}
	return fmt.Sprintf("set %s = %s", f.Pointer, encode(f.Value, "  "))
}

// encode renders a fix value as JSON text against the left margin: scalars
// and lists of scalars on one line, as the estate writes them, anything
// else indented by the file's unit. jsonc indents the continuation lines to
// the member the value joins.
func encode(v any, unit string) []byte {
	if list, ok := v.([]any); ok && scalars(list) {
		parts := make([]string, len(list))
		for i, e := range list {
			b, _ := json.Marshal(e)
			parts[i] = string(b)
		}
		return []byte("[" + strings.Join(parts, ", ") + "]")
	}
	switch v.(type) {
	case map[string]any, []any:
		b, err := json.Marshal(v, json.Deterministic(true), jsontext.WithIndent(unit))
		if err != nil {
			return []byte("null")
		}
		return b
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

func scalars(list []any) bool {
	for _, e := range list {
		switch e.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}
