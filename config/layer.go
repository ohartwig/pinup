// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package config loads configuration layers and merges them the way Renovate
// does, keeping a record of where every resolved value came from.
//
// The merge works on the generic shape - map[string]any, []any, scalars -
// rather than on the typed Config, for two reasons. Renovate's own semantics
// are defined over JSON, so "objects deep-merge, arrays replace, packageRules
// concatenate, null clears" is expressible directly; and a generic walk can
// record a provenance entry per JSON pointer without every field having to opt
// in. The typed Config is decoded once at the end.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohartwig/pinup/jsonc"
	"github.com/ohartwig/pinup/yamlx"
)

// Layer is one source of configuration, already parsed.
type Layer struct {
	// Source is "file:<path>", "preset:<name>", "env:<var>", "cli" or
	// "builtin". It appears verbatim in every Origin this layer produces, so
	// print-config --explain can name it.
	Source string
	Raw    map[string]any
}

// LoadFile reads a configuration file, choosing the parser by extension and
// tolerating comments wherever the format allows them.
func LoadFile(path string) (Layer, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return Layer{}, err
	}
	l, err := Parse(src, path)
	if err != nil {
		return Layer{}, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

// ParsePreset is Parse for a fetched preset: the document alone, by the
// file's name. Handed to preset.Remote, which sits beside the parsers.
func ParsePreset(src []byte, name string) (map[string]any, error) {
	l, err := Parse(src, name)
	if err != nil {
		return nil, err
	}
	return l.Raw, nil
}

// Parse parses a document, choosing the parser from the name.
func Parse(src []byte, name string) (Layer, error) {
	raw := map[string]any{}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		if err := yamlx.Unmarshal(src, &raw); err != nil {
			return Layer{}, err
		}
	case ".json5":
		// Unquoted keys and single-quoted strings, as the one such file in
		// the estate writes them, then the same stripping as JSONC.
		if err := json.Unmarshal(jsonc.FromJSON5(src), &raw); err != nil {
			return Layer{}, err
		}
	case ".json", ".jsonc", "":
		// Comments are stripped unconditionally. A plain .json file is
		// unaffected - stripping is a no-op when there is nothing to strip -
		// and a .json file that does contain comments is far more likely to be
		// a mistake in the extension than a syntax error the user wants
		// reported.
		if err := json.Unmarshal(jsonc.Strip(src), &raw); err != nil {
			return Layer{}, err
		}
	default:
		return Layer{}, fmt.Errorf("unsupported extension %q", filepath.Ext(name))
	}
	return Layer{Source: "file:" + name, Raw: raw}, nil
}

// SHA256Name is the conventional file set pinup looks for, in order. The first
// that exists wins; a repository carrying two is a mistake worth reporting
// rather than resolving silently.
var ConfigFileNames = []string{
	".pinup.yaml", ".pinup.yml", ".pinup.json", ".pinup.jsonc",
	"renovate.json", "renovate.json5", ".renovaterc", ".renovaterc.json",
}

// FindConfigFile returns the configuration file in dir, or "" if there is
// none. Two files is an error: picking one silently means the other is edited
// for weeks with no effect.
func FindConfigFile(dir string) (string, error) {
	var found []string
	for _, n := range ConfigFileNames {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("more than one configuration file: %s", strings.Join(found, ", "))
	}
}

// EscapePointer encodes one key as an RFC 6901 pointer segment.
func EscapePointer(key string) string {
	key = strings.ReplaceAll(key, "~", "~0")
	return strings.ReplaceAll(key, "/", "~1")
}

func escapePointer(key string) string { return EscapePointer(key) }
