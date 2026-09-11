// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"sort"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// ConcatKeys are the keys whose arrays concatenate instead of replacing.
//
// This is the one exception to "arrays replace", and it is the exception the
// whole rule system rests on: presets contribute packageRules that the
// repository's own rules extend rather than erase. Getting it wrong does not
// produce an error, it produces a configuration that quietly ignores half of
// what it was given.
var ConcatKeys = map[string]bool{
	"packageRules": true,
}

// Resolved is a merged configuration with its provenance.
type Resolved struct {
	// Raw is the merged generic document.
	Raw map[string]any
	// Prov maps an RFC 6901 JSON pointer to the ordered chain of origins that
	// wrote it. The whole chain is kept, not just the winner: print-config
	// --explain prints every place that set a key and which one won, which is
	// what makes a rule-ordering surprise diagnosable rather than repeatable.
	Prov map[string][]model.Origin
}

// Merge applies layers in order, parent first. Later layers override earlier
// ones per key.
//
// The semantics match Renovate:
//
//   - objects deep-merge
//   - arrays replace, except the keys in ConcatKeys, which concatenate
//   - an explicit null clears the inherited value
//
// The null case matters more than it looks: `minimumReleaseAge: null` means
// "no hold inherited from anywhere", while `"0"` means "no hold". Nine rules in
// the estate config depend on the difference.
func Merge(layers ...Layer) *Resolved {
	r := &Resolved{Raw: map[string]any{}, Prov: map[string][]model.Origin{}}
	order := 0
	for _, l := range layers {
		mergeInto(r, r.Raw, l.Raw, "", l.Source, &order)
	}
	return r
}

func mergeInto(r *Resolved, dst, src map[string]any, prefix, source string, order *int) {
	// Deterministic order, so provenance Order values are reproducible across
	// runs. Map iteration order would make two identical merges disagree about
	// which of two sibling keys was written first.
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := src[k]
		ptr := prefix + "/" + escapePointer(k)

		switch {
		case v == nil:
			// An explicit null clears. Recorded, because "why is this unset"
			// is exactly the question provenance exists to answer.
			delete(dst, k)
			r.record(ptr, source, model.NoRule, order, "cleared")

		case ConcatKeys[k]:
			add, ok := v.([]any)
			if !ok {
				// Not an array: fall back to replacing, and let schema
				// validation complain about the type rather than panicking
				// here.
				dst[k] = v
				r.record(ptr, source, model.NoRule, order, "")
				continue
			}
			existing, _ := dst[k].([]any)
			base := len(existing)
			merged := make([]any, 0, base+len(add))
			merged = append(merged, existing...)
			merged = append(merged, add...)
			dst[k] = merged
			// Each appended element carries the index it ended up at, so a
			// rule's provenance names its position in the flattened array -
			// which is the index the rule engine reports and a human counts.
			for i := range add {
				r.record(fmt.Sprintf("%s/%d", ptr, base+i), source, base+i, order, "")
			}

		default:
			srcMap, srcIsMap := v.(map[string]any)
			dstMap, dstIsMap := dst[k].(map[string]any)
			if srcIsMap && dstIsMap {
				mergeInto(r, dstMap, srcMap, ptr, source, order)
				continue
			}
			if srcIsMap {
				// Deep-copy, so a later layer editing this subtree cannot
				// reach back into the layer it came from.
				cp := map[string]any{}
				mergeInto(r, cp, srcMap, ptr, source, order)
				dst[k] = cp
				continue
			}
			dst[k] = v
			r.record(ptr, source, model.NoRule, order, "")
		}
	}
}

func (r *Resolved) record(ptr, source string, rule int, order *int, note string) {
	*order++
	o := model.Origin{Source: source, Pointer: ptr, Rule: rule, Order: *order}
	if note != "" {
		// Encoded in the pointer chain rather than as a field, so Origin stays
		// the same shape everywhere it appears in plan.json.
		o.Pointer = ptr + " (" + note + ")"
	}
	r.Prov[ptr] = append(r.Prov[ptr], o)
}

// Winner returns the origin that decided a pointer, and whether it is set.
func (r *Resolved) Winner(pointer string) (model.Origin, bool) {
	chain := r.Prov[pointer]
	if len(chain) == 0 {
		return model.Origin{}, false
	}
	return chain[len(chain)-1], true
}

// Explain renders the full origin chain for a pointer, winner last. An empty
// result means nothing ever set it, which is a different answer from "it is
// set to the default" and is reported as such.
func (r *Resolved) Explain(pointer string) string {
	chain := r.Prov[pointer]
	if len(chain) == 0 {
		return fmt.Sprintf("%s: never set by any layer", pointer)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", pointer)
	for i, o := range chain {
		mark := "  "
		if i == len(chain)-1 {
			mark = "> " // the winner
		}
		rule := ""
		if o.Rule != model.NoRule {
			rule = fmt.Sprintf(" (packageRules[%d])", o.Rule)
		}
		fmt.Fprintf(&b, "%s%s%s\n", mark, o.Source, rule)
	}
	return b.String()
}

// Pointers returns every pointer that was written, sorted. Used by the
// provenance completeness check: every leaf in the merged document must have
// one, and the count is asserted so an empty walk cannot pass.
func (r *Resolved) Pointers() []string {
	out := make([]string, 0, len(r.Prov))
	for p := range r.Prov {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Leaves walks the merged document and returns every pointer that carries an
// origin.
//
// A leaf is a scalar or an array, because an array replaces wholesale - the
// array is the unit a layer writes, not its elements. The exception is a
// concat key: there the elements are what layers contribute, so each element
// is a leaf and the container is not. That mirrors Merge exactly, which is the
// point: if the two disagreed, a completeness check over leaves would either
// demand provenance that is never recorded or miss provenance that should be.
func Leaves(v any, prefix string) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return []string{prefix}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []string
	for _, k := range keys {
		ptr := prefix + "/" + escapePointer(k)
		if elems, isArray := m[k].([]any); isArray && ConcatKeys[k] {
			for i := range elems {
				out = append(out, fmt.Sprintf("%s/%d", ptr, i))
			}
			continue
		}
		out = append(out, Leaves(m[k], ptr)...)
	}
	return out
}
