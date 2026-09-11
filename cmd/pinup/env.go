// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"net/url"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/httpx"
)

// platformEnv is what the process learns about its GitLab instance from the
// environment. It is read once, in main, and handed down; nothing below cmd
// reads os.Getenv.
//
// Two token forms are accepted because the two ways pinup runs differ:
//
//   - PINUP_GITLAB_TOKEN (or GITLAB_TOKEN): a personal or project access
//     token, sent as PRIVATE-TOKEN. This is the runner's credential.
//   - CI_JOB_TOKEN: the job's own token, sent as JOB-TOKEN. This is what a
//     pipeline job has without anyone provisioning anything, and it can read
//     tags of any project that allowlists the caller - which is the trigger
//     contract with yasrt.
//
// The instance comes from PINUP_GITLAB_URL, else CI_SERVER_URL. Without
// either, a dependency that names no registry is skipped with that reason.
type platformEnv struct {
	URL   string
	Host  string
	Token string
	// Header is the header that carries Token: PRIVATE-TOKEN or JOB-TOKEN.
	Header string
}

func platformFromEnv(getenv func(string) string) platformEnv {
	var p platformEnv
	p.URL = strings.TrimRight(firstSet(getenv, "PINUP_GITLAB_URL", "CI_SERVER_URL"), "/")
	if u, err := url.Parse(p.URL); err == nil {
		p.Host = u.Host
	}
	if tok := firstSet(getenv, "PINUP_GITLAB_TOKEN", "GITLAB_TOKEN"); tok != "" {
		p.Token, p.Header = tok, "PRIVATE-TOKEN"
	} else if tok := getenv("CI_JOB_TOKEN"); tok != "" {
		p.Token, p.Header = tok, "JOB-TOKEN"
	}
	return p
}

func firstSet(getenv func(string) string, names ...string) string {
	for _, n := range names {
		if v := getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// httpClient builds the one HTTP client every datasource shares. The token
// is bound to the instance's host and nothing else: httpx sends a host rule's
// credential only to that host, and drops it on a cross-host redirect.
func httpClient(p platformEnv) *httpx.Client {
	var rules []httpx.HostRule
	if p.Host != "" && p.Token != "" {
		rules = append(rules, httpx.HostRule{
			MatchHost: p.Host, Token: p.Token, HeaderName: p.Header,
		})
	}
	return httpx.New(httpx.Options{
		HostRules:  rules,
		MaxRetries: 3,
		UserAgent:  "pinup/" + version,
		Now:        time.Now,
		Sleep:      time.Sleep,
	})
}
