// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package dockerfile

import (
	"context"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
)

// TestUnmanagedPins is the incident of 2026-09-22 as a table: the file that
// carried a managed pin and an unmanaged one on adjacent lines, plus every
// shape that must stay quiet so the warning keeps meaning something.
func TestUnmanagedPins(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		// want is the substring every expected warning must contain, in
		// order; an empty slice asserts silence.
		want []string
	}{
		{
			name: "the measured case: annotated neighbour stays quiet, bare pin does not",
			in: "FROM alpine:3.21\n" +
				"# renovate: datasource=gitlab-tags depName=devops/images/php-runtime\n" +
				"ARG PHP_RUNTIME_IMAGE_TAG=3.0.31\n" +
				"ARG SUPERCRONIC_VERSION=0.2.45\n",
			want: []string{"SUPERCRONIC_VERSION=0.2.45"},
		},
		{
			name: "annotation separated from the pin by prose still counts",
			in: "FROM alpine:3.21\n" +
				"# renovate: datasource=github-releases depName=aptible/supercronic\n" +
				"# The checksum below is verified by php, not sha1sum.\n" +
				"ARG SUPERCRONIC_VERSION=0.2.49\n",
			want: nil,
		},
		{
			name: "pinup's own spelling is an annotation too",
			in:   "# pinup: datasource=github-releases depName=aptible/supercronic\nARG SUPERCRONIC_VERSION=0.2.49\n",
			want: nil,
		},
		{
			name: "prose alone does not annotate",
			in:   "# supercronic runs the crontab\nARG SUPERCRONIC_VERSION=0.2.45\n",
			want: []string{"SUPERCRONIC_VERSION=0.2.45"},
		},
		{
			name: "a checksum beside the pin is not a pin",
			in: "ARG SUPERCRONIC_SHA1_AMD64=e894b193bea75a5ee644e700c59e30eedc804cf7\n" +
				"ARG SUPERCRONIC_COMMIT=8b2a4b3c9d1e5f60718293a4b5c6d7e8f9012345\n",
			want: nil,
		},
		{
			name: "a line, not a release: two components name what to build against",
			in:   "ARG PHP_VERSION=8.5\nARG GO_VERSION=1.27\n",
			want: nil,
		},
		{
			name: "a value that is not a version at all",
			in:   "ARG BUILD_VERSION=development\nARG NODE_TAG=alpine\n",
			want: nil,
		},
		{
			name: "a name that does not announce a pin",
			in:   "ARG SUPERCRONIC=0.2.45\nARG TARGETARCH\n",
			want: nil,
		},
		{
			name: "an ARG the manager already resolved into a dependency is managed",
			in: "ARG BASE_TAG=3.0.31\n" +
				"FROM registry.example.com/app:${BASE_TAG}\n",
			want: nil,
		},
		{
			name: "ENV ages as quietly as ARG",
			in:   "ENV TRIVY_VERSION=0.72.0\n",
			want: []string{"TRIVY_VERSION=0.72.0"},
		},
		{
			name: "digest-pinned release without an annotation is still nobody's job",
			in:   "ARG RUNTIME_TAG=3.0.31@sha256:" + strings.Repeat("a", 64) + "\n",
			want: []string{"RUNTIME_TAG=3.0.31@sha256:"},
		},
		{
			name: "CRLF is read like LF",
			in:   "ARG SUPERCRONIC_VERSION=0.2.45\r\n",
			want: []string{"SUPERCRONIC_VERSION=0.2.45"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			res, err := m.Extract(context.Background(),
				extract.File{Path: "docker/scheduler/Containerfile", Content: []byte(tc.in)},
				extract.ManagerConfig{})
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if len(res.Warnings) != len(tc.want) {
				t.Fatalf("got %d warning(s), want %d: %+v", len(res.Warnings), len(tc.want), res.Warnings)
			}
			for i, want := range tc.want {
				got := res.Warnings[i]
				if got.Stage != "extract" {
					t.Errorf("warning %d stage = %q, want %q", i, got.Stage, "extract")
				}
				if got.File != "docker/scheduler/Containerfile" {
					t.Errorf("warning %d file = %q, want the file it read", i, got.File)
				}
				if !strings.Contains(got.Msg, want) {
					t.Errorf("warning %d msg = %q, want it to name %q", i, got.Msg, want)
				}
			}
		})
	}
}

// TestUnmanagedPinsNamesTheLine keeps the line number in the message: a
// warning that says only "somewhere in this file" costs the reader the search
// that the warning was supposed to save.
func TestUnmanagedPinsNamesTheLine(t *testing.T) {
	in := "FROM alpine:3.21\n\n# a comment\nARG SUPERCRONIC_VERSION=0.2.45\n"
	res, err := New().Extract(context.Background(),
		extract.File{Path: "Containerfile", Content: []byte(in)}, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %+v", len(res.Warnings), res.Warnings)
	}
	if !strings.Contains(res.Warnings[0].Msg, "line 4") {
		t.Errorf("msg = %q, want it to name line 4", res.Warnings[0].Msg)
	}
}

// TestUnmanagedPinsSurvivesAnEmptyFile guards the boundary the loop would hit
// first: no lines at all, and a file that is only a newline.
func TestUnmanagedPinsSurvivesAnEmptyFile(t *testing.T) {
	for _, in := range []string{"", "\n"} {
		res, err := New().Extract(context.Background(),
			extract.File{Path: "Containerfile", Content: []byte(in)}, extract.ManagerConfig{})
		if err != nil {
			t.Fatalf("Extract(%q): %v", in, err)
		}
		if len(res.Warnings) != 0 {
			t.Errorf("Extract(%q) warned: %+v", in, res.Warnings)
		}
	}
}
