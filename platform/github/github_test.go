// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/publish"
)

// testPageSize forces pagination through the Link header on every list.
const testPageSize = 2

type fakePull struct {
	number    int
	state     string // open, closed
	head, sha string
	base      string
	title     string
	body      string
	labels    []string
	autoMerge bool
	// notYet: auto-merge cannot be armed yet (nothing to wait for).
	notYet    bool
	createdAt time.Time
	updatedAt time.Time
	mergedAt  time.Time
}

func (m *fakePull) toJSON(owner, repo string) map[string]any {
	labels := []map[string]any{}
	for _, l := range m.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	var auto any
	if m.autoMerge {
		auto = map[string]any{"merge_method": "merge"}
	}
	var merged any
	if !m.mergedAt.IsZero() {
		merged = m.mergedAt.Format(time.RFC3339)
	}
	return map[string]any{
		"number": m.number, "node_id": fmt.Sprintf("PR_%d", m.number), "state": m.state, "title": m.title, "body": m.body,
		"labels": labels, "html_url": fmt.Sprintf("https://github.example/%s/%s/pull/%d", owner, repo, m.number),
		"auto_merge": auto,
		"head":       map[string]any{"ref": m.head, "sha": m.sha, "label": owner + ":" + m.head},
		"base":       map[string]any{"ref": m.base},
		"created_at": m.createdAt.Format(time.RFC3339), "updated_at": m.updatedAt.Format(time.RFC3339), "merged_at": merged,
	}
}

type fakeIssue struct {
	number int
	title  string
	body   string
	labels []string
	isPull bool
	author string
}

type fakeRepo struct {
	owner, name    string
	defaultBranch  string
	archived       bool
	push           bool
	allowAutoMerge bool
	pulls          []*fakePull
	issues         []*fakeIssue
	files          map[string]string // "ref\x00path" -> content
	verification   map[string]struct {
		verified bool
		reason   string
	}
	nextNumber int
}

type githubServer struct {
	mu       sync.Mutex
	token    string
	login    string
	repos    map[string]*fakeRepo // "owner/name"
	requests []string
}

func newServer(token string) *githubServer {
	return &githubServer{token: token, login: "pinup-bot", repos: map[string]*fakeRepo{}}
}

func (s *githubServer) addRepo(full, defaultBranch string) *fakeRepo {
	owner, name, _ := strings.Cut(full, "/")
	r := &fakeRepo{owner: owner, name: name, defaultBranch: defaultBranch, push: true, allowAutoMerge: true,
		files: map[string]string{}, verification: map[string]struct {
			verified bool
			reason   string
		}{}, nextNumber: 1}
	s.repos[full] = r
	return r
}

func (s *githubServer) count(method, contains string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if strings.HasPrefix(r, method+" ") && strings.Contains(r, contains) {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// paginate serves items[page] with a Link rel="next" while there is more.
func paginate(w http.ResponseWriter, r *http.Request, items []any) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := (page - 1) * testPageSize
	end := min(start+testPageSize, len(items))
	if start > len(items) {
		start = len(items)
	}
	if end < len(items) {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(page+1))
		next := "https://" + r.Host + r.URL.Path + "?" + q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next", <%s>; rel="last"`, next, next))
	}
	writeJSON(w, 200, items[start:end])
}

func (s *githubServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	s.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+s.token {
		writeJSON(w, 401, map[string]any{"message": "Bad credentials"})
		return
	}
	if r.Header.Get("X-GitHub-Api-Version") == "" {
		writeJSON(w, 400, map[string]any{"message": "the API version header is missing"})
		return
	}
	switch {
	case r.URL.Path == "/user":
		writeJSON(w, 200, map[string]any{"login": s.login})
	case r.URL.Path == "/user/repos":
		var items []any
		keys := make([]string, 0, len(s.repos))
		for k := range s.repos {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			rp := s.repos[k]
			items = append(items, map[string]any{"full_name": k, "archived": rp.archived, "permissions": map[string]any{"push": rp.push}})
		}
		paginate(w, r, items)
	case r.URL.Path == "/graphql":
		s.graphql(w, body)
	case strings.HasPrefix(r.URL.Path, "/repos/"):
		rest := strings.TrimPrefix(r.URL.Path, "/repos/")
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 2 {
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		rp, ok := s.repos[parts[0]+"/"+parts[1]]
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		sub := ""
		if len(parts) == 3 {
			sub = parts[2]
		}
		s.serveRepo(w, r, rp, sub, body)
	default:
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	}
}

func (s *githubServer) serveRepo(w http.ResponseWriter, r *http.Request, rp *fakeRepo, sub string, body []byte) {
	owner, name := rp.owner, rp.name
	switch {
	case sub == "":
		writeJSON(w, 200, map[string]any{"full_name": owner + "/" + name, "default_branch": rp.defaultBranch})
	case sub == "pulls" && r.Method == http.MethodGet:
		q := r.URL.Query()
		var pulls []*fakePull
		for _, m := range rp.pulls {
			if st := q.Get("state"); st != "" && st != "all" && m.state != st {
				continue
			}
			if h := q.Get("head"); h != "" && h != owner+":"+m.head {
				continue
			}
			pulls = append(pulls, m)
		}
		switch q.Get("sort") {
		case "created":
			slices.SortFunc(pulls, func(a, b *fakePull) int { return b.createdAt.Compare(a.createdAt) })
		case "updated":
			slices.SortFunc(pulls, func(a, b *fakePull) int { return b.updatedAt.Compare(a.updatedAt) })
		}
		var items []any
		for _, m := range pulls {
			items = append(items, m.toJSON(owner, name))
		}
		paginate(w, r, items)
	case sub == "pulls" && r.Method == http.MethodPost:
		var in struct{ Title, Head, Base, Body string }
		_ = json.Unmarshal(body, &in)
		if in.Head == "" || in.Base == "" || in.Title == "" {
			writeJSON(w, 422, map[string]any{"message": "Validation Failed", "errors": []map[string]any{{"message": "head, base and title are required"}}})
			return
		}
		m := &fakePull{number: rp.nextNumber, state: "open", head: in.Head, base: in.Base, title: in.Title, body: in.Body, sha: "sha-" + in.Head,
			createdAt: fixedTime(rp.nextNumber), updatedAt: fixedTime(rp.nextNumber), notYet: true}
		rp.nextNumber++
		rp.pulls = append(rp.pulls, m)
		writeJSON(w, 201, m.toJSON(owner, name))
	case strings.HasPrefix(sub, "pulls/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(sub, "pulls/"))
		for _, m := range rp.pulls {
			if m.number != n {
				continue
			}
			if r.Method == http.MethodPatch {
				var in map[string]any
				_ = json.Unmarshal(body, &in)
				if t, ok := in["title"].(string); ok {
					m.title = t
				}
				if b, ok := in["body"].(string); ok {
					m.body = b
				}
				if st, ok := in["state"].(string); ok {
					m.state = st
				}
			}
			writeJSON(w, 200, m.toJSON(owner, name))
			return
		}
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	case sub == "issues" && r.Method == http.MethodGet:
		q := r.URL.Query()
		var items []any
		for _, is := range rp.issues {
			if c := q.Get("creator"); c != "" && is.author != c {
				continue
			}
			items = append(items, issueBody(owner, name, is))
		}
		paginate(w, r, items)
	case sub == "issues" && r.Method == http.MethodPost:
		var in struct {
			Title, Body string
			Labels      []string
		}
		_ = json.Unmarshal(body, &in)
		is := &fakeIssue{number: rp.nextNumber, title: in.Title, body: in.Body, labels: in.Labels, author: s.login}
		rp.nextNumber++
		rp.issues = append(rp.issues, is)
		writeJSON(w, 201, issueBody(owner, name, is))
	case strings.HasPrefix(sub, "issues/") && strings.HasSuffix(sub, "/labels") && r.Method == http.MethodPut:
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(sub, "issues/"), "/labels"))
		var in struct{ Labels []string }
		_ = json.Unmarshal(body, &in)
		out := []map[string]any{}
		for _, l := range in.Labels {
			out = append(out, map[string]any{"name": l})
		}
		for _, m := range rp.pulls {
			if m.number == n {
				m.labels = in.Labels
			}
		}
		for _, is := range rp.issues {
			if is.number == n {
				is.labels = in.Labels
			}
		}
		writeJSON(w, 200, out)
	case strings.HasPrefix(sub, "issues/") && r.Method == http.MethodPatch:
		n, _ := strconv.Atoi(strings.TrimPrefix(sub, "issues/"))
		for _, is := range rp.issues {
			if is.number != n {
				continue
			}
			var in struct {
				Body   *string
				Labels []string
			}
			_ = json.Unmarshal(body, &in)
			if in.Body != nil {
				is.body = *in.Body
			}
			if in.Labels != nil {
				is.labels = in.Labels
			}
			writeJSON(w, 200, issueBody(owner, name, is))
			return
		}
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	case strings.HasPrefix(sub, "commits/"):
		sha := strings.TrimPrefix(sub, "commits/")
		v, ok := rp.verification[sha]
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "No commit found for SHA: " + sha})
			return
		}
		writeJSON(w, 200, map[string]any{"sha": sha, "commit": map[string]any{"verification": map[string]any{"verified": v.verified, "reason": v.reason}}})
	case strings.HasPrefix(sub, "contents/"):
		path := strings.TrimPrefix(sub, "contents/")
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = rp.defaultBranch
		}
		content, ok := rp.files[ref+"\x00"+path]
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
			// Without the raw media type GitHub answers a JSON envelope
			// with base64 content; the client must ask for raw bytes.
			writeJSON(w, 200, map[string]any{"encoding": "base64", "content": "..."})
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github.raw")
		_, _ = w.Write([]byte(content))
	default:
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	}
}

func issueBody(owner, name string, is *fakeIssue) map[string]any {
	labels := []map[string]any{}
	for _, l := range is.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	out := map[string]any{"number": is.number, "title": is.title, "body": is.body, "labels": labels, "state": "open",
		"html_url": fmt.Sprintf("https://github.example/%s/%s/issues/%d", owner, name, is.number)}
	if is.isPull {
		out["pull_request"] = map[string]any{"url": "x"}
	}
	return out
}

// graphql serves the two auto-merge mutations by the pull request's node id.
func (s *githubServer) graphql(w http.ResponseWriter, body []byte) {
	var in struct {
		Query     string
		Variables struct{ ID string }
	}
	_ = json.Unmarshal(body, &in)
	n, _ := strconv.Atoi(strings.TrimPrefix(in.Variables.ID, "PR_"))
	for _, rp := range s.repos {
		for _, m := range rp.pulls {
			if m.number != n {
				continue
			}
			switch {
			case strings.Contains(in.Query, "disablePullRequestAutoMerge"):
				m.autoMerge = false
				writeJSON(w, 200, map[string]any{"data": map[string]any{"disablePullRequestAutoMerge": map[string]any{"clientMutationId": nil}}})
			case !rp.allowAutoMerge:
				writeJSON(w, 200, map[string]any{"data": nil, "errors": []map[string]any{{"type": "UNPROCESSABLE", "message": "Auto merge is not allowed for this repository"}}})
			case m.notYet:
				writeJSON(w, 200, map[string]any{"data": nil, "errors": []map[string]any{{"type": "UNPROCESSABLE", "message": "Pull request is not in the correct state to enable auto-merge"}}})
			default:
				m.autoMerge = true
				writeJSON(w, 200, map[string]any{"data": map[string]any{"enablePullRequestAutoMerge": map[string]any{"clientMutationId": nil}}})
			}
			return
		}
	}
	writeJSON(w, 200, map[string]any{"data": nil, "errors": []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a node"}}})
}

func fixedTime(n int) time.Time {
	return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Hour)
}

func newFixture(t *testing.T) (*Platform, *githubServer, *harness.RefusingTransport) {
	t.Helper()
	srv := newServer("ghp_secret")
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	rt.Handle("api.github.example", srv)
	rt.Forbid("developer.mend.io")
	rt.Forbid("api.github.com")
	// The enterprise shape: web host with /api/v3 - handled by mapping the
	// fake to the API host the platform computes.
	p := New("https://github.example", rt, "ghp_secret")
	p.api = "https://api.github.example"
	return p, srv, rt
}

func TestProjectResolvesAndNamesBothReadingsOn404(t *testing.T) {
	p, srv, _ := newFixture(t)
	srv.addRepo("acme/site", "main")
	proj, err := p.Project(context.Background(), "acme/site")
	if err != nil || proj.Path != "acme/site" || proj.DefaultBranch != "main" {
		t.Fatalf("project = %+v, %v", proj, err)
	}
	if _, err := p.Project(context.Background(), "acme/nope"); err == nil || !strings.Contains(err.Error(), "does not exist or the token cannot see it") {
		t.Errorf("404 must name both readings: %v", err)
	}
	if _, err := p.Project(context.Background(), "group/sub/name"); err == nil {
		t.Error("a three-segment path is not a GitHub repository")
	}
}

func TestFindMergeRequestAsksByOwnerAndHead(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	rp.pulls = append(rp.pulls,
		&fakePull{number: 1, state: "closed", head: "renovate/x", base: "main", title: "old", createdAt: fixedTime(1), updatedAt: fixedTime(1)},
		&fakePull{number: 2, state: "open", head: "renovate/x", base: "main", title: "current", sha: "abc", labels: []string{"pinup"}, createdAt: fixedTime(2), updatedAt: fixedTime(2)},
	)
	mr, ok, err := p.FindMergeRequest(context.Background(), publish.Project{Path: "acme/site"}, "renovate/x")
	if err != nil || !ok || mr.IID != 2 || mr.State != "opened" || mr.SHA != "abc" || mr.Labels[0] != "pinup" {
		t.Fatalf("find = %+v %v %v", mr, ok, err)
	}
	if srv.count("GET", "head=acme%3Arenovate%2Fx") != 1 {
		t.Errorf("the head filter must be owner:branch: %v", srv.requests)
	}
	if _, ok, _ := p.FindMergeRequest(context.Background(), publish.Project{Path: "acme/site"}, "renovate/none"); ok {
		t.Error("no open request, no find")
	}
}

func TestHistoryPaginatesNewestFirst(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	for i := 1; i <= 5; i++ {
		rp.pulls = append(rp.pulls, &fakePull{number: i, state: "closed", head: "renovate/x", base: "main", title: fmt.Sprint(i), createdAt: fixedTime(i), updatedAt: fixedTime(i)})
	}
	hist, err := p.History(context.Background(), publish.Project{Path: "acme/site"}, "renovate/x")
	if err != nil || len(hist) != 5 || hist[0].IID != 5 || hist[4].IID != 1 {
		t.Fatalf("history = %+v %v", hist, err)
	}
	if n := srv.count("GET", "/pulls?"); n != 3 {
		t.Errorf("five items at a page size of two are three pages, got %d requests", n)
	}
}

func TestCreateSetsLabelsAndArmsAutomergeOnceItCan(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	ctx := context.Background()
	mr, err := p.CreateMergeRequest(ctx, publish.Project{Path: "acme/site", DefaultBranch: "main"}, publish.Request{
		SourceBranch: "renovate/lodash", TargetBranch: "main", Title: "t", Description: "d", Labels: []string{"pinup", "deps"}, Automerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mr.IID != 1 || mr.State != "opened" || !sameLabelSet(mr.Labels, []string{"pinup", "deps"}) {
		t.Errorf("created = %+v", mr)
	}
	if mr.Automerge || mr.AutomergeRefused != "" {
		t.Errorf("not yet is neither armed nor refused: %+v", mr)
	}
	// The checks have started: the next run arms it.
	rp.pulls[0].notYet = false
	upd, changed, err := p.UpdateMergeRequest(ctx, publish.Project{Path: "acme/site"}, 1, publish.Request{
		SourceBranch: "renovate/lodash", TargetBranch: "main", Title: "t", Description: "d", Labels: []string{"pinup", "deps"}, Automerge: true,
	})
	if err != nil || !upd.Automerge || !slices.Equal(changed, []string{"automerge"}) {
		t.Fatalf("update = %+v %v %v", upd, changed, err)
	}
	// And a rule that stopped allowing it takes it back.
	upd, changed, err = p.UpdateMergeRequest(ctx, publish.Project{Path: "acme/site"}, 1, publish.Request{
		SourceBranch: "renovate/lodash", TargetBranch: "main", Title: "t", Description: "d", Labels: []string{"pinup", "deps"}, Automerge: false,
	})
	if err != nil || upd.Automerge || !slices.Equal(changed, []string{"automerge"}) {
		t.Fatalf("disarm = %+v %v %v", upd, changed, err)
	}
}

func TestAutomergeRefusedByTheRepositoryIsReportedNotFailed(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	rp.allowAutoMerge = false
	mr, err := p.CreateMergeRequest(context.Background(), publish.Project{Path: "acme/site"}, publish.Request{
		SourceBranch: "renovate/x", TargetBranch: "main", Title: "t", Automerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mr.Automerge || !strings.Contains(mr.AutomergeRefused, "not allowed") {
		t.Errorf("refusal must be reported beside the request: %+v", mr)
	}
}

func TestUpdateChangesOnlyWhatDiffers(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	rp.pulls = append(rp.pulls, &fakePull{number: 1, state: "open", head: "renovate/x", base: "main", title: "t", body: "d\n", labels: []string{"pinup"}, createdAt: fixedTime(1), updatedAt: fixedTime(1)})
	ctx := context.Background()
	_, changed, err := p.UpdateMergeRequest(ctx, publish.Project{Path: "acme/site"}, 1, publish.Request{Title: "t", Description: "d", Labels: []string{"pinup"}})
	if err != nil || len(changed) != 0 {
		t.Fatalf("nothing differs: changed %v, %v", changed, err)
	}
	if srv.count("PATCH", "/pulls/1") != 0 || srv.count("PUT", "/labels") != 0 {
		t.Errorf("nothing differs, nothing is written: %v", srv.requests)
	}
	_, changed, err = p.UpdateMergeRequest(ctx, publish.Project{Path: "acme/site"}, 1, publish.Request{Title: "t2", Description: "d", Labels: []string{"pinup", "security"}})
	if err != nil || !slices.Equal(changed, []string{"title", "labels"}) {
		t.Fatalf("changed = %v, %v", changed, err)
	}
}

func TestCommitVerification(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	rp.verification["good"] = struct {
		verified bool
		reason   string
	}{true, "valid"}
	rp.verification["bad"] = struct {
		verified bool
		reason   string
	}{false, "unknown_key"}
	rp.verification["none"] = struct {
		verified bool
		reason   string
	}{false, "unsigned"}
	for sha, want := range map[string]string{"good": "verified", "bad": "unknown_key", "none": "unsigned"} {
		got, err := p.CommitVerification(context.Background(), publish.Project{Path: "acme/site"}, sha)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", sha, got, err, want)
		}
	}
}

func TestReadFileAsksForRawBytes(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/runner", "main")
	rp.files["main\x00default.json"] = `{"extends": []}`
	rp.files["v2\x00presets/fast.json"] = `{}`
	got, err := p.ReadFile(context.Background(), "acme/runner", "default.json", "")
	if err != nil || string(got) != `{"extends": []}` {
		t.Fatalf("read = %q %v", got, err)
	}
	if got, err := p.ReadFile(context.Background(), "acme/runner", "presets/fast.json", "v2"); err != nil || string(got) != "{}" {
		t.Errorf("read at ref = %q %v", got, err)
	}
	if _, err := p.ReadFile(context.Background(), "acme/runner", "missing.json", ""); err == nil || !strings.Contains(err.Error(), "has no file") {
		t.Errorf("missing file: %v", err)
	}
}

func TestListProjectsPaginatesAndSkipsWhatItCannotPush(t *testing.T) {
	p, srv, _ := newFixture(t)
	for _, n := range []string{"acme/a", "acme/b", "acme/c", "acme/d", "acme/e"} {
		srv.addRepo(n, "main")
	}
	srv.repos["acme/b"].archived = true
	srv.repos["acme/d"].push = false
	got, err := p.ListProjects(context.Background())
	if err != nil || !slices.Equal(got, []string{"acme/a", "acme/c", "acme/e"}) {
		t.Fatalf("list = %v %v", got, err)
	}
	if n := srv.count("GET", "/user/repos"); n != 3 {
		t.Errorf("five repositories at a page size of two are three pages, got %d", n)
	}
}

func TestUpsertIssueIsTheBotsOwnAndSkipsPullRequests(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	// Someone else's issue with the title, and a pull request the issues
	// endpoint lists too: neither is the dashboard.
	rp.issues = append(rp.issues,
		&fakeIssue{number: 1, title: "Dependency Dashboard", body: "forged", author: "someone"},
		&fakeIssue{number: 2, title: "Dependency Dashboard", body: "a pull request", author: "pinup-bot", isPull: true},
	)
	rp.nextNumber = 3
	ctx := context.Background()
	is, changed, err := p.UpsertIssue(ctx, publish.Project{Path: "acme/site"}, "Dependency Dashboard", "body", []string{"pinup"})
	if err != nil || !changed || is.IID != 3 {
		t.Fatalf("upsert = %+v %v %v", is, changed, err)
	}
	_, changed, err = p.UpsertIssue(ctx, publish.Project{Path: "acme/site"}, "Dependency Dashboard", "body\n", []string{"pinup"})
	if err != nil || changed {
		t.Errorf("same body modulo whitespace, same labels: nothing to change (%v %v)", changed, err)
	}
	_, changed, err = p.UpsertIssue(ctx, publish.Project{Path: "acme/site"}, "Dependency Dashboard", "body2", []string{"pinup"})
	if err != nil || !changed {
		t.Errorf("a new body is a change (%v %v)", changed, err)
	}
	_, body, ok, err := p.ReadIssue(ctx, publish.Project{Path: "acme/site"}, "Dependency Dashboard")
	if err != nil || !ok || body != "body2" {
		t.Errorf("read = %q %v %v", body, ok, err)
	}
	if srv.count("GET", "creator=pinup-bot") == 0 {
		t.Error("issues must be asked for by the bot's own login")
	}
}

func TestMergedMergeRequestsSinceACutoffStopsPaging(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	for i := 1; i <= 6; i++ {
		m := &fakePull{number: i, state: "closed", head: "renovate/" + fmt.Sprint(i), base: "main", title: fmt.Sprint(i), createdAt: fixedTime(i), updatedAt: fixedTime(i), mergedAt: fixedTime(i)}
		if i == 5 {
			m.mergedAt = time.Time{} // closed without merging
		}
		if i == 4 {
			m.head = "feature/x"
		}
		rp.pulls = append(rp.pulls, m)
	}
	got, err := p.MergedMergeRequests(context.Background(), publish.Project{Path: "acme/site"}, "renovate/", fixedTime(3))
	if err != nil {
		t.Fatal(err)
	}
	var iids []int
	for _, m := range got {
		iids = append(iids, m.IID)
	}
	if !slices.Equal(iids, []int{6, 3}) {
		t.Errorf("merged since = %v, want [6 3] (5 not merged, 4 another prefix, 1-2 before)", iids)
	}
	// Six requests, two a page: the third page holds the first one updated
	// before the cutoff, where the walk stops - the hypothetical fourth
	// page is never asked for.
	if n := srv.count("GET", "/acme/site/pulls?"); n != 3 {
		t.Errorf("the walk stops at the first request updated before the cutoff: %d pages", n)
	}
}

func TestTokenNeverAppearsInAnErrorMessage(t *testing.T) {
	p, srv, rt := newFixture(t)
	srv.addRepo("acme/site", "main")
	p.token = "ghp_wrong"
	_, err := p.Project(context.Background(), "acme/site")
	if err == nil || strings.Contains(err.Error(), "ghp_") {
		t.Fatalf("err = %v", err)
	}
	rt.MustNotHaveBeenCalled("api.github.com")
	rt.MustNotHaveBeenCalled("developer.mend.io")
}

func TestAPIBase(t *testing.T) {
	for in, want := range map[string]string{"": "https://api.github.com", "https://github.com": "https://api.github.com", "https://github.example/": "https://github.example/api/v3"} {
		if got := APIBase(in); got != want {
			t.Errorf("APIBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkHeaderNext(t *testing.T) {
	h := http.Header{}
	h.Set("Link", `<https://api.github.example/repos/a/b/pulls?page=2>; rel="next", <https://api.github.example/repos/a/b/pulls?page=9>; rel="last"`)
	if got := nextLink(h); got != "https://api.github.example/repos/a/b/pulls?page=2" {
		t.Errorf("next = %q", got)
	}
	h.Set("Link", `<https://x/?page=1>; rel="prev"`)
	if got := nextLink(h); got != "" {
		t.Errorf("no next: %q", got)
	}
	_ = url.QueryEscape
}

func TestClosePullRequestRetitlesAndCloses(t *testing.T) {
	p, srv, _ := newFixture(t)
	rp := srv.addRepo("acme/site", "main")
	rp.pulls = append(rp.pulls, &fakePull{number: 4, state: "open", head: "renovate/x", base: "main", title: "chore(deps): x", createdAt: fixedTime(1), updatedAt: fixedTime(1)})
	if err := p.CloseMergeRequest(context.Background(), publish.Project{Path: "acme/site"}, 4, "chore(deps): x - autoclosed"); err != nil {
		t.Fatal(err)
	}
	if rp.pulls[0].state != "closed" || rp.pulls[0].title != "chore(deps): x - autoclosed" {
		t.Errorf("pull = %+v", rp.pulls[0])
	}
	if _, ok, _ := p.FindMergeRequest(context.Background(), publish.Project{Path: "acme/site"}, "renovate/x"); ok {
		t.Error("a closed request is not found as open")
	}
}
