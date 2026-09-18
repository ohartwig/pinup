// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package advise

import (
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/model"
)

// Two frames: the file as written, and the resolved document. A rule the
// file writes at packageRules[2] stands at packageRules[base+2] once every
// preset's rules are concatenated before it, so a pointer in one frame is
// translated before it is used in the other.

// concatKeys are the arrays presets and file concatenate rather than
// replace - the ones whose indices shift between frames.
var concatKeys = map[string]bool{"packageRules": true, "customManagers": true, "description": true}

// base is the number of elements the presets contribute to a concat key
// before the file's own: the offset between the two frames.
func (in *Input) base(key string) int {
	resolved, _ := in.Resolved.Raw[key].([]any)
	own, _ := in.Layer.Raw[key].([]any)
	return len(resolved) - len(own)
}

// ownIndex maps a resolved index of a concat key to the file's index, and
// whether the element is the file's at all.
func (in *Input) ownIndex(key string, resolved int) (int, bool) {
	i := resolved - in.base(key)
	own, _ := in.Layer.Raw[key].([]any)
	return i, i >= 0 && i < len(own)
}

// ownRules are the file's packageRules, as objects, in file order.
func (in *Input) ownRules() []map[string]any {
	list, _ := in.Layer.Raw["packageRules"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		obj, _ := e.(map[string]any)
		out = append(out, obj)
	}
	return out
}

// resolvedRules are every rule the run evaluates, presets first.
func (in *Input) resolvedRules() []map[string]any {
	list, _ := in.Resolved.Raw["packageRules"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		obj, _ := e.(map[string]any)
		out = append(out, obj)
	}
	return out
}

// originAt is the layer that decided a resolved pointer: the nearest
// pointer with provenance, its winner; the defaults when nothing wrote it.
func (in *Input) originAt(pointer string) model.Origin {
	if at, ok := in.Resolved.Nearest(pointer); ok {
		if o, ok := in.Resolved.Winner(at); ok {
			return o
		}
	}
	return model.Origin{Source: config.DefaultsSource, Pointer: pointer, Rule: model.NoRule}
}

// fileOrigin is the file itself, for a pointer in the file frame.
func (in *Input) fileOrigin(pointer string, rule int) model.Origin {
	return model.Origin{Source: in.Layer.Source, Pointer: pointer, Rule: rule}
}

// ptr builds an RFC 6901 pointer from keys and indices.
func ptr(segs ...any) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteByte('/')
		switch x := s.(type) {
		case int:
			b.WriteString(strconv.Itoa(x))
		case string:
			b.WriteString(config.EscapePointer(x))
		default:
			b.WriteString(config.EscapePointer(fmt.Sprint(x)))
		}
	}
	return b.String()
}

// numberOf reads a JSON number; both parsers deliver float64.
func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// stringsOf reads a string or a list of strings; anything else is empty.
func stringsOf(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// canonical renders a value as JSON with sorted keys, for equality by
// content. Lists keep their order: a rule's order is load-bearing.
func canonical(v any) string {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

// isMatcher reports whether a rule key selects rather than sets.
func isMatcher(key string) bool {
	return strings.HasPrefix(key, "match") || strings.HasPrefix(key, "exclude")
}

// matcherKeys and applyKeys split a rule the way the engine does.
func matcherKeys(rule map[string]any) []string {
	var out []string
	for k := range rule {
		if isMatcher(k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func applyKeys(rule map[string]any) []string {
	var out []string
	for k := range rule {
		if !isMatcher(k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// firstDuplicate reports the index of the first string that repeats an
// earlier one, -1 when none does.
func firstDuplicate(list []string) int {
	seen := map[string]bool{}
	for i, s := range list {
		if seen[s] {
			return i
		}
		seen[s] = true
	}
	return -1
}
