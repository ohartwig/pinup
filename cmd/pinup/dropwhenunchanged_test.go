// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/plugin"
)

// postUpgradeTasks' dropWhenUnchanged is read as a JSON true and nothing
// else, and it reaches the tasks only when every update on the branch
// asked for it: an update whose own configuration did not must not be
// dropped together with the others.
func TestDropWhenUnchangedNeedsTheWholeBranch(t *testing.T) {
	parse := func(raw map[string]any) plugin.PostUpgrade {
		t.Helper()
		pu, ok := postUpgradeOf(map[string]any{"postUpgradeTasks": raw})
		if !ok {
			t.Fatalf("postUpgradeTasks %v not read", raw)
		}
		return pu
	}
	cmd := []any{"koh-lock {{{packageFile}}}"}
	filters := []any{"**/koh-lock/**"}
	lock := parse(map[string]any{"commands": cmd, "fileFilters": filters, "executionMode": "branch", "dropWhenUnchanged": true})
	plain := parse(map[string]any{"commands": cmd, "fileFilters": filters, "executionMode": "branch"})
	if !lock.DropWhenUnchanged || plain.DropWhenUnchanged {
		t.Fatalf("parsed %v / %v", lock.DropWhenUnchanged, plain.DropWhenUnchanged)
	}
	if parse(map[string]any{"commands": cmd, "dropWhenUnchanged": "true"}).DropWhenUnchanged {
		t.Errorf("the string \"true\" opted in")
	}

	up := func(file string) model.Update {
		return model.Update{DepKey: file + "|koh-snapshot", NewValue: "20261108",
			Dep: model.Dependency{Manager: "regex", File: file, DepName: "koh-snapshot", CurrentValue: "20261101"}}
	}
	a, b := up("a/Containerfile"), up("b/Containerfile")
	branch := model.Branch{Name: "pinup/snapshots", UpdateKeys: []string{a.Key(), b.Key()}}
	allowed := []string{`^koh-lock( [^ ;&|]+)+$`}
	for _, c := range []struct {
		name string
		pus  map[string]plugin.PostUpgrade
		want bool
	}{
		{"every update asked", map[string]plugin.PostUpgrade{a.Key(): lock, b.Key(): lock}, true},
		{"one update did not", map[string]plugin.PostUpgrade{a.Key(): lock, b.Key(): plain}, false},
		{"one update has no task", map[string]plugin.PostUpgrade{a.Key(): lock}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tasks, block := tasksFor(branch, []model.Update{a, b}, c.pus, nil, nil, allowed, nil)
			if block != nil || len(tasks) == 0 {
				t.Fatalf("tasks %+v, block %+v", tasks, block)
			}
			for _, task := range tasks {
				if task.DropWhenUnchanged != c.want {
					t.Errorf("task %v dropWhenUnchanged = %v, want %v", task.Command, task.DropWhenUnchanged, c.want)
				}
			}
		})
	}
}
