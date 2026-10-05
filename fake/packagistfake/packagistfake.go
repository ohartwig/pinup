// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package packagistfake is a Packagist security-advisory API that speaks the
// protocol: GET /api/security-advisories/ with packages[] or updatedSince,
// "advisories" as an object keyed by package, or as [] when nothing matched
// - the shape packagist.org answers in. Mount it on a harness transport.
package packagistfake

import (
	"encoding/json/v2"
	"net/http"
	"strconv"
	"sync"
)

// Advisory is one advisory as Packagist writes it.
type Advisory struct {
	AdvisoryID       string   `json:"advisoryId"`
	PackageName      string   `json:"packageName"`
	Title            string   `json:"title"`
	CVE              string   `json:"cve,omitempty"`
	AffectedVersions string   `json:"affectedVersions"`
	ReportedAt       string   `json:"reportedAt"`
	Severity         string   `json:"severity,omitempty"`
	Sources          []Source `json:"sources"`
}

// Source is one of an advisory's sources.
type Source struct {
	Name     string `json:"name"`
	RemoteID string `json:"remoteId"`
}

// Server answers from ByPackage (packages[] queries) and Updated
// (updatedSince queries). Status, when set, is answered instead.
type Server struct {
	mu        sync.Mutex
	ByPackage map[string][]Advisory
	Updated   map[string][]Advisory
	Status    int
	// Asked records each packages[] request's names, Since each
	// updatedSince value, in order.
	Asked [][]string
	Since []int64
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodGet || r.URL.Path != "/api/security-advisories/" {
		http.NotFound(w, r)
		return
	}
	if s.Status != 0 {
		w.WriteHeader(s.Status)
		return
	}
	q := r.URL.Query()
	out := map[string][]Advisory{}
	switch {
	case q.Has("updatedSince"):
		since, _ := strconv.ParseInt(q.Get("updatedSince"), 10, 64)
		s.Since = append(s.Since, since)
		out = s.Updated
	case len(q["packages[]"]) > 0:
		s.Asked = append(s.Asked, q["packages[]"])
		for _, n := range q["packages[]"] {
			if a, ok := s.ByPackage[n]; ok {
				out[n] = a
			}
		}
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var body any = out
	if len(out) == 0 {
		body = []any{}
	}
	raw, _ := json.Marshal(map[string]any{"advisories": body})
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}
