// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package tfversion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

// The corpus: both .terraform-version files of the synthetic tree, as
// Renovate extracted them. The value is taken verbatim, v included.
func TestAgreesWithTheCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/parity/renovate-43.288.0/extract/tfversion.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus map[string][]struct {
		PackageFile string `json:"packageFile"`
		Deps        []struct {
			CurrentValue string `json:"currentValue"`
			Datasource   string `json:"datasource"`
			DepName      string `json:"depName"`
			SkipReason   string `json:"skipReason"`
		} `json:"deps"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	files := corpus["terraform-version"]
	if len(files) != 2 {
		t.Fatalf("corpus has %d terraform-version files, want 2", len(files))
	}
	for _, pf := range files {
		content, err := os.ReadFile(filepath.Join("../../testdata/parity/synthetic/tfversion", pf.PackageFile))
		if err != nil {
			t.Fatal(err)
		}
		res, err := New().Extract(context.Background(), extract.File{Path: pf.PackageFile, Content: content}, extract.ManagerConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Deps) != 1 || len(pf.Deps) != 1 {
			t.Fatalf("%s: %d deps, corpus %d", pf.PackageFile, len(res.Deps), len(pf.Deps))
		}
		got, want := res.Deps[0], pf.Deps[0]
		if got.DepName != want.DepName || got.Datasource != want.Datasource || got.CurrentValue != want.CurrentValue || got.SkipReason != want.SkipReason {
			t.Errorf("%s:\n got %s %s %q %q\nwant %s %s %q %q", pf.PackageFile,
				got.DepName, got.Datasource, got.CurrentValue, got.SkipReason,
				want.DepName, want.Datasource, want.CurrentValue, want.SkipReason)
		}
		if string(content[got.Locus.ValueStart:got.Locus.ValueEnd]) != got.CurrentValue {
			t.Errorf("%s: locus does not bracket the value", pf.PackageFile)
		}
	}
}

func TestWhitespaceStaysOutsideTheValue(t *testing.T) {
	for _, tc := range []struct {
		src        string
		want       string
		start, end int
	}{
		{"1.9.8\n", "1.9.8", 0, 5},
		{"1.9.8", "1.9.8", 0, 5},
		{"\n\n  v1.7.0  \n", "v1.7.0", 4, 10},
		{"1.9.8\r\n", "1.9.8", 0, 5},
	} {
		res, err := New().Extract(context.Background(), extract.File{Path: ".terraform-version", Content: []byte(tc.src)}, extract.ManagerConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Deps) != 1 {
			t.Fatalf("%q: %d deps", tc.src, len(res.Deps))
		}
		d := res.Deps[0]
		if d.CurrentValue != tc.want || d.Locus.ValueStart != tc.start || d.Locus.ValueEnd != tc.end {
			t.Errorf("%q: got %q at [%d:%d], want %q at [%d:%d]", tc.src, d.CurrentValue, d.Locus.ValueStart, d.Locus.ValueEnd, tc.want, tc.start, tc.end)
		}
	}
	res, _ := New().Extract(context.Background(), extract.File{Path: ".terraform-version", Content: []byte("  \n\n")}, extract.ManagerConfig{})
	if len(res.Deps) != 0 {
		t.Errorf("a blank file yielded %d deps", len(res.Deps))
	}
}

func TestEditReplacesOnlyTheVersion(t *testing.T) {
	src := []byte("  v1.7.0  \n")
	f := extract.File{Path: "infra/.terraform-version", Content: src}
	res, _ := New().Extract(context.Background(), f, extract.ManagerConfig{})
	up := model.Update{Dep: res.Deps[0], NewValue: "v1.9.8"}
	e, err := New().Edit(context.Background(), f, up)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(src[:e.Start]) + e.New + string(src[e.End:]); got != "  v1.9.8  \n" {
		t.Errorf("edited file = %q", got)
	}
	f.Content = []byte("  v1.8.0  \n")
	if _, err := New().Edit(context.Background(), f, up); err == nil {
		t.Error("an edit against changed bytes was not refused")
	}
}
