// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Line is one flattened leaf: a path in the print-config notation and the
// leaf's value as canonical JSON.
type Line struct {
	Path  string
	Value string
}

func (l Line) String() string { return l.Path + " " + l.Value }

// Flatten renders a document as leaf lines in document order - object keys
// sorted, array elements by index:
//
//	packageRules[25].automerge true
//	packageRules[25].matchManagers[0] "custom.regex"
//
// Comparison happens over these lines, never over trees: rule order is
// load-bearing, a tree differ reorders array elements invisibly, and a swap
// of two rules must show up as the dozen differing lines it is. An empty
// array or object is a leaf of its own, so "set to nothing" is not "unset".
func Flatten(v any) []Line {
	var out []Line
	flatten("", v, &out)
	return out
}

func flatten(path string, v any, out *[]Line) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			*out = append(*out, Line{path, "{}"})
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			flatten(p, x[k], out)
		}
	case []any:
		if len(x) == 0 {
			*out = append(*out, Line{path, "[]"})
			return
		}
		for i, e := range x {
			flatten(fmt.Sprintf("%s[%d]", path, i), e, out)
		}
	default:
		b, _ := json.Marshal(v)
		*out = append(*out, Line{path, string(b)})
	}
}

// Diff compares two flattened documents as path sets. Lines only in a are
// reported with "-", only in b with "+", and a path present in both with
// different values as both. Order is a's order for removals, b's for
// additions, so the output reads like the documents do.
func Diff(a, b []Line) []string {
	av := make(map[string]string, len(a))
	for _, l := range a {
		av[l.Path] = l.Value
	}
	bv := make(map[string]string, len(b))
	for _, l := range b {
		bv[l.Path] = l.Value
	}
	var out []string
	for _, l := range a {
		if w, ok := bv[l.Path]; !ok || w != l.Value {
			out = append(out, "- "+l.String())
		}
	}
	for _, l := range b {
		if w, ok := av[l.Path]; !ok || w != l.Value {
			out = append(out, "+ "+l.String())
		}
	}
	return out
}

// PointerOf turns a print-config path back into the RFC 6901 pointer the
// provenance map is keyed by: "packageRules[25].automerge" ->
// "/packageRules/25/automerge".
func PointerOf(path string) string {
	var b strings.Builder
	for _, seg := range strings.Split(path, ".") {
		for {
			i := strings.IndexByte(seg, '[')
			if i < 0 {
				b.WriteString("/" + escapePointer(seg))
				break
			}
			if i > 0 {
				b.WriteString("/" + escapePointer(seg[:i]))
			}
			j := strings.IndexByte(seg, ']')
			b.WriteString("/" + seg[i+1:j])
			seg = seg[j+1:]
			if seg == "" {
				break
			}
		}
	}
	return b.String()
}
