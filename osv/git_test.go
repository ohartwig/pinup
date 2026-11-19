// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package osv

import (
	"net/http"
	"testing"
	"time"
)

// A repository is asked by its URL in the GIT ecosystem, one query per
// tag in one batch, and the answer comes back per tag in the order asked -
// with each record's modification time, which is what a caller needs to
// tell "not listed" from "not looked at yet". The shape is OSV's answer for
// Cisco-Talos/clamav on 2026-11-19: clamav-1.5.4 under three OSS-Fuzz
// records, clamav-1.5.5 under none.
func TestTagAdvisoriesAsksTheRepositoryByTag(t *testing.T) {
	srv := newFakeOSV()
	repo := "https://github.com/Cisco-Talos/clamav"
	srv.vulns[tupleKey(repo, "GIT", "clamav-1.5.4")] = []vulnRef{
		{ID: "OSV-2022-94", Modified: "2026-10-19T14:51:34.908103Z"},
		{ID: "OSV-2022-636", Modified: "2026-10-19T14:40:26.305945Z"},
	}
	srv.docs["OSV-2022-94"] = vulnDoc{ID: "OSV-2022-94", Summary: "Heap-buffer-overflow in cli_strlcat", Published: "2022-01-20T00:00:52Z", Modified: "2026-10-19T14:51:34.908103Z"}
	client, _, _ := newFixture(t, srv)

	hits, err := client.TagAdvisories(t.Context(), repo, []string{"clamav-1.5.4", "clamav-1.5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || len(hits[0]) != 2 || len(hits[1]) != 0 {
		t.Fatalf("hits %+v, want two records for 1.5.4 and none for 1.5.5", hits)
	}
	if want := time.Date(2026, 10, 19, 14, 51, 34, 908103000, time.UTC); hits[0][0].ID != "OSV-2022-94" || !hits[0][0].Modified.Equal(want) {
		t.Errorf("first hit %+v, want OSV-2022-94 modified %s", hits[0][0], want)
	}
	if srv.batchCalls != 1 || srv.batchSizes[0] != 2 {
		t.Errorf("batches %d of sizes %v, want one of two", srv.batchCalls, srv.batchSizes)
	}

	rec, err := client.Record(t.Context(), "OSV-2022-94", hits[0][0].Modified)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Summary != "Heap-buffer-overflow in cli_strlcat" || rec.Fixed != "" || rec.Published.IsZero() {
		t.Errorf("record %+v", rec)
	}
}

// OSV that cannot be asked is an error, not an empty answer: a caller
// must not read it as "no tag is affected".
func TestTagAdvisoriesFailsLoudly(t *testing.T) {
	srv := newFakeOSV()
	srv.batchStatus = http.StatusServiceUnavailable
	client, _, _ := newFixture(t, srv)
	if hits, err := client.TagAdvisories(t.Context(), "https://github.com/a/b", []string{"v1.0.0"}); err == nil {
		t.Fatalf("no error, hits %+v", hits)
	}
	if hits, err := client.TagAdvisories(t.Context(), "", []string{"v1.0.0"}); err != nil || hits != nil {
		t.Errorf("no repository: %v %v, want nothing asked", hits, err)
	}
}

func TestRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Cisco-Talos/clamav":     "https://github.com/Cisco-Talos/clamav",
		"https://github.com/Cisco-Talos/clamav.git": "https://github.com/Cisco-Talos/clamav",
		"https://github.com/caddyserver/caddy/":     "https://github.com/caddyserver/caddy",
		"http://github.com/a/b":                     "https://github.com/a/b",
		"git@github.com:a/b.git":                    "",
		"https://github.com":                        "",
		"":                                          "",
		"https://gitlab.example/group/sub/project":  "https://gitlab.example/group/sub/project",
	} {
		if got := RepoURL(in); got != want {
			t.Errorf("RepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}
