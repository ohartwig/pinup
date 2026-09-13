// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package report derives cross-repository views from plans.
//
// Layer 4. The consumer index is the first: every plan a run produces lists
// the dependencies of one repository, so the set of plans a partition
// produces is a reverse index from package to the repositories that depend
// on it - for free, from work already done. The release fast lane reads it
// to run against the consumers of a package that was just released, rather
// than searching or cloning the estate to find them, which is what cost the
// Renovate runner 30 to 120 minutes per release.
package report

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ohartwig/pinup/model"
)

// Index maps a dependency reference to the repositories that carry it.
type Index struct {
	GeneratedAt time.Time `json:"generatedAt"`
	// Consumers is keyed "datasource|packageName" (packageName falling back
	// to depName), each value the sorted repository paths.
	Consumers map[string][]string `json:"consumers"`
	// Repositories records when each repository was last indexed, so a
	// repository that disappeared is not carried forever.
	Repositories map[string]time.Time `json:"repositories"`
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{Consumers: map[string][]string{}, Repositories: map[string]time.Time{}}
}

// LoadIndex reads an index file; a missing file is an empty index.
func LoadIndex(path string) (*Index, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewIndex(), nil
	}
	if err != nil {
		return nil, err
	}
	idx := NewIndex()
	if err := json.Unmarshal(raw, idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// Save writes the index, sorted, so two saves of the same state are the
// same bytes.
func (x *Index) Save(path string) error {
	for k := range x.Consumers {
		sort.Strings(x.Consumers[k])
	}
	raw, err := json.MarshalIndent(x, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Key is the index key for a dependency.
func Key(d model.Dependency) string {
	name := d.PackageName
	if name == "" {
		name = d.DepName
	}
	return d.Datasource + "|" + name
}

// Record replaces what the index knows about one repository with the
// dependencies of its plan. Skipped dependencies count too: a dependency
// held by a rule is still a dependency, and a release of it is still
// something that repository would want to hear about.
func (x *Index) Record(repo string, plan *model.Plan, now time.Time) {
	x.forget(repo)
	for _, d := range plan.Deps {
		if d.Datasource == "" || (d.PackageName == "" && d.DepName == "") {
			continue
		}
		k := Key(d)
		if !contains(x.Consumers[k], repo) {
			x.Consumers[k] = append(x.Consumers[k], repo)
		}
	}
	x.Repositories[repo] = now.UTC()
	x.GeneratedAt = now.UTC()
}

func (x *Index) forget(repo string) {
	for k, repos := range x.Consumers {
		kept := repos[:0]
		for _, r := range repos {
			if r != repo {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			delete(x.Consumers, k)
		} else {
			x.Consumers[k] = kept
		}
	}
}

// ConsumersOf returns the repositories that depend on a released project,
// named by its project path as the release trigger names it -
// "devops/ci-cd-components/lint-tools". The match is by what the estate's
// managers extract for that project: the path itself (component includes,
// gitlab-tags), a registry image ending in the path (docker), or a
// composite "path:vendor/name" (the custom composer manager). Any
// datasource counts.
func (x *Index) ConsumersOf(projectPath string) []string {
	seen := map[string]bool{}
	var out []string
	for k, repos := range x.Consumers {
		if !RefersTo(k, projectPath) {
			continue
		}
		for _, r := range repos {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	sort.Strings(out)
	return out
}

// RefersTo reports whether an index key ("datasource|name") names the
// released project.
func RefersTo(key, projectPath string) bool {
	_, name, _ := strings.Cut(key, "|")
	return name == projectPath ||
		strings.HasPrefix(name, projectPath+":") ||
		strings.HasSuffix(name, "/"+projectPath)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
