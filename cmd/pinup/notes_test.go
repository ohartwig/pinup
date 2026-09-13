// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
	"git.ole-hartwig.eu/pinup/pinup/wire"
)

// fakeNotes records what it was asked and answers from a table.
type fakeNotes struct {
	asked []string
	fail  map[string]bool
}

func (f *fakeNotes) Notes(_ context.Context, sourceURL string, v versioning.Versioning, current, target string) ([]model.ReleaseNote, string, error) {
	f.asked = append(f.asked, sourceURL+" "+v.Name()+" "+current+".."+target)
	compare := sourceURL + "/compare/" + current + "..." + target
	if f.fail[sourceURL] {
		return nil, compare, errors.New("502 Bad Gateway")
	}
	return []model.ReleaseNote{{Version: target, Body: "notes for " + target}}, compare, nil
}

func TestFillNotesAsksOnlyForWhatWillBePushed(t *testing.T) {
	dep := func(name, src, current, locked, scheme string) model.Dependency {
		return model.Dependency{Manager: "npm", File: "package.json", DepName: name, Datasource: "npm", CurrentValue: current, LockedVersion: locked, SourceURL: src, Versioning: scheme}
	}
	updates := []model.Update{
		{DepKey: "a", Dep: dep("a", "https://github.com/o/a", "^1.0.0", "1.0.2", ""), NewValue: "^1.1.0", NewVersion: "1.1.0", Type: model.UpdateMinor},
		{DepKey: "b", Dep: dep("b", "https://github.com/o/b", "2.0.0", "", "semver"), NewValue: "2.1.0", NewVersion: "2.1.0", Type: model.UpdateMinor},
		{DepKey: "c", Dep: dep("c", "https://github.com/o/c", "3.0.0", "", "semver"), NewValue: "3.1.0", NewVersion: "3.1.0", Type: model.UpdateMinor},
		{DepKey: "d", Dep: dep("d", "https://github.com/o/d", "4.0.0", "", "semver"), NewValue: "4.0.0", NewDigest: "sha256:ff", Type: model.UpdateDigest},
		{DepKey: "e", Dep: dep("e", "", "5.0.0", "", "semver"), NewValue: "5.1.0", NewVersion: "5.1.0", Type: model.UpdateMinor},
		{DepKey: "f", Dep: dep("f", "https://github.com/o/f", "6.0.0", "", "semver"), NewValue: "6.1.0", NewVersion: "6.1.0", Type: model.UpdateMinor},
		{DepKey: "g", Dep: dep("g", "https://github.com/o/g", "7.0.0", "", "semver"), NewValue: "7.1.0", NewVersion: "7.1.0", Type: model.UpdateMinor, Blocks: []model.Block{{Reason: model.BlockSchedule}}, SuppressedBy: model.BlockSchedule},
		// The same key as b: one include read twice. Both carry the notes,
		// one fetch.
		{DepKey: "b", Dep: dep("b", "https://github.com/o/b", "2.0.0", "", "semver"), NewValue: "2.1.0", NewVersion: "2.1.0", Type: model.UpdateMinor},
	}
	key := func(i int) string { return updates[i].Key() }
	plan := &model.Plan{
		Updates: updates,
		Branches: []model.Branch{
			{Name: "renovate/a", UpdateKeys: []string{key(0)}},
			{Name: "renovate/group", UpdateKeys: []string{key(1), key(2), key(3), key(4), key(5)}},
			{Name: "renovate/g", UpdateKeys: []string{key(6)}, SuppressedBy: model.BlockSchedule},
		},
	}
	f := &fakeNotes{fail: map[string]bool{"https://github.com/o/f": true}}
	off := map[string]bool{key(2): true}
	byDatasource := func(ds string) string {
		if ds == "npm" {
			return "npm"
		}
		return ""
	}
	warnings := fillNotes(context.Background(), f, plan, off, wire.Versionings(), byDatasource)

	want := []string{
		"https://github.com/o/a npm 1.0.2..1.1.0", // the lock's version is what is in use; npm is the datasource's default scheme
		"https://github.com/o/b semver 2.0.0..2.1.0",
		"https://github.com/o/f semver 6.0.0..6.1.0",
	}
	if strings.Join(f.asked, "\n") != strings.Join(want, "\n") {
		t.Errorf("asked:\n%s\nwant:\n%s", strings.Join(f.asked, "\n"), strings.Join(want, "\n"))
	}
	if n := plan.Updates[0].Notes; len(n) != 1 || n[0].Body != "notes for 1.1.0" || plan.Updates[0].CompareURL != "https://github.com/o/a/compare/1.0.2...1.1.0" {
		t.Errorf("a: notes=%+v compare=%q", n, plan.Updates[0].CompareURL)
	}
	if len(plan.Updates[1].Notes) != 1 || len(plan.Updates[7].Notes) != 1 {
		t.Errorf("b twice: %d and %d notes", len(plan.Updates[1].Notes), len(plan.Updates[7].Notes))
	}
	for _, i := range []int{2, 3, 4, 6} {
		if plan.Updates[i].Notes != nil || plan.Updates[i].CompareURL != "" {
			t.Errorf("%s got notes although fetchChangeLogs off / digest / no source / held", plan.Updates[i].DepKey)
		}
	}
	// A failed fetch: no notes, the compare link stays, and the plan says so.
	if plan.Updates[5].Notes != nil || plan.Updates[5].CompareURL == "" {
		t.Errorf("f: notes=%+v compare=%q", plan.Updates[5].Notes, plan.Updates[5].CompareURL)
	}
	if len(warnings) != 1 || warnings[0].Stage != "changelog" || !strings.Contains(warnings[0].Msg, "f: 502") {
		t.Errorf("warnings = %+v", warnings)
	}
}
