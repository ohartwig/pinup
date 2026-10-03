// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/planner"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/rules"
)

// widenOutcome is what a widened plan looked at: how many updates of
// group members it planned, and every member lookup that failed or was
// answered from a stale cache.
type widenOutcome struct {
	Updates int
	Failed  []string
}

// widenOpenGroups plans an open group branch whole in a run narrowed to one
// package. The narrowed plan looks up one package; written from that, an
// open group branch lost its other members (2026-09-26), and held instead,
// the released update waited for the four-hourly scan whenever another
// member's request was still open: on 2026-10-03 the group request for
// ole-hartwig-blog 3.27.1 opened at 17:24Z and merged at 17:29Z, the fast
// lane for only-ole-site 3.7.5 at 17:25Z held the group, and nothing ran
// after the merge - pinup-push leaves pushes that merge a pinup branch
// alone. An hour later the same happened the other way round:
// ole-hartwig-blog 3.27.2 held behind the open request for only-ole-site
// 3.7.6. Two packages of one group released within minutes is the normal
// case, not the exception.
//
// So the run plans again, with every dependency the rules may put in one of
// those groups looked up as the full scan does (the cache answers, the
// released package is not asked twice), and the open branches are written
// with every member. Nothing else changes: other dependencies stay "not
// the released package", and a member's update named for another branch
// is held. It is one more plan inside the same job - no pipeline, no
// trigger, no clone.
//
// It returns the plan to execute and the branches it planned whole. When
// the widening cannot be trusted - the plan fails, a member's lookup
// failed or was stale, an open branch is not in the widened plan - it
// returns the narrowed plan and no branch, and the caller holds them as
// before: an older answer than the open branch carries would drop or step
// back a member, which is the failure the hold exists for.
func widenOpenGroups(ctx context.Context, opts whatifOptions, plan *model.Plan, open map[string]bool, errw io.Writer) (*model.Plan, map[string]bool) {
	targets := map[string]string{}
	for _, b := range plan.Branches {
		if b.GroupName != "" && b.SuppressedBy == "" && open[b.Name] {
			// Named as the planner names it: an adopted branch carries
			// its old prefix in Name and today's name in PlannedName.
			targets[cmp.Or(b.PlannedName, b.Name)] = b.GroupName
		}
	}
	if len(targets) == 0 {
		return plan, nil
	}
	names := slices.Sorted(maps.Keys(targets))
	// The second plan is what the widening costs the fast lane; the log
	// says how long it took, so the cost stays measured, not assumed.
	start := time.Now()
	keep := func(why string) (*model.Plan, map[string]bool) {
		fmt.Fprintf(errw, "warning: %s: the open group branches %s stay held (%s): %s\n", plan.Repo.Path, strings.Join(names, ", "), time.Since(start).Round(100*time.Millisecond), why)
		plan.Warnings = append(plan.Warnings, model.Warning{Stage: "plan", Msg: "widening to the open groups failed, the branches stay held: " + why})
		return plan, nil
	}
	w := opts
	w.Widen = targets
	w.widened = &widenOutcome{}
	// The dashboard was read by the first plan; once is enough.
	if opts.checksRead != nil {
		w.Checks = *opts.checksRead
	}
	w.ReadChecks, w.checksRead = nil, nil
	widened, err := whatif(ctx, w)
	if err != nil {
		return keep(err.Error())
	}
	if len(w.widened.Failed) > 0 {
		return keep("a member's lookup failed: " + strings.Join(w.widened.Failed, "; "))
	}
	written := map[string]bool{}
	for _, b := range widened.Branches {
		if _, ok := targets[cmp.Or(b.PlannedName, b.Name)]; ok {
			written[b.Name] = true
		}
	}
	for _, name := range names {
		if !slices.ContainsFunc(widened.Branches, func(b model.Branch) bool { return cmp.Or(b.PlannedName, b.Name) == name }) {
			return keep(name + " is not in the widened plan")
		}
	}
	fmt.Fprintf(errw, "%s: widened to the open groups %s: %d member updates, plan %s\n", plan.Repo.Path, strings.Join(names, ", "), w.widened.Updates, time.Since(start).Round(100*time.Millisecond))
	return widened, written
}

// widens reports whether a narrowed run plans d although it is not the
// package the run is for: the run is widened, and the rules put d in one of
// the widened groups for at least one kind of update. The group can depend
// on the update type (matchUpdateTypes), which is not known before the
// lookup, so every type is asked; an update that then lands on another
// branch is held in planUpdates. A dependency the configuration disables
// is no member - the full scan plans no update for it either.
func (r *whatifRun) widens(d model.Dependency, base map[string]any) bool {
	o := r.o
	if len(o.Widen) == 0 || d.SkipReason != "" || (o.Released == "" && o.Package == "") {
		return false
	}
	if o.Released != "" && report.RefersTo(report.Key(d), o.Released) {
		return false
	}
	if o.Package != "" && report.Key(d) == o.Package {
		return false
	}
	ruled := applyDepRules(r.engine, base, d)
	if ruled.Disabled != "" {
		return false
	}
	groups := map[string]bool{}
	for g := range maps.Values(o.Widen) {
		groups[g] = true
	}
	for t := model.UpdateMajor; t <= model.UpdateCompatibility; t++ {
		res := r.engine.Apply(base, rules.SubjectOf(ruled, t.String()))
		if g, _ := planner.Overlay(res.Config, t)["groupName"].(string); groups[g] {
			return true
		}
	}
	return false
}
