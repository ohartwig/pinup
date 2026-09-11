// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package customds

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/fake/harness"
	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

func client(rt http.RoundTripper) *httpx.Client {
	return httpx.New(httpx.Options{Transport: rt, Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}})
}

// The estate's protonpass datasource, against the payload shape the
// endpoint serves.
func TestProtonpassTransform(t *testing.T) {
	served := 0
	rt := harness.NewRefusingTransport(t).Handle("proton.me", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		if r.URL.Path != "/download/pass-cli/versions.json" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"passCliVersions":[{"version":"1.2.3","os":"linux"},{"version":"1.3.0","os":"linux"}]}`))
	}))
	ds := New(model.CustomDatasource{
		Name: "protonpass", Format: "json",
		DefaultRegistryURLTemplate: "https://proton.me/download/pass-cli/versions.json",
		TransformTemplates:         []string{`{ "releases": [ { "version": passCliVersions.version } ] }`},
	}, client(rt))
	if ds.Name() != "custom.protonpass" {
		t.Errorf("name %q", ds.Name())
	}
	rs, err := ds.Releases(context.Background(), lookup.Ref{Datasource: "custom.protonpass", PackageName: "pass-cli"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs.Releases {
		got = append(got, r.Version)
	}
	if strings.Join(got, ",") != "1.2.3,1.3.0" || served != 1 {
		t.Errorf("versions %v, served %d", got, served)
	}
}

func TestPackageNameTemplateAndPlainFormat(t *testing.T) {
	rt := harness.NewRefusingTransport(t).Handle("idx.example.org", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go-1.27.txt" {
			w.Write([]byte("# versions\n1.27.0-r0\n\n1.27.1-r0\n"))
			return
		}
		w.WriteHeader(404)
	}))
	ds := New(model.CustomDatasource{Name: "plainidx", Format: "plain", DefaultRegistryURLTemplate: "https://idx.example.org/{{packageName}}.txt"}, client(rt))
	rs, err := ds.Releases(context.Background(), lookup.Ref{PackageName: "go-1.27"})
	if err != nil || len(rs.Releases) != 2 || rs.Releases[1].Version != "1.27.1-r0" {
		t.Fatalf("plain: %+v %v", rs, err)
	}
	// A transform that yields no releases list is an error, not an empty
	// answer; a missing package is the status error.
	bad := New(model.CustomDatasource{Name: "bad", Format: "plain", DefaultRegistryURLTemplate: "https://idx.example.org/{{packageName}}.txt",
		TransformTemplates: []string{`{ "nothing": releases }`}}, client(rt))
	if _, err := bad.Releases(context.Background(), lookup.Ref{PackageName: "go-1.27"}); err == nil || !strings.Contains(err.Error(), "no releases list") {
		t.Errorf("want a no-releases error, got %v", err)
	}
	if _, err := ds.Releases(context.Background(), lookup.Ref{PackageName: "nonesuch"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("want a 404 error, got %v", err)
	}
}
