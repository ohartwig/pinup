// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package rules

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// matchSourceUrls against the table captured by tools/capture/sourceurls.sh:
// every pattern form the config uses, tried against every URL shape a
// datasource reports, through Renovate's own resolver. The table settles
// three things reading the docs would not: a glob or exact pattern tolerates
// a trailing slash on the URL but not the other way round, a regex sees the
// URL as-is (so `^…$` does not admit the slash and `\/$` demands it), and an
// absent sourceUrl matches nothing - not even a lone negative pattern.
func TestMatchSourceUrlsAgreesWithTheCapturedTable(t *testing.T) {
	f, err := os.Open("../testdata/parity/renovate-43.288.0/rules/source-urls.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("empty table")
	}
	var header struct{ Patterns, URLs int }
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		t.Fatal(err)
	}

	engines := map[string]*Engine{}
	rows, matched := 0, 0
	for sc.Scan() {
		var row struct {
			Pattern   string  `json:"pattern"`
			SourceURL *string `json:"sourceUrl"`
			Matched   bool    `json:"matched"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows++
		if row.Matched {
			matched++
		}
		eng, ok := engines[row.Pattern]
		if !ok {
			eng = rulesOf(t, map[string]any{"matchSourceUrls": []any{row.Pattern}, "enabled": false})
			engines[row.Pattern] = eng
		}
		s := Subject{DepName: "x", PackageName: "x", Datasource: "npm", CurrentValue: "1.0.0"}
		url := "<absent>"
		if row.SourceURL != nil {
			s.SourceURL = *row.SourceURL
			url = *row.SourceURL
		}
		res := eng.Apply(map[string]any{"enabled": true}, s)
		if got := res.Config["enabled"] == false; got != row.Matched {
			t.Errorf("pattern %q against %q: matched=%v, Renovate says %v", row.Pattern, url, got, row.Matched)
		}
	}
	if want := header.Patterns * header.URLs; rows != want || len(engines) != header.Patterns {
		t.Fatalf("read %d rows over %d patterns, header promises %d over %d", rows, len(engines), want, header.Patterns)
	}
	// The table has to be able to fail: both outcomes must be present.
	if matched == 0 || matched == rows {
		t.Fatalf("%d of %d rows matched - a table that cannot discriminate proves nothing", matched, rows)
	}
}
