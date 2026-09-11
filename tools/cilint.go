// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

//go:build ignore

// Command cilint asks GitLab which jobs the resolved pipeline contains and
// checks every name in .gitlab/expected-jobs.txt is among them.
//
// It asks the pipeline, not the file: an upstream stage-list change in the
// composed template drops jobs silently, and only the resolved job list shows
// it. Why not curl and jq: no image in the estate ships jq, and pulling one in
// would be a second package channel in a pipeline that otherwise needs
// nothing but Go.
//
// Usage: go run tools/cilint.go [.gitlab-ci.yml] [.gitlab/expected-jobs.txt]
//
// Reads CI_API_V4_URL, CI_PROJECT_ID and CI_JOB_TOKEN from the environment.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cilint:", err)
		os.Exit(1)
	}
}

func run() error {
	ciFile, expectedFile := ".gitlab-ci.yml", ".gitlab/expected-jobs.txt"
	if len(os.Args) > 1 {
		ciFile = os.Args[1]
	}
	if len(os.Args) > 2 {
		expectedFile = os.Args[2]
	}
	api, project, token := os.Getenv("CI_API_V4_URL"), os.Getenv("CI_PROJECT_ID"), os.Getenv("CI_JOB_TOKEN")
	if api == "" || project == "" || token == "" {
		return fmt.Errorf("CI_API_V4_URL, CI_PROJECT_ID and CI_JOB_TOKEN must all be set")
	}

	content, err := os.ReadFile(ciFile)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"content": string(content)})
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/projects/%s/ci/lint?include_jobs=true", api, project), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("JOB-TOKEN", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// Check the status, never the emptiness of the body: a 404 body is a
	// non-empty string and would read as success.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ci/lint returned HTTP %d: %s", resp.StatusCode, raw)
	}

	var lint struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
		Jobs   []struct {
			Name string `json:"name"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &lint); err != nil {
		return fmt.Errorf("ci/lint answered something that is not its schema: %w", err)
	}
	if !lint.Valid {
		return fmt.Errorf("pipeline is not valid:\n  %s", strings.Join(lint.Errors, "\n  "))
	}
	resolved := map[string]bool{}
	for _, j := range lint.Jobs {
		resolved[j.Name] = true
	}

	f, err := os.Open(expectedFile)
	if err != nil {
		return err
	}
	defer f.Close()
	expected, missing := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		want := strings.TrimSpace(sc.Text())
		if want == "" || strings.HasPrefix(want, "#") {
			continue
		}
		expected++
		if !resolved[want] {
			fmt.Println("MISSING JOB:", want)
			missing++
		}
	}
	fmt.Printf("resolved %d jobs, expected %d\n", len(resolved), expected)
	// A file that expects nothing checks nothing, and would pass forever.
	if expected == 0 {
		return fmt.Errorf("%s names no jobs", expectedFile)
	}
	if missing != 0 {
		return fmt.Errorf("%d expected jobs are missing from the resolved pipeline", missing)
	}
	return nil
}
