// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"regexp"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPlatformFromEnv(t *testing.T) {
	p, err := platformFromEnv(envOf(map[string]string{
		"CI_SERVER_URL": "https://git.example.org/", "CI_JOB_TOKEN": "job",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://git.example.org" || p.Host != "git.example.org" || p.Header != "JOB-TOKEN" || p.Token != "job" {
		t.Errorf("job token: %+v", p)
	}

	p, err = platformFromEnv(envOf(map[string]string{
		"PINUP_GITLAB_URL": "https://git.example.org", "PINUP_GITLAB_TOKEN": "pat", "CI_JOB_TOKEN": "job",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Header != "PRIVATE-TOKEN" || p.Token != "pat" {
		t.Errorf("a personal token wins over the job token: %+v", p)
	}

	if _, err := platformFromEnv(envOf(map[string]string{"GITLAB_TOKEN": "pat"})); err == nil {
		t.Error("a token with no instance must be refused, not silently unsent")
	}
	if p, err := platformFromEnv(envOf(nil)); err != nil || p.Token != "" {
		t.Errorf("no environment at all is fine (anonymous): %+v %v", p, err)
	}

	// GITHUB_COM_TOKEN needs no instance and binds to api.github.com only.
	p, err = platformFromEnv(envOf(map[string]string{"GITHUB_COM_TOKEN": "ghp_x"}))
	if err != nil || p.GitHubToken != "ghp_x" {
		t.Errorf("github token: %+v %v", p, err)
	}
	if c := httpClient(p); c == nil {
		t.Error("no client")
	}

	// GitHub: named, or implied by a GitHub token with no GitLab instance.
	p, err = platformFromEnv(envOf(map[string]string{"PINUP_GITHUB_TOKEN": "ghp_y"}))
	if err != nil || p.Kind != "github" || p.URL != "https://github.com" || p.Host != "github.com" || p.Token != "ghp_y" || p.Header != "" {
		t.Errorf("github implied: %+v %v", p, err)
	}
	if p.GitHubToken != "ghp_y" {
		t.Error("on github.com the platform token serves the github-* datasources too")
	}
	p, err = platformFromEnv(envOf(map[string]string{"PINUP_PLATFORM": "github", "PINUP_GITHUB_URL": "https://github.example/", "GITHUB_TOKEN": "ghp_z", "GITHUB_COM_TOKEN": "ghp_com"}))
	if err != nil || p.Kind != "github" || p.URL != "https://github.example" || p.Host != "github.example" || p.Token != "ghp_z" || p.GitHubToken != "ghp_com" {
		t.Errorf("github enterprise: %+v %v", p, err)
	}
	if _, err := platformFromEnv(envOf(map[string]string{"PINUP_PLATFORM": "github"})); err == nil {
		t.Error("github without a token must be refused")
	}
	if _, err := platformFromEnv(envOf(map[string]string{"PINUP_PLATFORM": "bitbucket"})); err == nil {
		t.Error("an unknown platform must be refused")
	}
	// A GitLab instance in the environment keeps GitLab the default even
	// beside a GitHub token (the estate's runner carries GITHUB_COM_TOKEN
	// for the github-* datasources).
	p, err = platformFromEnv(envOf(map[string]string{"CI_SERVER_URL": "https://git.example.org", "CI_JOB_TOKEN": "job", "GITHUB_TOKEN": "ghp_q"}))
	if err != nil || p.Kind != "gitlab" {
		t.Errorf("gitlab stays the default: %+v %v", p, err)
	}
}

func TestAskpassAnswersGitHubAsAccessToken(t *testing.T) {
	t.Setenv("PINUP_GITLAB_URL", "")
	t.Setenv("CI_SERVER_URL", "")
	t.Setenv("PINUP_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("CI_JOB_TOKEN", "")
	t.Setenv("PINUP_PLATFORM", "github")
	t.Setenv("PINUP_GITHUB_TOKEN", "ghp_x")
	var out strings.Builder
	if err := cmdAskpass([]string{"Username for 'https://github.com':"}, &out, io.Discard); err != nil || strings.TrimSpace(out.String()) != "x-access-token" {
		t.Errorf("username: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cmdAskpass([]string{"Password for 'https://x-access-token@github.com':"}, &out, io.Discard); err != nil || strings.TrimSpace(out.String()) != "ghp_x" {
		t.Errorf("password: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cmdAskpass([]string{"Password for 'https://x-access-token@gitlab.com':"}, &out, io.Discard); err == nil || out.Len() != 0 {
		t.Errorf("another host answered: %q %v", out.String(), err)
	}
}

func TestGitIdentityFromEnv(t *testing.T) {
	if _, _, err := gitIdentityFromEnv(envOf(map[string]string{"PINUP_GIT_NAME": "x"})); err == nil {
		t.Error("a missing email must be refused")
	}
	if _, _, err := gitIdentityFromEnv(envOf(map[string]string{"PINUP_GIT_NAME": "x", "PINUP_GIT_EMAIL": "x@y"})); err == nil {
		t.Error("an unset signing format must be refused, not read as unsigned")
	}
	_, s, err := gitIdentityFromEnv(envOf(map[string]string{"PINUP_GIT_NAME": "x", "PINUP_GIT_EMAIL": "x@y", "PINUP_SIGNING_FORMAT": "none"}))
	if err != nil || s.Format != "" {
		t.Errorf("explicit none: %+v %v", s, err)
	}
	if _, _, err := gitIdentityFromEnv(envOf(map[string]string{"PINUP_GIT_NAME": "x", "PINUP_GIT_EMAIL": "x@y", "PINUP_SIGNING_FORMAT": "ssh", "PINUP_SIGNING_KEY": "k"})); err == nil {
		t.Error("ssh without allowed signers must be refused")
	}
	_, s, err = gitIdentityFromEnv(envOf(map[string]string{"PINUP_GIT_NAME": "x", "PINUP_GIT_EMAIL": "x@y", "PINUP_SIGNING_FORMAT": "openpgp", "PINUP_SIGNING_KEY": "ABCD"}))
	if err != nil || s.Format != "openpgp" || s.Key != "ABCD" {
		t.Errorf("openpgp: %+v %v", s, err)
	}
}

func TestAskpassAnswersFromTheEnvironment(t *testing.T) {
	t.Setenv("PINUP_GITLAB_URL", "https://git.example.org")
	t.Setenv("PINUP_GITLAB_TOKEN", "glpat-x")
	var out strings.Builder
	if err := cmdAskpass([]string{"Username for 'https://git.example.org':"}, &out, io.Discard); err != nil || strings.TrimSpace(out.String()) != "oauth2" {
		t.Errorf("username: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cmdAskpass([]string{"Password for 'https://oauth2@git.example.org':"}, &out, io.Discard); err != nil || strings.TrimSpace(out.String()) != "glpat-x" {
		t.Errorf("password: %q %v", out.String(), err)
	}
	// The group's GITLAB_TOKEN is in every CI job's environment now; the
	// job-token fallback is only reached without it.
	t.Setenv("PINUP_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("CI_JOB_TOKEN", "job")
	out.Reset()
	cmdAskpass([]string{"Username for 'https://git.example.org':"}, &out, io.Discard)
	if strings.TrimSpace(out.String()) != "gitlab-ci-token" {
		t.Errorf("job token username: %q", out.String())
	}
	// A prompt for any other host gets nothing: the token is the
	// instance's, whatever led git elsewhere.
	for _, prompt := range []string{"Password for 'https://oauth2@evil.example.net':", "Password for 'https://git.example.org.evil.net':", "Password:"} {
		out.Reset()
		if err := cmdAskpass([]string{prompt}, &out, io.Discard); err == nil || out.Len() != 0 {
			t.Errorf("%q answered: %q %v", prompt, out.String(), err)
		}
	}
}

// The instance rule reaches what the datasources call and nothing a
// repository configuration could aim it at.
func TestInstancePathsAdmitTheDatasourcesAndNothingElse(t *testing.T) {
	re := regexp.MustCompile(instancePaths)
	for _, p := range []string{
		"/api/v4/projects/devops/ci-cd-components/lint-tools/releases",
		"/api/v4/projects/devops/images/pinup/repository/tags",
		"/api/v4/projects/devops/renovate-runner/repository/files/default.json/raw",
		"/api/v4/projects/790/packages/npm/@koh/x",
		"/api/v4/projects/1/packages",
		"/api/v4/group/1210/-/packages/composer/packages.json",
		"/api/v4/user",
		"/api/v4/personal_access_tokens/self",
	} {
		if !re.MatchString(p) {
			t.Errorf("%s must carry the token", p)
		}
	}
	for _, p := range []string{
		"/api/v4/groups/1210/variables",
		"/api/v4/projects/826/variables",
		"/api/v4/projects/826/variables/PINUP_GITLAB_TOKEN",
		"/api/v4/users",
		"/api/v4/projects",
		"/api/v4/projects/826/members",
		"/api/v4/projects/826/pipelines",
		"/devops/images/pinup/-/raw/main/Containerfile",
	} {
		if re.MatchString(p) {
			t.Errorf("%s must not carry the token", p)
		}
	}
}

// The rotation's targets: bare keys under --scope, keys with a scope of
// their own, and the shapes that are refused.
func TestParseTargets(t *testing.T) {
	got, err := parseTargets("PINUP_GITLAB_TOKEN, projects/826:PINUP_GITLAB_TOKEN,GITLAB_TOKEN", "groups/1210")
	if err != nil || len(got) != 3 || got[0].Scope != "groups/1210" || got[1].Scope != "projects/826" || got[1].Key != "PINUP_GITLAB_TOKEN" || got[2].Key != "GITLAB_TOKEN" {
		t.Errorf("got %+v %v", got, err)
	}
	for _, bad := range []string{"", "A", "users/1:A"} {
		if _, err := parseTargets(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	var out, errw strings.Builder
	t.Setenv("PINUP_GITLAB_URL", "https://git.example.org")
	t.Setenv("PINUP_GITLAB_TOKEN", "glpat-x")
	t.Setenv("PINUP_READ_TOKEN", "")
	if err := cmdToken([]string{"rotate", "--variables", "projects/827:A", "--also", "PINUP_READ_TOKEN=projects/827:B"}, &out, &errw); err == nil || !strings.Contains(err.Error(), "not set in this environment") {
		t.Errorf("an --also token that is not in the environment: %v", err)
	}
}

// PINUP_APK_VIEWS adds native apk views over the public Wolfi default and
// may replace it; a malformed value or a view without a mirror is an
// error, not a silently empty index.
func TestApkViewsFromEnv(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	views, err := apkViews(env(""))
	if err != nil || len(views) != 1 || len(views["custom.wolfi"].Mirrors) != 1 {
		t.Fatalf("default views = %v, %v", views, err)
	}
	views, err = apkViews(env(`{"custom.corp-apk": {"mirrors": ["https://pkgs.example.test"]}, "custom.wolfi": {"mirrors": ["https://packages.wolfi.dev/os", "https://mirror.example.test"], "arches": ["x86_64"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if v := views["custom.corp-apk"]; len(v.Mirrors) != 1 || len(v.Arches) != 2 {
		t.Errorf("corp-apk = %+v; arches should default to both", v)
	}
	if v := views["custom.wolfi"]; len(v.Mirrors) != 2 || len(v.Arches) != 1 {
		t.Errorf("wolfi = %+v; the default view should be replaced", v)
	}
	for _, bad := range []string{`{"corp-apk": {"mirrors": ["x"]}}`, `{"custom.x": {}}`, `not json`} {
		if _, err := apkViews(env(bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}
