// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ohartwig/pinup/datasource/apkds"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/plugin"
	"github.com/ohartwig/pinup/wire"
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
	// Kind is the platform: "gitlab" (the default) or "github".
	// PINUP_PLATFORM names it; without that, a GitHub token with no GitLab
	// instance in the environment means GitHub.
	Kind  string
	URL   string
	Host  string
	Token string
	// Header is the header that carries Token: PRIVATE-TOKEN or JOB-TOKEN
	// on GitLab; empty on GitHub, where it is a bearer token.
	Header string
	// GitHubToken is GITHUB_COM_TOKEN - the name the Renovate runner uses;
	// the same variable serves pinup, bound to api.github.com and nothing
	// else. Without it github lookups run anonymously against the
	// 60-an-hour limit, which the estate's handful of GitHub dependencies
	// fits.
	GitHubToken string
	// RegistryHost is the estate's container registry - PINUP_REGISTRY_HOST,
	// else CI_REGISTRY - the one registry the platform credential may be
	// exchanged for a pull token at.
	RegistryHost string
}

func platformFromEnv(getenv func(string) string) (platformEnv, error) {
	var p platformEnv
	kind := strings.ToLower(getenv("PINUP_PLATFORM"))
	ghTok := firstSet(getenv, "PINUP_GITHUB_TOKEN", "GITHUB_TOKEN")
	if kind == "" && ghTok != "" && firstSet(getenv, "PINUP_GITLAB_URL", "CI_SERVER_URL", "PINUP_GITLAB_TOKEN", "GITLAB_TOKEN") == "" {
		kind = "github"
	}
	switch kind {
	case "", "gitlab":
		p.Kind = "gitlab"
	case "github":
		return githubFromEnv(getenv, ghTok)
	default:
		return p, fmt.Errorf("PINUP_PLATFORM=%q: the platforms are gitlab and github", kind)
	}
	p.URL = strings.TrimRight(firstSet(getenv, "PINUP_GITLAB_URL", "CI_SERVER_URL"), "/")
	if u, err := url.Parse(p.URL); err == nil {
		p.Host = u.Host
	}
	if tok := firstSet(getenv, "PINUP_GITLAB_TOKEN", "GITLAB_TOKEN"); tok != "" {
		p.Token, p.Header = tok, "PRIVATE-TOKEN"
	} else if tok := getenv("CI_JOB_TOKEN"); tok != "" {
		p.Token, p.Header = tok, "JOB-TOKEN"
	}
	p.GitHubToken = getenv("GITHUB_COM_TOKEN")
	p.RegistryHost = firstSet(getenv, "PINUP_REGISTRY_HOST", "CI_REGISTRY")
	if strings.Contains(p.RegistryHost, "://") {
		if u, err := url.Parse(p.RegistryHost); err == nil {
			p.RegistryHost = u.Host
		}
	}
	// A token with no host to bind it to would never be sent, and every
	// private project would answer 404 "does not exist or the token cannot
	// read it" - true, and useless. Refuse the configuration instead.
	if p.Token != "" && p.Host == "" {
		return p, fmt.Errorf("a GitLab token is set but no instance to send it to: set PINUP_GITLAB_URL (or CI_SERVER_URL)")
	}
	return p, nil
}

// githubFromEnv reads the GitHub shape: PINUP_GITHUB_URL (else github.com)
// is the web host git clones from and the platform derives its API base
// from; the token is a bearer token, and it also serves the github-*
// datasources when GITHUB_COM_TOKEN names none of its own.
func githubFromEnv(getenv func(string) string, token string) (platformEnv, error) {
	p := platformEnv{Kind: "github"}
	p.URL = strings.TrimRight(firstSet(getenv, "PINUP_GITHUB_URL", "GITHUB_SERVER_URL"), "/")
	if p.URL == "" {
		p.URL = "https://github.com"
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" {
		return p, fmt.Errorf("PINUP_GITHUB_URL %q is not a URL", p.URL)
	}
	p.Host = u.Host
	p.Token = token
	if p.Token == "" {
		return p, fmt.Errorf("PINUP_PLATFORM=github needs PINUP_GITHUB_TOKEN (or GITHUB_TOKEN)")
	}
	p.GitHubToken = getenv("GITHUB_COM_TOKEN")
	if p.GitHubToken == "" && strings.EqualFold(p.Host, "github.com") {
		p.GitHubToken = p.Token
	}
	p.RegistryHost = firstSet(getenv, "PINUP_REGISTRY_HOST")
	return p, nil
}

// gitLabURL is the GitLab instance the run knows, or empty on another
// platform - what the gitlab-* datasources and the release-notes fetcher
// default to.
func (p platformEnv) gitLabURL() string {
	if p.Kind == "gitlab" {
		return p.URL
	}
	return ""
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
// token uses the fixed username GitLab documents for it. The credential
// is bound three ways: the registry asking must be the estate's own
// (PINUP_REGISTRY_HOST, else CI_REGISTRY), the realm must be the instance
// over TLS, and the scope is the one a lookup needs (the datasource sees
// to that). Without a registry host no registry credential exists.
//
// PINUP_APK_VIEWS adds apk datasources served natively, a JSON object by
// the name the configuration uses: {"custom.koh-apk": {"mirrors": [...],
// "arches": ["x86_64", "aarch64"]}}. They join wire.DefaultApkViews; a
// name already there is replaced, so an installation with a mirror of
// Wolfi lists it under custom.wolfi with the public repository.
func datasourceOptions(p platformEnv, getenv func(string) string) (wire.DatasourceOptions, error) {
	o := wire.DatasourceOptions{GitLabURL: p.gitLabURL()}
	views, err := apkViews(getenv)
	if err != nil {
		return o, err
	}
	o.ApkViews = views
	if p.Kind != "gitlab" || p.Host == "" || p.Token == "" || p.RegistryHost == "" {
		return o, nil
	}
	user := "oauth2"
	if p.Header == "JOB-TOKEN" {
		user = "gitlab-ci-token"
	}
	o.RegistryCredentials = func(registryHost string, realm *url.URL) (string, string, bool) {
		if !strings.EqualFold(registryHost, p.RegistryHost) || !strings.EqualFold(realm.Host, p.Host) || realm.Scheme != "https" {
			return "", "", false
		}
		return user, p.Token, true
	}
	return o, nil
}

// apkViews reads PINUP_APK_VIEWS over the default views.
func apkViews(getenv func(string) string) (map[string]apkds.View, error) {
	views := wire.DefaultApkViews()
	raw := getenv("PINUP_APK_VIEWS")
	if raw == "" {
		return views, nil
	}
	var extra map[string]struct {
		Mirrors []string `json:"mirrors"`
		Arches  []string `json:"arches"`
	}
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return nil, fmt.Errorf("PINUP_APK_VIEWS: %w", err)
	}
	for name, v := range extra {
		if !strings.HasPrefix(name, "custom.") || len(v.Mirrors) == 0 {
			return nil, fmt.Errorf("PINUP_APK_VIEWS: %s needs a custom. name and at least one mirror", name)
		}
		arches := v.Arches
		if len(arches) == 0 {
			arches = []string{"x86_64", "aarch64"}
		}
		views[name] = apkds.View{Mirrors: v.Mirrors, Arches: arches}
	}
	return views, nil
}

// instancePaths are the API paths the platform token may reach through
// the datasources' client: a project's releases, tags, files and
// packages, a group's composer registry, the token's own endpoints. A
// URL a repository configuration names on the instance - a custom
// datasource, a registryUrls entry - is requested anonymously unless it
// is one of these; the bot's api scope reads CI variables, and a
// Developer on any scanned repository must not be able to make it
// (decision record dependency-bot-credential-scope, review S4).
// Every wildcard is [^/]+, one path segment: `.+` spans `/`, which let
// `projects/1/releases/../../groups/5/variables` satisfy a pattern written
// for a project's releases. httpx refuses dot segments outright now, and
// this is the second lock on the same door - a segment wildcard cannot
// walk out of the path it was written for whatever the instance does with
// the bytes. The file path keeps `.+` deliberately: a repository file path
// contains slashes, and the `/raw` suffix bounds it.
const instancePaths = `^/api/v4/(projects/[^/]+/(releases|repository/tags|repository/files/.+/raw|packages)(/|$)|group/[^/]+/-/packages/composer|user$|personal_access_tokens/self)`

// httpClient builds the one HTTP client every datasource shares. The token
// is bound to the instance's host and nothing else: httpx sends a host rule's
// credential only to that host, and drops it - Authorization and the rule's
// own header alike - on a cross-host redirect.
func httpClient(p platformEnv) *httpx.Client {
	var rules []httpx.HostRule
	// On GitHub the platform token is the api.github.com rule below (or,
	// on an enterprise host, none: the github-* datasources are bound to
	// github.com); the instance rule is GitLab's shape.
	if p.Kind == "gitlab" && p.Host != "" && p.Token != "" {
		rules = append(rules, httpx.HostRule{
			MatchHost: p.Host, Token: p.Token, HeaderName: p.Header,
			PathPattern: instancePaths,
		})
	}
	if p.GitHubToken != "" {
		rules = append(rules, httpx.HostRule{MatchHost: "api.github.com", Token: p.GitHubToken})
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
// PINUP_TASK_NETRC is a .netrc a task's toolchain fetches first-party
// modules with (`machine <host> login <user> password <read-only token>`),
// written into the task's scratch HOME and never seen as a variable.
func taskRunner(getenv func(string) string) *plugin.Runner {
	r := &plugin.Runner{Now: time.Now, Netrc: getenv("PINUP_TASK_NETRC")}
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

// defaultDashboardTitle names the dashboard issue when the configuration
// does not: a name of its own, so that it lived beside Renovate's
// "Dependency Dashboard" through the cutover.
const defaultDashboardTitle = "pinup Dashboard"

// dashboardTitle is the dashboard issue's title: PINUP_DASHBOARD_TITLE,
// the operator's override, wins over the configuration's
// dependencyDashboardTitle (configured, resolved per repository), and
// the default stands where neither says anything.
func dashboardTitle(getenv func(string) string, configured string) string {
	if v := getenv("PINUP_DASHBOARD_TITLE"); v != "" {
		return v
	}
	if configured != "" {
		return configured
	}
	return defaultDashboardTitle
}
