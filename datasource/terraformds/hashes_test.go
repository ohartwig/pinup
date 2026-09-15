// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package terraformds

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/lookup"
)

// releases is a server standing in for releases.hashicorp.com: one zip per
// platform and the SHA256SUMS that lists them. The datasource is told
// where they are by the registry's download endpoint, never by
// convention.
type releases struct {
	zips map[string][]byte // file name -> bytes
}

func (r *releases) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	name := strings.TrimPrefix(req.URL.Path, "/")
	if name == "SHA256SUMS" {
		var b strings.Builder
		for n, z := range r.zips {
			fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(z), n)
		}
		// A release's SHA256SUMS also lists its manifest, which is no zip.
		b.WriteString("0000000000000000000000000000000000000000000000000000000000000000  terraform-provider-tls_4.4.1_manifest.json\n")
		_, _ = w.Write([]byte(b.String()))
		return
	}
	z, ok := r.zips[name]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	_, _ = w.Write(z)
}

// buildZip writes files into a zip in the order given; the h1 hash must
// not depend on that order.
func buildZip(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(f[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// downloadRegistry extends the fake registry with the platforms a version
// lists and the download document per platform.
type downloadRegistry struct {
	*registry
	platforms map[string][]string // version -> os/arch
	host      string
}

func (d *downloadRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rest := strings.TrimPrefix(req.URL.Path, d.providersPath+"/")
	switch {
	case strings.HasSuffix(rest, "/versions"):
		var body struct {
			Versions []map[string]any `json:"versions"`
		}
		for v, ps := range d.platforms {
			var plats []map[string]string
			for _, p := range ps {
				os, arch, _ := strings.Cut(p, "/")
				plats = append(plats, map[string]string{"os": os, "arch": arch})
			}
			body.Versions = append(body.Versions, map[string]any{"version": v, "platforms": plats})
		}
		writeJSON(w, body)
	case strings.Contains(rest, "/download/"):
		// hashicorp/tls/4.4.1/download/linux/amd64
		parts := strings.Split(rest, "/")
		version, os, arch := parts[2], parts[4], parts[5]
		if !slices.Contains(d.platforms[version], os+"/"+arch) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{
			"download_url": fmt.Sprintf("https://%s/terraform-provider-tls_%s_%s_%s.zip", d.host, version, os, arch),
			"shasums_url":  fmt.Sprintf("https://%s/SHA256SUMS", d.host),
		})
	default:
		d.registry.ServeHTTP(w, req)
	}
}

func TestProviderHashesAreTheLocksH1AndZh(t *testing.T) {
	f := newFixture(t, "registry.terraform.io")
	linux := buildZip(t, [][2]string{{"terraform-provider-tls_v4.4.1_x5", "ELF linux amd64 build\n"}, {"LICENSE.txt", "MPL-2.0\n"}})
	darwin := buildZip(t, [][2]string{{"LICENSE.txt", "MPL-2.0\n"}, {"terraform-provider-tls_v4.4.1_x5", "Mach-O darwin arm64 build\n"}})
	rel := &releases{zips: map[string][]byte{
		"terraform-provider-tls_4.4.1_linux_amd64.zip":  linux,
		"terraform-provider-tls_4.4.1_darwin_arm64.zip": darwin,
	}}
	f.rt.Handle("releases.example", rel)
	f.rt.Handle("registry.terraform.io", &downloadRegistry{
		registry:  f.reg,
		platforms: map[string][]string{"4.4.1": {"linux/amd64", "darwin/arm64"}, "4.4.0": {"linux/amd64"}},
		host:      "releases.example",
	})

	ds := New(Provider, f.client, "https://registry.terraform.io")
	got, err := ds.ProviderHashes(t.Context(), lookup.Ref{Datasource: "terraform-provider", PackageName: "hashicorp/tls"}, "4.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if f.rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", f.rec.String())
	}
	// The h1 values were computed by an independent implementation of the
	// dirhash Hash1 scheme (python: sha256 per file, "%x  %s\n" lines sorted
	// by name, base64 of their sha256) over the same contents; the darwin
	// zip's entries are written in the other order to prove sorting.
	want := []string{
		"h1:BPV6WX0WJd3L9/GJcOPtmqZ4W6RjyCfiN26mrQJkoHg=",
		"h1:GoMgMwtJZKf8gJeflxSlHn42fB56IWfHya6jyepjrpM=",
		fmt.Sprintf("zh:%x", sha256.Sum256(darwin)),
		fmt.Sprintf("zh:%x", sha256.Sum256(linux)),
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("hashes:\n got %q\nwant %q", got, want)
	}
	if n := f.reg.requestCount("/SHA256SUMS"); n != 0 {
		t.Errorf("SHA256SUMS asked of the registry %d times; it lives on the release host", n)
	}

	if _, err := ds.ProviderHashes(t.Context(), lookup.Ref{Datasource: "terraform-provider", PackageName: "hashicorp/tls"}, "9.9.9"); err == nil {
		t.Error("a version the registry does not list must be an error, not an empty lock entry")
	}
}

func TestAZipEntryLyingAboutItsSizeIsRefused(t *testing.T) {
	f := newFixture(t, "registry.terraform.io")
	z := buildZip(t, [][2]string{{"terraform-provider-tls_v4.4.1_x5", strings.Repeat("x", 4096)}})
	// Rewrite the central directory's uncompressed-size claim to 10 bytes
	// (offset 24 of the "PK\x01\x02" record): the entry then inflates past
	// what its header promised. archive/zip refuses that itself once the
	// stream passes the claim, and the copy is bounded by the claim on top
	// of it; either way no hash comes out of it.
	i := bytes.Index(z, []byte("PK\x01\x02"))
	if i < 0 {
		t.Fatal("no central directory")
	}
	copy(z[i+24:], []byte{10, 0, 0, 0})
	rel := &releases{zips: map[string][]byte{"terraform-provider-tls_4.4.1_linux_amd64.zip": z}}
	f.rt.Handle("releases.example", rel)
	f.rt.Handle("registry.terraform.io", &downloadRegistry{
		registry:  f.reg,
		platforms: map[string][]string{"4.4.1": {"linux/amd64"}},
		host:      "releases.example",
	})
	ds := New(Provider, f.client, "https://registry.terraform.io")
	_, err := ds.ProviderHashes(t.Context(), lookup.Ref{Datasource: "terraform-provider", PackageName: "hashicorp/tls"}, "4.4.1")
	if err == nil || !strings.Contains(err.Error(), "terraform-provider-tls_v4.4.1_x5") {
		t.Fatalf("err = %v, want the size lie refused, naming the entry", err)
	}
}
