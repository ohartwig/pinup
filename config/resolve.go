// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/model"
)

// ResolveFile loads one configuration file, expands its extends against
// src, and layers the result over the builtin defaults. The result carries
// provenance for every key: "builtin" for a default, the file for what it
// wrote itself, "preset:<name>" for what a preset contributed, and the
// whole chain where several did.
//
// Warnings name inert presets the file uses. They are returned, not
// dropped: a configuration that extends something pinup does not act on
// must say so somewhere a reader looks.
func ResolveFile(path string, src preset.Source) (*Resolved, []string, error) {
	l, err := LoadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ResolveLayer(l, src)
}

// ResolveLayer expands a parsed layer's extends and migrates the result.
// See ResolveFile.
func ResolveLayer(l Layer, src preset.Source) (*Resolved, []string, error) {
	res, err := preset.Resolve(l.Raw, src)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", l.Source, err)
	}
	migrated, notes := Migrate(res.Config)
	res.Config = migrated
	r := &Resolved{Raw: map[string]any{}, Prov: map[string][]model.Origin{}, Migrations: notes}
	order := 0
	// The defaults first, so every key the configuration does not set is
	// still there and says where it came from. Written directly rather
	// than merged: a default of null is a key Renovate carries, not a
	// clearing of something above it.
	for k, v := range Defaults() {
		r.Raw[k] = v
		order++
		r.Prov["/"+escapePointer(k)] = []model.Origin{{Source: DefaultsSource, Pointer: "/" + escapePointer(k), Rule: model.NoRule, Order: order}}
	}
	for ptr, chain := range res.Origins {
		rule := model.NoRule
		if rest, ok := strings.CutPrefix(ptr, "/packageRules/"); ok {
			if n, err := strconv.Atoi(rest); err == nil {
				rule = n
			}
		}
		for _, who := range chain {
			order++
			source := "preset:" + who
			if who == preset.OwnSource {
				source = l.Source
			}
			r.Prov[ptr] = append(r.Prov[ptr], model.Origin{Source: source, Pointer: ptr, Rule: rule, Order: order})
		}
	}
	// Now the resolved document over the defaults. Objects deep-merge -
	// measured: digest: {automerge: true} keeps the default digest
	// templates - and everything else replaces; the provenance for what the
	// document set is already recorded above, so this pass records nothing.
	overlayResolved(r.Raw, res.Config)
	return r, res.Warnings, nil
}

// overlayResolved writes doc over dst: objects one level deep merge, all
// else replaces. One level is what the captured defaults need - the
// update-type objects and manager objects are flat.
func overlayResolved(dst, doc map[string]any) {
	for k, v := range doc {
		sub, subOK := v.(map[string]any)
		existing, existingOK := dst[k].(map[string]any)
		if subOK && existingOK {
			merged := make(map[string]any, len(existing)+len(sub))
			for kk, vv := range existing {
				merged[kk] = vv
			}
			for kk, vv := range sub {
				merged[kk] = vv
			}
			dst[k] = merged
			continue
		}
		dst[k] = v
	}
}
