// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
)

// multiTokenServer speaks the token and variable endpoints for an account
// with several tokens and variables in several scopes: the run's own api
// token, a read-only one the dry-run partitions carry, and the project
// variables each lives in. Rotating by id needs the api token; a token
// answers /self for itself.
type multiTokenServer struct {
	mu     sync.Mutex
	tokens map[string]*fakeToken // by value
	vars   map[string]string     // "scope/KEY" -> value
	log    []string
}

type fakeToken struct {
	id        int
	name      string
	scopes    []string
	expiresAt string
}

func (s *multiTokenServer) byID(id int) (string, *fakeToken) {
	for v, t := range s.tokens {
		if t.id == id {
			return v, t
		}
	}
	return "", nil
}

func (s *multiTokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, r.Method+" "+r.URL.Path)
	presented := r.Header.Get("PRIVATE-TOKEN")
	me, ok := s.tokens[presented]
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "401 Unauthorized"})
		return
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/v4/personal_access_tokens/self":
		writeJSON(w, http.StatusOK, map[string]any{"id": me.id, "name": me.name, "scopes": me.scopes, "active": true, "revoked": false, "expires_at": me.expiresAt})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/v4/personal_access_tokens/") && strings.HasSuffix(path, "/rotate"):
		var target *fakeToken
		oldValue := ""
		if path == "/api/v4/personal_access_tokens/self/rotate" {
			target, oldValue = me, presented
		} else {
			var id int
			fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(path, "/api/v4/personal_access_tokens/"), "/rotate"), "%d", &id)
			oldValue, target = s.byID(id)
			if target == nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
				return
			}
			// Rotating another token takes the api scope.
			hasAPI := false
			for _, sc := range me.scopes {
				hasAPI = hasAPI || sc == "api"
			}
			if !hasAPI {
				writeJSON(w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
				return
			}
		}
		exp := r.URL.Query().Get("expires_at")
		fresh := &fakeToken{id: target.id + 1000, name: target.name, scopes: target.scopes, expiresAt: exp}
		delete(s.tokens, oldValue)
		newValue := target.name + "-rotated-" + exp
		s.tokens[newValue] = fresh
		writeJSON(w, http.StatusOK, map[string]any{"id": fresh.id, "token": newValue, "expires_at": exp})
	case strings.Contains(path, "/variables/"):
		// Only the api token writes variables; a read token may read.
		scope := strings.TrimPrefix(path[:strings.Index(path, "/variables/")], "/api/v4/")
		key := path[strings.Index(path, "/variables/")+len("/variables/"):]
		id := scope + "/" + key
		switch r.Method {
		case http.MethodGet:
			v, ok := s.vars[id]
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": v})
		case http.MethodPut:
			hasAPI := false
			for _, sc := range me.scopes {
				hasAPI = hasAPI || sc == "api"
			}
			if !hasAPI {
				writeJSON(w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
				return
			}
			var body struct {
				Value string `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.vars[id] = body.Value
			writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": body.Value})
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// Two tokens, three variables in two projects: the read token is due and
// rotated by id with the api token, stored in its project variable; the
// api token is due too, rotates itself last and lands in both projects.
// Afterwards every variable holds a token that answers for itself.
func TestRotationRotatesSeveralTokensIntoSeveralScopes(t *testing.T) {
	srv := &multiTokenServer{
		tokens: map[string]*fakeToken{
			"glpat-api":  {id: 211, name: "pinup", scopes: []string{"api"}, expiresAt: "2026-09-15"},
			"glpat-read": {id: 300, name: "pinup-read", scopes: []string{"read_api"}, expiresAt: "2026-09-14"},
		},
		vars: map[string]string{
			"projects/827/PINUP_GITLAB_TOKEN": "glpat-api",
			"projects/826/PINUP_GITLAB_TOKEN": "glpat-api",
			"projects/827/PINUP_READ_TOKEN":   "glpat-read",
		},
	}
	rt := harness.NewRefusingTransport(t).Handle("git.example.org", srv)
	out := &strings.Builder{}
	rot := Rotation{
		Platform:  New("https://git.example.org", rt, Token{Value: "glpat-api", Header: "PRIVATE-TOKEN"}),
		Targets:   []Target{{"projects/827", "PINUP_GITLAB_TOKEN"}, {"projects/826", "PINUP_GITLAB_TOKEN"}},
		Others:    []Other{{Name: "PINUP_READ_TOKEN", Value: "glpat-read", Targets: []Target{{"projects/827", "PINUP_READ_TOKEN"}}}},
		Threshold: 7 * 24 * time.Hour, Lifetime: 30 * 24 * time.Hour, Out: out, Sleep: func(time.Duration) {}, Retries: 2,
	}
	if err := rot.Run(context.Background(), rotateNow); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for id, want := range map[string]string{
		"projects/827/PINUP_GITLAB_TOKEN": "pinup-rotated-2026-10-12",
		"projects/826/PINUP_GITLAB_TOKEN": "pinup-rotated-2026-10-12",
		"projects/827/PINUP_READ_TOKEN":   "pinup-read-rotated-2026-10-12",
	} {
		if got := srv.vars[id]; got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
		if _, ok := srv.tokens[srv.vars[id]]; !ok {
			t.Errorf("%s holds a value no token answers for", id)
		}
	}
	// The read token was rotated by id, with the api token, before the
	// api token rotated itself.
	joined := strings.Join(srv.log, "\n")
	if strings.Index(joined, "POST /api/v4/personal_access_tokens/300/rotate") > strings.Index(joined, "POST /api/v4/personal_access_tokens/self/rotate") {
		t.Errorf("the other token must rotate before the run's own:\n%s", joined)
	}
	if !strings.Contains(out.String(), `rotated token "PINUP_READ_TOKEN" (id 300)`) {
		t.Errorf("output:\n%s", out.String())
	}

	// A second run: nothing is due, nothing is written.
	rot.Platform = New("https://git.example.org", rt, Token{Value: "pinup-rotated-2026-10-12", Header: "PRIVATE-TOKEN"})
	rot.Others[0].Value = "pinup-read-rotated-2026-10-12"
	out.Reset()
	if err := rot.Run(context.Background(), rotateNow); err != nil || !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("second run: %v\n%s", err, out.String())
	}
}

// Only the token that is due rotates; a variable that holds a different
// value than the token it is said to carry stops the run before anything
// rotates.
func TestRotationRotatesOnlyWhatIsDueAndRefusesAStrandedVariable(t *testing.T) {
	srv := &multiTokenServer{
		tokens: map[string]*fakeToken{
			"glpat-api":  {id: 211, name: "pinup", scopes: []string{"api"}, expiresAt: "2027-09-15"},
			"glpat-read": {id: 300, name: "pinup-read", scopes: []string{"read_api"}, expiresAt: "2026-09-14"},
		},
		vars: map[string]string{"projects/827/PINUP_GITLAB_TOKEN": "glpat-api", "projects/827/PINUP_READ_TOKEN": "glpat-read"},
	}
	rt := harness.NewRefusingTransport(t).Handle("git.example.org", srv)
	out := &strings.Builder{}
	rot := Rotation{
		Platform:  New("https://git.example.org", rt, Token{Value: "glpat-api", Header: "PRIVATE-TOKEN"}),
		Targets:   []Target{{"projects/827", "PINUP_GITLAB_TOKEN"}},
		Others:    []Other{{Name: "PINUP_READ_TOKEN", Value: "glpat-read", Targets: []Target{{"projects/827", "PINUP_READ_TOKEN"}}}},
		Threshold: 7 * 24 * time.Hour, Lifetime: 30 * 24 * time.Hour, Out: out, Sleep: func(time.Duration) {}, Retries: 2,
	}
	if err := rot.Run(context.Background(), rotateNow); err != nil {
		t.Fatal(err)
	}
	if srv.vars["projects/827/PINUP_GITLAB_TOKEN"] != "glpat-api" || srv.vars["projects/827/PINUP_READ_TOKEN"] != "pinup-read-rotated-2026-10-12" {
		t.Errorf("vars = %v", srv.vars)
	}

	srv.vars["projects/827/PINUP_READ_TOKEN"] = "something-else"
	srv.tokens["pinup-read-rotated-2026-10-12"].expiresAt = "2026-09-14"
	rot.Others[0].Value = "pinup-read-rotated-2026-10-12"
	before := len(srv.log)
	err := rot.Run(context.Background(), rotateNow)
	if err == nil || !strings.Contains(err.Error(), "does not hold token PINUP_READ_TOKEN") {
		t.Errorf("stranded variable: %v", err)
	}
	for _, line := range srv.log[before:] {
		if strings.Contains(line, "/rotate") {
			t.Errorf("rotated despite a stranded variable: %s", line)
		}
	}
}
