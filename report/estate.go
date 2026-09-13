// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// The estate overview is the one page that says, for every dependency the
// estate uses, which versions are in use where, what the newest known is,
// and what stands between the two. It is made from a run's plans - the
// same facts every merge request rests on - and written as an issue the
// runner keeps current, and as a file beside the reports.

// EstateDep is one dependency across the estate.
type EstateDep struct {
	Datasource string
	Name       string
	// Uses are the distinct current values in use, each with its repositories.
	Uses []EstateUse
	// Behind counts the uses a plan moves; Held counts those held, with the
	// reasons seen.
	Behind, Held int
	Reasons      map[model.BlockReason]int
	Advisories   int
}

// EstateUse is one current value of a dependency and where it is used.
type EstateUse struct {
	Current string
	Repos   []EstateRepoUse
}

// EstateRepoUse is one repository's use of a value: what its plan proposes
// for it, "" if nothing, and the hold if the proposal is held.
type EstateRepoUse struct {
	Repo     string
	Proposed string
	Held     model.BlockReason
}

// Estate summarises the plans.
type Estate struct {
	Repos, Deps, Uses, Current, Behind, Held, Vulnerable int
	ByDatasource                                         map[string][]EstateDep
	Datasources                                          []string
}

// EstateOf builds the overview from the plans of one run.
func EstateOf(plans []*model.Plan) Estate {
	type useKey struct{ ds, name, current string }
	deps := map[string]*EstateDep{}
	uses := map[useKey]*EstateUse{}
	repos := map[string]bool{}
	for _, p := range plans {
		repos[p.Repo.Path] = true
		proposed := map[string]model.Update{}
		for _, u := range p.Updates {
			// The highest proposal per dependency key wins the overview;
			// a plan may propose a minor and a major side by side.
			if prev, ok := proposed[u.DepKey]; !ok || u.Type == model.UpdateMajor || u.Type == model.UpdateMajorAvailable && prev.Type != model.UpdateMajor {
				proposed[u.DepKey] = u
			}
		}
		for _, d := range p.Deps {
			if d.Datasource == "" || d.DepName == "" || d.CurrentValue == "" && d.CurrentDigest == "" {
				continue
			}
			dk := d.Datasource + "\x00" + d.DepName
			ed, ok := deps[dk]
			if !ok {
				ed = &EstateDep{Datasource: d.Datasource, Name: d.DepName, Reasons: map[model.BlockReason]int{}}
				deps[dk] = ed
			}
			current := d.CurrentValue
			if current == "" {
				current = short(d.CurrentDigest)
			}
			uk := useKey{d.Datasource, d.DepName, current}
			eu, ok := uses[uk]
			if !ok {
				eu = &EstateUse{Current: current}
				uses[uk] = eu
				ed.Uses = append(ed.Uses, *eu)
			}
			ru := EstateRepoUse{Repo: p.Repo.Path}
			if len(d.Advisories) > 0 {
				ed.Advisories++
			}
			if u, ok := proposed[d.Key()]; ok {
				ru.Proposed = u.NewValue
				if u.Type == model.UpdateDigest || u.Type == model.UpdatePinDigest {
					ru.Proposed = current + "@" + short(u.NewDigest)
				}
				if u.Blocked() {
					ru.Held = u.SuppressedBy
					ed.Reasons[u.SuppressedBy]++
				}
			}
			dup := false
			for _, r := range eu.Repos {
				if r.Repo == ru.Repo {
					dup = true
				}
			}
			if !dup {
				eu.Repos = append(eu.Repos, ru)
			}
		}
	}
	var e Estate
	e.Repos = len(repos)
	e.ByDatasource = map[string][]EstateDep{}
	for _, ed := range deps {
		// Uses were appended as copies; rebuild from the map.
		var us []EstateUse
		for _, u := range ed.Uses {
			us = append(us, *uses[useKey{ed.Datasource, ed.Name, u.Current}])
		}
		sort.Slice(us, func(i, j int) bool { return us[i].Current < us[j].Current })
		ed.Uses = us
		ed.Behind, ed.Held = 0, 0
		for _, u := range us {
			for _, r := range u.Repos {
				e.Uses++
				switch {
				case r.Proposed == "":
					e.Current++
				case r.Held != "":
					ed.Held++
					e.Held++
				default:
					ed.Behind++
					e.Behind++
				}
			}
		}
		if ed.Advisories > 0 {
			e.Vulnerable++
		}
		e.Deps++
		e.ByDatasource[ed.Datasource] = append(e.ByDatasource[ed.Datasource], *ed)
	}
	for ds, list := range e.ByDatasource {
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		e.ByDatasource[ds] = list
		e.Datasources = append(e.Datasources, ds)
	}
	sort.Strings(e.Datasources)
	return e
}

// EstateMarkdown renders the overview.
func EstateMarkdown(e Estate, version string, now time.Time) string {
	var b strings.Builder
	b.WriteString("Every dependency the estate uses, from the last run's plans: the versions in use and where, the newest known, and what stands between the two.\n\n")
	fmt.Fprintf(&b, "| Repositories | Dependencies | Uses | Current | Behind | Held | With advisories |\n|---|---|---|---|---|---|---|\n| %d | %d | %d | %d | %d | %d | %d |\n\n",
		e.Repos, e.Deps, e.Uses, e.Current, e.Behind, e.Held, e.Vulnerable)
	b.WriteString("*Uses* counts every repository × version; *behind* is a use a plan moves, *held* one it moves but a rule holds (schedule, release age, approval, …).\n\n")
	for _, ds := range e.Datasources {
		list := e.ByDatasource[ds]
		n := 0
		for _, d := range list {
			n += len(d.Uses)
		}
		fmt.Fprintf(&b, "<details><summary>%s (%d dependencies, %d versions in use)</summary>\n\n", ds, len(list), n)
		b.WriteString("| Dependency | In use | Repositories (→ proposed, held) |\n|---|---|---|\n")
		for _, d := range list {
			for i, u := range d.Uses {
				name := ""
				if i == 0 {
					name = "`" + d.Name + "`"
					if d.Advisories > 0 {
						name += " ⚠️"
					}
				}
				var cells []string
				for _, r := range u.Repos {
					c := r.Repo
					if r.Proposed != "" {
						c += " → `" + r.Proposed + "`"
					}
					if r.Held != "" {
						c += " (" + string(r.Held) + ")"
					}
					cells = append(cells, c)
				}
				repos := strings.Join(cells, "; ")
				if len(cells) > 8 {
					repos = strings.Join(cells[:8], "; ") + fmt.Sprintf("; … (%d)", len(cells))
				}
				fmt.Fprintf(&b, "| %s | `%s` | %s |\n", name, u.Current, repos)
			}
		}
		b.WriteString("\n</details>\n\n")
	}
	fmt.Fprintf(&b, "---\n\n*pinup %s, %s*\n", version, now.UTC().Format("2006-01-02 15:04 UTC"))
	return b.String()
}
