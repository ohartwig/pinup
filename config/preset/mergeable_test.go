// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package preset

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ohartwig/pinup/fake/fixture"
)

// The tables are the capture: every option the container combines by
// concatenating or merging is in the matching table, and nothing else is.
// A re-capture that moves an option fails here until the table follows.
func TestMergeTablesMatchTheCapture(t *testing.T) {
	f, err := os.Open(fixture.SharedCaptured(t, "config", "merge.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	wantConcat, wantMerge := map[string]bool{}, map[string]bool{}
	options := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct{ Name, Merge string }
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if line.Name == "" {
			continue
		}
		options++
		switch line.Merge {
		case "concat":
			wantConcat[line.Name] = true
		case "merge":
			wantMerge[line.Name] = true
		case "replace":
		default:
			t.Errorf("%s: merge %q is not an outcome the probe reports", line.Name, line.Merge)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	// Measured on 43.288.0: 224 object or array options, 27 concatenate,
	// 142 merge. An empty capture must not pass as an empty table.
	if options < 200 || len(wantConcat) < 20 || len(wantMerge) < 100 {
		t.Fatalf("capture holds %d options, %d concat, %d merge; it is not the capture", options, len(wantConcat), len(wantMerge))
	}
	if !reflect.DeepEqual(concatKeys, wantConcat) {
		t.Errorf("concatKeys differ from the capture:\n got %v\nwant %v", concatKeys, wantConcat)
	}
	if !reflect.DeepEqual(mergeKeys, wantMerge) {
		t.Errorf("mergeKeys differ from the capture")
		for k := range wantMerge {
			if !mergeKeys[k] {
				t.Errorf("  missing %s", k)
			}
		}
		for k := range mergeKeys {
			if !wantMerge[k] {
				t.Errorf("  extra %s", k)
			}
		}
	}
}

// The capture's edge vectors, replayed through Resolve: a preset as the
// parent, the configuration's own keys as the child.
func TestMergeEdgeVectorsAsCaptured(t *testing.T) {
	f, err := os.Open(fixture.SharedCaptured(t, "config", "merge.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seen := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Vector string
			Parent map[string]any
			Child  map[string]any
			Result map[string]any
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if line.Vector == "" || line.Parent == nil {
			continue
		}
		seen++
		child := map[string]any{"extends": []any{"parent"}}
		for k, v := range line.Child {
			child[k] = v
		}
		got, err := Resolve(child, fakeSource{"parent": line.Parent})
		if err != nil {
			t.Fatalf("%s: %v", line.Vector, err)
		}
		if !reflect.DeepEqual(got.Config, line.Result) {
			t.Errorf("%s:\n got %v\nwant %v", line.Vector, got.Config, line.Result)
		}
	}
	if seen < 8 {
		t.Fatalf("replayed %d vectors, want the capture's 8", seen)
	}
}

// The case that made a repository repeat the runner: a file that sets one
// key of vulnerabilityAlerts keeps the keys the runner set, and a preset
// listed later merges over an earlier one the same way. Before, the later
// object replaced the earlier one whole, and devops/images/golang carried
// the runner's labels, schedule and automerge in its own file to keep them.
func TestMergeObjectsCombineAcrossLayers(t *testing.T) {
	src := fakeSource{
		"runner": {
			"vulnerabilityAlerts": map[string]any{"labels": []any{"security"}, "schedule": []any{"at any time"}, "automerge": true, "addLabels": []any{"a"}},
			"ignoreDeps":          []any{"x"},
			"postUpgradeTasks":    map[string]any{"commands": []any{"make"}, "fileFilters": []any{"go.sum"}},
			"digest":              map[string]any{"automerge": true},
		},
		"later": {"digest": map[string]any{"labels": []any{"digest"}}},
	}
	got, err := Resolve(map[string]any{
		"extends":             []any{"runner", "later"},
		"vulnerabilityAlerts": map[string]any{"groupName": "snapshots", "addLabels": []any{"b"}},
		"ignoreDeps":          []any{"y"},
		"postUpgradeTasks":    map[string]any{"commands": []any{"own"}},
	}, src)
	if err != nil {
		t.Fatal(err)
	}
	va, _ := got.Config["vulnerabilityAlerts"].(map[string]any)
	want := map[string]any{"labels": []any{"security"}, "schedule": []any{"at any time"}, "automerge": true, "groupName": "snapshots", "addLabels": []any{"a", "b"}}
	if !reflect.DeepEqual(va, want) {
		t.Errorf("vulnerabilityAlerts = %v, want %v", va, want)
	}
	if d := got.Config["digest"]; !reflect.DeepEqual(d, map[string]any{"automerge": true, "labels": []any{"digest"}}) {
		t.Errorf("digest = %v, want both presets' keys", d)
	}
	if d := got.Config["ignoreDeps"]; !reflect.DeepEqual(d, []any{"x", "y"}) {
		t.Errorf("ignoreDeps = %v, want both layers' entries", d)
	}
	// postUpgradeTasks is an object the container replaces: a task is
	// read whole, never assembled from two files.
	if d := got.Config["postUpgradeTasks"]; !reflect.DeepEqual(d, map[string]any{"commands": []any{"own"}}) {
		t.Errorf("postUpgradeTasks = %v, want the file's own, whole", d)
	}
	// Provenance per key: the runner's labels, the file's groupName, and
	// the object itself written by both.
	for ptr, want := range map[string][]string{
		"/vulnerabilityAlerts/labels":      {"runner"},
		"/vulnerabilityAlerts/groupName":   {OwnSource},
		"/vulnerabilityAlerts":             {"runner", OwnSource},
		"/vulnerabilityAlerts/addLabels/1": {OwnSource},
		"/ignoreDeps/0":                    {"runner"},
		"/ignoreDeps/1":                    {OwnSource},
		"/postUpgradeTasks":                {"runner", OwnSource},
	} {
		if got := got.Origins[ptr]; !reflect.DeepEqual(got, want) {
			t.Errorf("origins of %s = %v, want %v", ptr, got, want)
		}
	}
}
