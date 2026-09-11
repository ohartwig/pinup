// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"fmt"
	"sort"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// RollingMajor is one bare-major reference (`@N`) somewhere in the estate,
// with the newer major that is available for it, if any.
type RollingMajor struct {
	Repo    string
	File    string
	DepName string
	Current string
	// Newest is the newest major available, "" when the reference is
	// current.
	Newest string
}

// RollingMajors collects every bare-major reference across the plans and
// which of them have a newer major waiting. Both counts matter: the
// references found are the denominator that proves the scan looked at
// anything, the notices are what a person acts on.
func RollingMajors(plans []*model.Plan) (refs []RollingMajor, notices int) {
	seen := map[string]int{}
	for _, p := range plans {
		for _, d := range p.Deps {
			if !isBareMajor(d.CurrentValue) {
				continue
			}
			key := p.Repo.Path + "|" + d.File + "|" + d.DepName + "|" + d.CurrentValue
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = len(refs)
			refs = append(refs, RollingMajor{Repo: p.Repo.Path, File: d.File, DepName: d.DepName, Current: d.CurrentValue})
		}
		for _, u := range p.Updates {
			if u.Type != model.UpdateMajorAvailable {
				continue
			}
			key := p.Repo.Path + "|" + u.Dep.File + "|" + u.Dep.DepName + "|" + u.Dep.CurrentValue
			i, ok := seen[key]
			if !ok {
				continue
			}
			if refs[i].Newest == "" || u.NewVersion > refs[i].Newest {
				if refs[i].Newest == "" {
					notices++
				}
				refs[i].Newest = u.NewVersion
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].DepName != refs[j].DepName {
			return refs[i].DepName < refs[j].DepName
		}
		if refs[i].Repo != refs[j].Repo {
			return refs[i].Repo < refs[j].Repo
		}
		return refs[i].File < refs[j].File
	})
	return refs, notices
}

// RollingMajorIssue renders the estate-wide notice: one table per
// component that has a newer major, listing every repository still on the
// old one, then the references that are current. It is a report, not a
// request: adopting a major is a decision with a rollout behind it.
func RollingMajorIssue(refs []RollingMajor, notices int) string {
	var b strings.Builder
	b.WriteString("Bare-major references (`@N`) float within their major by design; a newer major is the one thing they cannot see. ")
	fmt.Fprintf(&b, "This notice lists every such reference pinup found - %d across the estate - and the %d that have a newer major available. pinup never rewrites them.\n\n", len(refs), notices)
	byDep := map[string][]RollingMajor{}
	var deps []string
	for _, r := range refs {
		if r.Newest == "" {
			continue
		}
		if _, ok := byDep[r.DepName]; !ok {
			deps = append(deps, r.DepName)
		}
		byDep[r.DepName] = append(byDep[r.DepName], r)
	}
	sort.Strings(deps)
	for _, d := range deps {
		fmt.Fprintf(&b, "## %s\n\n| Repository | File | Pinned to | Newest major |\n|---|---|---|---|\n", d)
		for _, r := range byDep[d] {
			fmt.Fprintf(&b, "| %s | `%s` | `@%s` | %s |\n", r.Repo, r.File, r.Current, r.Newest)
		}
		b.WriteString("\n")
	}
	current := 0
	for _, r := range refs {
		if r.Newest == "" {
			current++
		}
	}
	fmt.Fprintf(&b, "%d references are on the newest major.\n", current)
	return b.String()
}

func isBareMajor(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
