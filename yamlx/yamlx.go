// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package yamlx is the single import site of gopkg.in/yaml.v3 in this tree.
//
// Confining it to one package has two purposes. It keeps the dependency
// swappable - a move to go.yaml.in/yaml/v3 is one file, not a hundred - and it
// gives one place to hold the conversion every caller would otherwise
// reimplement: YAML decodes maps as map[string]any only if every key is a
// string, and yaml.v3 will hand back map[any]any the moment a key is a number
// or a boolean. A config file with a key `on:` or `8.5:` is not exotic in a
// GitLab pipeline.
//
// It also exposes node positions as byte offsets, which is what
// format-preserving edits need: a manager replaces the bytes of a scalar and
// copies everything else through untouched.
package yamlx

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Unmarshal decodes a YAML document into the generic shape the config merger
// works in: map[string]any, []any and scalars.
func Unmarshal(src []byte, out *map[string]any) error {
	var raw any
	if err := yaml.Unmarshal(src, &raw); err != nil {
		return err
	}
	if raw == nil {
		*out = map[string]any{}
		return nil
	}
	v, err := normalise(raw)
	if err != nil {
		return err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("document is a %T, want a mapping at the top level", v)
	}
	*out = m
	return nil
}

// normalise turns yaml.v3's output into the JSON-shaped subset. Non-string
// keys are stringified rather than rejected: `8.5:` in a config is a key, and
// refusing the file would be worse than accepting the obvious reading.
func normalise(v any) (any, error) {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			n, err := normalise(e)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			n, err := normalise(e)
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(k)] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			n, err := normalise(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case int:
		// The merger compares against JSON-decoded values, where every number
		// is a float64. Keeping one numeric type avoids "1 != 1" surprises
		// when a YAML layer merges over a JSON one.
		return float64(v), nil
	default:
		return v, nil
	}
}

// ParseTree parses a document into a node tree, preserving positions.
// Managers use it to locate a scalar's bytes without re-serializing the file.
func ParseTree(src []byte) (*Node, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(src, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// Offset converts a node's line and column into a byte offset into src.
//
// yaml.v3 reports 1-based line and column and no offset, so this walks the
// line starts once. The byte offset is what an Edit needs; a line and column
// would have to be re-resolved by every caller, and each of them would have to
// agree about tabs.
func Offset(src []byte, line, column int) int {
	if line < 1 || column < 1 {
		return -1
	}
	off, cur := 0, 1
	for cur < line && off < len(src) {
		if src[off] == '\n' {
			cur++
		}
		off++
	}
	if cur != line {
		return -1
	}
	off += column - 1
	if off > len(src) {
		return -1
	}
	return off
}

// The node vocabulary, re-exported so a consumer can walk a tree without
// importing yaml.v3 itself. A convention test holds every other package to
// that: the dependency is meant to be swappable, and it is only swappable if
// exactly one package names it.
type Node = yaml.Node

const (
	DocumentNode = yaml.DocumentNode
	MappingNode  = yaml.MappingNode
	SequenceNode = yaml.SequenceNode
	ScalarNode   = yaml.ScalarNode
	AliasNode    = yaml.AliasNode
)
