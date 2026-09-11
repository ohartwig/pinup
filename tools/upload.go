// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

//go:build ignore

// Command upload puts files into a GitLab generic package with the job token.
// Why not curl: the Go image ships no curl, and one tool for the pipeline is
// the whole point of it. Every file must exist and be non-empty.
//
// Usage: go run tools/upload.go <package-url> <file>...
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: upload <package-url> <file>...")
		os.Exit(2)
	}
	token := os.Getenv("CI_JOB_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "upload: CI_JOB_TOKEN is not set")
		os.Exit(1)
	}
	base := os.Args[1]
	for _, name := range os.Args[2:] {
		info, err := os.Stat(name)
		if err != nil || info.Size() == 0 {
			fmt.Fprintf(os.Stderr, "upload: %s is missing or empty\n", name)
			os.Exit(1)
		}
		f, err := os.Open(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, "upload:", err)
			os.Exit(1)
		}
		req, _ := http.NewRequest(http.MethodPut, base+"/"+name, f)
		req.ContentLength = info.Size()
		req.Header.Set("JOB-TOKEN", token)
		resp, err := http.DefaultClient.Do(req)
		f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "upload:", err)
			os.Exit(1)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			fmt.Fprintf(os.Stderr, "upload: %s: HTTP %d: %s\n", name, resp.StatusCode, body)
			os.Exit(1)
		}
		fmt.Printf("uploaded %s (%d bytes)\n", name, info.Size())
	}
}
