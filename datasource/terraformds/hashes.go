// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package terraformds

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// ProviderHashes computes what a .terraform.lock.hcl records for one
// provider version: a "zh:" hash for every platform's zip, read off the
// release's SHA256SUMS, and an "h1:" hash for every platform, which is the
// hash of the zip's *contents* (the sum of each file, one line per file,
// sorted - the same scheme go.sum uses for a module) and can only be had by
// downloading the zip. Renovate does the same, one download per platform;
// `tofu providers lock` would too, and would need the binary in the
// toolchain. The platforms are the release's, all of them, which is what a
// lock written by `tofu init` on one machine and completed by
// `providers lock` carries (measured: koh-infra, fifteen h1 per provider).
//
// Sorted as the lock sorts them: h1 first, zh second, each group by value.
func (d *Datasource) ProviderHashes(ctx context.Context, ref lookup.Ref, version string) ([]string, error) {
	if d.kind != Provider {
		return nil, fmt.Errorf("%s: hashes are a provider's", d.kind)
	}
	namespace, name, err := splitProviderName(ref.PackageName)
	if err != nil {
		return nil, err
	}
	origin := d.registryFor(ref)
	providersBase, _ := d.discover(ctx, origin)
	base := providersBase + "/" + namespace + "/" + name

	resp, err := d.client.Get(ctx, base+"/versions", httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("terraform-provider: %s: versions: %w", ref.PackageName, err)
	}
	var doc struct {
		Versions []struct {
			Version   string `json:"version"`
			Platforms []struct {
				OS   string `json:"os"`
				Arch string `json:"arch"`
			} `json:"platforms"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return nil, fmt.Errorf("terraform-provider: %s: versions: %w", ref.PackageName, err)
	}
	var platforms []string
	for _, v := range doc.Versions {
		if strings.TrimPrefix(v.Version, "v") != strings.TrimPrefix(version, "v") {
			continue
		}
		for _, p := range v.Platforms {
			platforms = append(platforms, p.OS+"/"+p.Arch)
		}
	}
	if len(platforms) == 0 {
		return nil, fmt.Errorf("terraform-provider: %s: version %s lists no platforms at %s", ref.PackageName, version, origin)
	}
	sort.Strings(platforms)

	var hashes []string
	seenZH := false
	for _, p := range platforms {
		resp, err := d.client.Get(ctx, base+"/"+version+"/download/"+p, httpx.ReqOptions{Accept: "application/json"})
		if err != nil {
			return nil, fmt.Errorf("terraform-provider: %s %s %s: %w", ref.PackageName, version, p, err)
		}
		var dl struct {
			DownloadURL string `json:"download_url"`
			ShasumsURL  string `json:"shasums_url"`
		}
		if err := json.Unmarshal(resp.Body, &dl); err != nil || dl.DownloadURL == "" {
			return nil, fmt.Errorf("terraform-provider: %s %s %s: no download_url", ref.PackageName, version, p)
		}
		if !seenZH {
			sums, err := d.client.Get(ctx, dl.ShasumsURL, httpx.ReqOptions{})
			if err != nil {
				return nil, fmt.Errorf("terraform-provider: %s %s: SHA256SUMS: %w", ref.PackageName, version, err)
			}
			for _, zh := range zipSums(sums.Body) {
				hashes = append(hashes, "zh:"+zh)
			}
			seenZH = true
		}
		h1, err := d.zipHash1(ctx, dl.DownloadURL)
		if err != nil {
			return nil, fmt.Errorf("terraform-provider: %s %s %s: %w", ref.PackageName, version, p, err)
		}
		hashes = append(hashes, h1)
	}
	sort.Strings(hashes) // "h1:" sorts before "zh:", each group by value
	return hashes, nil
}

// zipSums reads the sha256 of every .zip a SHA256SUMS file lists.
func zipSums(sums []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.HasSuffix(fields[1], ".zip") {
			out = append(out, fields[0])
		}
	}
	return out
}

// zipHash1 downloads a zip and returns its h1 hash: for every entry, the
// hex sha256 of its contents and its name on one line, "%x  %s\n", the
// lines sorted by name, the sha256 of that, base64. The zip's directory is
// at its end, so the whole file is held once; a provider's zip is tens of
// megabytes, which is what a lock costs.
func (d *Datasource) zipHash1(ctx context.Context, rawURL string) (string, error) {
	var data []byte
	err := d.client.Stream(ctx, rawURL, func(r io.Reader) error {
		var err error
		data, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("zip: %w", err)
	}
	type entry struct{ name, sum string }
	entries := make([]entry, 0, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("zip %s: %w", f.Name, err)
		}
		h := sha256.New()
		_, err = io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("zip %s: %w", f.Name, err)
		}
		entries = append(entries, entry{f.Name, fmt.Sprintf("%x", h.Sum(nil))})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s  %s\n", e.sum, e.name)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
