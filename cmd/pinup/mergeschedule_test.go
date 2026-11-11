// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/wire"
)

// automergeSchedule decides the merge window only where something set it:
// its builtin default, "at any time", must not open a merge the schedule
// keeps in the night (devops/koh-gitops!8807, 2026-11-11), and an explicit
// "at any time" must, or a schedule that only paces creation could never
// let a merge out of its hours.
func TestAutomergeScheduleCountsOnlyWhereTheConfigurationSetsIt(t *testing.T) {
	defaults := []model.Origin{{Source: config.DefaultsSource, Pointer: "/automergeSchedule", Rule: model.NoRule}}
	set := append(defaults[:1:1], model.Origin{Source: "preset:local>devops/renovate-runner", Pointer: "/automergeSchedule", Rule: model.NoRule})
	for _, tc := range []struct {
		name string
		prov map[string][]model.Origin
		want bool
	}{
		{"the builtin default", map[string][]model.Origin{"/automergeSchedule": defaults}, false},
		{"nothing at all", nil, false},
		{"a preset sets it", map[string][]model.Origin{"/automergeSchedule": set}, true},
		{"the update type's object sets it", map[string][]model.Origin{"/automergeSchedule": defaults, "/lockFileMaintenance/automergeSchedule": set[1:]}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := automergeScheduleConfigured(&config.Resolved{Prov: tc.prov}, model.UpdateLockFileMaintenance); got != tc.want {
				t.Errorf("configured = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyUpdateRulesSaysWhetherARuleWroteAutomergeSchedule(t *testing.T) {
	base := map[string]any{"automergeSchedule": []any{"at any time"}, "timezone": "Europe/Berlin"}
	dep := model.Dependency{Manager: "kustomize", DepName: "registry.example/mariadb", Datasource: "docker", CurrentValue: "3.6.80"}
	u := model.Update{Dep: dep, DepKey: "mariadb", Type: model.UpdatePatch, NewValue: "3.6.82"}
	for _, tc := range []struct {
		name  string
		rules []any
		want  bool
	}{
		{"a night window by schedule alone", []any{map[string]any{"matchDepNames": []any{"registry.example/mariadb"}, "schedule": []any{"* 3-5 * * *"}}}, false},
		{"a rule that sets automergeSchedule", []any{map[string]any{"matchDepNames": []any{"registry.example/mariadb"}, "automergeSchedule": []any{"at any time"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := rules.Compile(tc.rules, wire.Versionings())
			if err != nil {
				t.Fatal(err)
			}
			_, _, written, err := applyUpdateRules(engine, base, u, time.Date(2026, 11, 11, 8, 13, 0, 0, time.UTC), nil)
			if err != nil {
				t.Fatal(err)
			}
			if written != tc.want {
				t.Errorf("written = %v, want %v", written, tc.want)
			}
		})
	}
}
