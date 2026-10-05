// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package osv

import (
	"context"
	"testing"

	"github.com/ohartwig/pinup/versioning"
)

// A custom datasource is asked under the ecosystem its configuration names,
// and only for queries written in that ecosystem's versioning: the same
// custom.wolfi serves Containerfile apk pins and composer's php platform
// entries, and only the apk pins are Wolfi package versions. A custom
// datasource that names none is not asked at all.
func TestCheckAsksANamedEcosystemOnlyInItsVersioning(t *testing.T) {
	srv := newFakeOSV()
	srv.docs["CGA-test-0001"] = vulnDoc{
		ID: "CGA-test-0001", Modified: "2026-10-01T00:00:00Z",
		Affected: []affected{{Package: pkgRef{Name: "openssl", Ecosystem: "Wolfi"},
			Ranges: []rangeEntry{{Type: "ECOSYSTEM", Events: []event{{Introduced: "0"}, {Fixed: "3.0.8"}}}}}},
	}
	srv.vulns[tupleKey("openssl", "Wolfi", "3.0.0")] = []vulnRef{{ID: "CGA-test-0001", Modified: "2026-10-01T00:00:00Z"}}
	client, _, _ := newFixture(t, srv)
	reg := versioning.Registry{"apk": testScheme{}, "semver": testScheme{}}

	for _, tc := range []struct {
		name      string
		query     Query
		ecosystem string
		fixed     string
	}{
		{"apk pin under Wolfi", Query{Datasource: "custom.wolfi", PackageName: "openssl", Version: "3.0.0", Versioning: "apk", Ecosystem: "Wolfi"}, "Wolfi", "3.0.8"},
		{"php platform entry, semver, same datasource", Query{Datasource: "custom.wolfi", PackageName: "openssl", Version: "3.0.0", Versioning: "semver", Ecosystem: "Wolfi"}, "", ""},
		{"custom datasource naming no ecosystem", Query{Datasource: "custom.koh-apk", PackageName: "openssl", Version: "3.0.0", Versioning: "apk"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Asks(tc.query); got != (tc.ecosystem != "") {
				t.Errorf("Asks = %t", got)
			}
			findings, err := client.Check(context.Background(), reg, []Query{tc.query})
			if err != nil {
				t.Fatal(err)
			}
			f := findings[0]
			if f.Ecosystem != tc.ecosystem {
				t.Errorf("ecosystem %q, want %q", f.Ecosystem, tc.ecosystem)
			}
			if f.Bound != tc.fixed {
				t.Errorf("bound %q, want %q", f.Bound, tc.fixed)
			}
		})
	}
	if srv.batchCalls != 1 {
		t.Errorf("querybatch calls %d, want 1: only the apk pin is asked", srv.batchCalls)
	}
}
