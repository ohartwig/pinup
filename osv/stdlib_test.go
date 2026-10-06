// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package osv

import (
	"context"
	"testing"
)

// The Go toolchain (golang-version) is asked as OSV's Go "stdlib": a
// toolchain release x.y.z is, a language directive like "1.26" is not -
// and is no warning either, only unasked.
func TestTheGoToolchainIsAskedAsStdlib(t *testing.T) {
	srv := newFakeOSV()
	srv.docs["GO-2024-0001"] = vulnDoc{ID: "GO-2024-0001", Modified: "2024-01-01T00:00:00Z",
		Affected: []affected{{Package: pkgRef{Name: "stdlib", Ecosystem: "Go"}, Ranges: []rangeEntry{rangeOf("0", "1.22.5")}}}}
	srv.vulns[tupleKey("stdlib", "Go", "1.22.0")] = []vulnRef{{ID: "GO-2024-0001", Modified: "2024-01-01T00:00:00Z"}}
	client, _, _ := newFixture(t, srv)
	findings, err := client.Check(context.Background(), registry(), []Query{
		{Datasource: "golang-version", PackageName: "go", Version: "1.22.0", Versioning: "test"},
		{Datasource: "golang-version", PackageName: "go", Version: "1.26", Versioning: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if findings[0].Ecosystem != "Go" || len(findings[0].Advisories) != 1 || findings[0].Bound != "1.22.5" {
		t.Errorf("1.22.0: %+v", findings[0])
	}
	if findings[1].Ecosystem != "" || len(findings[1].Warnings) != 0 {
		t.Errorf("1.26 is a language version: asked %q, warnings %v", findings[1].Ecosystem, findings[1].Warnings)
	}
	if len(srv.batchSizes) != 1 || srv.batchSizes[0] != 1 {
		t.Errorf("batches %v, want one query", srv.batchSizes)
	}
}

func TestStdlibVersion(t *testing.T) {
	for v, want := range map[string]bool{"1.22.0": true, "v1.27.1": true, "1.26": false, "1.27rc1": false, "go1.27.0": false} {
		if StdlibVersion(v) != want {
			t.Errorf("StdlibVersion(%q) = %v", v, !want)
		}
	}
}
