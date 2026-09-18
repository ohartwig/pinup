// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/publish"
)

// testPageSize forces pagination to be exercised on every run, the same
// convention datasource/gitlabds uses.
const testPageSize = 2

// fakeMR is one merge request in the fake server's in-memory state.
type fakeMR struct {
	iid                  int
	state                string
	sourceBranch         string
	targetBranch         string
	title                string
	description          string
	labels               []string
	sha                  string
	webURL               string
	automerge            bool
	canMerge             bool // whether the /merge endpoint currently succeeds
	staleHead            bool // the request still records the head before a rebase push
	mayNotMerge          bool // the token's user lacks merge permission: GitLab answers 401
	createdAt, updatedAt time.Time
	mergedAt             time.Time
}

func (m *fakeMR) toJSON() mrJSON {
	return mrJSON{
		IID: m.iid, State: m.state, SourceBranch: m.sourceBranch, TargetBranch: m.targetBranch,
		Title: m.title, Description: m.description, Labels: append([]string(nil), m.labels...),
		SHA: m.sha, WebURL: m.webURL, Automerge: m.automerge,
		CreatedAt: m.createdAt, UpdatedAt: m.updatedAt, MergedAt: m.mergedAt,
	}
}

// fakeProject is one project's state: its merge requests and any recorded
// commit signatures.
type fakeIssue struct {
	iid         int
	title, desc string
	labels      []string
	state       string
	// authorID is who wrote it; the bot is 322, anyone else is not.
	authorID int
}

type fakeProject struct {
	pathWithNamespace string
	defaultBranch     string
	mrs               []*fakeMR
	issues            []*fakeIssue
	nextIID           int
	signatures        map[string]string // sha -> verification_status
	files             map[string]string // "path@ref" -> content
}

// requestLog is one request the fake received, kept so tests can assert on
// exactly which fields a PUT carried.
type requestLog struct {
	method string
	uri    string
	body   []byte
}

// gitlabServer speaks enough of the GitLab REST v4 API to exercise
// Platform: project resolution, merge request listing with state and branch
// filtering and X-Next-Page pagination at a forced page size of two, create
// and update, the merge (automerge) endpoint, and the commit signature
// endpoint.
type gitlabServer struct {
	mu          sync.Mutex
	token       string
	projects    map[string]*fakeProject // URL-encoded project path -> project
	requests    []requestLog
	mayNotMerge bool // the token's user lacks merge permission everywhere: /merge answers 401
}

func newGitlabServer(token string) *gitlabServer {
	return &gitlabServer{token: token, projects: map[string]*fakeProject{}}
}

func (s *gitlabServer) addProject(path, defaultBranch string) *fakeProject {
	p := &fakeProject{pathWithNamespace: path, defaultBranch: defaultBranch, signatures: map[string]string{}}
	s.mu.Lock()
	s.projects[pathEscape(path)] = p
	s.mu.Unlock()
	return p
}

// pathEscape mirrors url.PathEscape without importing net/url twice in this
// file's flow of thought - it must match exactly what the client sends.
func pathEscape(path string) string {
	return strings.ReplaceAll(path, "/", "%2F")
}

func (s *gitlabServer) requestCountOf(method, uriContains string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.method == method && strings.Contains(r.uri, uriContains) {
			n++
		}
	}
	return n
}

func (s *gitlabServer) lastRequestBody(method, uriContains string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		r := s.requests[i]
		if r.method == method && strings.Contains(r.uri, uriContains) {
			return r.body
		}
	}
	return nil
}

func (s *gitlabServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v4/projects" && r.Method == http.MethodGet {
		// Two per page, X-Next-Page until the last.
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		s.mu.Lock()
		var all []map[string]any
		for _, p := range s.projects {
			all = append(all, map[string]any{"path_with_namespace": p.pathWithNamespace})
		}
		s.mu.Unlock()
		sort.Slice(all, func(i, j int) bool {
			return all[i]["path_with_namespace"].(string) < all[j]["path_with_namespace"].(string)
		})
		start, end := (page-1)*2, min(page*2, len(all))
		if start > len(all) {
			start = len(all)
		}
		if end < len(all) {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		}
		writeJSON(w, http.StatusOK, all[start:end])
		return
	}
	// A GET built by http.NewRequestWithContext with a nil body leaves
	// r.Body nil, unlike a request read off the wire - guard against that
	// rather than assume every handler only ever sees POST/PUT.
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, bodyLimit))
	}

	s.mu.Lock()
	s.requests = append(s.requests, requestLog{method: r.Method, uri: r.URL.RequestURI(), body: body})
	s.mu.Unlock()

	if s.token != "" && r.Header.Get("PRIVATE-TOKEN") != s.token {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// /user: the account behind the token. The bot is user 322, as on
	// the estate; issues carry their author.
	if r.URL.Path == "/api/v4/user" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"id": 322, "username": "renovate-bot"})
		return
	}

	const prefix = "/api/v4/projects/"
	// EscapedPath, not Path: the whole point of PathEscape is that GitLab
	// sees "group%2Fproj" as ONE segment.
	path := r.URL.EscapedPath()
	if !strings.HasPrefix(path, prefix) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(path, prefix)
	projectID, remainder, _ := strings.Cut(rest, "/")

	s.mu.Lock()
	proj, ok := s.projects[projectID]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch {
	case remainder == "":
		s.serveProject(w, proj)
	case remainder == "merge_requests":
		s.serveMergeRequests(w, r, proj, body)
	case strings.HasPrefix(remainder, "merge_requests/"):
		s.serveOneMergeRequest(w, r, proj, strings.TrimPrefix(remainder, "merge_requests/"), body)
	case remainder == "issues" || strings.HasPrefix(remainder, "issues/"):
		s.serveIssues(w, r, proj, strings.TrimPrefix(strings.TrimPrefix(remainder, "issues"), "/"), body)
	case strings.HasPrefix(remainder, "repository/commits/"):
		s.serveSignature(w, proj, strings.TrimPrefix(remainder, "repository/commits/"))
	case strings.HasPrefix(remainder, "repository/files/"):
		// files/<escaped path>/raw?ref=
		rest := strings.TrimPrefix(remainder, "repository/files/")
		rest = strings.TrimSuffix(rest, "/raw")
		if path, err := url.PathUnescape(rest); err == nil {
			if body, ok := proj.files[path+"@"+r.URL.Query().Get("ref")]; ok {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(body))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *gitlabServer) serveProject(w http.ResponseWriter, proj *fakeProject) {
	writeJSON(w, http.StatusOK, map[string]any{
		"path_with_namespace": proj.pathWithNamespace,
		"default_branch":      proj.defaultBranch,
	})
}

func (s *gitlabServer) serveMergeRequests(w http.ResponseWriter, r *http.Request, proj *fakeProject, body []byte) {
	switch r.Method {
	case http.MethodGet:
		s.listMergeRequests(w, r, proj)
	case http.MethodPost:
		s.createMergeRequest(w, proj, body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *gitlabServer) listMergeRequests(w http.ResponseWriter, r *http.Request, proj *fakeProject) {
	q := r.URL.Query()
	branch := q.Get("source_branch")
	state := q.Get("state")
	orderBy := q.Get("order_by")

	s.mu.Lock()
	all := append([]*fakeMR(nil), proj.mrs...)
	s.mu.Unlock()

	var filtered []*fakeMR
	for _, m := range all {
		if branch != "" && m.sourceBranch != branch {
			continue
		}
		if state != "" && m.state != state {
			continue
		}
		if after := q.Get("updated_after"); after != "" {
			if t, err := time.Parse(time.RFC3339, after); err == nil && m.updatedAt.Before(t) {
				continue
			}
		}
		filtered = append(filtered, m)
	}
	if orderBy == "created_at" {
		// Newest first; the fixtures never tie.
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	start := (page - 1) * testPageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := min(start+testPageSize, len(filtered))

	if end < len(filtered) {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	}
	var out []mrJSON
	for _, m := range filtered[start:end] {
		out = append(out, m.toJSON())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *gitlabServer) createMergeRequest(w http.ResponseWriter, proj *fakeProject, body []byte) {
	var payload struct {
		SourceBranch       string `json:"source_branch"`
		TargetBranch       string `json:"target_branch"`
		Title              string `json:"title"`
		Description        string `json:"description"`
		Labels             string `json:"labels"`
		RemoveSourceBranch bool   `json:"remove_source_branch"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	proj.nextIID++
	iid := proj.nextIID
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(iid) * time.Hour)
	mr := &fakeMR{
		iid: iid, state: "opened", sourceBranch: payload.SourceBranch, targetBranch: payload.TargetBranch,
		title: payload.Title, description: payload.Description, labels: splitLabels(payload.Labels),
		sha: fmt.Sprintf("%040d", iid), webURL: fmt.Sprintf("https://git.example.org/x/-/merge_requests/%d", iid),
		createdAt: now, updatedAt: now,
	}
	proj.mrs = append(proj.mrs, mr)
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, mr.toJSON())
}

func (s *gitlabServer) serveOneMergeRequest(w http.ResponseWriter, r *http.Request, proj *fakeProject, rest string, body []byte) {
	iidStr, sub, hasSub := strings.Cut(rest, "/")
	iid, _ := strconv.Atoi(iidStr)

	s.mu.Lock()
	var mr *fakeMR
	for _, m := range proj.mrs {
		if m.iid == iid {
			mr = m
			break
		}
	}
	s.mu.Unlock()
	if mr == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if !hasSub {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, mr.toJSON())
		case http.MethodPut:
			s.updateMergeRequest(w, mr, body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if sub == "merge" && r.Method == http.MethodPut {
		s.mergeMergeRequest(w, mr)
		return
	}
	if sub == "cancel_merge_when_pipeline_succeeds" && r.Method == http.MethodPost {
		s.mu.Lock()
		mr.automerge = false
		out := mr.toJSON()
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, out)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (s *gitlabServer) updateMergeRequest(w http.ResponseWriter, mr *fakeMR, body []byte) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if raw, ok := payload["title"]; ok {
		_ = json.Unmarshal(raw, &mr.title)
	}
	if raw, ok := payload["description"]; ok {
		_ = json.Unmarshal(raw, &mr.description)
	}
	if raw, ok := payload["labels"]; ok {
		var labels string
		_ = json.Unmarshal(raw, &labels)
		mr.labels = splitLabels(labels)
	}
	if raw, ok := payload["state_event"]; ok {
		var ev string
		_ = json.Unmarshal(raw, &ev)
		if ev == "close" {
			mr.state = "closed"
		}
	}
	mr.updatedAt = mr.updatedAt.Add(time.Minute)
	out := mr.toJSON()
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, out)
}

func (s *gitlabServer) mergeMergeRequest(w http.ResponseWriter, mr *fakeMR) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mr.staleHead {
		// GitLab answers 409 when the sha sent is not the branch's head -
		// which the request's own record is, right after a rebase push.
		writeJSON(w, http.StatusConflict, map[string]string{"message": "SHA does not match HEAD of source branch: " + mr.sha})
		return
	}
	if mr.mayNotMerge || s.mayNotMerge {
		// GitLab answers 401 when the user may not accept the request - a
		// Developer on a protected branch (measured: nozzleops/platform).
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "401 Unauthorized"})
		return
	}
	if !mr.canMerge {
		// GitLab answers 405 when the pipeline has not started yet.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mr.automerge = true
	mr.updatedAt = mr.updatedAt.Add(time.Minute)
	writeJSON(w, http.StatusOK, mr.toJSON())
}

func (s *gitlabServer) serveSignature(w http.ResponseWriter, proj *fakeProject, rest string) {
	sha, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "signature" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.mu.Lock()
	status, ok := proj.signatures[sha]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verification_status": status})
}

func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// newFixture wires a Platform to a fresh gitlabServer through the refusing
// transport, exactly as datasource/gitlabds's test does.
func newFixture(t *testing.T, token string) (*Platform, *gitlabServer, *harness.RefusingTransport) {
	t.Helper()
	srv := newGitlabServer(token)
	rt := harness.NewRefusingTransport(t).Handle("git.example.org", srv)
	tok := Token{}
	if token != "" {
		tok = Token{Value: token, Header: "PRIVATE-TOKEN"}
	}
	return New("https://git.example.org", rt, tok), srv, rt
}

func TestProjectResolvesAndNamesBothReadingsOn404(t *testing.T) {
	pf, srv, rt := newFixture(t, "")
	srv.addProject("group/proj", "main")

	proj, err := pf.Project(context.Background(), "group/proj")
	if err != nil {
		t.Fatal(err)
	}
	if proj.Path != "group/proj" || proj.DefaultBranch != "main" {
		t.Errorf("got %+v", proj)
	}

	_, err = pf.Project(context.Background(), "group/nosuch")
	if err == nil {
		t.Fatal("a missing project resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "cannot see it") {
		t.Errorf("the 404 error must name both readings: %q", msg)
	}
	if n := rt.Count("git.example.org"); n != 2 {
		t.Errorf("made %d requests, want 2", n)
	}
}

func TestFindMergeRequestReturnsOnlyTheOpenedOne(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")
	proj.mrs = []*fakeMR{
		{iid: 1, state: "closed", sourceBranch: "pinup/x", createdAt: fixedTime(1), updatedAt: fixedTime(1)},
		{iid: 2, state: "merged", sourceBranch: "pinup/x", createdAt: fixedTime(2), updatedAt: fixedTime(2)},
		{iid: 3, state: "opened", sourceBranch: "pinup/x", createdAt: fixedTime(3), updatedAt: fixedTime(3)},
	}
	proj.nextIID = 3

	mr, ok, err := pf.FindMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, "pinup/x")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || mr.IID != 3 || mr.State != "opened" {
		t.Errorf("got mr=%+v ok=%v, want the opened one (iid 3)", mr, ok)
	}
}

func TestFindMergeRequestNoneOpened(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")
	proj.mrs = []*fakeMR{
		{iid: 1, state: "closed", sourceBranch: "pinup/x", createdAt: fixedTime(1), updatedAt: fixedTime(1)},
		{iid: 2, state: "merged", sourceBranch: "pinup/x", createdAt: fixedTime(2), updatedAt: fixedTime(2)},
	}
	proj.nextIID = 2

	_, ok, err := pf.FindMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, "pinup/x")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("no merge request is opened; ok should be false")
	}
}

func TestHistoryPaginatesNewestFirst(t *testing.T) {
	pf, srv, rt := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")
	proj.mrs = []*fakeMR{
		{iid: 1, state: "closed", sourceBranch: "pinup/x", createdAt: fixedTime(1), updatedAt: fixedTime(1)},
		{iid: 2, state: "closed", sourceBranch: "pinup/x", createdAt: fixedTime(2), updatedAt: fixedTime(2)},
		{iid: 3, state: "opened", sourceBranch: "pinup/x", createdAt: fixedTime(3), updatedAt: fixedTime(3)},
	}
	proj.nextIID = 3

	hist, err := pf.History(context.Background(), publish.Project{Path: "group/proj"}, "pinup/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("got %d merge requests, want 3", len(hist))
	}
	if hist[0].IID != 3 || hist[1].IID != 2 || hist[2].IID != 1 {
		t.Errorf("not newest-first: %v, %v, %v", hist[0].IID, hist[1].IID, hist[2].IID)
	}
	// Three items at a forced page size of two is two requests.
	if n := rt.Count("git.example.org"); n != 2 {
		t.Errorf("made %d requests, want 2 (pagination not exercised)", n)
	}
}

// A bot that may not merge in a project still opens the request; the
// refusal travels with it for the run to report, and is not an error that
// would make the run believe the request does not exist (measured on
// nozzleops/platform, 2026-09-14: eight requests created, eight "failed").
func TestAutomergeRefusedByPermissionIsReportedNotFailed(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")
	srv.mu.Lock()
	srv.mayNotMerge = true
	srv.mu.Unlock()
	req := publish.Request{SourceBranch: "renovate/x", TargetBranch: "main", Title: "bump x", Automerge: true}
	mr, err := pf.CreateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, req)
	if err != nil {
		t.Fatalf("the request was created; the refusal is not an error: %v", err)
	}
	if mr.IID == 0 || mr.Automerge || !strings.Contains(mr.AutomergeRefused, "may not merge") {
		t.Errorf("mr = %+v", mr)
	}
	if len(proj.mrs) != 1 {
		t.Errorf("%d requests exist, want the one", len(proj.mrs))
	}
	mr2, changed, err := pf.UpdateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, mr.IID, req)
	if err != nil || mr2.Automerge || slices.Contains(changed, "automerge") || !strings.Contains(mr2.AutomergeRefused, "may not merge") {
		t.Errorf("update: err=%v mr=%+v changed=%v", err, mr2, changed)
	}
}

func TestCreateWithAutomergeThenUpdateSetsItOnceThePipelineIsReady(t *testing.T) {
	pf, srv, rt := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")

	req := publish.Request{
		SourceBranch: "pinup/branch-x", TargetBranch: "main",
		Title: "bump x", Description: "d", Labels: []string{"dependencies"},
		Automerge: true, RemoveSourceBranch: true,
	}

	mr, err := pf.CreateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, req)
	if err != nil {
		t.Fatal(err)
	}
	if mr.Automerge {
		t.Error("the pipeline has not started; Automerge must be false")
	}
	// POST create, then PUT merge (405): two requests.
	if n := rt.Count("git.example.org"); n != 2 {
		t.Errorf("made %d requests, want 2 (POST create + PUT merge)", n)
	}

	// A run that rebased: GitLab still records the old head, and the
	// merge call is refused with 409. Not yet, not an error.
	proj.mrs[0].staleHead = true
	mrS, changed, err := pf.UpdateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, mr.IID, req)
	if err != nil || mrS.Automerge || slices.Contains(changed, "automerge") {
		t.Errorf("stale head: err=%v automerge=%v changed=%v", err, mrS.Automerge, changed)
	}
	proj.mrs[0].staleHead = false
	// GET current, then PUT merge (409): four requests so far.
	if n := rt.Count("git.example.org"); n != 4 {
		t.Errorf("made %d requests, want 4", n)
	}

	// The next run: the pipeline is ready now.
	proj.mrs[0].canMerge = true

	mr2, changed, err := pf.UpdateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, mr.IID, req)
	if err != nil {
		t.Fatal(err)
	}
	if !mr2.Automerge {
		t.Error("the pipeline is ready now; Automerge must be true")
	}
	if !slices.Contains(changed, "automerge") {
		t.Errorf("changed = %v, want it to contain \"automerge\"", changed)
	}
	// GET current, then PUT merge (200): two more requests, six total.
	if n := rt.Count("git.example.org"); n != 6 {
		t.Errorf("made %d requests total, want 6", n)
	}

	// A rule stops allowing automerge: the flag is taken back through
	// cancel_merge_when_pipeline_succeeds - GET, then one POST.
	req.Automerge = false
	mr3, changed, err := pf.UpdateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, mr.IID, req)
	if err != nil {
		t.Fatal(err)
	}
	if mr3.Automerge || !slices.Contains(changed, "automerge") {
		t.Errorf("automerge must be cleared and reported: %+v %v", mr3, changed)
	}
	if n := rt.Count("git.example.org"); n != 8 {
		t.Errorf("made %d requests total, want 8", n)
	}
	if proj.mrs[0].automerge {
		t.Error("the fake still has automerge set")
	}
}

func TestUpdateChangesOnlyWhatDiffers(t *testing.T) {
	pf, srv, rt := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")
	proj.mrs = []*fakeMR{{
		iid: 1, state: "opened", sourceBranch: "pinup/x", targetBranch: "main",
		title: "t", description: "d", labels: []string{"dependencies"},
		createdAt: fixedTime(1), updatedAt: fixedTime(1),
	}}
	proj.nextIID = 1

	same := publish.Request{
		SourceBranch: "pinup/x", TargetBranch: "main",
		Title: "t", Description: "d", Labels: []string{"dependencies"},
	}
	_, changed, err := pf.UpdateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, 1, same)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Errorf("nothing differs; changed = %v", changed)
	}
	// Only the GET.
	if n := srv.requestCountOf(http.MethodPut, "merge_requests/1"); n != 0 {
		t.Errorf("a PUT was sent when nothing differed: %d", n)
	}
	if n := rt.Count("git.example.org"); n != 1 {
		t.Errorf("made %d requests, want 1 (GET only)", n)
	}

	oneLabel := same
	oneLabel.Labels = []string{"security"}
	_, changed, err = pf.UpdateMergeRequest(context.Background(), publish.Project{Path: "group/proj"}, 1, oneLabel)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "labels" {
		t.Errorf("changed = %v, want exactly [\"labels\"]", changed)
	}
	if n := srv.requestCountOf(http.MethodPut, "/merge_requests/1"); n != 1 {
		t.Errorf("got %d PUTs to the merge request, want exactly 1", n)
	}
	body := srv.lastRequestBody(http.MethodPut, "/merge_requests/1")
	if !strings.Contains(string(body), `"labels"`) {
		t.Errorf("PUT body missing labels: %s", body)
	}
	if strings.Contains(string(body), `"title"`) || strings.Contains(string(body), `"description"`) {
		t.Errorf("PUT body carried fields that did not differ: %s", body)
	}
}

func TestCommitVerification(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")
	proj.signatures["abc123"] = "verified"

	status, err := pf.CommitVerification(context.Background(), publish.Project{Path: "group/proj"}, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if status != "verified" {
		t.Errorf("got %q, want verified", status)
	}

	status, err = pf.CommitVerification(context.Background(), publish.Project{Path: "group/proj"}, "nosuch")
	if err != nil {
		t.Fatal(err)
	}
	if status != "unsigned" {
		t.Errorf("got %q, want unsigned for a commit GitLab has no signature record for", status)
	}
}

func TestTokenNeverAppearsInAnErrorMessage(t *testing.T) {
	_, srv, _ := newFixture(t, "correct-token")
	srv.addProject("group/proj", "main")

	// A different Platform, pointed at the same fake, with the wrong token.
	rt := harness.NewRefusingTransport(t).Handle("git.example.org", srv)
	bad := New("https://git.example.org", rt, Token{Value: "wrong-token-xyz", Header: "PRIVATE-TOKEN"})

	_, err := bad.Project(context.Background(), "group/proj")
	if err == nil {
		t.Fatal("the wrong token should have been refused")
	}
	if strings.Contains(err.Error(), "wrong-token-xyz") || strings.Contains(err.Error(), "correct-token") {
		t.Errorf("the error names a token: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "group/proj") {
		t.Errorf("the error does not name the project: %q", err.Error())
	}
}

func TestForbiddenHostIsNeverReached(t *testing.T) {
	rt := harness.NewRefusingTransport(t)
	rt.Forbid("developer.mend.io")
	rt.MustNotHaveBeenCalled("developer.mend.io")
}

func fixedTime(n int) time.Time {
	return time.Date(2026, 1, 1+n, 0, 0, 0, 0, time.UTC)
}

func TestReadFileThroughTheRawEndpoint(t *testing.T) {
	pf, srv, rt := newFixture(t, "")
	proj := srv.addProject("devops/renovate-runner", "main")
	proj.files = map[string]string{"default.json@": `{"labels":["renovate"]}`, "release-fast.json@v2": `{"schedule":["at any time"]}`}
	body, err := pf.ReadFile(context.Background(), "devops/renovate-runner", "default.json", "")
	if err != nil || string(body) != `{"labels":["renovate"]}` {
		t.Fatalf("default.json: %q %v", body, err)
	}
	body, err = pf.ReadFile(context.Background(), "devops/renovate-runner", "release-fast.json", "v2")
	if err != nil || !strings.Contains(string(body), "at any time") {
		t.Fatalf("ref: %q %v", body, err)
	}
	_, err = pf.ReadFile(context.Background(), "devops/renovate-runner", "nonesuch.json", "")
	if err == nil || !strings.Contains(err.Error(), "nonesuch.json") {
		t.Errorf("a missing file must be named: %v", err)
	}
	if n := rt.Count("git.example.org"); n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
}

func TestListProjectsPaginates(t *testing.T) {
	pf, srv, rt := newFixture(t, "")
	for _, p := range []string{"devops/images/b", "devops/images/a", "pinup/pinup"} {
		srv.addProject(p, "main")
	}
	got, err := pf.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "devops/images/a,devops/images/b,pinup/pinup" {
		t.Errorf("got %v", got)
	}
	if n := rt.Count("git.example.org"); n != 2 {
		t.Errorf("three projects at two per page are two requests, got %d", n)
	}
}

// serveIssues: GET lists open issues (search is matched on the title),
// POST creates, PUT <iid> updates. Speaks enough of the protocol for
// UpsertIssue to be measured against.
func (s *gitlabServer) serveIssues(w http.ResponseWriter, r *http.Request, proj *fakeProject, rest string, body []byte) {
	encode := func(is *fakeIssue) map[string]any {
		return map[string]any{"iid": is.iid, "title": is.title, "description": is.desc, "labels": is.labels, "state": is.state,
			"web_url": fmt.Sprintf("https://git.example.org/%s/-/issues/%d", proj.pathWithNamespace, is.iid)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && rest == "":
		search := r.URL.Query().Get("search")
		author := r.URL.Query().Get("author_id")
		var out []map[string]any
		for _, is := range proj.issues {
			if author != "" && fmt.Sprint(is.authorID) != author {
				continue
			}
			if is.state == "opened" && strings.Contains(is.title, search) {
				out = append(out, encode(is))
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && rest == "":
		var in struct{ Title, Description, Labels string }
		_ = json.Unmarshal(body, &in)
		proj.nextIID++
		is := &fakeIssue{iid: proj.nextIID, title: in.Title, desc: in.Description, state: "opened", authorID: 322}
		if in.Labels != "" {
			is.labels = strings.Split(in.Labels, ",")
		}
		proj.issues = append(proj.issues, is)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(encode(is))
	case r.Method == http.MethodPut && rest != "":
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		for _, is := range proj.issues {
			if fmt.Sprint(is.iid) != rest {
				continue
			}
			if d, ok := in["description"]; ok {
				is.desc = d
			}
			if l, ok := in["labels"]; ok {
				is.labels = strings.Split(l, ",")
			}
			_ = json.NewEncoder(w).Encode(encode(is))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// An issue is created once, updated only when its text or labels differ,
// and found by its exact title - a search hit with a longer title is not it.
func TestUpsertIssueCreatesThenUpdatesOnlyOnChange(t *testing.T) {
	p, srv, _ := newFixture(t, "tok")
	proj := srv.addProject("pinup/runner", "main")
	proj.issues = append(proj.issues, &fakeIssue{iid: 1, title: "Rolling majors (old)", desc: "x", state: "opened"})
	proj.nextIID = 1
	ctx := context.Background()
	pr := publish.Project{Path: "pinup/runner", DefaultBranch: "main"}

	is, changed, err := p.UpsertIssue(ctx, pr, "Rolling majors", "body v1", []string{"pinup"})
	if err != nil || !changed || is.IID != 2 {
		t.Fatalf("create: %+v %v %v", is, changed, err)
	}
	if n := srv.requestCountOf(http.MethodPost, "issues"); n != 1 {
		t.Errorf("POSTs = %d, want 1", n)
	}
	is, changed, err = p.UpsertIssue(ctx, pr, "Rolling majors", "body v1\n", []string{"pinup"})
	if err != nil || changed || is.IID != 2 {
		t.Errorf("unchanged (trailing newline): %+v %v %v", is, changed, err)
	}
	if n := srv.requestCountOf(http.MethodPut, "issues/2"); n != 0 {
		t.Errorf("an unchanged issue was written: %d PUTs", n)
	}
	is, changed, err = p.UpsertIssue(ctx, pr, "Rolling majors", "body v2", []string{"pinup"})
	if err != nil || !changed || is.IID != 2 {
		t.Errorf("update: %+v %v %v", is, changed, err)
	}
	if n := srv.requestCountOf(http.MethodPost, "issues"); n != 1 {
		t.Errorf("a second issue was opened: %d POSTs", n)
	}
	if got := proj.issues[1].desc; got != "body v2" {
		t.Errorf("description = %q", got)
	}
}

// MergedMergeRequests asks GitLab for merged requests updated since the
// cut-off and keeps those under the prefix whose merge time is inside it.
func TestMergedMergeRequestsSinceACutoff(t *testing.T) {
	pf, srv, _ := newFixture(t, "")
	proj := srv.addProject("group/proj", "main")
	proj.mrs = []*fakeMR{
		{iid: 1, state: "merged", sourceBranch: "renovate/old", createdAt: fixedTime(1), updatedAt: fixedTime(1), mergedAt: fixedTime(1)},
		{iid: 2, state: "merged", sourceBranch: "renovate/lock-file-maintenance", createdAt: fixedTime(3), updatedAt: fixedTime(4), mergedAt: fixedTime(4)},
		{iid: 3, state: "merged", sourceBranch: "pinup/other", createdAt: fixedTime(4), updatedAt: fixedTime(4), mergedAt: fixedTime(4)},
		{iid: 4, state: "opened", sourceBranch: "renovate/open", createdAt: fixedTime(4), updatedAt: fixedTime(4)},
	}
	proj.nextIID = 4
	got, err := pf.MergedMergeRequests(context.Background(), publish.Project{Path: "group/proj"}, "renovate/", fixedTime(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IID != 2 || got[0].State != "merged" {
		t.Errorf("got %+v, want the one merged renovate/ request since the cut-off", got)
	}
}

// A redirect off the instance is refused, not followed: the token would
// travel with it. Measured on the estate: artifacts and packages redirect
// to object storage.
func TestARedirectOffTheInstanceIsRefused(t *testing.T) {
	var reached atomic.Bool
	elsewhere := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	instance := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://storage.example.net/blob", http.StatusFound)
	})
	rt := harness.NewRefusingTransport(t).Handle("git.example.org", instance).Handle("storage.example.net", elsewhere)
	pf := New("https://git.example.org", rt, Token{Value: "glpat-x", Header: "PRIVATE-TOKEN"})
	_, err := pf.Project(context.Background(), "group/proj")
	if err == nil || !strings.Contains(err.Error(), "refusing to follow a redirect off the instance") {
		t.Errorf("err = %v", err)
	}
	if reached.Load() {
		t.Error("the other host was reached")
	}
}

// The dashboard is the bot's own issue. An issue somebody else opened
// under the same title, boxes ticked, is not read and not updated: the
// bot writes its own beside it.
func TestTheDashboardIsTheBotsOwnIssue(t *testing.T) {
	pf, srv, _ := newFixture(t, "glpat-x")
	proj := srv.addProject("group/proj", "main")
	proj.nextIID++
	proj.issues = append(proj.issues, &fakeIssue{iid: proj.nextIID, title: "pinup Dashboard", desc: "- [x] <!-- approve-all-pending-prs -->", state: "opened", authorID: 7})
	if _, body, ok, err := pf.ReadIssue(context.Background(), publish.Project{Path: "group/proj"}, "pinup Dashboard"); err != nil || ok || body != "" {
		t.Errorf("a stranger's issue was read as the dashboard: ok=%v body=%q err=%v", ok, body, err)
	}
	is, created, err := pf.UpsertIssue(context.Background(), publish.Project{Path: "group/proj"}, "pinup Dashboard", "the bot's own", nil)
	if err != nil || !created || is.IID == 1 {
		t.Errorf("upsert: %+v created=%v err=%v", is, created, err)
	}
	if _, body, ok, _ := pf.ReadIssue(context.Background(), publish.Project{Path: "group/proj"}, "pinup Dashboard"); !ok || body != "the bot's own" {
		t.Errorf("the bot's own issue is not read back: ok=%v body=%q", ok, body)
	}
}

func TestCloseMergeRequestRetitlesAndCloses(t *testing.T) {
	p, srv, _ := newFixture(t, "glpat-x")
	proj := srv.addProject("group/app", "main")
	proj.mrs = append(proj.mrs, &fakeMR{iid: 7, state: "opened", sourceBranch: "renovate/x", title: "chore(deps): x"})
	if err := p.CloseMergeRequest(context.Background(), publish.Project{Path: "group/app"}, 7, "chore(deps): x - autoclosed"); err != nil {
		t.Fatal(err)
	}
	if proj.mrs[0].state != "closed" || proj.mrs[0].title != "chore(deps): x - autoclosed" {
		t.Errorf("mr = %+v", proj.mrs[0])
	}
	if _, ok, _ := p.FindMergeRequest(context.Background(), publish.Project{Path: "group/app"}, "renovate/x"); ok {
		t.Error("a closed request is not found as open")
	}
}
