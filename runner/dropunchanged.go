// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/model"
)

// scopeUnchanged is what pushBranch returns for a branch dropWhenUnchanged
// drops: not a failure, nothing is pushed, and Execute holds the branch
// with BlockScopeUnchanged and the note as its reason.
type scopeUnchanged struct{ note string }

func (s *scopeUnchanged) Error() string { return s.note }

// dropUnchanged decides whether a branch whose tasks have all run is
// dropped rather than written: postUpgradeTasks' dropWhenUnchanged.
//
// changed holds, per task in the order of tasks, the paths that task
// changed; tracked the paths the branch's tree holds. The branch is
// dropped when
//
//   - at least one task asked for it (DropWhenUnchanged),
//   - no task of the branch changed anything - the asking tasks' scope is
//     their fileFilters, and a lock refresh beside them that moved its lock
//     is a change worth a merge request as well, and
//   - a tracked file lies within the fileFilters of an asking task.
//
// The last condition is what keeps a repository that has not adopted the
// lock flowing as before: koh-lock exits 0 and writes nothing where an
// image has no koh-lock/ yet, and that image's snapshot bump must still
// arrive. A task that failed never reaches this decision - its branch
// fails as any other.
//
// The reason returned names the asking commands, their scope and how many
// files in it were found unchanged, so "why did my bump not come" is
// answered by the plan and the dashboard.
func dropUnchanged(tasks []model.Task, changed [][]string, tracked []string) (string, bool) {
	if len(changed) != len(tasks) {
		return "", false
	}
	var asking []scope
	var commands, filters []string
	for i, t := range tasks {
		if len(changed[i]) > 0 {
			return "", false
		}
		if !t.DropWhenUnchanged {
			continue
		}
		asking = append(asking, scopeOf(t))
		commands = appendNew(commands, strings.Join(t.Command, " "))
		for _, f := range t.FileFilters {
			filters = appendNew(filters, f)
		}
	}
	if len(asking) == 0 {
		return "", false
	}
	inScope := 0
	for _, p := range tracked {
		if slices.ContainsFunc(asking, func(s scope) bool { return s.holds(p) }) {
			inScope++
		}
	}
	if inScope == 0 {
		return "", false
	}
	files := "files"
	if inScope == 1 {
		files = "file"
	}
	return fmt.Sprintf("dropWhenUnchanged: %s changed none of the %d %s within %s; the edits alone are not written",
		strings.Join(commands, "; "), inScope, files, strings.Join(filters, ", ")), true
}

// scope is a task's fileFilters, compiled, below the task's directory.
type scope struct {
	dir      string
	matchers []*glob.Matcher
}

func scopeOf(t model.Task) scope {
	s := scope{dir: t.Dir}
	for _, f := range t.FileFilters {
		s.matchers = append(s.matchers, glob.Compile(f))
	}
	return s
}

// holds reports whether a path relative to the checkout lies in the scope,
// the filters read relative to the task's directory as apply.InScope
// reads them.
func (s scope) holds(p string) bool {
	rel := p
	if s.dir != "" {
		r, err := filepath.Rel(s.dir, p)
		if err != nil || strings.HasPrefix(r, "..") {
			return false
		}
		rel = filepath.ToSlash(r)
	}
	return slices.ContainsFunc(s.matchers, func(m *glob.Matcher) bool { return m.Match(rel) })
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// dropOrigin is the origin of the first task that asked for the drop: the
// configuration that decided it.
func dropOrigin(b *model.Branch) model.Origin {
	if i := slices.IndexFunc(b.Tasks, func(t model.Task) bool { return t.DropWhenUnchanged }); i >= 0 {
		return b.Tasks[i].Origin
	}
	return model.Origin{Source: "config", Rule: model.NoRule}
}
