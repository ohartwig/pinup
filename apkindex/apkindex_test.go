// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package apkindex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"slices"
	"strings"
	"testing"
)

// buildIndexTarGz builds an in-memory APKINDEX.tar.gz whose single member is
// named memberName and holds content. Fixtures are built in memory rather
// than read from testdata so the test stays hermetic and self-explaining.
func buildIndexTarGz(t *testing.T, memberName, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name: memberName,
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

// record renders one APKINDEX record from a name and a version, in the shape
// a real APKINDEX carries (other letters interspersed, blank line to close).
func record(name, version string) string {
	return "C:Q1abcdef\nP:" + name + "\nV:" + version + "\nA:x86_64\n\n"
}

func TestParseNormal(t *testing.T) {
	body := record("glibc", "2.39-r1") + record("glibc", "2.40-r0") + record("openssl", "3.3.1-r2")
	idx, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, body)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, c := range []struct {
		pkg  string
		want []string
	}{
		{"glibc", []string{"2.39-r1", "2.40-r0"}},
		{"openssl", []string{"3.3.1-r2"}},
		{"unknown-package", nil},
	} {
		got := idx.Versions(c.pkg)
		if !slices.Equal(got, c.want) {
			t.Errorf("Versions(%q) = %v, want %v", c.pkg, got, c.want)
		}
	}

	wantPackages := []string{"glibc", "openssl"}
	if got := idx.Packages(); !slices.Equal(got, wantPackages) {
		t.Errorf("Packages() = %v, want %v", got, wantPackages)
	}
	if got := idx.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
}

func TestParseRevisionSurvivesVerbatim(t *testing.T) {
	for _, c := range []struct {
		version string
	}{
		{"1.2.3-r0"},
		{"1.2.3-r10"},
		{"20230801-r4"},
		{"1.1.1w-r1"},
	} {
		body := record("openssl", c.version)
		idx, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, body)))
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.version, err)
		}
		got := idx.Versions("openssl")
		want := []string{c.version}
		if !slices.Equal(got, want) {
			t.Errorf("Versions() for %q = %v, want %v", c.version, got, want)
		}
	}
}

func TestParseSafeNameFilter(t *testing.T) {
	for _, c := range []struct {
		name string
		safe bool
	}{
		{"glibc", true},
		{"php-8.3-frankenphp", true},
		{"libxml2", true},
		{"a+b_c.d-e", true},
		{"../etc/passwd", false},
		{"-leading-dash", false},
		{".leading-dot", false},
		{"has space", false},
		{"semicolon;here", false},
	} {
		body := record(c.name, "1.0-r0")
		idx, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, body)))
		if c.safe {
			if err != nil {
				t.Errorf("Parse with safe name %q: unexpected error: %v", c.name, err)
				continue
			}
			if got := idx.Versions(c.name); !slices.Equal(got, []string{"1.0-r0"}) {
				t.Errorf("Versions(%q) = %v, want [1.0-r0]", c.name, got)
			}
			continue
		}
		// An unsafe name is dropped; on its own that leaves zero packages,
		// which Parse must also reject.
		if err == nil {
			t.Errorf("Parse with unsafe name %q: want error (dropped to zero packages), got index with %d packages", c.name, idx.Len())
		}
	}
}

func TestParseUnsafeNameDroppedAmongSafeOnes(t *testing.T) {
	body := record("glibc", "2.39-r1") + record("../etc/passwd", "1.0-r0")
	idx, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, body)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := idx.Versions("../etc/passwd"); got != nil {
		t.Errorf("Versions for unsafe name = %v, want nil (must be skipped)", got)
	}
	if got := idx.Packages(); !slices.Equal(got, []string{"glibc"}) {
		t.Errorf("Packages() = %v, want [glibc]", got)
	}
}

func TestParseCorruptGzip(t *testing.T) {
	full := buildIndexTarGz(t, tarMember, record("glibc", "2.39-r1"))
	truncated := full[:len(full)/2]
	if _, err := Parse(bytes.NewReader(truncated)); err == nil {
		t.Error("Parse on truncated gzip stream: want error, got nil")
	}
}

func TestParseNoAPKINDEXMember(t *testing.T) {
	body := record("glibc", "2.39-r1")
	tgz := buildIndexTarGz(t, "DESCRIPTION", body)
	if _, err := Parse(bytes.NewReader(tgz)); err == nil {
		t.Error("Parse with no APKINDEX member: want error, got nil")
	}
}

func TestParseEmptyIndexIsError(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
	}{
		{"no records at all", ""},
		{"name without version", "P:glibc\n\n"},
		{"version without name", "V:1.0-r0\n\n"},
		{"only unsafe names", record("; rm -rf /", "1.0-r0")},
	} {
		tgz := buildIndexTarGz(t, tarMember, c.body)
		idx, err := Parse(bytes.NewReader(tgz))
		if err == nil {
			t.Errorf("%s: Parse succeeded with %d packages, want error", c.name, idx.Len())
			continue
		}
		if !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "zero") {
			t.Errorf("%s: error %q does not mention the index being empty", c.name, err.Error())
		}
	}
}

func TestUnion(t *testing.T) {
	for _, c := range []struct {
		name   string
		bodies []string
		pkg    string
		want   []string
	}{
		{
			name:   "version present in only one mirror is still offered",
			bodies: []string{record("openssl", "3.3.1-r2"), record("libwebp", "1.4.0-r0")},
			pkg:    "libwebp",
			want:   []string{"1.4.0-r0"},
		},
		{
			name:   "same package, disjoint versions per mirror merge",
			bodies: []string{record("openssl", "3.3.1-r2"), record("openssl", "3.3.2-r0")},
			pkg:    "openssl",
			want:   []string{"3.3.1-r2", "3.3.2-r0"},
		},
	} {
		var indexes []*Index
		for _, body := range c.bodies {
			idx, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, body)))
			if err != nil {
				t.Fatalf("%s: Parse: %v", c.name, err)
			}
			indexes = append(indexes, idx)
		}
		merged := Union(indexes...)
		if got := merged.Versions(c.pkg); !slices.Equal(got, c.want) {
			t.Errorf("%s: Union(...).Versions(%q) = %v, want %v", c.name, c.pkg, got, c.want)
		}
	}
}

func TestIntersect(t *testing.T) {
	for _, c := range []struct {
		name       string
		archBodies []string
		pkg        string
		want       []string
	}{
		{
			name: "version present on every architecture is offered",
			archBodies: []string{
				record("openssl", "3.3.1-r2") + record("openssl", "3.3.2-r0"),
				record("openssl", "3.3.1-r2"),
			},
			pkg:  "openssl",
			want: []string{"3.3.1-r2"},
		},
		{
			name: "package present on only one architecture is dropped entirely",
			archBodies: []string{
				record("openssl", "3.3.1-r2") + record("libwebp", "1.4.0-r0"),
				record("openssl", "3.3.1-r2"),
			},
			pkg:  "libwebp",
			want: nil,
		},
	} {
		var indexes []*Index
		for _, body := range c.archBodies {
			idx, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, body)))
			if err != nil {
				t.Fatalf("%s: Parse: %v", c.name, err)
			}
			indexes = append(indexes, idx)
		}
		common := Intersect(indexes...)
		if got := common.Versions(c.pkg); !slices.Equal(got, c.want) {
			t.Errorf("%s: Intersect(...).Versions(%q) = %v, want %v", c.name, c.pkg, got, c.want)
		}
	}
}

func TestIntersectDropsPackageMissingEntirelyFromOneArch(t *testing.T) {
	x8664 := record("glibc", "2.39-r1") + record("libwebp", "1.4.0-r0")
	aarch64 := record("glibc", "2.39-r1")

	idxX86, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, x8664)))
	if err != nil {
		t.Fatalf("Parse x86_64: %v", err)
	}
	idxArm, err := Parse(bytes.NewReader(buildIndexTarGz(t, tarMember, aarch64)))
	if err != nil {
		t.Fatalf("Parse aarch64: %v", err)
	}

	common := Intersect(idxX86, idxArm)
	if got := common.Packages(); !slices.Equal(got, []string{"glibc"}) {
		t.Errorf("Intersect(...).Packages() = %v, want [glibc]", got)
	}
	if got := common.Versions("libwebp"); got != nil {
		t.Errorf("Versions(libwebp) = %v, want nil - only present on x86_64", got)
	}
}
