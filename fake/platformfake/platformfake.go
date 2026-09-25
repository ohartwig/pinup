// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package platformfake is an in-memory publish.Platform for tests: it
// keeps merge requests per branch, records every call, and fails on
// demand through exported fields.
package platformfake

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/pinup/publish"
)

// Platform is the fake.
type Platform struct {
	mu sync.Mutex
	// MRs holds every merge request ever created, newest last.
	MRs []publish.MergeRequest
	// Calls records method names in order.
	Calls []string
	// CreateErr and UpdateErr, when set, are returned by the respective
	// calls. CreateErrTimes bounds how often CreateErr is returned; zero
	// means always.
	CreateErr, UpdateErr error
	CreateErrTimes       int
	createErrs           int
	// Verification is what CommitVerification answers.
	Verification string
	// Files answers ReadFile, keyed "project|path|ref".
	Files map[string]string
	// Projects answers ListProjects.
	Projects []string
	// Issues holds every issue upserted, by title.
	Issues map[string]publish.Issue
	// IssueBodies holds the last description per title.
	IssueBodies map[string]string
	// StaleHeads makes FindMergeRequest report the head "stale" for the
	// branch's next N calls, as a platform does that has not yet processed
	// a push. After that it reports no head, which the runner does not
	// wait for.
	StaleHeads map[string]int
	// Requests records every request UpdateMergeRequest was given.
	Requests []publish.Request
	// ArmAfter makes the platform arm automerge only on the ArmAfter-th
	// update after a request was created, as GitLab does once the pipeline
	// exists; zero arms it on creation.
	ArmAfter int
	updates  int
	nextIID  int
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
			mr := p.MRs[i]
			if p.StaleHeads[branch] > 0 {
				p.StaleHeads[branch]--
				mr.SHA = "stale"
			}
			return mr, true, nil
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
	if p.CreateErr != nil && (p.CreateErrTimes == 0 || p.createErrs < p.CreateErrTimes) {
		p.createErrs++
		return publish.MergeRequest{}, p.CreateErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextIID++
	mr := publish.MergeRequest{
		IID: p.nextIID, State: "opened", SourceBranch: r.SourceBranch, TargetBranch: r.TargetBranch,
		Title: r.Title, Description: r.Description, Labels: r.Labels, Automerge: r.Automerge && p.ArmAfter == 0,
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
	p.Requests = append(p.Requests, r)
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
		p.updates++
		if p.MRs[i].Automerge != r.Automerge && (!r.Automerge || p.updates >= p.ArmAfter) {
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

func (p *Platform) ReadFile(_ context.Context, project, path, ref string) ([]byte, error) {
	p.record("ReadFile " + project + "/" + path)
	if s, ok := p.Files[project+"|"+path+"|"+ref]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("fake: %s has no file %s at %q", project, path, ref)
}

func (p *Platform) ListProjects(context.Context) ([]string, error) {
	p.record("ListProjects")
	return append([]string(nil), p.Projects...), nil
}

func (p *Platform) CloseMergeRequest(_ context.Context, _ publish.Project, iid int, title string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls = append(p.Calls, "CloseMergeRequest")
	for i := range p.MRs {
		if p.MRs[i].IID == iid {
			p.MRs[i].State, p.MRs[i].Title = "closed", title
			return nil
		}
	}
	return fmt.Errorf("platformfake: no merge request %d", iid)
}

func (p *Platform) OpenMergeRequests(_ context.Context, _ publish.Project, prefix string) ([]publish.MergeRequest, error) {
	p.record("Open " + prefix)
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []publish.MergeRequest
	for _, m := range p.MRs {
		if m.State == "opened" && strings.HasPrefix(m.SourceBranch, prefix) {
			out = append(out, m)
		}
	}
	return out, nil
}

// MergedMergeRequests lists the recorded merged requests under prefix
// updated at or after since.
func (p *Platform) MergedMergeRequests(_ context.Context, _ publish.Project, prefix string, since time.Time) ([]publish.MergeRequest, error) {
	p.record("Merged " + prefix)
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []publish.MergeRequest
	for _, m := range p.MRs {
		if m.State == "merged" && strings.HasPrefix(m.SourceBranch, prefix) && !m.UpdatedAt.Before(since) {
			out = append(out, m)
		}
	}
	return out, nil
}

// ReadIssue returns the recorded issue and its body.
func (p *Platform) ReadIssue(_ context.Context, _ publish.Project, title string) (publish.Issue, string, bool, error) {
	p.record("ReadIssue")
	p.mu.Lock()
	defer p.mu.Unlock()
	is, ok := p.Issues[title]
	if !ok {
		return publish.Issue{}, "", false, nil
	}
	return is, p.IssueBodies[title], true, nil
}

// UpsertIssue records the issue by title; a second call with the same
// description reports no change.
func (p *Platform) UpsertIssue(_ context.Context, _ publish.Project, title, description string, labels []string) (publish.Issue, bool, error) {
	p.record("UpsertIssue")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Issues == nil {
		p.Issues = map[string]publish.Issue{}
		p.IssueBodies = map[string]string{}
	}
	if is, ok := p.Issues[title]; ok {
		if p.IssueBodies[title] == description {
			return is, false, nil
		}
		p.IssueBodies[title] = description
		return is, true, nil
	}
	p.nextIID++
	is := publish.Issue{IID: p.nextIID, Title: title, State: "opened", URL: "https://fake/issues/" + title}
	p.Issues[title] = is
	p.IssueBodies[title] = description
	return is, true, nil
}
