// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/ohartwig/pinup/model"
)

// Co-resolution: composer branches that can only be resolved together.
//
// pinup plans one branch per group, and composer resolves each branch's lock
// refresh on its own. Usually that is right. It is wrong when two groups need
// each other: on 2026-10-13 every TYPO3 installation carried
// enshrined/svg-sanitize 0.22.0, which had three advisories, so composer
// refused to load it. TYPO3 14.3.9 allowed svg-sanitize ^1.0 only together
// with sitepackage-base 4.12.5, and sitepackage-base 4.12.5 required it only
// together with TYPO3 14.3.9. pinup planned the two in pinup/typo3-core and
// pinup/first-party-composer; each alone was unresolvable, and so was every
// other composer branch of those repositories, security bumps included -
// for five days, as dashboard entries nobody acted on.
//
// After the regular branches, the runner looks at the composer branches whose
// lock refresh failed with composer's "could not be resolved". Where one
// branch's failure names a package another of them updates, the two are
// tried once more as one branch: the edits of both, one lock refresh naming
// every package of both. The branch name is derived from the members, so the
// next run updates the same merge request instead of opening another.

// unresolvable is composer's own sentence for a set it cannot install.
const unresolvable = "could not be resolved to an installable set of packages"

// packageName matches a composer package name (vendor/name) as composer
// prints it in its problem report.
var packageName = regexp.MustCompile(`[a-z0-9](?:[_.-]?[a-z0-9]+)*/[a-z0-9](?:(?:[_.]|-{1,2})?[a-z0-9]+)*`)

// pushFailure is a branch as it was planned, before fail() dropped its
// edits, and the error its push ended with.
type pushFailure struct {
	branch model.Branch
	err    string
}

// composerRefresh is the composer lock refresh of a branch: the directory,
// the packages it names, and the flags after them. A branch without one, or
// with a refresh that names no package (lock-file maintenance), has none.
func composerRefresh(b model.Branch) (dir string, names, flags []string, ok bool) {
	for _, t := range b.Tasks {
		if t.Kind != model.TaskLockRefresh || t.Manager != "composer" || len(t.Command) < 2 ||
			t.Command[0] != "composer" || t.Command[1] != "update" {
			continue
		}
		for _, a := range t.Command[2:] {
			if strings.HasPrefix(a, "-") {
				if a != "--with-all-dependencies" {
					flags = append(flags, a)
				}
				continue
			}
			names = append(names, a)
		}
		return t.Dir, names, flags, len(names) > 0
	}
	return "", nil, nil, false
}

// coResolve returns the combined branches worth one more attempt, and a
// warning for each group that could not be combined.
func coResolve(failures []pushFailure) ([]model.Branch, []string) {
	type member struct {
		f     pushFailure
		dir   string
		names []string
		flags []string
	}
	var ms []member
	for _, f := range failures {
		if !strings.Contains(f.err, unresolvable) {
			continue
		}
		dir, names, flags, ok := composerRefresh(f.branch)
		if !ok {
			continue
		}
		ms = append(ms, member{f: f, dir: dir, names: names, flags: flags})
	}
	if len(ms) < 2 {
		return nil, nil
	}

	// Union-find over "a's failure names a package b updates", per directory.
	parent := make([]int, len(ms))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for i := range ms {
		mentioned := map[string]bool{}
		for _, n := range packageName.FindAllString(strings.ToLower(ms[i].f.err), -1) {
			mentioned[n] = true
		}
		for j := range ms {
			if i == j || ms[i].dir != ms[j].dir {
				continue
			}
			if slices.ContainsFunc(ms[j].names, func(n string) bool { return mentioned[strings.ToLower(n)] }) {
				parent[find(i)] = find(j)
			}
		}
	}
	groups := map[int][]member{}
	var roots []int
	for i := range ms {
		r := find(i)
		if _, seen := groups[r]; !seen {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], ms[i])
	}

	var out []model.Branch
	var warnings []string
	for _, r := range roots {
		g := groups[r]
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(a, b int) bool { return g[a].f.branch.Name < g[b].f.branch.Name })
		var memberNames []string
		for _, m := range g {
			memberNames = append(memberNames, m.f.branch.Name)
		}

		var edits []model.Edit
		overlap := ""
		for _, m := range g {
			for _, e := range m.f.branch.Edits {
				if i := slices.IndexFunc(edits, func(o model.Edit) bool { return o.Overlaps(e) }); i >= 0 {
					if edits[i] == e {
						continue // the same edit planned twice
					}
					overlap = e.File
				}
				edits = append(edits, e)
			}
		}
		if overlap != "" {
			warnings = append(warnings, fmt.Sprintf("co-resolve: %s need each other but edit the same bytes of %s; resolve them by hand",
				strings.Join(memberNames, ", "), overlap))
			continue
		}
		sort.SliceStable(edits, func(a, b int) bool {
			if edits[a].File != edits[b].File {
				return edits[a].File < edits[b].File
			}
			return edits[a].Start < edits[b].Start
		})

		var names []string
		for _, m := range g {
			for _, n := range m.names {
				if !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
		command := append([]string{"composer", "update"}, names...)
		command = append(command, "--with-all-dependencies")
		command = append(command, g[0].flags...)
		tasks := []model.Task{{
			Kind: model.TaskLockRefresh, Manager: "composer", Dir: g[0].dir, Command: command,
			ExecutionMode: model.ExecBranch, FileFilters: []string{"composer.lock"},
			AllowedBy: -1, Origin: model.Origin{Source: "pinup", Rule: model.NoRule},
		}}
		// The members' own post-upgrade tasks still run, once each.
		seen := map[string]bool{}
		for _, m := range g {
			for _, t := range m.f.branch.Tasks {
				key := t.Dir + "\x00" + strings.Join(t.Command, "\x00")
				if t.Kind == model.TaskLockRefresh || seen[key] {
					continue
				}
				seen[key] = true
				tasks = append(tasks, t)
			}
		}

		b := model.Branch{
			Name:      coResolveName(memberNames),
			Slug:      strings.TrimPrefix(coResolveName(memberNames), branchPrefix(memberNames[0])),
			Title:     coResolveTitle(g[0].f.branch.Title, titles(g, func(m member) string { return m.f.branch.Title })),
			GroupName: "co-resolve",
			Edits:     edits,
			Tasks:     tasks,
			Automerge: true,
		}
		var body strings.Builder
		body.WriteString("These updates can only be resolved together; each one alone fails in composer:\n\n")
		for _, m := range g {
			fmt.Fprintf(&body, "- %s (%s)\n", m.f.branch.Title, m.f.branch.Name)
			b.UpdateKeys = append(b.UpdateKeys, m.f.branch.UpdateKeys...)
			b.Labels = union(b.Labels, m.f.branch.Labels)
			b.Prov = append(b.Prov, m.f.branch.Prov...)
			b.Automerge = b.Automerge && m.f.branch.Automerge
			if b.AutomergeType == "" {
				b.AutomergeType = m.f.branch.AutomergeType
			}
		}
		b.CommitBody = strings.TrimSpace(body.String())
		b.Body = b.CommitBody
		out = append(out, b)
	}
	return out, warnings
}

func titles[T any](g []T, f func(T) string) []string {
	out := make([]string, 0, len(g))
	for _, m := range g {
		out = append(out, f(m))
	}
	return out
}

// coResolveName is stable for the same members, whatever order they failed
// in, so a later run finds the request this one opened.
func coResolveName(members []string) string {
	sorted := slices.Clone(members)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return branchPrefix(sorted[0]) + "co-resolve-" + hex.EncodeToString(sum[:])[:10]
}

// branchPrefix is everything up to the last slash of a branch name: the
// configuration's branchPrefix, as the planner applied it.
func branchPrefix(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i+1]
	}
	return ""
}

// coResolveTitle keeps the semantic type of the strongest member (a fix
// releases, a chore may not) and the security marker if any member carries
// it.
func coResolveTitle(first string, all []string) string {
	kind := "chore(deps)"
	security := false
	for _, t := range all {
		if strings.HasPrefix(t, "fix") {
			kind = "fix(deps)"
		}
		if strings.Contains(t, "[security]") {
			security = true
		}
	}
	title := fmt.Sprintf("%s: update %d co-dependent groups together", kind, len(all))
	if security {
		title += " [security]"
	}
	return title
}
