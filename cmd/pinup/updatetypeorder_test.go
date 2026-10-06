// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/planner"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/wire"
)

// Renovate merges an update's configuration in three steps (lib/workers/
// repository/updates/flatten.ts): the package rules, the update type's own
// object, the rules again. So a rule beats the object. pinup stopped after
// the object until 2026-10-06, and a rule switching off what the object
// switched on - automerge: false against :automergeDigest - did nothing.
func TestARuleWinsOverTheUpdateTypesObject(t *testing.T) {
	base := map[string]any{
		"digest":              map[string]any{"automerge": true, "commitMessageTopic": "{{{depName}}} digest"},
		"lockFileMaintenance": map[string]any{"schedule": []any{"after 1am and before 6am"}, "automerge": true},
	}
	library := model.Dependency{Manager: "custom.regex", DepName: "BSI-Bund/Stand-der-Technik-Bibliothek", Datasource: "git-refs", CurrentValue: "main"}
	other := model.Dependency{Manager: "custom.regex", DepName: "other/repo", Datasource: "git-refs", CurrentValue: "main"}

	for _, tc := range []struct {
		name      string
		rules     []any
		dep       model.Dependency
		t         model.UpdateType
		key       string
		want      any
		described []any
	}{
		{"a rule switches off the object's automerge",
			[]any{map[string]any{"description": "announce, do not merge", "matchDepNames": []any{"BSI-Bund/Stand-der-Technik-Bibliothek"}, "automerge": false}},
			library, model.UpdateDigest, "automerge", false, []any{"announce, do not merge"}},
		{"a dependency no rule names keeps the object's value",
			[]any{map[string]any{"matchDepNames": []any{"BSI-Bund/Stand-der-Technik-Bibliothek"}, "automerge": false}},
			other, model.UpdateDigest, "automerge", true, nil},
		{"a rule that writes the object itself is merged through it",
			[]any{map[string]any{"matchDepNames": []any{"BSI-Bund/Stand-der-Technik-Bibliothek"}, "digest": map[string]any{"automerge": false}}},
			library, model.UpdateDigest, "automerge", false, nil},
		{"a rule's schedule beats lockFileMaintenance.schedule",
			[]any{map[string]any{"matchUpdateTypes": []any{"lockFileMaintenance"}, "schedule": []any{"at any time"}}},
			model.Dependency{}, model.UpdateLockFileMaintenance, "schedule", []any{"at any time"}, nil},
		{"the object still applies where no rule speaks",
			nil, library, model.UpdateDigest, "commitMessageTopic", "{{{depName}}} digest", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := rules.Compile(tc.rules, wire.Versionings())
			if err != nil {
				t.Fatal(err)
			}
			res := resolveUpdate(engine, base, rules.SubjectOf(tc.dep, tc.t.Renovate().String()), tc.t)
			if got := res.Config[tc.key]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s = %v; want %v", tc.key, got, tc.want)
			}
			// The branch naming merges the object once more; it has to
			// arrive at the same answer.
			if got := planner.Overlay(res.Config, tc.t)[tc.key]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("after the naming's overlay %s = %v; want %v", tc.key, got, tc.want)
			}
			if got, _ := res.Config["description"].([]any); !reflect.DeepEqual(got, tc.described) {
				t.Errorf("description %v; want %v (each rule once)", got, tc.described)
			}
		})
	}
}

// The base the caller passes is not written through by either pass.
func TestResolveUpdateLeavesTheBaseAlone(t *testing.T) {
	digest := map[string]any{"automerge": true}
	base := map[string]any{"digest": digest}
	engine, err := rules.Compile([]any{map[string]any{"matchUpdateTypes": []any{"digest"}, "automerge": false}}, wire.Versionings())
	if err != nil {
		t.Fatal(err)
	}
	resolveUpdate(engine, base, rules.SubjectOf(model.Dependency{DepName: "x"}, "digest"), model.UpdateDigest)
	if digest["automerge"] != true || len(base) != 1 {
		t.Errorf("base changed: %v", base)
	}
}
