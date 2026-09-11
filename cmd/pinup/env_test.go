// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import "testing"

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
}
