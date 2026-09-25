// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ohartwig/pinup/model"
)

// PlanDiff says what a configuration change does, in terms of the plan: the
// merge requests it adds, the ones it takes away, the ones it changes, the
// updates it starts or stops holding, and the warnings it brings.
//
// It exists for the merge request that changes renovate.json or .pinup.*.
// Reading such a change for its effect meant running `pinup whatif` by hand
// with the runner's tokens (the k3s group of koh-gitops!2923, 2026-09-24,
// was checked that way). Two plans, one before and one after, answer it for
// whoever reviews the change. Deterministic: no clock, sorted output.
func PlanDiff(base, head *model.Plan) string {
	var b strings.Builder
	b.WriteString("### What this configuration change does\n\n")

	baseW, headW := written(base), written(head)
	var added, removed, changed []string
	for _, name := range sortedKeys(headW) {
		hb := headW[name]
		bb, ok := baseW[name]
		if !ok {
			added = append(added, fmt.Sprintf("`%s` — %s%s", name, hb.Title, automergeNote(hb)))
			continue
		}
		if what := branchChanges(bb, hb); len(what) > 0 {
			changed = append(changed, fmt.Sprintf("`%s` — %s", name, strings.Join(what, "; ")))
		}
	}
	for _, name := range sortedKeys(baseW) {
		if _, ok := headW[name]; !ok {
			removed = append(removed, fmt.Sprintf("`%s` — %s%s", name, baseW[name].Title, heldNow(head, name)))
		}
	}

	baseH, headH := held(base), held(head)
	var nowHeld, released []string
	for _, k := range sortedKeys(headH) {
		if bh, ok := baseH[k]; !ok || bh.reason != headH[k].reason {
			nowHeld = append(nowHeld, headH[k].line)
		}
	}
	for _, k := range sortedKeys(baseH) {
		if _, ok := headH[k]; !ok {
			released = append(released, baseH[k].change)
		}
	}

	var warnings []string
	seen := map[string]bool{}
	for _, w := range base.Warnings {
		seen[w.Msg] = true
	}
	for _, w := range head.Warnings {
		if !seen[w.Msg] {
			warnings = append(warnings, w.Msg)
			seen[w.Msg] = true
		}
	}

	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "**%s** (%d)\n\n", title, len(items))
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
		b.WriteString("\n")
	}
	section("New merge requests", added)
	section("No longer proposed", removed)
	section("Changed merge requests", changed)
	section("Newly held", nowHeld)
	section("No longer held", released)
	section("New warnings", warnings)

	if len(added)+len(removed)+len(changed)+len(nowHeld)+len(released)+len(warnings) == 0 {
		b.WriteString("No difference: with this change pinup proposes, holds and warns about exactly what it did before.\n")
	}
	return b.String()
}

// written is the branches a run would push, by name.
func written(p *model.Plan) map[string]model.Branch {
	out := map[string]model.Branch{}
	for _, br := range p.Branches {
		if br.SuppressedBy == "" {
			out[br.Name] = br
		}
	}
	return out
}

type heldUpdate struct {
	reason, line, change string
}

// held is the plan's held updates by key, with their first block.
func held(p *model.Plan) map[string]heldUpdate {
	out := map[string]heldUpdate{}
	for _, u := range p.Updates {
		if len(u.Blocks) == 0 {
			continue
		}
		blk := u.Blocks[0]
		change := fmt.Sprintf("`%s` %s → %s", u.Dep.DepName, u.Dep.CurrentValue, u.NewValue)
		line := change + " — " + string(blk.Reason)
		if blk.Note != "" {
			line += ": " + blk.Note
		}
		if o := heldBy(blk.Org); o != "—" {
			line += " (held by " + o + ")"
		}
		out[u.Key()] = heldUpdate{reason: string(blk.Reason), line: line, change: change}
	}
	return out
}

func branchChanges(a, b model.Branch) []string {
	var out []string
	if a.Title != b.Title {
		out = append(out, fmt.Sprintf("title %q → %q", a.Title, b.Title))
	}
	if a.Automerge != b.Automerge {
		out = append(out, fmt.Sprintf("automerge %v → %v", a.Automerge, b.Automerge))
	}
	if !sameSet(a.Labels, b.Labels) {
		out = append(out, fmt.Sprintf("labels %v → %v", sortedCopy(a.Labels), sortedCopy(b.Labels)))
	}
	if !sameSet(a.UpdateKeys, b.UpdateKeys) {
		out = append(out, fmt.Sprintf("updates %d → %d", len(a.UpdateKeys), len(b.UpdateKeys)))
	}
	return out
}

func automergeNote(br model.Branch) string {
	if br.Automerge {
		return " (automerge)"
	}
	return ""
}

// heldNow says why a branch that is no longer written is held in head, when
// it still exists there.
func heldNow(head *model.Plan, name string) string {
	for _, br := range head.Branches {
		if br.Name == name && br.SuppressedBy != "" {
			return " — now held: " + string(br.SuppressedBy)
		}
	}
	return ""
}

func sameSet(a, b []string) bool {
	return slices.Equal(sortedCopy(a), sortedCopy(b))
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
