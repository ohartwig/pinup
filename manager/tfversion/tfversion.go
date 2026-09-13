// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package tfversion is Renovate's terraform-version manager: a
// `.terraform-version` file (tfenv's) whose whole content is one version.
//
// Measured on testdata/parity/synthetic/tfversion (corpus key
// "terraform-version"): the value is taken as written - `v1.7.0` keeps its
// v - the dependency is `hashicorp/terraform` on github-releases, looked
// up (the runner carries a GitHub token; the first capture ran without one
// and recorded the github-token-required skip Renovate applies then).
// `.opentofu-version` next to it is not read by 43.288.0 and is not read
// here.
package tfversion

import (
	"context"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

const name = "terraform-version"

// Manager implements extract.Manager for .terraform-version files.
type Manager struct{}

// New returns a ready-to-use Manager.
func New() *Manager { return &Manager{} }

// Name implements extract.Manager.
func (m *Manager) Name() string { return name }

// FilePatterns implements extract.Manager.
func (m *Manager) FilePatterns() []string { return []string{"**/.terraform-version"} }

// NeedsPlugin implements extract.Manager.
func (m *Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Extract implements extract.Manager. The first non-blank line is the
// version; surrounding whitespace is not part of it and stays in the file
// when the value is rewritten.
func (m *Manager) Extract(_ context.Context, f extract.File, _ extract.ManagerConfig) (extract.Result, error) {
	src := string(f.Content)
	start, end, line := valueSpan(src)
	if start == end {
		return extract.Result{}, nil
	}
	return extract.Result{Deps: []model.Dependency{{
		Manager:       name,
		File:          f.Path,
		CustomManager: model.NoCustomManager,
		DepName:       "hashicorp/terraform",
		Datasource:    "github-releases",
		CurrentValue:  src[start:end],
		Locus:         model.Locus{ValueStart: start, ValueEnd: end, DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: line},
	}}}, nil
}

// valueSpan finds the first non-blank line's trimmed content. A file of
// nothing but whitespace answers an empty span.
func valueSpan(src string) (start, end, line int) {
	offset := 0
	for i, l := range strings.SplitAfter(src, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			offset += len(l)
			continue
		}
		start = offset + strings.Index(l, trimmed)
		return start, start + len(trimmed), i + 1
	}
	return 0, 0, 0
}

// Edit implements extract.Manager: the version bytes and nothing else.
func (m *Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	l := up.Dep.Locus
	if l.ValueStart < 0 || l.ValueEnd > len(f.Content) || l.ValueStart >= l.ValueEnd {
		return model.Edit{}, fmt.Errorf("%s: %s: no editable range recorded", name, f.Path)
	}
	got := string(f.Content[l.ValueStart:l.ValueEnd])
	if got != up.Dep.CurrentValue {
		return model.Edit{}, fmt.Errorf("%s: %s changed since extraction: expected %q at [%d:%d], found %q",
			name, f.Path, up.Dep.CurrentValue, l.ValueStart, l.ValueEnd, got)
	}
	return model.Edit{File: f.Path, Start: l.ValueStart, End: l.ValueEnd, Old: got, New: up.NewValue, Manager: name}, nil
}
