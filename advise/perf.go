// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package advise

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/sched"
)

// The performance checks find work the run does for nothing: directories
// walked that a preset had excluded, a lock refresh every four hours, a
// manager reading every file.
var perfChecks = []Check{
	{ID: "perf/ignore-paths-replaced", Run: ignorePathsReplaced},
	{ID: "perf/lock-file-maintenance-unscheduled", Run: lockFileMaintenanceUnscheduled},
	{ID: "perf/pr-limit-unbounded", Run: prLimitUnbounded},
	{ID: "perf/file-pattern-catch-all", Run: filePatternCatchAll},
}

// ignorePathsReplaced finds an ignorePaths that drops what the presets
// excluded: arrays replace, so a file listing one path of its own walks
// node_modules again.
func ignorePathsReplaced(in *Input) []Finding {
	own, ok := in.Layer.Raw["ignorePaths"].([]any)
	if !ok {
		return nil
	}
	inherited := stringsOf(in.Inherited["ignorePaths"])
	ownList := stringsOf(own)
	var dropped []string
	for _, p := range inherited {
		if !slices.Contains(ownList, p) {
			dropped = append(dropped, p)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	merged := make([]any, 0, len(inherited)+len(ownList))
	for _, p := range inherited {
		merged = append(merged, p)
	}
	for _, p := range ownList {
		if !slices.Contains(inherited, p) {
			merged = append(merged, p)
		}
	}
	return []Finding{{ID: "perf/ignore-paths-replaced", Category: Performance, Severity: Warn, Pointer: "/ignorePaths", Frame: FrameFile, Origin: in.fileOrigin("/ignorePaths", model.NoRule),
		Msg: fmt.Sprintf("ignorePaths replaces the inherited list; %s are walked again", strings.Join(dropped, ", ")),
		Fix: &Fix{Pointer: "/ignorePaths", Op: OpSet, Value: merged, Changes: []string{"/ignorePaths"}}}}
}

// lockFileMaintenanceUnscheduled finds a lock refresh with no window: it
// runs on every four-hourly run (plan.md §6.5).
func lockFileMaintenanceUnscheduled(in *Input) []Finding {
	lfm, ok := in.Resolved.Raw["lockFileMaintenance"].(map[string]any)
	if !ok || lfm["enabled"] != true {
		return nil
	}
	exprs := stringsOf(lfm["schedule"])
	for _, e := range exprs {
		if !sched.IsAnyTime(strings.TrimSpace(e)) {
			return nil
		}
	}
	f := Finding{ID: "perf/lock-file-maintenance-unscheduled", Category: Performance, Severity: Warn, Pointer: "/lockFileMaintenance/schedule", Frame: FrameResolved, Origin: in.originAt("/lockFileMaintenance"),
		Msg: "lockFileMaintenance has no schedule; the lock refresh runs on every four-hourly run"}
	if own, ok := in.Layer.Raw["lockFileMaintenance"].(map[string]any); ok && own != nil {
		f.Fix = &Fix{Pointer: "/lockFileMaintenance/schedule", Op: OpSet, Value: []any{"before 5am on monday"}, Changes: []string{"/lockFileMaintenance/schedule"}}
	}
	return []Finding{f}
}

// prLimitUnbounded finds an explicit zero, which means no cap: a stale
// repository opens every branch at once.
func prLimitUnbounded(in *Input) []Finding {
	var out []Finding
	for _, k := range []string{"prHourlyLimit", "prConcurrentLimit"} {
		n, ok := numberOf(in.Resolved.Raw[k])
		if !ok || n != 0 {
			continue
		}
		p := ptr(k)
		out = append(out, Finding{ID: "perf/pr-limit-unbounded", Category: Performance, Severity: Info, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p),
			Msg: fmt.Sprintf("%s is 0: no cap, a repository behind on updates opens every branch at once", k)})
	}
	return out
}

// catchAll are the patterns that match every file.
var catchAll = map[string]bool{"/.*/": true, "/^.*$/": true, "/.+/": true, "/^.+$/": true, "**": true, "**/*": true}

// filePatternCatchAll finds a custom manager the file declares that reads
// every file in the repository.
func filePatternCatchAll(in *Input) []Finding {
	list, _ := in.Layer.Raw["customManagers"].([]any)
	var out []Finding
	for i, e := range list {
		cm, _ := e.(map[string]any)
		key := "managerFilePatterns"
		patterns := stringsOf(cm[key])
		if len(patterns) == 0 {
			key = "fileMatch"
			patterns = stringsOf(cm[key])
		}
		for j, p := range patterns {
			if !catchAll[p] {
				continue
			}
			ptr := ptr("customManagers", i, key, j)
			out = append(out, Finding{ID: "perf/file-pattern-catch-all", Category: Performance, Severity: Info, Pointer: ptr, Frame: FrameFile, Origin: in.fileOrigin(ptr, model.NoRule),
				Msg: fmt.Sprintf("customManagers[%d] reads every file in the repository", i)})
		}
	}
	return out
}
