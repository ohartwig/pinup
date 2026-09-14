// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/report"
)

// osvHandler speaks the two endpoints the watch uses, for a fixed table:
// lodash 4.17.20 has one advisory fixed in 4.17.21, everything else none.
func osvHandler(t *testing.T, calls *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
			*calls++
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Queries []struct {
					Package struct{ Name, Ecosystem string } `json:"package"`
					Version string                           `json:"version"`
				} `json:"queries"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("querybatch body: %v", err)
			}
			results := make([]map[string]any, len(req.Queries))
			for i, q := range req.Queries {
				results[i] = map[string]any{}
				if q.Package.Name == "lodash" && q.Package.Ecosystem == "npm" && q.Version == "4.17.20" {
					results[i]["vulns"] = []map[string]string{{"id": "GHSA-35jh-r3h4-6jhm", "modified": "2024-01-01T00:00:00Z"}}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"results": results})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
			io.WriteString(w, `{"id":"GHSA-35jh-r3h4-6jhm","summary":"Command injection in lodash","aliases":["CVE-2021-23337"],
			  "modified":"2024-01-01T00:00:00Z","published":"2021-02-15T13:15:00Z",
			  "severity":[{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H"}],
			  "affected":[{"package":{"name":"lodash","ecosystem":"npm"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"4.17.21"}]}]}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func advisoriesIndex() *report.Index {
	idx := report.NewIndex()
	idx.Dependencies["pinup/shadow-fixture"] = []report.Dependency{
		{Datasource: "npm", PackageName: "lodash", Version: "4.17.20", Versioning: "npm", File: "package.json"},
	}
	idx.Dependencies["development/site"] = []report.Dependency{
		{Datasource: "npm", PackageName: "lodash", Version: "4.17.20", Versioning: "npm", File: "package.json"},
		{Datasource: "npm", PackageName: "dayjs", Version: "1.11.13", Versioning: "npm", File: "package.json"},
		{Datasource: "docker", PackageName: "alpine", Version: "3.22", File: "Containerfile"},                                  // no OSV ecosystem: never queried
		{Datasource: "packagist", PackageName: "symfony/yaml", Version: "^7.3", Versioning: "composer", File: "composer.json"}, // a range: not a version
	}
	idx.Dependencies["devops/tooling"] = []report.Dependency{
		{Datasource: "npm", PackageName: "lodash", Version: "4.17.21", Versioning: "npm", File: "package.json"},
	}
	return idx
}

// The watch: every distinct version once, api.osv.dev alone, a new
// advisory reported with the repositories that carry the version - the
// control among them counted, never listed - and reported once only.
func TestAdvisoriesWatchReportsNewFindingsOnce(t *testing.T) {
	calls := 0
	rt := harness.NewRefusingTransport(t)
	rt.Handle("api.osv.dev", osvHandler(t, &calls))
	client := &osv.Client{Transport: rt}
	state := &advisoriesState{Seen: map[string]time.Time{}}
	only := glob.NewSet([]string{"development/**", "devops/**"})
	now := time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC)

	rep, err := watchAdvisories(context.Background(), client, advisoriesIndex(), only, "pinup/shadow-fixture", state, now)
	if err != nil {
		t.Fatal(err)
	}
	// lodash 4.17.20 (two repositories, one query), dayjs, lodash 4.17.21:
	// three queries; the docker image and the composer range are not sent.
	if rep.Queried != 3 || calls != 1 {
		t.Errorf("queried %d in %d batch calls, want 3 in 1", rep.Queried, calls)
	}
	if len(rep.New) != 1 {
		t.Fatalf("new advisories = %+v", rep.New)
	}
	h := rep.New[0]
	if h.Advisory != "GHSA-35jh-r3h4-6jhm" || h.PackageName != "lodash" || h.Version != "4.17.20" || h.Fixed != "4.17.21" || h.Severity == "" {
		t.Errorf("hit = %+v", h)
	}
	if strings.Join(h.Repositories, ",") != "development/site" {
		t.Errorf("repositories = %v; the control is counted, not listed, and devops/tooling is on the fixed version", h.Repositories)
	}
	if rep.Control == nil || rep.Control.Findings != 1 || rep.Control.Queried != 1 {
		t.Errorf("control = %+v", rep.Control)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), "symfony/yaml") {
		t.Errorf("the range must be reported as skipped, warnings = %v", rep.Warnings)
	}

	// The same advisory is not news twice.
	again, err := watchAdvisories(context.Background(), client, advisoriesIndex(), only, "pinup/shadow-fixture", state, now.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.New) != 0 || again.Findings != 1 {
		t.Errorf("second run: new %d, findings %d", len(again.New), again.Findings)
	}
}

// A control that yields nothing is a broken watch: the command exits with
// an error rather than reporting a quiet estate.
func TestAdvisoriesControlMustYield(t *testing.T) {
	calls := 0
	rt := harness.NewRefusingTransport(t)
	rt.Handle("api.osv.dev", osvHandler(t, &calls))
	idx := report.NewIndex()
	idx.Dependencies["pinup/shadow-fixture"] = []report.Dependency{{Datasource: "npm", PackageName: "dayjs", Version: "1.11.13", Versioning: "npm"}}
	rep, err := watchAdvisories(context.Background(), &osv.Client{Transport: rt}, idx, nil, "pinup/shadow-fixture", &advisoriesState{Seen: map[string]time.Time{}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Control == nil || rep.Control.Findings != 0 || rep.Control.Queried != 1 {
		t.Fatalf("control = %+v", rep.Control)
	}
}
