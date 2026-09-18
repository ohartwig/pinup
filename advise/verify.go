// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
)

// Verify is the gate between a rewrite and the disk: it resolves the file
// before and after, as a run would, flattens both, and refuses the rewrite
// if any resolved line changed that the applied fixes did not declare. A
// fix with no declared changes must leave the resolution byte-identical.
//
// Declared pointers are in the file's frame; for the arrays presets
// concatenate they are translated by the presets' contribution, on both
// sides - removing a whole rule shifts every later one, and a declared
// whole element admits that shift.
func Verify(before, after []byte, name string, sources preset.Source, applied []Fix) error {
	from, err := config.Parse(before, name)
	if err != nil {
		return fmt.Errorf("the original does not parse: %w", err)
	}
	to, err := config.Parse(after, name)
	if err != nil {
		return fmt.Errorf("the rewrite does not parse: %w", err)
	}
	rFrom, _, err := config.ResolveLayer(from, sources)
	if err != nil {
		return fmt.Errorf("the original does not resolve: %w", err)
	}
	rTo, _, err := config.ResolveLayer(to, sources)
	if err != nil {
		return fmt.Errorf("the rewrite does not resolve: %w", err)
	}
	linesFrom, linesTo := config.Flatten(rFrom.Raw), config.Flatten(rTo.Raw)
	valueFrom, valueTo := values(linesFrom), values(linesTo)

	var allowed []allowance
	for _, f := range applied {
		for _, declared := range f.Changes {
			allowed = append(allowed, translate(declared, from, rFrom, to, rTo)...)
		}
	}
	for _, path := range config.DiffPaths(linesFrom, linesTo) {
		p := config.PointerOf(path)
		if strings.HasPrefix(p, "/description") {
			continue // migrated into comments and lists; carries no behaviour
		}
		admitted := false
		for _, a := range allowed {
			if a.admits(p, valueTo[path]) {
				admitted = true
				break
			}
		}
		if !admitted {
			return fmt.Errorf("%s changed (%s became %s), which no fix declared; nothing written", path, orAbsent(valueFrom[path]), orAbsent(valueTo[path]))
		}
	}
	return nil
}

func values(lines []config.Line) map[string]string {
	m := make(map[string]string, len(lines))
	for _, l := range lines {
		m[l.Path] = l.Value
	}
	return m
}

func orAbsent(v string) string {
	if v == "" {
		return "absent"
	}
	return v
}

// allowance is one resolved pointer a rewrite may change, with everything
// under it; from is a lower bound on the index of a concat element, for a
// declared whole element whose removal shifts what follows.
type allowance struct {
	pointer string
	key     string // the concat key, when the allowance is an index floor
	from    int
}

// admits reports whether a changed pointer, now holding after, is covered:
// at or below the allowance, or above it when the container collapsed to
// nothing - removing a rule's only key leaves {} where the rule was.
func (a allowance) admits(p, after string) bool {
	if a.key != "" {
		rest, ok := strings.CutPrefix(p, "/"+a.key+"/")
		if !ok {
			return false
		}
		idx, _, _ := strings.Cut(rest, "/")
		n, err := strconv.Atoi(idx)
		return err == nil && n >= a.from
	}
	if under(p, a.pointer) {
		return true
	}
	return under(a.pointer, p) && (after == "{}" || after == "[]")
}

// translate maps one declared file pointer into the resolved frame, on both
// sides of the rewrite.
func translate(declared string, from config.Layer, rFrom *config.Resolved, to config.Layer, rTo *config.Resolved) []allowance {
	segs := strings.Split(strings.TrimPrefix(declared, "/"), "/")
	if len(segs) < 2 || !concatKeys[segs[0]] {
		return []allowance{{pointer: declared}}
	}
	i, err := strconv.Atoi(segs[1])
	if err != nil {
		return []allowance{{pointer: declared}}
	}
	var out []allowance
	for _, side := range []struct {
		layer config.Layer
		res   *config.Resolved
	}{{from, rFrom}, {to, rTo}} {
		resolved, _ := side.res.Raw[segs[0]].([]any)
		own, _ := side.layer.Raw[segs[0]].([]any)
		base := len(resolved) - len(own)
		if len(segs) == 2 {
			out = append(out, allowance{key: segs[0], from: base + i})
			continue
		}
		shifted := append([]string{segs[0], strconv.Itoa(base + i)}, segs[2:]...)
		out = append(out, allowance{pointer: "/" + strings.Join(shifted, "/")})
	}
	return out
}
