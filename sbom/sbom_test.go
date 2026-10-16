// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"reflect"
	"testing"
)

// The shapes measured on 2026-10-16: syft's CycloneDX for an image
// (attest:sbom in devops/images/monitoring-grafana), Trivy's report for a
// published apk (audit:published in devops/wolfi-packages), an SPDX document
// as melange writes it, and `go version -m` output.
func TestParseReadsEveryFormat(t *testing.T) {
	for _, c := range []struct {
		name, format, doc string
		want              []Component
	}{
		{"cyclonedx", "cyclonedx", `{"bomFormat":"CycloneDX","specVersion":"1.7","components":[
			{"type":"library","name":"github.com/getkin/kin-openapi","version":"v0.144.0","purl":"pkg:golang/github.com/getkin/kin-openapi@v0.144.0"},
			{"type":"library","name":"stdlib","version":"v1.26.9","purl":"pkg:golang/stdlib@v1.26.9"},
			{"type":"library","name":"busybox","version":"1.37.0-r51","purl":"pkg:apk/wolfi/busybox@1.37.0-r51?arch=aarch64"},
			{"type":"library","group":"@types","name":"node","version":"22.0.0","purl":"pkg:npm/%40types/node@22.0.0"},
			{"type":"file","name":"/usr/bin/grafana"}]}`,
			[]Component{
				{Type: "golang", Name: "github.com/getkin/kin-openapi", Version: "v0.144.0", PURL: "pkg:golang/github.com/getkin/kin-openapi@v0.144.0"},
				{Type: "golang", Name: "stdlib", Version: "v1.26.9", PURL: "pkg:golang/stdlib@v1.26.9"},
				{Type: "apk", Name: "wolfi/busybox", Version: "1.37.0-r51", PURL: "pkg:apk/wolfi/busybox@1.37.0-r51?arch=aarch64"},
				{Type: "npm", Name: "@types/node", Version: "22.0.0", PURL: "pkg:npm/%40types/node@22.0.0"},
				{Type: "", Name: "/usr/bin/grafana", Version: ""},
			}},
		{"trivy", "trivy", `{"SchemaVersion":2,"ArtifactType":"filesystem","Results":[{"Target":"usr/bin/cosign","Class":"lang-pkgs","Type":"gobinary","Packages":[
			{"ID":"github.com/sigstore/cosign/v3@v3.1.3+dirty","Name":"github.com/sigstore/cosign/v3","Identifier":{"PURL":"pkg:golang/github.com/sigstore/cosign/v3@v3.1.3%2Bdirty"},"Version":"v3.1.3+dirty","Relationship":"root"},
			{"Name":"golang.org/x/crypto","Identifier":{"PURL":"pkg:golang/golang.org/x/crypto@v0.57.0"},"Version":"v0.57.0"}]}]}`,
			[]Component{
				{Type: "golang", Name: "github.com/sigstore/cosign/v3", Version: "v3.1.3+dirty", PURL: "pkg:golang/github.com/sigstore/cosign/v3@v3.1.3%2Bdirty"},
				{Type: "golang", Name: "golang.org/x/crypto", Version: "v0.57.0", PURL: "pkg:golang/golang.org/x/crypto@v0.57.0"},
			}},
		{"spdx", "spdx", `{"spdxVersion":"SPDX-2.3","packages":[
			{"name":"grafana","versionInfo":"13.2.4-r1","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:apk/koh/grafana@13.2.4-r1?arch=aarch64"}]},
			{"name":"google.golang.org/grpc","versionInfo":"v1.83.2","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:golang/google.golang.org/grpc@v1.83.2"}]},
			{"name":"SPDXRef-DOCUMENT-only","versionInfo":""}]}`,
			[]Component{
				{Type: "apk", Name: "koh/grafana", Version: "13.2.4-r1", PURL: "pkg:apk/koh/grafana@13.2.4-r1?arch=aarch64"},
				{Type: "golang", Name: "google.golang.org/grpc", Version: "v1.83.2", PURL: "pkg:golang/google.golang.org/grpc@v1.83.2"},
				{Type: "", Name: "SPDXRef-DOCUMENT-only"},
			}},
		{"go version -m", "go-modules", "/usr/bin/grafana: go1.27.2\n\tpath\tgithub.com/grafana/grafana/pkg/cmd/grafana\n\tmod\tgithub.com/grafana/grafana\t(devel)\n\tdep\tgithub.com/getkin/kin-openapi\tv0.144.0\th1:abc=\n\tdep\tgolang.org/x/net\tv0.58.0\t=>\tgolang.org/x/net\tv0.61.0\th1:def=\n\tbuild\t-buildmode=exe\n",
			[]Component{
				{Type: "golang", Name: "github.com/grafana/grafana"},
				{Type: "golang", Name: "github.com/getkin/kin-openapi", Version: "v0.144.0"},
				{Type: "golang", Name: "golang.org/x/net", Version: "v0.61.0"},
			}},
		{"go list -m all", "go-modules", "github.com/grafana/grafana\ngithub.com/getkin/kin-openapi v0.144.0\n# a comment\n",
			[]Component{
				{Type: "golang", Name: "github.com/grafana/grafana"},
				{Type: "golang", Name: "github.com/getkin/kin-openapi", Version: "v0.144.0"},
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Parse(c.format, []byte(c.doc))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Parse =\n%+v\nwant\n%+v", got, c.want)
			}
		})
	}
}

// A document of the wrong shape is an error that names the format, never
// an empty list that reads as "nothing shipped".
func TestParseRefusesTheWrongShape(t *testing.T) {
	for _, c := range []struct{ name, format, doc string }{
		{"not json", "cyclonedx", "<xml/>"},
		{"spdx as cyclonedx", "cyclonedx", `{"spdxVersion":"SPDX-2.3"}`},
		{"cyclonedx as spdx", "spdx", `{"bomFormat":"CycloneDX"}`},
		{"empty object as trivy", "trivy", `{}`},
		{"unknown format", "syft", `{}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(c.format, []byte(c.doc)); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestParsePURL(t *testing.T) {
	for _, c := range []struct {
		purl               string
		typ, name, version string
		ok                 bool
	}{
		{"pkg:golang/github.com/getkin/kin-openapi@v0.144.0", "golang", "github.com/getkin/kin-openapi", "v0.144.0", true},
		{"pkg:golang/github.com/sigstore/cosign/v3@v3.1.3%2Bdirty", "golang", "github.com/sigstore/cosign/v3", "v3.1.3+dirty", true},
		{"pkg:npm/%40types/node@22.0.0", "npm", "@types/node", "22.0.0", true},
		{"pkg:apk/wolfi/busybox@1.37.0-r51?arch=aarch64", "apk", "wolfi/busybox", "1.37.0-r51", true},
		{"pkg:composer/monolog/monolog@3.8.0", "composer", "monolog/monolog", "3.8.0", true},
		{"pkg:GOLANG/stdlib", "golang", "stdlib", "", true},
		{"cpe:2.3:a:getkin:kin-openapi", "", "", "", false},
		{"pkg:", "", "", "", false},
	} {
		typ, name, version, ok := ParsePURL(c.purl)
		if typ != c.typ || name != c.name || version != c.version || ok != c.ok {
			t.Errorf("ParsePURL(%q) = %q %q %q %t, want %q %q %q %t", c.purl, typ, name, version, ok, c.typ, c.name, c.version, c.ok)
		}
	}
}

// Two binaries in one package carry two copies of a module: the lowest is
// the one an advisory range contains.
func TestLowestKeepsTheLowestVersionPerName(t *testing.T) {
	got := Lowest([]Component{
		{Type: "golang", Name: "golang.org/x/net", Version: "v0.61.0"},
		{Type: "golang", Name: "golang.org/x/net", Version: "v0.58.0"},
		{Type: "golang", Name: "golang.org/x/net", Version: "v0.60.0"},
		{Type: "golang", Name: "github.com/a/b", Version: ""},
		{Type: "golang", Name: "github.com/a/b", Version: "v1.10.0"},
		{Type: "golang", Name: "github.com/a/b", Version: "v1.9.0"},
		{Type: "apk", Name: "wolfi/zlib", Version: "1.3.1-r1"},
	})
	want := []Component{
		{Type: "apk", Name: "wolfi/zlib", Version: "1.3.1-r1"},
		{Type: "golang", Name: "github.com/a/b", Version: "v1.9.0"},
		{Type: "golang", Name: "golang.org/x/net", Version: "v0.58.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lowest =\n%+v\nwant\n%+v", got, want)
	}
}
