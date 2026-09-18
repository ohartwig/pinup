// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRecorderCollectsInsteadOfFailing(t *testing.T) {
	var r Recorder
	if r.Failed() {
		t.Error("a fresh recorder reports failure")
	}
	r.Errorf("something %s happened", "bad")
	if !r.Failed() {
		t.Error("recorder did not register the failure")
	}
	if !r.Mentions("something bad") {
		t.Errorf("recorder lost the message: %q", r.String())
	}
	if r.Mentions("unrelated") {
		t.Error("recorder matched a message it never saw")
	}
}

func TestRefusingTransportServesRegisteredHosts(t *testing.T) {
	var rec Recorder
	rt := NewRefusingTransport(&rec)
	rt.Handle("registry.example", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"tags":["1.0.0"]}`)
	}))

	resp, err := rt.Client().Get("https://registry.example/v2/x/tags/list")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:deadbeef" {
		t.Errorf("header lost: %q", got)
	}
	if !strings.Contains(string(body), "1.0.0") {
		t.Errorf("body lost: %q", body)
	}
	if rec.Failed() {
		t.Errorf("a registered host should not fail the test: %s", rec.String())
	}
	if n := rt.Count("registry.example"); n != 1 {
		t.Errorf("counted %d requests, want 1", n)
	}
}

// The whole point of the transport: an unregistered host fails the test, and
// the message carries the full URL so the escaping call is identifiable.
func TestRefusingTransportRefusesUnregisteredHosts(t *testing.T) {
	var rec Recorder
	rt := NewRefusingTransport(&rec)

	_, err := rt.Client().Get("https://api.github.com/repos/x/y/tags?per_page=100")
	if err == nil {
		t.Error("the request succeeded; it should have been refused")
	}
	if !rec.Failed() {
		t.Fatal("an unregistered host did not fail the test")
	}
	if !rec.Mentions("https://api.github.com/repos/x/y/tags?per_page=100") {
		t.Errorf("the failure does not carry the full URL: %q", rec.String())
	}
}

func TestForbiddenHostIsReportedAndCounted(t *testing.T) {
	var rec Recorder
	rt := NewRefusingTransport(&rec)
	rt.Forbid("developer.mend.io")

	// Asserting on a host nothing touched is the positive proof that a dropped
	// feature is unreachable.
	rt.MustNotHaveBeenCalled("developer.mend.io")
	if rec.Failed() {
		t.Errorf("an untouched forbidden host should pass: %s", rec.String())
	}
	if len(rec.Logs) == 0 {
		t.Error("the assertion passed silently; it should say what it checked")
	}

	// And when something does reach it, the test must go red.
	var rec2 Recorder
	rt2 := NewRefusingTransport(&rec2)
	rt2.Forbid("developer.mend.io")
	_, _ = rt2.Client().Get("https://developer.mend.io/api/v1/confidence")
	if !rec2.Failed() {
		t.Error("reaching a forbidden host did not fail the test")
	}
	rt2.MustNotHaveBeenCalled("developer.mend.io")
	if !rec2.Mentions("received 1 requests") {
		t.Errorf("the count was not reported: %q", rec2.String())
	}
}
