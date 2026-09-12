// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/fake/harness"
)

// tokenServer speaks the personal-access-token and CI-variable endpoints:
// one token that can rotate itself, one scope of variables. It refuses a
// request without the current token and rewrites which token is current
// on rotation, so a test can prove the new value is the one that answers
// afterwards.
type tokenServer struct {
	mu        sync.Mutex
	current   string
	expiresAt string // "" for a token without one
	active    bool
	variables map[string]string
	// writeStatus forces the variable PUT to answer this status (0 = 200).
	writeStatus int
	// failWritesAfterRotate makes every PUT after a rotation fail.
	failWritesAfterRotate bool
	rotated               int
	puts                  int
	log                   []string
}

func newTokenServer(current, expiresAt string) *tokenServer {
	return &tokenServer{current: current, expiresAt: expiresAt, active: true, variables: map[string]string{}}
}

func (s *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, r.Method+" "+r.URL.Path)
	if r.Header.Get("PRIVATE-TOKEN") != s.current {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "401 Unauthorized"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v4/personal_access_tokens/self":
		doc := map[string]any{"id": 211, "name": "pinup", "scopes": []string{"api", "self_rotate"}, "active": s.active, "revoked": false}
		if s.expiresAt != "" {
			doc["expires_at"] = s.expiresAt
		}
		writeJSON(w, http.StatusOK, doc)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v4/personal_access_tokens/self/rotate":
		s.rotated++
		s.current = "glpat-rotated-" + r.URL.Query().Get("expires_at")
		s.expiresAt = r.URL.Query().Get("expires_at")
		writeJSON(w, http.StatusOK, map[string]any{"id": 212, "token": s.current, "expires_at": s.expiresAt})
	case strings.HasPrefix(r.URL.Path, "/api/v4/groups/1210/variables/"):
		key := strings.TrimPrefix(r.URL.Path, "/api/v4/groups/1210/variables/")
		switch r.Method {
		case http.MethodGet:
			v, ok := s.variables[key]
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": v, "masked": true})
		case http.MethodPut:
			s.puts++
			if s.writeStatus != 0 || (s.failWritesAfterRotate && s.rotated > 0) {
				status := s.writeStatus
				if status == 0 {
					status = http.StatusBadGateway
				}
				writeJSON(w, status, map[string]any{"message": "no"})
				return
			}
			var body struct {
				Value string `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.variables[key] = body.Value
			writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": body.Value})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func rotationFixture(t *testing.T, srv *tokenServer) (Rotation, *strings.Builder) {
	t.Helper()
	rt := harness.NewRefusingTransport(t).Handle("git.example.org", srv)
	out := &strings.Builder{}
	return Rotation{
		Platform:  New("https://git.example.org", rt, Token{Value: srv.current, Header: "PRIVATE-TOKEN"}),
		Scope:     "groups/1210",
		Variables: []string{"PINUP_GITLAB_TOKEN", "GITLAB_TOKEN"},
		Threshold: 7 * 24 * time.Hour,
		Lifetime:  30 * 24 * time.Hour,
		Out:       out,
		Sleep:     func(time.Duration) {},
		Retries:   3,
	}, out
}

var rotateNow = time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)

func TestRotationLeavesAFarExpiryAlone(t *testing.T) {
	srv := newTokenServer("glpat-old", "2027-09-12")
	srv.variables["PINUP_GITLAB_TOKEN"], srv.variables["GITLAB_TOKEN"] = "glpat-old", "glpat-old"
	rot, out := rotationFixture(t, srv)
	if err := rot.Run(context.Background(), rotateNow); err != nil {
		t.Fatal(err)
	}
	if srv.rotated != 0 || srv.puts != 0 {
		t.Errorf("rotated %d times, wrote %d times; want neither for 365 days left", srv.rotated, srv.puts)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("output %q does not say nothing to do", out.String())
	}
}

func TestRotationRotatesStoresAndVerifies(t *testing.T) {
	srv := newTokenServer("glpat-old", "2026-09-15")
	srv.variables["PINUP_GITLAB_TOKEN"], srv.variables["GITLAB_TOKEN"] = "glpat-old", "glpat-old"
	rot, out := rotationFixture(t, srv)
	if err := rot.Run(context.Background(), rotateNow); err != nil {
		t.Fatal(err)
	}
	if srv.rotated != 1 {
		t.Fatalf("rotated %d times, want once", srv.rotated)
	}
	want := "glpat-rotated-2026-10-12"
	for _, k := range rot.Variables {
		if srv.variables[k] != want {
			t.Errorf("%s = %q, want the rotated value", k, srv.variables[k])
		}
	}
	if strings.Contains(out.String(), want) || strings.Contains(out.String(), "glpat-old") {
		t.Errorf("a token value reached the output: %q", out.String())
	}
	if !strings.Contains(out.String(), "expires 2026-10-12") {
		t.Errorf("output %q does not report the new expiry", out.String())
	}
	// The rehearsal wrote each variable once before the rotation, the
	// real write once after: the rotation sits between the two.
	joined := strings.Join(srv.log, "\n")
	rehearsal := strings.Index(joined, "PUT /api/v4/groups/1210/variables/PINUP_GITLAB_TOKEN")
	rotate := strings.Index(joined, "POST /api/v4/personal_access_tokens/self/rotate")
	if rehearsal < 0 || rotate < 0 || rehearsal > rotate {
		t.Errorf("the write was not rehearsed before the rotation:\n%s", joined)
	}
}

func TestRotationRefusesATokenWithoutAnExpiry(t *testing.T) {
	srv := newTokenServer("glpat-old", "")
	srv.variables["PINUP_GITLAB_TOKEN"], srv.variables["GITLAB_TOKEN"] = "glpat-old", "glpat-old"
	rot, _ := rotationFixture(t, srv)
	err := rot.Run(context.Background(), rotateNow)
	if !errors.Is(err, ErrNoExpiry) {
		t.Fatalf("got %v, want ErrNoExpiry", err)
	}
	if srv.rotated != 0 {
		t.Error("rotated a token whose urgency it could not know")
	}
}

func TestRotationStopsBeforeRotatingWhenTheWriteIsForbidden(t *testing.T) {
	srv := newTokenServer("glpat-old", "2026-09-15")
	srv.variables["PINUP_GITLAB_TOKEN"], srv.variables["GITLAB_TOKEN"] = "glpat-old", "glpat-old"
	srv.writeStatus = http.StatusForbidden
	rot, _ := rotationFixture(t, srv)
	err := rot.Run(context.Background(), rotateNow)
	if err == nil || !strings.Contains(err.Error(), "rehearsing the write") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("got %v, want the rehearsal to fail with 403", err)
	}
	if srv.rotated != 0 {
		t.Error("rotated although the new value could never have been stored")
	}
}

func TestRotationStopsWhenAVariableHoldsAnotherToken(t *testing.T) {
	srv := newTokenServer("glpat-old", "2026-09-15")
	srv.variables["PINUP_GITLAB_TOKEN"], srv.variables["GITLAB_TOKEN"] = "glpat-old", "glpat-someone-else"
	rot, _ := rotationFixture(t, srv)
	err := rot.Run(context.Background(), rotateNow)
	if err == nil || !strings.Contains(err.Error(), "GITLAB_TOKEN does not hold the token") {
		t.Fatalf("got %v, want a refusal naming the variable", err)
	}
	if srv.rotated != 0 || srv.puts != 0 {
		t.Error("touched something although a variable belongs to another token")
	}
}

func TestRotationRetriesTheStoreAndShoutsWhenStranded(t *testing.T) {
	srv := newTokenServer("glpat-old", "2026-09-15")
	srv.variables["PINUP_GITLAB_TOKEN"], srv.variables["GITLAB_TOKEN"] = "glpat-old", "glpat-old"
	srv.failWritesAfterRotate = true
	rot, out := rotationFixture(t, srv)
	slept := 0
	rot.Sleep = func(time.Duration) { slept++ }
	err := rot.Run(context.Background(), rotateNow)
	if !errors.Is(err, ErrStranded) {
		t.Fatalf("got %v, want ErrStranded", err)
	}
	// Two rehearsal writes, then three attempts on the first variable.
	if srv.puts != 2+3 || slept != 2 {
		t.Errorf("puts %d, sleeps %d; want 3 attempts with 2 waits after the rehearsal", srv.puts, slept)
	}
	if strings.Contains(out.String(), "glpat-rotated") {
		t.Error("the rotated value reached the output")
	}
}

func TestRotationDryRunRehearsesAndStops(t *testing.T) {
	// A far expiry: the dry run rehearses anyway, which is its point.
	srv := newTokenServer("glpat-old", "2027-09-12")
	srv.variables["PINUP_GITLAB_TOKEN"], srv.variables["GITLAB_TOKEN"] = "glpat-old", "glpat-old"
	rot, out := rotationFixture(t, srv)
	rot.DryRun = true
	if err := rot.Run(context.Background(), rotateNow); err != nil {
		t.Fatal(err)
	}
	if srv.rotated != 0 || srv.puts != 2 {
		t.Errorf("rotated %d, puts %d; want the rehearsal only", srv.rotated, srv.puts)
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("output %q", out.String())
	}
}
