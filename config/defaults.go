// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
)

// defaults.json is Renovate's option defaults as the pinned container
// resolved them - the branch and commit-message templates, the per-manager
// file patterns, the update-type objects - generated from the captured
// --print-config output by tools/defaultsgen. A test keeps the two
// identical. The configuration is layered over these: objects deep-merge
// (digest: {automerge: true} keeps the default digest templates), anything
// else the configuration sets replaces the default.
//
//go:embed defaults.json
var defaultsJSON []byte

// Defaults returns a fresh copy of the builtin defaults.
func Defaults() map[string]any {
	var doc struct {
		Defaults map[string]any `json:"defaults"`
	}
	if err := json.NewDecoder(bytes.NewReader(defaultsJSON)).Decode(&doc); err != nil {
		panic("config: embedded defaults do not parse: " + err.Error())
	}
	return doc.Defaults
}

// DefaultsSource is the origin recorded for a value the defaults supplied.
const DefaultsSource = "builtin"
