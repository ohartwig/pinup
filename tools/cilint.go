// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// Command cilint checks that every job named in .gitlab/expected-jobs.txt
// exists in the pipeline this job is running in.
//
// It asks the pipeline, not the file: an upstream stage-list change in the
// composed template drops jobs silently, and only the created job list shows
// it. The list comes from GET /projects/:id/pipelines/:pipeline_id/jobs, an
// endpoint the job's own token may read, so nothing needs provisioning.
// (POST /ci/lint, the obvious alternative, refuses a job token with 404.)
// Why not curl and jq: no image in the estate ships jq, and pulling one in
// would be a second package channel in a pipeline that otherwise needs
// nothing but Go.
//
// Usage: go run tools/cilint.go [.gitlab/expected-jobs.txt]
//
// Reads CI_API_V4_URL, CI_PROJECT_ID, CI_PIPELINE_ID and CI_JOB_TOKEN, and
// CI_COMMIT_TAG to decide whether "@tag" entries apply, CI_PIPELINE_SOURCE
// and CI_COMMIT_BRANCH for "@schedule" and "@main".
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cilint:", err)
		os.Exit(1)
	}
}

func run() error {
	expectedFile := ".gitlab/expected-jobs.txt"
	if len(os.Args) > 1 {
		expectedFile = os.Args[1]
	}
	api, project, pipeline, token := os.Getenv("CI_API_V4_URL"), os.Getenv("CI_PROJECT_ID"),
		os.Getenv("CI_PIPELINE_ID"), os.Getenv("CI_JOB_TOKEN")
	if api == "" || project == "" || pipeline == "" || token == "" {
		return fmt.Errorf("CI_API_V4_URL, CI_PROJECT_ID, CI_PIPELINE_ID and CI_JOB_TOKEN must all be set")
	}

	// Paginate: a pipeline with more jobs than one page would otherwise
	// report the ones on page two as missing.
	resolved := map[string]bool{}
	for page := 1; page != 0; {
		url := fmt.Sprintf("%s/projects/%s/pipelines/%s/jobs?per_page=100&page=%d", api, project, pipeline, page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("JOB-TOKEN", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Check the status, never the emptiness of the body: a 404 body is
		// a non-empty string and would read as success.
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("pipeline jobs returned HTTP %d: %s", resp.StatusCode, raw)
		}
		var jobs []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &jobs); err != nil {
			return fmt.Errorf("pipeline jobs answered something that is not its schema: %w", err)
		}
		for _, j := range jobs {
			resolved[j.Name] = true
		}
		page, _ = strconv.Atoi(resp.Header.Get("X-Next-Page"))
	}

	f, err := os.Open(expectedFile)
	if err != nil {
		return err
	}
	defer f.Close()
	onTag := os.Getenv("CI_COMMIT_TAG") != ""
	expected, missing := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		want := strings.TrimSpace(sc.Text())
		if want == "" || strings.HasPrefix(want, "#") {
			continue
		}
		// "name @tag": expected in tag pipelines only; "name @schedule" in
		// scheduled ones.
		if name, ok := strings.CutSuffix(want, " @tag"); ok {
			if !onTag {
				continue
			}
			want = name
		}
		if name, ok := strings.CutSuffix(want, " @schedule"); ok {
			if os.Getenv("CI_PIPELINE_SOURCE") != "schedule" {
				continue
			}
			want = name
		}
		// "name @main": a push to the default branch only - the release
		// jobs yasrt runs there, which a tag or a schedule does not carry.
		if name, ok := strings.CutSuffix(want, " @main"); ok {
			if onTag || os.Getenv("CI_PIPELINE_SOURCE") != "push" || os.Getenv("CI_COMMIT_BRANCH") != os.Getenv("CI_DEFAULT_BRANCH") {
				continue
			}
			want = name
		}
		expected++
		if !resolved[want] {
			fmt.Println("MISSING JOB:", want)
			missing++
		}
	}
	kind := "branch"
	switch {
	case onTag:
		kind = "tag"
	case os.Getenv("CI_PIPELINE_SOURCE") == "schedule":
		kind = "schedule"
	case os.Getenv("CI_PIPELINE_SOURCE") == "push" && os.Getenv("CI_COMMIT_BRANCH") == os.Getenv("CI_DEFAULT_BRANCH"):
		kind = "main"
	}
	fmt.Printf("resolved %d jobs, expected %d (%s pipeline)\n", len(resolved), expected, kind)
	// A file that expects nothing checks nothing, and would pass forever.
	if expected == 0 {
		return fmt.Errorf("%s names no jobs", expectedFile)
	}
	if missing != 0 {
		return fmt.Errorf("%d expected jobs are missing from the resolved pipeline", missing)
	}
	return nil
}
