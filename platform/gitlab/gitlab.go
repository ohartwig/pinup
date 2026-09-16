// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package gitlab implements publish.Platform against the GitLab REST API v4,
// a public specification (docs.gitlab.com/api). Nothing here is derived from
// Renovate's implementation - the platform's own merge-request lifecycle
// (find, create, update, automerge, commit signature) is re-implemented from
// the documented endpoints.
//
// # Why this package does not use httpx
//
// httpx.Client offers GET only, with a fixed set of conditional headers and
// no way to attach a per-request body. Every write here - opening a merge
// request, updating one, asking GitLab to merge when the pipeline succeeds -
// is a POST or PUT with a JSON body, and reads need a project-scoped
// PRIVATE-TOKEN or JOB-TOKEN header httpx has no hook for either. Rather than
// bend httpx to a shape it was not built for, this package takes an
// http.RoundTripper directly (so tests can still use fake/harness) and does
// its own minimal, context-aware request helper, the same way
// datasource/dockerds does for the registry protocol.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/pinup/publish"
)

// Name is the platform name a plan's report refers to.
const Name = "gitlab"

// bodyLimit caps how much of a response this package will read into memory.
// GitLab's merge request payloads are small; a much larger body is a sign
// something is wrong, not a legitimate response to buffer whole.
const bodyLimit = 4 << 20

// Token is the credential attached to every request. Header is the name
// GitLab expects it under - "PRIVATE-TOKEN" for a personal or project access
// token, "JOB-TOKEN" for CI_JOB_TOKEN.
type Token struct {
	Value  string
	Header string
}

// Platform implements publish.Platform against one GitLab instance.
type Platform struct {
	hc    *http.Client
	base  string
	token Token

	// userMu guards userID, the id of the account the token belongs to,
	// read once from /user: the dashboard is the issue that account
	// wrote, not any issue with the title (review S7, 2026-09-13).
	userMu sync.Mutex
	userID int
}

// selfUserID answers the id of the account behind the token, from /user,
// once.
func (p *Platform) selfUserID(ctx context.Context) (int, error) {
	p.userMu.Lock()
	defer p.userMu.Unlock()
	if p.userID != 0 {
		return p.userID, nil
	}
	resp, err := p.do(ctx, http.MethodGet, p.base+"/api/v4/user", nil)
	if err != nil {
		return 0, err
	}
	if err := classifyToken(resp, "read its own user"); err != nil {
		return 0, err
	}
	var payload struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil || payload.ID == 0 {
		return 0, fmt.Errorf("gitlab: decode /user: %v", err)
	}
	p.userID = payload.ID
	return p.userID, nil
}

// New returns a Platform that dials base (e.g. "https://gitlab.example.org")
// through transport. A nil transport means http.DefaultTransport.
func New(base string, transport http.RoundTripper, token Token) *Platform {
	if transport == nil {
		transport = http.DefaultTransport
	}
	p := &Platform{
		base:  strings.TrimRight(base, "/"),
		token: token,
	}
	// Nothing this client calls should leave the instance: a redirect to
	// another host - object storage behind artifacts and packages, or a
	// misconfigured instance - would carry PRIVATE-TOKEN with it, since
	// net/http strips only Authorization. Refuse rather than follow.
	p.hc = &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return fmt.Errorf("gitlab: refusing to follow a redirect off the instance to %s", req.URL.Host)
		}
		if len(via) >= 10 {
			return errors.New("gitlab: stopped after 10 redirects")
		}
		return nil
	}}
	return p
}

func (p *Platform) Name() string { return Name }

// Project resolves a project path to its canonical path and default branch.
func (p *Platform) Project(ctx context.Context, path string) (publish.Project, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s", p.base, url.PathEscape(path))
	resp, err := p.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return publish.Project{}, err
	}
	if err := classify(resp, path); err != nil {
		return publish.Project{}, err
	}

	var payload struct {
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return publish.Project{}, fmt.Errorf("gitlab: decode project %q: %w", path, err)
	}
	return publish.Project{Path: payload.PathWithNamespace, DefaultBranch: payload.DefaultBranch}, nil
}

// FindMergeRequest returns the open merge request for sourceBranch, if any.
// GitLab's state=opened filter does the narrowing; there should be at most
// one match, so the first is returned.
func (p *Platform) FindMergeRequest(ctx context.Context, proj publish.Project, sourceBranch string) (publish.MergeRequest, bool, error) {
	q := url.Values{}
	q.Set("source_branch", sourceBranch)
	q.Set("state", "opened")

	items, err := p.listMergeRequests(ctx, proj.Path, q)
	if err != nil {
		return publish.MergeRequest{}, false, err
	}
	if len(items) == 0 {
		return publish.MergeRequest{}, false, nil
	}
	return toPublish(items[0]), true, nil
}

// History lists every merge request there ever was for sourceBranch, newest
// first.
func (p *Platform) History(ctx context.Context, proj publish.Project, sourceBranch string) ([]publish.MergeRequest, error) {
	q := url.Values{}
	q.Set("source_branch", sourceBranch)
	q.Set("order_by", "created_at")
	q.Set("sort", "desc")

	items, err := p.listMergeRequests(ctx, proj.Path, q)
	if err != nil {
		return nil, err
	}
	out := make([]publish.MergeRequest, 0, len(items))
	for _, m := range items {
		out = append(out, toPublish(m))
	}
	return out, nil
}

// CreateMergeRequest opens a merge request and, if r.Automerge was asked for,
// tries once to set merge-when-pipeline-succeeds on it.
func (p *Platform) CreateMergeRequest(ctx context.Context, proj publish.Project, r publish.Request) (publish.MergeRequest, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", p.base, url.PathEscape(proj.Path))
	payload := map[string]any{
		"source_branch":        r.SourceBranch,
		"target_branch":        r.TargetBranch,
		"title":                r.Title,
		"description":          r.Description,
		"labels":               strings.Join(r.Labels, ","),
		"remove_source_branch": r.RemoveSourceBranch,
	}

	resp, err := p.do(ctx, http.MethodPost, u, payload)
	if err != nil {
		return publish.MergeRequest{}, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.MergeRequest{}, err
	}

	var mr mrJSON
	if err := json.Unmarshal(resp.body, &mr); err != nil {
		return publish.MergeRequest{}, fmt.Errorf("gitlab: decode created merge request for %q: %w", proj.Path, err)
	}

	refused := ""
	if r.Automerge {
		merged, ok, why, err := p.trySetAutomerge(ctx, proj.Path, mr.IID, mr.SHA)
		if err != nil {
			return publish.MergeRequest{}, err
		}
		if ok {
			mr = merged
		}
		refused = why
		// Not ok, no error: the pipeline has not started or the MR cannot yet
		// be merged. mr keeps Automerge=false; the next run tries again.
	}
	out := toPublish(mr)
	out.AutomergeRefused = refused
	return out, nil
}

// UpdateMergeRequest brings an existing merge request in line with r,
// changing only what differs and reporting what it changed.
func (p *Platform) UpdateMergeRequest(ctx context.Context, proj publish.Project, iid int, r publish.Request) (publish.MergeRequest, []string, error) {
	mrURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", p.base, url.PathEscape(proj.Path), iid)

	resp, err := p.do(ctx, http.MethodGet, mrURL, nil)
	if err != nil {
		return publish.MergeRequest{}, nil, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.MergeRequest{}, nil, err
	}
	var cur mrJSON
	if err := json.Unmarshal(resp.body, &cur); err != nil {
		return publish.MergeRequest{}, nil, fmt.Errorf("gitlab: decode merge request %d for %q: %w", iid, proj.Path, err)
	}

	fields := map[string]any{}
	var changed []string
	refused := ""
	if cur.Title != r.Title {
		fields["title"] = r.Title
		changed = append(changed, "title")
	}
	// GitLab stores a description without its trailing whitespace; a
	// comparison on the bytes as sent would rewrite every request on
	// every run.
	if strings.TrimSpace(cur.Description) != strings.TrimSpace(r.Description) {
		fields["description"] = r.Description
		changed = append(changed, "description")
	}
	if !sameLabelSet(cur.Labels, r.Labels) {
		fields["labels"] = strings.Join(r.Labels, ",")
		changed = append(changed, "labels")
	}

	if len(fields) > 0 {
		resp, err := p.do(ctx, http.MethodPut, mrURL, fields)
		if err != nil {
			return publish.MergeRequest{}, nil, err
		}
		if err := classify(resp, proj.Path); err != nil {
			return publish.MergeRequest{}, nil, err
		}
		if err := json.Unmarshal(resp.body, &cur); err != nil {
			return publish.MergeRequest{}, nil, fmt.Errorf("gitlab: decode updated merge request %d for %q: %w", iid, proj.Path, err)
		}
	}

	// Automerge, handled the same way as CreateMergeRequest: a separate call
	// to the merge endpoint, tolerant of "not yet". Turning it off is its
	// own endpoint, cancel_merge_when_pipeline_succeeds - a rule that
	// stopped allowing automerge must be able to take it back.
	switch {
	case r.Automerge && !cur.Automerge:
		merged, ok, why, err := p.trySetAutomerge(ctx, proj.Path, iid, cur.SHA)
		if err != nil {
			return publish.MergeRequest{}, nil, err
		}
		if ok {
			cur = merged
			changed = append(changed, "automerge")
		}
		refused = why
	case !r.Automerge && cur.Automerge:
		u := fmt.Sprintf("%s/cancel_merge_when_pipeline_succeeds", mrURL)
		resp, err := p.do(ctx, http.MethodPost, u, nil)
		if err != nil {
			return publish.MergeRequest{}, nil, err
		}
		if err := classify(resp, proj.Path); err != nil {
			return publish.MergeRequest{}, nil, err
		}
		cur.Automerge = false
		changed = append(changed, "automerge")
	}

	out := toPublish(cur)
	out.AutomergeRefused = refused
	return out, changed, nil
}

// CloseMergeRequest closes the request under a new title.
func (p *Platform) CloseMergeRequest(ctx context.Context, proj publish.Project, iid int, title string) error {
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", p.base, url.PathEscape(proj.Path), iid)
	resp, err := p.do(ctx, http.MethodPut, u, map[string]any{"state_event": "close", "title": title})
	if err != nil {
		return err
	}
	return classify(resp, proj.Path)
}

// CommitVerification reports how GitLab judged a commit's signature. A 404
// means GitLab has no signature record for the commit at all, which is what
// an unsigned commit looks like.
func (p *Platform) CommitVerification(ctx context.Context, proj publish.Project, sha string) (string, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits/%s/signature",
		p.base, url.PathEscape(proj.Path), url.PathEscape(sha))

	resp, err := p.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if resp.status == http.StatusNotFound {
		return "unsigned", nil
	}
	if err := classify(resp, proj.Path); err != nil {
		return "", err
	}

	var payload struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return "", fmt.Errorf("gitlab: decode signature for %s@%q: %w", sha, proj.Path, err)
	}
	return payload.VerificationStatus, nil
}

// ReadFile fetches a repository file through the raw-file endpoint.
func (p *Platform) ReadFile(ctx context.Context, project, path, ref string) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw", p.base, url.PathEscape(project), url.PathEscape(path))
	if ref != "" {
		u += "?ref=" + url.QueryEscape(ref)
	}
	resp, err := p.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusNotFound {
		return nil, fmt.Errorf("gitlab: %s has no file %s at %q, or the token cannot read it", project, path, ref)
	}
	if err := classify(resp, project); err != nil {
		return nil, err
	}
	return resp.body, nil
}

// ReadIssue returns the open issue with exactly this title and its
// description, or false when there is none.
func (p *Platform) ReadIssue(ctx context.Context, proj publish.Project, title string) (publish.Issue, string, bool, error) {
	base := fmt.Sprintf("%s/api/v4/projects/%s/issues", p.base, url.PathEscape(proj.Path))
	me, err := p.selfUserID(ctx)
	if err != nil {
		return publish.Issue{}, "", false, err
	}
	// Only the bot's own issue is the dashboard: anyone who can open an
	// issue could otherwise write one with the title and every box ticked.
	q := url.Values{"state": {"opened"}, "search": {title}, "in": {"title"}, "per_page": {"100"}, "author_id": {strconv.Itoa(me)}}
	resp, err := p.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return publish.Issue{}, "", false, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.Issue{}, "", false, err
	}
	var found []issueJSON
	if err := json.Unmarshal(resp.body, &found); err != nil {
		return publish.Issue{}, "", false, fmt.Errorf("gitlab: decode issues of %q: %w", proj.Path, err)
	}
	for _, is := range found {
		if is.Title == title {
			return is.issue(), is.Description, true, nil
		}
	}
	return publish.Issue{}, "", false, nil
}

// UpsertIssue searches the project's open issues for the exact title and
// updates description and labels when they differ, or creates the issue.
func (p *Platform) UpsertIssue(ctx context.Context, proj publish.Project, title, description string, labels []string) (publish.Issue, bool, error) {
	base := fmt.Sprintf("%s/api/v4/projects/%s/issues", p.base, url.PathEscape(proj.Path))
	me, err := p.selfUserID(ctx)
	if err != nil {
		return publish.Issue{}, false, err
	}
	q := url.Values{"state": {"opened"}, "search": {title}, "in": {"title"}, "per_page": {"100"}, "author_id": {strconv.Itoa(me)}}
	resp, err := p.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return publish.Issue{}, false, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.Issue{}, false, err
	}
	var found []issueJSON
	if err := json.Unmarshal(resp.body, &found); err != nil {
		return publish.Issue{}, false, fmt.Errorf("gitlab: decode issues of %q: %w", proj.Path, err)
	}
	for _, is := range found {
		if is.Title != title {
			continue
		}
		fields := map[string]any{}
		if strings.TrimSpace(is.Description) != strings.TrimSpace(description) {
			fields["description"] = description
		}
		if !sameSet(is.Labels, labels) {
			fields["labels"] = strings.Join(labels, ",")
		}
		if len(fields) == 0 {
			return is.issue(), false, nil
		}
		resp, err := p.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d", base, is.IID), fields)
		if err != nil {
			return publish.Issue{}, false, err
		}
		if err := classify(resp, proj.Path); err != nil {
			return publish.Issue{}, false, err
		}
		var updated issueJSON
		if err := json.Unmarshal(resp.body, &updated); err != nil {
			return publish.Issue{}, false, fmt.Errorf("gitlab: decode updated issue %d of %q: %w", is.IID, proj.Path, err)
		}
		return updated.issue(), true, nil
	}
	resp, err = p.do(ctx, http.MethodPost, base, map[string]any{
		"title": title, "description": description, "labels": strings.Join(labels, ","),
	})
	if err != nil {
		return publish.Issue{}, false, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.Issue{}, false, err
	}
	var created issueJSON
	if err := json.Unmarshal(resp.body, &created); err != nil {
		return publish.Issue{}, false, fmt.Errorf("gitlab: decode created issue of %q: %w", proj.Path, err)
	}
	return created.issue(), true, nil
}

type issueJSON struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	State       string   `json:"state"`
	WebURL      string   `json:"web_url"`
}

func (i issueJSON) issue() publish.Issue {
	return publish.Issue{IID: i.IID, Title: i.Title, URL: i.WebURL, State: i.State}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

// OpenMergeRequests lists the open merge requests whose source branch
// starts with prefix.
// MergedMergeRequests lists merged requests updated at or after since whose
// source branch starts with prefix. updated_after is GitLab's filter; the
// merge time itself is what is compared.
func (p *Platform) MergedMergeRequests(ctx context.Context, proj publish.Project, prefix string, since time.Time) ([]publish.MergeRequest, error) {
	q := url.Values{"state": {"merged"}, "updated_after": {since.UTC().Format(time.RFC3339)}}
	items, err := p.listMergeRequests(ctx, proj.Path, q)
	if err != nil {
		return nil, err
	}
	var out []publish.MergeRequest
	for _, m := range items {
		if strings.HasPrefix(m.SourceBranch, prefix) && (m.MergedAt.IsZero() || !m.MergedAt.Before(since)) {
			out = append(out, toPublish(m))
		}
	}
	return out, nil
}

func (p *Platform) OpenMergeRequests(ctx context.Context, proj publish.Project, prefix string) ([]publish.MergeRequest, error) {
	q := url.Values{"state": {"opened"}}
	items, err := p.listMergeRequests(ctx, proj.Path, q)
	if err != nil {
		return nil, err
	}
	var out []publish.MergeRequest
	for _, m := range items {
		if strings.HasPrefix(m.SourceBranch, prefix) {
			out = append(out, toPublish(m))
		}
	}
	return out, nil
}

// ListProjects walks /projects?membership=true&archived=false, following
// X-Next-Page, and returns the paths sorted.
func (p *Platform) ListProjects(ctx context.Context) ([]string, error) {
	var out []string
	page := "1"
	for page != "" {
		u := fmt.Sprintf("%s/api/v4/projects?membership=true&archived=false&simple=true&per_page=100&page=%s", p.base, page)
		resp, err := p.do(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if err := classify(resp, "(listing)"); err != nil {
			return nil, err
		}
		var items []struct {
			Path string `json:"path_with_namespace"`
		}
		if err := json.Unmarshal(resp.body, &items); err != nil {
			return nil, fmt.Errorf("gitlab: decode project list: %w", err)
		}
		for _, it := range items {
			out = append(out, it.Path)
		}
		page = resp.header.Get("X-Next-Page")
	}
	slices.Sort(out)
	return out, nil
}

// trySetAutomerge asks GitLab to merge sourceBranch's request when its
// pipeline succeeds. A 405 (pipeline has not started) or 406 (not currently
// mergeable) is reported as ok=false with no error: the caller keeps
// Automerge=false and a later run tries again. A 401 or 403 is GitLab's
// answer when the token's user may not merge in this project (Developer on
// a protected branch; measured on nozzleops/platform, 2026-09-14, where the
// request had been created a second before): ok=false, no error, and the
// refusal returned for the run to report beside the request. Any other
// non-2xx is an error.
func (p *Platform) trySetAutomerge(ctx context.Context, projectPath string, iid int, sha string) (mrJSON, bool, string, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/merge", p.base, url.PathEscape(projectPath), iid)
	payload := map[string]any{
		"merge_when_pipeline_succeeds": true,
		"should_remove_source_branch":  true,
	}
	if sha != "" {
		// Bound to the head the run pushed: GitLab refuses the request if
		// the branch has moved, which is the right outcome.
		payload["sha"] = sha
	}

	resp, err := p.do(ctx, http.MethodPut, u, payload)
	if err != nil {
		return mrJSON{}, false, "", err
	}
	switch {
	case resp.status >= 200 && resp.status < 300:
		var mr mrJSON
		if err := json.Unmarshal(resp.body, &mr); err != nil {
			return mrJSON{}, false, "", fmt.Errorf("gitlab: decode merge response for %q !%d: %w", projectPath, iid, err)
		}
		// The response is not guaranteed to echo the flag back consistently
		// across GitLab versions; the call succeeding is what matters.
		mr.Automerge = true
		return mr, true, "", nil
	case resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden:
		return mrJSON{}, false, fmt.Sprintf("automerge refused by %q (status %d): the bot may not merge here, a Maintainer merges", projectPath, resp.status), nil
	case resp.status == http.StatusBadRequest || resp.status == http.StatusMethodNotAllowed ||
		resp.status == http.StatusNotAcceptable || resp.status == http.StatusUnprocessableEntity ||
		resp.status == http.StatusConflict:
		// Measured on a freshly created request: 400 "SHA must be
		// provided when merging" while GitLab is still preparing the
		// diff; 405 without a pipeline; 406 when it cannot be merged;
		// 409 "SHA does not match HEAD of source branch" right after a
		// rebase push, while the request still records the old head
		// (pinup/pinup!1, 2026-09-13). All mean "not yet", and the next
		// run asks again.
		return mrJSON{}, false, "", nil
	default:
		return mrJSON{}, false, "", classify(resp, projectPath)
	}
}

// listMergeRequests walks every page of the merge_requests endpoint for
// query, following X-Next-Page until GitLab stops sending one.
func (p *Platform) listMergeRequests(ctx context.Context, projectPath string, query url.Values) ([]mrJSON, error) {
	base := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", p.base, url.PathEscape(projectPath))
	q := maps.Clone(query)
	if q.Get("per_page") == "" {
		q.Set("per_page", "100")
	}

	var out []mrJSON
	page := "1"
	for page != "" {
		q.Set("page", page)
		resp, err := p.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if err := classify(resp, projectPath); err != nil {
			return nil, err
		}

		var items []mrJSON
		if err := json.Unmarshal(resp.body, &items); err != nil {
			return nil, fmt.Errorf("gitlab: decode merge requests for %q: %w", projectPath, err)
		}
		out = append(out, items...)
		page = resp.header.Get("X-Next-Page")
	}
	return out, nil
}

// mrJSON is the shape of one merge request as GitLab's REST v4 API returns
// it, trimmed to the fields publish.MergeRequest needs.
type mrJSON struct {
	IID          int       `json:"iid"`
	State        string    `json:"state"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Labels       []string  `json:"labels"`
	SHA          string    `json:"sha"`
	WebURL       string    `json:"web_url"`
	Automerge    bool      `json:"merge_when_pipeline_succeeds"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MergedAt     time.Time `json:"merged_at"`
}

func toPublish(m mrJSON) publish.MergeRequest {
	return publish.MergeRequest{
		IID:          m.IID,
		State:        m.State,
		SourceBranch: m.SourceBranch,
		TargetBranch: m.TargetBranch,
		Title:        m.Title,
		Description:  m.Description,
		Labels:       slices.Clone(m.Labels),
		SHA:          m.SHA,
		WebURL:       m.WebURL,
		Automerge:    m.Automerge,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// sameLabelSet compares two label lists as sets: order does not distinguish
// them, but a label added or dropped does.
func sameLabelSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := slices.Clone(a), slices.Clone(b)
	slices.Sort(sa)
	slices.Sort(sb)
	return slices.Equal(sa, sb)
}

// apiResponse is the trimmed-down shape this package needs from an
// http.Response: the body already read into memory, and the headers callers
// need (X-Next-Page).
type apiResponse struct {
	status int
	body   []byte
	header http.Header
}

// do performs one request, attaching the token header and a JSON body when
// payload is non-nil. It never classifies the status: callers that need
// uniform error handling call classify; trySetAutomerge inspects the status
// itself because 405/406 there are not errors.
func (p *Platform) do(ctx context.Context, method, rawURL string, payload any) (*apiResponse, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("gitlab: encode request body: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("gitlab: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token.Value != "" && p.token.Header != "" {
		req.Header.Set(p.token.Header, p.token.Value)
	}

	resp, err := p.hc.Do(req)
	if err != nil {
		// The stdlib error text names addresses and protocols, never our
		// headers, so it is safe to wrap directly - no token to leak here.
		return nil, fmt.Errorf("gitlab: request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return nil, fmt.Errorf("gitlab: read response from %s: %w", req.URL.Host, err)
	}
	return &apiResponse{status: resp.StatusCode, body: respBody, header: resp.Header.Clone()}, nil
}

// classify turns a non-2xx response into the error a caller should see.
// GitLab does not distinguish "does not exist" from "you may not see it" on a
// 404, so the message must say both.
func classify(resp *apiResponse, projectPath string) error {
	switch {
	case resp.status >= 200 && resp.status < 300:
		return nil
	case resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden:
		return fmt.Errorf("gitlab: the token cannot access project %q (status %d)", projectPath, resp.status)
	case resp.status == http.StatusNotFound:
		return fmt.Errorf("gitlab: project %q does not exist or the token cannot see it", projectPath)
	case resp.status == http.StatusTooManyRequests:
		return fmt.Errorf("gitlab: rate limited while accessing project %q", projectPath)
	default:
		// GitLab explains a 4xx in the body ({"message": ...}); the
		// explanation is what makes a 400 actionable, and it never carries
		// the token.
		if msg := apiMessage(resp.body); msg != "" {
			return fmt.Errorf("gitlab: status %d for project %q: %s", resp.status, projectPath, msg)
		}
		return fmt.Errorf("gitlab: unexpected status %d for project %q", resp.status, projectPath)
	}
}

// apiMessage extracts GitLab's "message" from an error body, whatever its
// shape (a string, a list, or a map of field errors), truncated for a log
// line.
func apiMessage(body []byte) string {
	var doc struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	var msg string
	switch m := doc.Message.(type) {
	case string:
		msg = m
	case nil:
		msg = doc.Error
	default:
		b, _ := json.Marshal(m)
		msg = string(b)
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
