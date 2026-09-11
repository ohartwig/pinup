// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"strconv"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/config/preset"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// ResolveFile loads one configuration file and expands its extends against
// src. The result carries provenance for every key: the file for what it
// wrote itself, "preset:<name>" for what a preset contributed, and the
// whole chain where both did.
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

// ResolveLayer expands a parsed layer's extends. See ResolveFile.
func ResolveLayer(l Layer, src preset.Source) (*Resolved, []string, error) {
	res, err := preset.Resolve(l.Raw, src)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", l.Source, err)
	}
	r := &Resolved{Raw: res.Config, Prov: map[string][]model.Origin{}}
	order := 0
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
	return r, res.Warnings, nil
}
