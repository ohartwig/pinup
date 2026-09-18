// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package github implements publish.Platform against the GitHub REST API
// (docs.github.com/rest) and the one GraphQL mutation REST has no
// equivalent for: arming a pull request's auto-merge. Nothing here is
// derived from Renovate's implementation; the pull-request lifecycle is
// re-implemented from the documented endpoints, the way platform/gitlab
// does for GitLab.
//
// What GitHub has no equivalent for is said, not faked: a pull request
// cannot ask for its branch to be deleted on merge (that is the
// repository's delete_branch_on_merge setting), and auto-merge needs the
// repository to allow it - the refusal is reported beside the request, as
// the GitLab one is when the bot may not merge.
//
// Like platform/gitlab this package takes an http.RoundTripper and does
// its own request helper: every write is a POST or PATCH with a JSON body,
// and the credential goes in Authorization, which httpx has no hook for.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/pinup/publish"
)

// Name is the platform name a plan's report refers to.
const Name = "github"

// bodyLimit caps how much of a response is read into memory.
const bodyLimit = 4 << 20

// apiVersion is the REST API version this package was written against.
const apiVersion = "2022-11-28"

// Platform implements publish.Platform against one GitHub instance:
// github.com through api.github.com, or an enterprise host through its
// /api/v3 prefix.
type Platform struct {
	hc    *http.Client
	api   string // https://api.github.com or https://host/api/v3
	token string

	loginMu sync.Mutex
	login   string
}

// APIBase returns the REST base for a GitHub web URL: api.github.com for
// github.com, <url>/api/v3 for an enterprise host.
func APIBase(webURL string) string {
	u := strings.TrimRight(webURL, "/")
	if u == "" || strings.EqualFold(u, "https://github.com") || strings.EqualFold(u, "http://github.com") {
		return "https://api.github.com"
	}
	return u + "/api/v3"
}

// New returns a Platform for the instance whose web URL is webURL (empty
// means github.com), dialling through transport with token. A nil
// transport means http.DefaultTransport.
func New(webURL string, transport http.RoundTripper, token string) *Platform {
	if transport == nil {
		transport = http.DefaultTransport
	}
	p := &Platform{api: APIBase(webURL), token: token}
	p.hc = &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// A redirect off the API host would carry the token with it; the
		// raw-content endpoint stays on the API host, so nothing this
		// package calls needs one.
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return fmt.Errorf("github: refusing to follow a redirect off the API host to %s", req.URL.Host)
		}
		if len(via) >= 10 {
			return errors.New("github: stopped after 10 redirects")
		}
		return nil
	}}
	return p
}

func (p *Platform) Name() string { return Name }

// selfLogin answers the login of the account behind the token, from
// /user, once: the dashboard is the issue that account wrote.
func (p *Platform) selfLogin(ctx context.Context) (string, error) {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	if p.login != "" {
		return p.login, nil
	}
	resp, err := p.do(ctx, http.MethodGet, p.api+"/user", nil)
	if err != nil {
		return "", err
	}
	if err := classify(resp, "(the token's own user)"); err != nil {
		return "", err
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil || payload.Login == "" {
		return "", fmt.Errorf("github: decode /user: %v", err)
	}
	p.login = payload.Login
	return p.login, nil
}

// splitPath reads "owner/repo"; a deeper path is not a GitHub repository.
func splitPath(path string) (string, string, error) {
	owner, repo, ok := strings.Cut(strings.Trim(path, "/"), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("github: %q is not an owner/repository path", path)
	}
	return owner, repo, nil
}

func (p *Platform) repoURL(path string) (string, error) {
	owner, repo, err := splitPath(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/repos/%s/%s", p.api, url.PathEscape(owner), url.PathEscape(repo)), nil
}

// Project resolves a repository path to its canonical full name and
// default branch.
func (p *Platform) Project(ctx context.Context, path string) (publish.Project, error) {
	u, err := p.repoURL(path)
	if err != nil {
		return publish.Project{}, err
	}
	resp, err := p.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return publish.Project{}, err
	}
	if err := classify(resp, path); err != nil {
		return publish.Project{}, err
	}
	var payload struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return publish.Project{}, fmt.Errorf("github: decode repository %q: %w", path, err)
	}
	return publish.Project{Path: payload.FullName, DefaultBranch: payload.DefaultBranch}, nil
}

// FindMergeRequest returns the open pull request whose head is
// sourceBranch in the repository itself (GitHub's head filter is
// "owner:branch"; a fork's branch of the same name is another request).
func (p *Platform) FindMergeRequest(ctx context.Context, proj publish.Project, sourceBranch string) (publish.MergeRequest, bool, error) {
	owner, _, err := splitPath(proj.Path)
	if err != nil {
		return publish.MergeRequest{}, false, err
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + sourceBranch}}
	items, err := p.listPulls(ctx, proj.Path, q, nil)
	if err != nil {
		return publish.MergeRequest{}, false, err
	}
	if len(items) == 0 {
		return publish.MergeRequest{}, false, nil
	}
	return toPublish(items[0]), true, nil
}

// History lists every pull request there ever was for sourceBranch,
// newest first.
func (p *Platform) History(ctx context.Context, proj publish.Project, sourceBranch string) ([]publish.MergeRequest, error) {
	owner, _, err := splitPath(proj.Path)
	if err != nil {
		return nil, err
	}
	q := url.Values{"state": {"all"}, "head": {owner + ":" + sourceBranch}, "sort": {"created"}, "direction": {"desc"}}
	items, err := p.listPulls(ctx, proj.Path, q, nil)
	if err != nil {
		return nil, err
	}
	out := make([]publish.MergeRequest, 0, len(items))
	for _, m := range items {
		out = append(out, toPublish(m))
	}
	return out, nil
}

// CreateMergeRequest opens a pull request, sets its labels, and, if
// r.Automerge was asked for, tries once to arm auto-merge.
func (p *Platform) CreateMergeRequest(ctx context.Context, proj publish.Project, r publish.Request) (publish.MergeRequest, error) {
	base, err := p.repoURL(proj.Path)
	if err != nil {
		return publish.MergeRequest{}, err
	}
	resp, err := p.do(ctx, http.MethodPost, base+"/pulls", map[string]any{
		"title": r.Title, "head": r.SourceBranch, "base": r.TargetBranch, "body": r.Description,
	})
	if err != nil {
		return publish.MergeRequest{}, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.MergeRequest{}, err
	}
	var pr pullJSON
	if err := json.Unmarshal(resp.body, &pr); err != nil {
		return publish.MergeRequest{}, fmt.Errorf("github: decode created pull request for %q: %w", proj.Path, err)
	}
	if len(r.Labels) > 0 {
		labels, err := p.setLabels(ctx, base, pr.Number, r.Labels)
		if err != nil {
			return publish.MergeRequest{}, err
		}
		pr.Labels = labels
	}
	refused := ""
	if r.Automerge {
		ok, why, err := p.setAutomerge(ctx, proj.Path, pr.NodeID, pr.Number, true)
		if err != nil {
			return publish.MergeRequest{}, err
		}
		if ok {
			pr.AutoMerge = &struct{}{}
		}
		refused = why
	}
	out := toPublish(pr)
	out.AutomergeRefused = refused
	return out, nil
}

// UpdateMergeRequest brings pull request number in line with r, changing
// only what differs and reporting what it changed.
func (p *Platform) UpdateMergeRequest(ctx context.Context, proj publish.Project, number int, r publish.Request) (publish.MergeRequest, []string, error) {
	base, err := p.repoURL(proj.Path)
	if err != nil {
		return publish.MergeRequest{}, nil, err
	}
	prURL := fmt.Sprintf("%s/pulls/%d", base, number)
	resp, err := p.do(ctx, http.MethodGet, prURL, nil)
	if err != nil {
		return publish.MergeRequest{}, nil, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.MergeRequest{}, nil, err
	}
	var cur pullJSON
	if err := json.Unmarshal(resp.body, &cur); err != nil {
		return publish.MergeRequest{}, nil, fmt.Errorf("github: decode pull request %d for %q: %w", number, proj.Path, err)
	}

	fields := map[string]any{}
	var changed []string
	if cur.Title != r.Title {
		fields["title"] = r.Title
		changed = append(changed, "title")
	}
	if strings.TrimSpace(cur.Body) != strings.TrimSpace(r.Description) {
		fields["body"] = r.Description
		changed = append(changed, "description")
	}
	if len(fields) > 0 {
		resp, err := p.do(ctx, http.MethodPatch, prURL, fields)
		if err != nil {
			return publish.MergeRequest{}, nil, err
		}
		if err := classify(resp, proj.Path); err != nil {
			return publish.MergeRequest{}, nil, err
		}
		if err := json.Unmarshal(resp.body, &cur); err != nil {
			return publish.MergeRequest{}, nil, fmt.Errorf("github: decode updated pull request %d for %q: %w", number, proj.Path, err)
		}
	}
	if !sameLabelSet(cur.labelNames(), r.Labels) {
		labels, err := p.setLabels(ctx, base, number, r.Labels)
		if err != nil {
			return publish.MergeRequest{}, nil, err
		}
		cur.Labels = labels
		changed = append(changed, "labels")
	}

	refused := ""
	switch {
	case r.Automerge && cur.AutoMerge == nil:
		ok, why, err := p.setAutomerge(ctx, proj.Path, cur.NodeID, number, true)
		if err != nil {
			return publish.MergeRequest{}, nil, err
		}
		if ok {
			cur.AutoMerge = &struct{}{}
			changed = append(changed, "automerge")
		}
		refused = why
	case !r.Automerge && cur.AutoMerge != nil:
		if _, _, err := p.setAutomerge(ctx, proj.Path, cur.NodeID, number, false); err != nil {
			return publish.MergeRequest{}, nil, err
		}
		cur.AutoMerge = nil
		changed = append(changed, "automerge")
	}
	out := toPublish(cur)
	out.AutomergeRefused = refused
	return out, changed, nil
}

// CloseMergeRequest closes the pull request under a new title.
func (p *Platform) CloseMergeRequest(ctx context.Context, proj publish.Project, number int, title string) error {
	base, err := p.repoURL(proj.Path)
	if err != nil {
		return err
	}
	resp, err := p.do(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", base, number), map[string]any{"state": "closed", "title": title})
	if err != nil {
		return err
	}
	return classify(resp, proj.Path)
}

// setLabels replaces the labels of issue/pull request number and returns
// what GitHub now records.
func (p *Platform) setLabels(ctx context.Context, repoURL string, number int, labels []string) ([]labelJSON, error) {
	resp, err := p.do(ctx, http.MethodPut, fmt.Sprintf("%s/issues/%d/labels", repoURL, number), map[string]any{"labels": labels})
	if err != nil {
		return nil, err
	}
	if err := classify(resp, repoURL); err != nil {
		return nil, err
	}
	var out []labelJSON
	if err := json.Unmarshal(resp.body, &out); err != nil {
		return nil, fmt.Errorf("github: decode labels of #%d: %w", number, err)
	}
	return out, nil
}

// setAutomerge arms or disarms auto-merge through GraphQL, the only API
// GitHub offers for it. A pull request that is "not in the correct state"
// - already mergeable with nothing to wait for, or without required
// checks - is "not yet", not an error: ok=false and the next run asks
// again. A repository that does not allow auto-merge, or a token that may
// not, is a refusal reported beside the request.
func (p *Platform) setAutomerge(ctx context.Context, path, nodeID string, number int, on bool) (bool, string, error) {
	mutation := "mutation($id: ID!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: MERGE}) { clientMutationId } }"
	if !on {
		mutation = "mutation($id: ID!) { disablePullRequestAutoMerge(input: {pullRequestId: $id}) { clientMutationId } }"
	}
	resp, err := p.do(ctx, http.MethodPost, p.api+"/graphql", map[string]any{
		"query": mutation, "variables": map[string]any{"id": nodeID},
	})
	if err != nil {
		return false, "", err
	}
	if err := classify(resp, path); err != nil {
		return false, "", err
	}
	var payload struct {
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return false, "", fmt.Errorf("github: decode auto-merge response for %q #%d: %w", path, number, err)
	}
	if len(payload.Errors) == 0 {
		return true, "", nil
	}
	msg := payload.Errors[0].Message
	switch {
	case strings.Contains(msg, "not in the correct state"), strings.Contains(msg, "clean status"):
		// Nothing to wait for, or the checks have not started: not yet.
		return false, "", nil
	case strings.Contains(msg, "not allowed"), strings.Contains(msg, "not enabled"), payload.Errors[0].Type == "FORBIDDEN", payload.Errors[0].Type == "UNPROCESSABLE":
		return false, fmt.Sprintf("auto-merge refused by %q: %s", path, msg), nil
	}
	return false, "", fmt.Errorf("github: auto-merge for %q #%d: %s", path, number, msg)
}

// CommitVerification reports GitHub's verdict on a commit's signature:
// "verified", or the reason it is not.
func (p *Platform) CommitVerification(ctx context.Context, proj publish.Project, sha string) (string, error) {
	base, err := p.repoURL(proj.Path)
	if err != nil {
		return "", err
	}
	resp, err := p.do(ctx, http.MethodGet, base+"/commits/"+url.PathEscape(sha), nil)
	if err != nil {
		return "", err
	}
	if err := classify(resp, proj.Path); err != nil {
		return "", err
	}
	var payload struct {
		Commit struct {
			Verification struct {
				Verified bool   `json:"verified"`
				Reason   string `json:"reason"`
			} `json:"verification"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return "", fmt.Errorf("github: decode commit %s of %q: %w", sha, proj.Path, err)
	}
	if payload.Commit.Verification.Verified {
		return "verified", nil
	}
	if payload.Commit.Verification.Reason == "unsigned" || payload.Commit.Verification.Reason == "" {
		return "unsigned", nil
	}
	return payload.Commit.Verification.Reason, nil
}

// ReadFile fetches a repository file through the contents endpoint, as raw
// bytes.
func (p *Platform) ReadFile(ctx context.Context, project, path, ref string) ([]byte, error) {
	base, err := p.repoURL(project)
	if err != nil {
		return nil, err
	}
	u := base + "/contents/" + escapePath(path)
	if ref != "" {
		u += "?ref=" + url.QueryEscape(ref)
	}
	resp, err := p.doAccept(ctx, http.MethodGet, u, nil, "application/vnd.github.raw+json")
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusNotFound {
		return nil, fmt.Errorf("github: %s has no file %s at %q, or the token cannot read it", project, path, ref)
	}
	if err := classify(resp, project); err != nil {
		return nil, err
	}
	return resp.body, nil
}

// escapePath escapes each segment of a repository path, keeping the slashes.
func escapePath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// ReadIssue returns the open issue with exactly this title that the
// token's own account wrote, and its body.
func (p *Platform) ReadIssue(ctx context.Context, proj publish.Project, title string) (publish.Issue, string, bool, error) {
	found, err := p.ownIssues(ctx, proj.Path)
	if err != nil {
		return publish.Issue{}, "", false, err
	}
	for _, is := range found {
		if is.Title == title {
			return is.issue(), is.Body, true, nil
		}
	}
	return publish.Issue{}, "", false, nil
}

// UpsertIssue finds the bot's open issue with exactly this title and brings
// body and labels in line, or opens it.
func (p *Platform) UpsertIssue(ctx context.Context, proj publish.Project, title, description string, labels []string) (publish.Issue, bool, error) {
	base, err := p.repoURL(proj.Path)
	if err != nil {
		return publish.Issue{}, false, err
	}
	found, err := p.ownIssues(ctx, proj.Path)
	if err != nil {
		return publish.Issue{}, false, err
	}
	for _, is := range found {
		if is.Title != title {
			continue
		}
		fields := map[string]any{}
		if strings.TrimSpace(is.Body) != strings.TrimSpace(description) {
			fields["body"] = description
		}
		if !sameLabelSet(is.labelNames(), labels) {
			fields["labels"] = labels
		}
		if len(fields) == 0 {
			return is.issue(), false, nil
		}
		resp, err := p.do(ctx, http.MethodPatch, fmt.Sprintf("%s/issues/%d", base, is.Number), fields)
		if err != nil {
			return publish.Issue{}, false, err
		}
		if err := classify(resp, proj.Path); err != nil {
			return publish.Issue{}, false, err
		}
		var updated issueJSON
		if err := json.Unmarshal(resp.body, &updated); err != nil {
			return publish.Issue{}, false, fmt.Errorf("github: decode updated issue #%d of %q: %w", is.Number, proj.Path, err)
		}
		return updated.issue(), true, nil
	}
	resp, err := p.do(ctx, http.MethodPost, base+"/issues", map[string]any{"title": title, "body": description, "labels": labels})
	if err != nil {
		return publish.Issue{}, false, err
	}
	if err := classify(resp, proj.Path); err != nil {
		return publish.Issue{}, false, err
	}
	var created issueJSON
	if err := json.Unmarshal(resp.body, &created); err != nil {
		return publish.Issue{}, false, fmt.Errorf("github: decode created issue of %q: %w", proj.Path, err)
	}
	return created.issue(), true, nil
}

// ownIssues lists the open issues the token's account created. GitHub's
// issues endpoint returns pull requests too; those are left out.
func (p *Platform) ownIssues(ctx context.Context, path string) ([]issueJSON, error) {
	base, err := p.repoURL(path)
	if err != nil {
		return nil, err
	}
	me, err := p.selfLogin(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{"state": {"open"}, "creator": {me}, "per_page": {"100"}}
	var out []issueJSON
	next := base + "/issues?" + q.Encode()
	for next != "" {
		resp, err := p.do(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		if err := classify(resp, path); err != nil {
			return nil, err
		}
		var items []issueJSON
		if err := json.Unmarshal(resp.body, &items); err != nil {
			return nil, fmt.Errorf("github: decode issues of %q: %w", path, err)
		}
		for _, is := range items {
			if is.PullRequest == nil {
				out = append(out, is)
			}
		}
		next = nextLink(resp.header)
	}
	return out, nil
}

// OpenMergeRequests lists the open pull requests whose head branch starts
// with prefix.
func (p *Platform) OpenMergeRequests(ctx context.Context, proj publish.Project, prefix string) ([]publish.MergeRequest, error) {
	items, err := p.listPulls(ctx, proj.Path, url.Values{"state": {"open"}}, nil)
	if err != nil {
		return nil, err
	}
	var out []publish.MergeRequest
	for _, m := range items {
		if strings.HasPrefix(m.Head.Ref, prefix) {
			out = append(out, toPublish(m))
		}
	}
	return out, nil
}

// MergedMergeRequests lists the pull requests merged at or after since
// whose head branch starts with prefix. GitHub has no merged filter; the
// closed requests are walked newest-updated first and the walk stops at
// the first one updated before since.
func (p *Platform) MergedMergeRequests(ctx context.Context, proj publish.Project, prefix string, since time.Time) ([]publish.MergeRequest, error) {
	q := url.Values{"state": {"closed"}, "sort": {"updated"}, "direction": {"desc"}}
	items, err := p.listPulls(ctx, proj.Path, q, func(m pullJSON) bool { return m.UpdatedAt.Before(since) })
	if err != nil {
		return nil, err
	}
	var out []publish.MergeRequest
	for _, m := range items {
		if m.MergedAt.IsZero() || m.MergedAt.Before(since) || !strings.HasPrefix(m.Head.Ref, prefix) {
			continue
		}
		out = append(out, toPublish(m))
	}
	return out, nil
}

// ListProjects returns the full names of every repository the token can
// see and push to that is not archived, sorted.
func (p *Platform) ListProjects(ctx context.Context) ([]string, error) {
	var out []string
	next := p.api + "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&sort=full_name"
	for next != "" {
		resp, err := p.do(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		if err := classify(resp, "(listing)"); err != nil {
			return nil, err
		}
		var items []struct {
			FullName    string `json:"full_name"`
			Archived    bool   `json:"archived"`
			Permissions struct {
				Push bool `json:"push"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(resp.body, &items); err != nil {
			return nil, fmt.Errorf("github: decode repository list: %w", err)
		}
		for _, it := range items {
			if !it.Archived && it.Permissions.Push {
				out = append(out, it.FullName)
			}
		}
		next = nextLink(resp.header)
	}
	slices.Sort(out)
	return out, nil
}

// listPulls walks every page of the pulls endpoint for query, following
// the Link header's rel="next", until stop says a page's item is past the
// point of interest.
func (p *Platform) listPulls(ctx context.Context, path string, query url.Values, stop func(pullJSON) bool) ([]pullJSON, error) {
	base, err := p.repoURL(path)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	if q.Get("per_page") == "" {
		q.Set("per_page", "100")
	}
	var out []pullJSON
	next := base + "/pulls?" + q.Encode()
	for next != "" {
		resp, err := p.do(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		if err := classify(resp, path); err != nil {
			return nil, err
		}
		var items []pullJSON
		if err := json.Unmarshal(resp.body, &items); err != nil {
			return nil, fmt.Errorf("github: decode pull requests of %q: %w", path, err)
		}
		for _, m := range items {
			if stop != nil && stop(m) {
				return out, nil
			}
			out = append(out, m)
		}
		next = nextLink(resp.header)
	}
	return out, nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextLink reads the URL of the next page out of a Link header.
func nextLink(h http.Header) string {
	for _, l := range h.Values("Link") {
		if m := linkNext.FindStringSubmatch(l); m != nil {
			return m[1]
		}
	}
	return ""
}

type labelJSON struct {
	Name string `json:"name"`
}

// pullJSON is one pull request as the REST API returns it, trimmed to what
// publish.MergeRequest needs.
type pullJSON struct {
	Number    int         `json:"number"`
	NodeID    string      `json:"node_id"`
	State     string      `json:"state"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	Labels    []labelJSON `json:"labels"`
	HTMLURL   string      `json:"html_url"`
	AutoMerge *struct{}   `json:"auto_merge"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	MergedAt  time.Time `json:"merged_at"`
}

func (m pullJSON) labelNames() []string {
	out := make([]string, 0, len(m.Labels))
	for _, l := range m.Labels {
		out = append(out, l.Name)
	}
	return out
}

func toPublish(m pullJSON) publish.MergeRequest {
	state := "closed"
	switch {
	case m.State == "open":
		state = "opened"
	case !m.MergedAt.IsZero():
		state = "merged"
	}
	return publish.MergeRequest{
		IID: m.Number, State: state, SourceBranch: m.Head.Ref, TargetBranch: m.Base.Ref,
		Title: m.Title, Description: m.Body, Labels: m.labelNames(), SHA: m.Head.SHA, WebURL: m.HTMLURL,
		Automerge: m.AutoMerge != nil, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

type issueJSON struct {
	Number      int         `json:"number"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	Labels      []labelJSON `json:"labels"`
	State       string      `json:"state"`
	HTMLURL     string      `json:"html_url"`
	PullRequest *struct{}   `json:"pull_request"`
}

func (i issueJSON) labelNames() []string {
	out := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		out = append(out, l.Name)
	}
	return out
}

func (i issueJSON) issue() publish.Issue {
	state := i.State
	if state == "open" {
		state = "opened"
	}
	return publish.Issue{IID: i.Number, Title: i.Title, URL: i.HTMLURL, State: state}
}

func sameLabelSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := slices.Clone(a), slices.Clone(b)
	slices.Sort(sa)
	slices.Sort(sb)
	return slices.Equal(sa, sb)
}

type apiResponse struct {
	status int
	body   []byte
	header http.Header
}

func (p *Platform) do(ctx context.Context, method, rawURL string, payload any) (*apiResponse, error) {
	return p.doAccept(ctx, method, rawURL, payload, "application/vnd.github+json")
}

// doAccept performs one request with the token in Authorization and a JSON
// body when payload is non-nil. It never classifies the status.
func (p *Platform) doAccept(ctx context.Context, method, rawURL string, payload any, accept string) (*apiResponse, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("github: encode request body: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return nil, fmt.Errorf("github: read response from %s: %w", req.URL.Host, err)
	}
	return &apiResponse{status: resp.StatusCode, body: respBody, header: resp.Header.Clone()}, nil
}

// classify turns a non-2xx response into the error a caller should see.
// GitHub answers 404 for a repository the token may not see as well as for
// one that does not exist.
func classify(resp *apiResponse, path string) error {
	switch {
	case resp.status >= 200 && resp.status < 300:
		return nil
	case resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden:
		if msg := apiMessage(resp.body); strings.Contains(msg, "rate limit") {
			return fmt.Errorf("github: rate limited while accessing %q", path)
		}
		return fmt.Errorf("github: the token cannot access %q (status %d)", path, resp.status)
	case resp.status == http.StatusNotFound:
		return fmt.Errorf("github: repository %q does not exist or the token cannot see it", path)
	case resp.status == http.StatusTooManyRequests:
		return fmt.Errorf("github: rate limited while accessing %q", path)
	default:
		if msg := apiMessage(resp.body); msg != "" {
			return fmt.Errorf("github: status %d for %q: %s", resp.status, path, msg)
		}
		return fmt.Errorf("github: unexpected status %d for %q", resp.status, path)
	}
}

// apiMessage extracts GitHub's "message" (and the first validation error)
// from an error body, truncated for a log line.
func apiMessage(body []byte) string {
	var doc struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Field   string `json:"field"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	msg := doc.Message
	if len(doc.Errors) > 0 {
		e := doc.Errors[0]
		detail := e.Message
		if detail == "" {
			detail = e.Field + " " + e.Code
		}
		msg += ": " + strings.TrimSpace(detail)
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
