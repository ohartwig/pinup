// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

// stub is a manager that does whatever the test needs. It lives here rather
// than in fake/ because nothing outside this package's own tests wants it.
type stub struct {
	name  string
	res   Result
	err   error
	panic bool
}

func (s stub) Name() string           { return s.name }
func (s stub) FilePatterns() []string { return []string{"/^" + s.name + "$/"} }
func (s stub) Extract(context.Context, File, ManagerConfig) (Result, error) {
	if s.panic {
		panic("deliberate")
	}
	return s.res, s.err
}
func (s stub) Edit(context.Context, File, model.Update) (model.Edit, error) {
	return model.Edit{}, errors.New("not implemented")
}
func (s stub) NeedsPlugin() *PluginRequirement { return nil }

// A manager is handed a regex from configuration and a file from a repository
// nobody controls. A panic must cost that file and not the run - and must be
// visible, because a manager that panics is a defect even when the rest of the
// repository is fine.
func TestRunContainsAPanicAsAWarning(t *testing.T) {
	res, err := Run(context.Background(), stub{name: "boom", panic: true},
		File{Path: "a.yml"}, ManagerConfig{})
	if err != nil {
		t.Fatalf("a panic became an error: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(res.Warnings))
	}
	w := res.Warnings[0]
	if w.File != "a.yml" || !strings.Contains(w.Msg, "boom") || !strings.Contains(w.Msg, "panicked") {
		t.Errorf("the warning does not identify the manager and file: %+v", w)
	}
	if len(res.Deps) != 0 {
		t.Error("a panicking manager contributed dependencies")
	}
}

func TestRunPassesThroughANormalResult(t *testing.T) {
	want := Result{Deps: []model.Dependency{{DepName: "x"}}}
	res, err := Run(context.Background(), stub{name: "ok", res: want}, File{}, ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deps) != 1 || res.Deps[0].DepName != "x" {
		t.Errorf("result was not passed through: %+v", res)
	}
}

func TestRunPassesThroughAnError(t *testing.T) {
	sentinel := errors.New("io failure")
	if _, err := Run(context.Background(), stub{name: "bad", err: sentinel}, File{}, ManagerConfig{}); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the manager's own error", err)
	}
}

func TestRegistry(t *testing.T) {
	r := Registry{"dockerfile": stub{name: "dockerfile"}, "gitlabci": stub{name: "gitlabci"}}
	if _, err := r.Get("dockerfile"); err != nil {
		t.Errorf("lookup failed: %v", err)
	}
	_, err := r.Get("nosuch")
	if err == nil {
		t.Error("an unknown manager resolved")
	} else if !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("the error does not name the manager: %v", err)
	}
	names := r.Names()
	if len(names) != 2 || names[0] != "dockerfile" || names[1] != "gitlabci" {
		t.Errorf("Names() = %v, want a sorted list", names)
	}
}

// Determinism is what lets a golden plan be compared byte for byte, so the
// ordering is asserted rather than assumed.
func TestSortDeps(t *testing.T) {
	deps := []model.Dependency{
		{File: "b.yml", DepName: "z", Locus: model.Locus{ValueStart: 10}},
		{File: "a.yml", DepName: "b", Locus: model.Locus{ValueStart: 50}},
		{File: "a.yml", DepName: "a", Locus: model.Locus{ValueStart: 10}},
		{File: "a.yml", DepName: "c", Locus: model.Locus{ValueStart: 10}},
	}
	SortDeps(deps)
	for i, want := range []struct {
		file, dep string
		at        int
	}{
		{"a.yml", "a", 10},
		{"a.yml", "c", 10},
		{"a.yml", "b", 50},
		{"b.yml", "z", 10},
	} {
		got := deps[i]
		if got.File != want.file || got.DepName != want.dep || got.Locus.ValueStart != want.at {
			t.Errorf("position %d = %s/%s@%d, want %s/%s@%d",
				i, got.File, got.DepName, got.Locus.ValueStart, want.file, want.dep, want.at)
		}
	}
}
