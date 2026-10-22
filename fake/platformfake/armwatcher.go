// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package platformfake

import (
	"context"
	"fmt"
	"sync"

	"github.com/ohartwig/pinup/publish"
)

// Pipeline is a head pipeline as ArmWatcher reports it.
type Pipeline struct {
	Status, SHA string
}

// ArmWatcher is an in-memory publish.ArmWatcher: the open requests of an
// estate, keyed per request by "project!iid", recording every call.
type ArmWatcher struct {
	mu sync.Mutex
	// MRs is what OpenLabelled lists, the open ones, whatever the label.
	MRs []publish.MergeRequest
	// Pipelines, Authors and Cancels answer the per-request reads, keyed
	// "project!iid"; a missing key is no pipeline, no commits, no cancel.
	Pipelines map[string]Pipeline
	Authors   map[string][]string
	Cancels   map[string][]string
	// NotYet names the requests ArmAutomerge leaves unarmed without a
	// refusal; Refuse answers with that refusal. Every other request is
	// armed - or merged, with direct and MergeableNow.
	NotYet       map[string]bool
	Refuse       map[string]string
	MergeableNow map[string]bool
	// ListErr and ReadErr fail the listing and every per-request read.
	ListErr, ReadErr error
	// Calls records method and key in order; Armed records the arm calls
	// as "project!iid sha direct".
	Calls []string
	Armed []string
}

var _ publish.ArmWatcher = (*ArmWatcher)(nil)

func key(project string, iid int) string { return fmt.Sprintf("%s!%d", project, iid) }

func (a *ArmWatcher) record(call string) {
	a.mu.Lock()
	a.Calls = append(a.Calls, call)
	a.mu.Unlock()
}

func (a *ArmWatcher) OpenLabelled(_ context.Context, label string) ([]publish.MergeRequest, error) {
	a.record("OpenLabelled " + label)
	if a.ListErr != nil {
		return nil, a.ListErr
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []publish.MergeRequest
	for _, m := range a.MRs {
		if m.State == "opened" {
			out = append(out, m)
		}
	}
	return out, nil
}

func (a *ArmWatcher) HeadPipeline(_ context.Context, project string, iid int) (string, string, error) {
	k := key(project, iid)
	a.record("HeadPipeline " + k)
	if a.ReadErr != nil {
		return "", "", a.ReadErr
	}
	p := a.Pipelines[k]
	return p.Status, p.SHA, nil
}

func (a *ArmWatcher) CommitAuthors(_ context.Context, project string, iid int) ([]string, error) {
	k := key(project, iid)
	a.record("CommitAuthors " + k)
	if a.ReadErr != nil {
		return nil, a.ReadErr
	}
	return a.Authors[k], nil
}

func (a *ArmWatcher) AutomergeCanceledBy(_ context.Context, project string, iid int) ([]string, error) {
	k := key(project, iid)
	a.record("AutomergeCanceledBy " + k)
	if a.ReadErr != nil {
		return nil, a.ReadErr
	}
	return a.Cancels[k], nil
}

func (a *ArmWatcher) ArmAutomerge(_ context.Context, project string, iid int, sha string, direct bool) (publish.MergeRequest, error) {
	k := key(project, iid)
	a.record("ArmAutomerge " + k)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Armed = append(a.Armed, fmt.Sprintf("%s %s %v", k, sha, direct))
	out := publish.MergeRequest{Project: project, IID: iid, State: "opened", SHA: sha}
	switch {
	case a.Refuse[k] != "":
		out.AutomergeRefused = a.Refuse[k]
	case a.NotYet[k]:
	case direct && a.MergeableNow[k]:
		out.State = "merged"
	default:
		out.Automerge = true
	}
	for i := range a.MRs {
		if a.MRs[i].Project == project && a.MRs[i].IID == iid {
			a.MRs[i].Automerge, a.MRs[i].State = out.Automerge, out.State
		}
	}
	return out, nil
}
