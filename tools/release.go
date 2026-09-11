// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

//go:build ignore

// Command release adds asset links to the release semantic-release already
// created for the tag. One call per link, form-encoded, with the job token.
//
// Usage: go run tools/release.go <package-url> <name>:<link_type>...
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: release <package-url> <name>:<link_type>...")
		os.Exit(2)
	}
	api, project, tag, token := os.Getenv("CI_API_V4_URL"), os.Getenv("CI_PROJECT_ID"), os.Getenv("CI_COMMIT_TAG"), os.Getenv("CI_JOB_TOKEN")
	if api == "" || project == "" || tag == "" || token == "" {
		fmt.Fprintln(os.Stderr, "release: CI_API_V4_URL, CI_PROJECT_ID, CI_COMMIT_TAG and CI_JOB_TOKEN must be set")
		os.Exit(1)
	}
	base := os.Args[1]
	endpoint := fmt.Sprintf("%s/projects/%s/releases/%s/assets/links", api, project, url.PathEscape(tag))
	for _, spec := range os.Args[2:] {
		name, kind, ok := strings.Cut(spec, ":")
		if !ok {
			fmt.Fprintf(os.Stderr, "release: %q is not name:link_type\n", spec)
			os.Exit(1)
		}
		form := url.Values{"name": {name}, "url": {base + "/" + name}, "link_type": {kind}}
		req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		req.Header.Set("JOB-TOKEN", token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "release:", err)
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			fmt.Fprintf(os.Stderr, "release: linking %s: HTTP %d\n", name, resp.StatusCode)
			os.Exit(1)
		}
		fmt.Printf("linked %s (%s)\n", name, kind)
	}
}
