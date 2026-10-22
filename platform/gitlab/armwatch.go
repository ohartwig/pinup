// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/ohartwig/pinup/publish"
)

// This file is publish.ArmWatcher: what the arm watch reads and does
// without a clone. Every call is one documented REST v4 endpoint.

// OpenLabelled walks GET /merge_requests?scope=all&state=opened&labels=label,
// every page: the open requests of every project the token can see that
// carry the label.
func (p *Platform) OpenLabelled(ctx context.Context, label string) ([]publish.MergeRequest, error) {
	q := url.Values{"scope": {"all"}, "state": {"opened"}, "labels": {label}}
	items, err := p.pages[mrJSON](ctx, p.base+"/api/v4/merge_requests", q, "(merge requests labelled "+label+")")
	if err != nil {
		return nil, err
	}
	out := make([]publish.MergeRequest, 0, len(items))
	for _, m := range items {
		mr := toPublish(m)
		if project, _, ok := strings.CutLast(m.References.Full, "!"); ok {
			mr.Project = project
		}
		out = append(out, mr)
	}
	return out, nil
}

// HeadPipeline reads the request itself; the list endpoints do not carry
// head_pipeline.
func (p *Platform) HeadPipeline(ctx context.Context, project string, iid int) (string, string, error) {
	resp, err := p.do(ctx, http.MethodGet, p.mrURL(project, iid), nil)
	if err != nil {
		return "", "", err
	}
	if err := classify(resp, project); err != nil {
		return "", "", err
	}
	var doc struct {
		HeadPipeline *struct {
			Status string `json:"status"`
			SHA    string `json:"sha"`
		} `json:"head_pipeline"`
	}
	if err := json.Unmarshal(resp.body, &doc); err != nil {
		return "", "", fmt.Errorf("gitlab: decode %q !%d: %w", project, iid, err)
	}
	if doc.HeadPipeline == nil {
		return "", "", nil
	}
	return doc.HeadPipeline.Status, doc.HeadPipeline.SHA, nil
}

// CommitAuthors walks GET /merge_requests/:iid/commits.
func (p *Platform) CommitAuthors(ctx context.Context, project string, iid int) ([]string, error) {
	type commit struct {
		AuthorEmail string `json:"author_email"`
	}
	items, err := p.pages[commit](ctx, p.mrURL(project, iid)+"/commits", url.Values{}, project)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, c := range items {
		out = append(out, c.AuthorEmail)
	}
	return out, nil
}

// AutomergeCanceledBy reads the request's system notes. GitLab writes
// "canceled the automatic merge" as the user who canceled it; "aborted the
// automatic merge because ..." is GitLab's own doing on a push and is not a
// person's answer. The token's own user is pinup, whose cancellation
// follows a rule that stopped allowing automerge and takes the marker away
// with it.
func (p *Platform) AutomergeCanceledBy(ctx context.Context, project string, iid int) ([]string, error) {
	self, err := p.selfUserID(ctx)
	if err != nil {
		return nil, err
	}
	type note struct {
		Body   string `json:"body"`
		System bool   `json:"system"`
		Author struct {
			ID       int    `json:"id"`
			Username string `json:"username"`
		} `json:"author"`
	}
	q := url.Values{"sort": {"desc"}, "order_by": {"created_at"}}
	items, err := p.pages[note](ctx, p.mrURL(project, iid)+"/notes", q, project)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range items {
		body := strings.ToLower(strings.TrimSpace(n.Body))
		if !n.System || n.Author.ID == self {
			continue
		}
		if strings.HasPrefix(body, "canceled the automatic merge") || strings.HasPrefix(body, "cancelled the automatic merge") {
			out = append(out, n.Author.Username)
		}
	}
	return out, nil
}

// ArmAutomerge is trySetAutomerge for one request named by project and iid.
func (p *Platform) ArmAutomerge(ctx context.Context, project string, iid int, sha string, direct bool) (publish.MergeRequest, error) {
	m, ok, why, err := p.trySetAutomerge(ctx, project, iid, sha, direct)
	if err != nil {
		return publish.MergeRequest{}, err
	}
	if !ok {
		return publish.MergeRequest{IID: iid, Project: project, State: "opened", AutomergeRefused: why}, nil
	}
	out := toPublish(m)
	out.Project = project
	return out, nil
}

func (p *Platform) mrURL(project string, iid int) string {
	return fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", p.base, url.PathEscape(project), iid)
}

// pages walks every page of a list endpoint, following X-Next-Page until
// GitLab stops sending one, and decodes each page as a list of T.
func (p *Platform) pages[T any](ctx context.Context, base string, query url.Values, what string) ([]T, error) {
	q := maps.Clone(query)
	if q == nil {
		q = url.Values{}
	}
	if q.Get("per_page") == "" {
		q.Set("per_page", "100")
	}
	var out []T
	page := "1"
	for page != "" {
		q.Set("page", page)
		resp, err := p.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if err := classify(resp, what); err != nil {
			return nil, err
		}
		var items []T
		if err := json.Unmarshal(resp.body, &items); err != nil {
			return nil, fmt.Errorf("gitlab: decode %s: %w", what, err)
		}
		out = append(out, items...)
		page = resp.header.Get("X-Next-Page")
	}
	return out, nil
}
