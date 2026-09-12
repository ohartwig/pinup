// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"git.ole-hartwig.eu/pinup/pinup/plugin"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/wire"
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

func platformFromEnv(getenv func(string) string) (platformEnv, error) {
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
	// A token with no host to bind it to would never be sent, and every
	// private project would answer 404 "does not exist or the token cannot
	// read it" - true, and useless. Refuse the configuration instead.
	if p.Token != "" && p.Host == "" {
		return p, fmt.Errorf("a GitLab token is set but no instance to send it to: set PINUP_GITLAB_URL (or CI_SERVER_URL)")
	}
	return p, nil
}

func firstSet(getenv func(string) string, names ...string) string {
	for _, n := range names {
		if v := getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// datasourceOptions binds the environment's GitLab identity to the docker
// datasource's token realm as well: the estate's registry authenticates at
// https://<instance>/jwt/auth with the same token, as basic auth. A job
// token uses the fixed username GitLab documents for it.
func datasourceOptions(p platformEnv) wire.DatasourceOptions {
	o := wire.DatasourceOptions{GitLabURL: p.URL}
	if p.Host == "" || p.Token == "" {
		return o
	}
	user := "oauth2"
	if p.Header == "JOB-TOKEN" {
		user = "gitlab-ci-token"
	}
	o.RegistryCredentials = func(realmHost string) (string, string, bool) {
		if !strings.EqualFold(realmHost, p.Host) {
			return "", "", false
		}
		return user, p.Token, true
	}
	return o
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
	// GITHUB_COM_TOKEN is the name the Renovate runner uses; the same
	// variable serves pinup, bound to api.github.com and nothing else.
	// Without it github lookups run anonymously against the 60-an-hour
	// limit, which the estate's handful of GitHub dependencies fits.
	if tok := os.Getenv("GITHUB_COM_TOKEN"); tok != "" {
		rules = append(rules, httpx.HostRule{MatchHost: "api.github.com", Token: tok})
	}
	return httpx.New(httpx.Options{
		HostRules:  rules,
		MaxRetries: 3,
		UserAgent:  "pinup/" + version,
		Now:        time.Now,
		Sleep:      time.Sleep,
	})
}

// taskRunner builds the exec task runner from the job's environment:
// PINUP_PLUGIN_ENV names the variables a task may see besides the
// baseline (a registry credential for first-party packages, say),
// PINUP_EXECUTION_TIMEOUT is minutes per task - Renovate's unit; the
// estate's runner sets 45 for its largest composer repository.
func taskRunner(getenv func(string) string) *plugin.Runner {
	r := &plugin.Runner{Now: time.Now}
	for _, name := range strings.Split(getenv("PINUP_PLUGIN_ENV"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			r.PassEnv = append(r.PassEnv, name)
		}
	}
	if v := getenv("PINUP_EXECUTION_TIMEOUT"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil && minutes > 0 {
			r.Timeout = time.Duration(minutes) * time.Minute
		}
	}
	return r
}

// allowedCommands reads PINUP_ALLOWED_COMMANDS, a JSON array of anchored
// patterns the runner - never a repository - decides on. nil means the
// configuration file's own allowedCommands apply.
func allowedCommands(getenv func(string) string) ([]string, error) {
	v := getenv("PINUP_ALLOWED_COMMANDS")
	if v == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, fmt.Errorf("PINUP_ALLOWED_COMMANDS: %w", err)
	}
	return out, nil
}

// repositoryConcurrency is how many repositories a partition works on at
// once: PINUP_REPOSITORY_CONCURRENCY, default 4. The Renovate runner ran
// eight; four keeps a two-core job image from thrashing on clones while
// still hiding most of the network wait.
func repositoryConcurrency(getenv func(string) string) int {
	if v := getenv("PINUP_REPOSITORY_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}
