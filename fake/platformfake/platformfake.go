// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package platformfake is an in-memory publish.Platform for tests: it
// keeps merge requests per branch, records every call, and fails on
// demand through exported fields.
package platformfake

import (
	"context"
	"fmt"
	"sync"

	"git.ole-hartwig.eu/pinup/pinup/publish"
)

// Platform is the fake.
type Platform struct {
	mu sync.Mutex
	// MRs holds every merge request ever created, newest last.
	MRs []publish.MergeRequest
	// Calls records method names in order.
	Calls []string
	// CreateErr and UpdateErr, when set, are returned by the respective
	// calls.
	CreateErr, UpdateErr error
	// Verification is what CommitVerification answers.
	Verification string
	nextIID      int
}

var _ publish.Platform = (*Platform)(nil)

func (p *Platform) record(call string) {
	p.mu.Lock()
	p.Calls = append(p.Calls, call)
	p.mu.Unlock()
}

func (p *Platform) Name() string { return "fake" }

func (p *Platform) Project(_ context.Context, path string) (publish.Project, error) {
	p.record("Project")
	return publish.Project{Path: path, DefaultBranch: "main"}, nil
}

func (p *Platform) FindMergeRequest(_ context.Context, _ publish.Project, branch string) (publish.MergeRequest, bool, error) {
	p.record("Find " + branch)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.MRs) - 1; i >= 0; i-- {
		if p.MRs[i].SourceBranch == branch && p.MRs[i].State == "opened" {
			return p.MRs[i], true, nil
		}
	}
	return publish.MergeRequest{}, false, nil
}

func (p *Platform) History(_ context.Context, _ publish.Project, branch string) ([]publish.MergeRequest, error) {
	p.record("History " + branch)
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []publish.MergeRequest
	for i := len(p.MRs) - 1; i >= 0; i-- {
		if p.MRs[i].SourceBranch == branch {
			out = append(out, p.MRs[i])
		}
	}
	return out, nil
}

func (p *Platform) CreateMergeRequest(_ context.Context, _ publish.Project, r publish.Request) (publish.MergeRequest, error) {
	p.record("Create " + r.SourceBranch)
	if p.CreateErr != nil {
		return publish.MergeRequest{}, p.CreateErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextIID++
	mr := publish.MergeRequest{
		IID: p.nextIID, State: "opened", SourceBranch: r.SourceBranch, TargetBranch: r.TargetBranch,
		Title: r.Title, Description: r.Description, Labels: r.Labels, Automerge: r.Automerge,
		WebURL: fmt.Sprintf("https://fake/mr/%d", p.nextIID),
	}
	p.MRs = append(p.MRs, mr)
	return mr, nil
}

func (p *Platform) UpdateMergeRequest(_ context.Context, _ publish.Project, iid int, r publish.Request) (publish.MergeRequest, []string, error) {
	p.record(fmt.Sprintf("Update %d", iid))
	if p.UpdateErr != nil {
		return publish.MergeRequest{}, nil, p.UpdateErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.MRs {
		if p.MRs[i].IID != iid {
			continue
		}
		var changed []string
		if p.MRs[i].Title != r.Title {
			p.MRs[i].Title = r.Title
			changed = append(changed, "title")
		}
		if p.MRs[i].Description != r.Description {
			p.MRs[i].Description = r.Description
			changed = append(changed, "description")
		}
		if fmt.Sprint(p.MRs[i].Labels) != fmt.Sprint(r.Labels) {
			p.MRs[i].Labels = r.Labels
			changed = append(changed, "labels")
		}
		if p.MRs[i].Automerge != r.Automerge {
			p.MRs[i].Automerge = r.Automerge
			changed = append(changed, "automerge")
		}
		return p.MRs[i], changed, nil
	}
	return publish.MergeRequest{}, nil, fmt.Errorf("no merge request %d", iid)
}

func (p *Platform) CommitVerification(context.Context, publish.Project, string) (string, error) {
	p.record("Verify")
	if p.Verification == "" {
		return "unsigned", nil
	}
	return p.Verification, nil
}
