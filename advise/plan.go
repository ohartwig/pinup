// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/rules"
)

// The plan checks read what runs actually found - the plans whatif wrote -
// and hold the configuration against it: a rule no dependency reached, a
// manager that produced nothing, a repository whose every update is held
// by one setting. They run only with --plan and say so otherwise.
var planChecks = []Check{
	{ID: "plan/rule-never-matched", Run: ruleNeverMatched, Plan: true},
	{ID: "plan/manager-idle", Run: managerIdle, Plan: true},
	{ID: "plan/custom-manager-idle", Run: customManagerIdle, Plan: true},
	{ID: "plan/all-held", Run: allHeld, Plan: true},
	{ID: "plan/limit-holds", Run: limitHolds, Plan: true},
	{ID: "plan/datasource-failing", Run: datasourceFailing, Plan: true},
	{ID: "plan/component-pinned-exact", Run: componentPinnedExact, Plan: true},
}

// ruleNeverMatched replays the plans' dependencies and updates through the
// rule engine and names the file's rules that fired for none of them. No
// fix: the plans may simply not be the rule's audience.
func ruleNeverMatched(in *Input) []Finding {
	if in.Engine == nil {
		return nil
	}
	matched := map[int]bool{}
	deps := 0
	for _, p := range in.Plans {
		for _, d := range p.Deps {
			deps++
			for _, i := range in.Engine.Apply(in.Resolved.Raw, rules.SubjectOf(d, "")).Matched {
				matched[i] = true
			}
		}
		for _, u := range p.Updates {
			s := rules.SubjectOf(u.Dep, u.Type.Renovate().String())
			if u.Effective != model.RiskUnknown {
				s.Effective = u.Effective.String()
			}
			for _, i := range in.Engine.Apply(in.Resolved.Raw, s).Matched {
				matched[i] = true
			}
		}
	}
	base := in.base("packageRules")
	var out []Finding
	for i := range in.ownRules() {
		if matched[base+i] {
			continue
		}
		p := ptr("packageRules", i)
		out = append(out, Finding{ID: "plan/rule-never-matched", Category: Hygiene, Severity: Info, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, base+i),
			Msg: fmt.Sprintf("packageRules[%d] matched none of %d dependencies across %d plans", i, deps, len(in.Plans))})
	}
	return out
}

// managerIdle names enabled managers that found nothing in any plan; their
// discovery globs still run every four hours.
func managerIdle(in *Input) []Finding {
	own := stringsOf(in.Layer.Raw["enabledManagers"])
	if len(own) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, p := range in.Plans {
		for _, d := range p.Deps {
			if d.CustomManager != model.NoCustomManager {
				seen["custom.regex"] = true
			} else {
				seen[d.Manager] = true
			}
		}
	}
	var out []Finding
	for j, m := range own {
		if seen[m] {
			continue
		}
		p := ptr("enabledManagers", j)
		out = append(out, Finding{ID: "plan/manager-idle", Category: Performance, Severity: Info, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, model.NoRule),
			Msg: fmt.Sprintf("manager `%s` produced nothing in %d plans; its discovery still runs every four hours", m, len(in.Plans)),
			Fix: &Fix{Pointer: p, Op: OpRemove, Changes: []string{"/enabledManagers"}}})
	}
	return out
}

// customManagerIdle names the file's custom managers that produced nothing.
func customManagerIdle(in *Input) []Finding {
	list, _ := in.Layer.Raw["customManagers"].([]any)
	if len(list) == 0 {
		return nil
	}
	seen := map[int]bool{}
	for _, p := range in.Plans {
		for _, d := range p.Deps {
			seen[d.CustomManager] = true
		}
	}
	base := in.base("customManagers")
	var out []Finding
	for i := range list {
		if seen[base+i] {
			continue
		}
		p := ptr("customManagers", i)
		out = append(out, Finding{ID: "plan/custom-manager-idle", Category: Performance, Severity: Info, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, model.NoRule),
			Msg: fmt.Sprintf("customManagers[%d] produced nothing in %d plans", i, len(in.Plans))})
	}
	return out
}

// blockKey is the configuration key behind a block reason without a rule.
var blockKey = map[model.BlockReason]string{
	model.BlockMinimumReleaseAge: "/minimumReleaseAge",
	model.BlockSchedule:          "/schedule",
	model.BlockHourlyLimit:       "/prHourlyLimit",
	model.BlockConcurrentLimit:   "/prConcurrentLimit",
	model.BlockDashboardApproval: "/dependencyDashboardApproval",
	model.BlockDisabled:          "/enabled",
	model.BlockAllowedVersions:   "/allowedVersions",
	model.BlockInternalChecks:    "/internalChecksFilter",
	model.BlockTaskRefused:       "/allowedCommands",
}

// allHeld finds a plan whose every update one setting holds.
func allHeld(in *Input) []Finding {
	var out []Finding
	for _, p := range in.Plans {
		if len(p.Updates) == 0 {
			continue
		}
		var reason model.BlockReason
		var org model.Origin
		common := true
		for _, u := range p.Updates {
			if !u.Blocked() {
				common = false
				break
			}
			if reason == "" {
				reason, org = u.Blocks[0].Reason, u.Blocks[0].Org
			} else if u.Blocks[0].Reason != reason {
				common = false
				break
			}
		}
		if !common {
			continue
		}
		pointer := blockKey[reason]
		if org.Rule != model.NoRule && org.Rule >= 0 {
			pointer = ptr("packageRules", org.Rule)
		}
		if pointer == "" {
			pointer = "/"
		}
		out = append(out, Finding{ID: "plan/all-held", Category: Performance, Severity: Warn, Pointer: pointer, Frame: FrameResolved, Origin: in.originAt(pointer),
			Msg: fmt.Sprintf("plan %s: all %d updates are held by %s", p.Repo.Path, len(p.Updates), reason)})
	}
	return out
}

// limitHolds counts the updates the two caps held across the plans.
func limitHolds(in *Input) []Finding {
	counts := map[model.BlockReason]int{}
	for _, p := range in.Plans {
		for _, u := range p.Updates {
			for _, b := range u.Blocks {
				if b.Reason == model.BlockHourlyLimit || b.Reason == model.BlockConcurrentLimit {
					counts[b.Reason]++
				}
			}
		}
	}
	var out []Finding
	for _, reason := range slices.Sorted(maps.Keys(counts)) {
		pointer := blockKey[reason]
		out = append(out, Finding{ID: "plan/limit-holds", Category: Performance, Severity: Info, Pointer: pointer, Frame: FrameResolved, Origin: in.originAt(pointer),
			Msg: fmt.Sprintf("%s=%v held %d updates across %d plans", pointer[1:], in.Resolved.Raw[pointer[1:]], counts[reason], len(in.Plans))})
	}
	return out
}

var exactSemver = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// componentPinnedExact names the plans whose CI component includes are
// pinned to a patch version. A component follows its project by rolling
// major (`@1`): the catalogue resolves it to the newest 1.x, every release
// reaches the consumer without a merge request, and a newer major arrives
// as majorAvailable - the decision stays with the consumer, the churn does
// not. The estate's handbook decides this for every consumer
// (rolling-major-tag-for-pipelines); a patch pin is what pinup raises
// release by release. One finding per plan, the includes listed by file and
// line; no fix, the file is the repository's, not the configuration.
func componentPinnedExact(in *Input) []Finding {
	var out []Finding
	for _, p := range in.Plans {
		var pinned []string
		major := ""
		for _, d := range p.Deps {
			if d.Manager != "gitlabci" || d.DepType != "repository" || !exactSemver.MatchString(d.CurrentValue) {
				continue
			}
			if major == "" {
				major, _, _ = strings.Cut(d.CurrentValue, ".")
			}
			pinned = append(pinned, fmt.Sprintf("%s:%d %s@%s", d.File, d.Locus.Line, d.DepName, d.CurrentValue))
		}
		if len(pinned) == 0 {
			continue
		}
		out = append(out, Finding{ID: "plan/component-pinned-exact", Category: Hygiene, Severity: Info, Pointer: "/", Frame: FrameResolved, Origin: in.originAt("/"),
			Msg: fmt.Sprintf("plan %s: %d component includes pinned to a patch version; a rolling major (@%s) follows the project's releases and pinup reports a newer major as majorAvailable: %s",
				p.Repo.Path, len(pinned), major, strings.Join(pinned, ", "))})
	}
	return out
}

var viaCustom = regexp.MustCompile(`via custom\.([A-Za-z0-9_.-]+):`)

// datasourceFailing names a custom datasource whose lookups the plans
// record as failed.
func datasourceFailing(in *Input) []Finding {
	failing := map[string][]string{}
	for _, p := range in.Plans {
		for _, w := range p.Warnings {
			if w.Stage != "lookup" {
				continue
			}
			m := viaCustom.FindStringSubmatch(w.Msg)
			if m == nil {
				continue
			}
			if _, defined := in.Decoded.CustomDatasources[m[1]]; defined {
				failing[m[1]] = append(failing[m[1]], p.Repo.Path)
			}
		}
	}
	var out []Finding
	for _, name := range slices.Sorted(maps.Keys(failing)) {
		p := ptr("customDatasources", name)
		repos := slices.Compact(slices.Sorted(slices.Values(failing[name])))
		out = append(out, Finding{ID: "plan/datasource-failing", Category: Compat, Severity: Warn, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p),
			Msg: fmt.Sprintf("customDatasources.%s failed lookups in %s", name, strings.Join(repos, ", "))})
	}
	return out
}
