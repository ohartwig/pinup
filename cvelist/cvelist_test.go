// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package cvelist

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ohartwig/pinup/httpx"
)

// clamavRecord is CVE-2026-76411 as the CVE List served it on 2026-11-19,
// cut to the fields this package reads: Cisco's CNA names the product
// "Cisco Secure Endpoint" and ClamAV only in the description.
const clamavRecord = `{
 "dataType": "CVE_RECORD", "dataVersion": "5.1",
 "cveMetadata": {"cveId": "CVE-2026-76411", "state": "PUBLISHED", "assignerShortName": "cisco",
  "dateReserved": "2026-08-19T12:02:03.632Z", "datePublished": "2026-11-18T16:30:52.793Z", "dateUpdated": "2026-11-18T16:46:45.806Z"},
 "containers": {
  "cna": {
   "descriptions": [{"lang": "en", "value": "A vulnerability in the phishcheck functionality of ClamAV could allow an unauthenticated, remote attacker to cause a DoS condition on an affected device.\r\n\r\nThis vulnerability is due to improper allocation of memory."}],
   "affected": [{"vendor": "Cisco", "product": "Cisco Secure Endpoint", "versions": [{"version": "6.0.9", "status": "affected"}]}],
   "metrics": [{"format": "cvssV3_1", "cvssV3_1": {"version": "3.1", "vectorString": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H", "baseScore": 7.5}}]
  },
  "adp": [{"title": "CISA ADP Vulnrichment"}]
 }
}`

func TestParseReadsWhatAConfirmationNeeds(t *testing.T) {
	r, err := Parse([]byte(clamavRecord))
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "CVE-2026-76411" || r.State != Published {
		t.Errorf("id %q state %q", r.ID, r.State)
	}
	if want := time.Date(2026, 11, 18, 16, 30, 52, 793000000, time.UTC); !r.Published.Equal(want) {
		t.Errorf("published %s, want %s", r.Published, want)
	}
	if want := "A vulnerability in the phishcheck functionality of ClamAV could allow an unauthenticated, remote attacker to cause a DoS condition on an affected device."; r.Title != want {
		t.Errorf("title %q, want the description's first sentence", r.Title)
	}
	if r.Severity != "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H" {
		t.Errorf("severity %q", r.Severity)
	}
	for name, want := range map[string]bool{"clamav": true, "ClamAV": true, "cisco secure endpoint": true, "clam": false, "av": false, "x": false, "": false, "caddy": false} {
		if got := r.Mentions(name); got != want {
			t.Errorf("Mentions(%q) = %t, want %t", name, got, want)
		}
	}
	if _, err := Parse([]byte(`{"containers": {}}`)); err == nil {
		t.Error("a document without an id parsed")
	}
}

func TestIDsFindsEveryCVEOnce(t *testing.T) {
	notes := "- [CVE-2026-76422](https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2026-76422): fixed\n- cve-2026-76411 and CVE-2026-76422 again; not CVE-26-1 or XCVE-2026-1234x"
	if got, want := IDs(notes), []string{"CVE-2026-76422", "CVE-2026-76411"}; !slices.Equal(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}
}

type memStore map[string][]byte

func (m memStore) Get(key string, _ time.Duration, _ time.Time) ([]byte, bool, bool) {
	p, ok := m[key]
	return p, ok, ok
}
func (m memStore) Put(key string, payload []byte, _ time.Time) { m[key] = payload }

// A record is fetched once and kept; an id the CVE List does not know, or
// a server that does not answer, is an error - it confirms nothing.
func TestGetFetchesOnceAndFailsClosed(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/cve/CVE-2026-76411":
			w.Write([]byte(clamavRecord))
		case "/api/cve/CVE-2026-99999":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"CVE_RECORD_DNE"}`))
		}
	}))
	defer srv.Close()
	store := memStore{}
	c := &Client{HTTP: httpx.New(httpx.Options{Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}}), Base: srv.URL, Store: store, Now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	for range 2 {
		r, err := c.Get(t.Context(), "cve-2026-76411")
		if err != nil || r.ID != "CVE-2026-76411" {
			t.Fatalf("record %+v, err %v", r, err)
		}
	}
	if calls != 1 {
		t.Errorf("%d requests, want one: the second answer is the cached record", calls)
	}
	for _, id := range []string{"CVE-2026-00001", "CVE-2026-99999", "not-a-cve"} {
		if _, err := c.Get(t.Context(), id); err == nil {
			t.Errorf("%s: no error", id)
		}
	}
}
