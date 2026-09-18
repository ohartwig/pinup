// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// Command zip packs files into a zip archive, preserving the executable bit.
//
// Why not /usr/bin/zip: the golden image does not ship it, and an apt-get for
// it would be a second package channel in a pipeline that otherwise needs
// nothing but Go. The executable bit has to travel, because a binary extracted
// from an archive that lost it arrives without +x and cannot be started.
//
// Timestamps are fixed so two builds of the same input produce the same bytes.
//
// Usage: go run tools/zip.go <archive.zip> <file>...
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// fixedTime keeps the archive reproducible. The value is arbitrary; that it
// does not move is the point.
var fixedTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: zip <archive.zip> <file>...")
		os.Exit(2)
	}
	if err := pack(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "zip:", err)
		os.Exit(1)
	}
}

func pack(archive string, files []string) error {
	out, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer out.Close()

	w := zip.NewWriter(out)
	for _, name := range files {
		if err := add(w, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return out.Close()
}

func add(w *zip.Writer, name string) error {
	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	head, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	head.Name = filepath.Base(name)
	head.Method = zip.Deflate
	head.Modified = fixedTime
	// FileInfoHeader carries the mode through, which is what preserves +x.

	dst, err := w.CreateHeader(head)
	if err != nil {
		return err
	}
	src, err := os.Open(name)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(dst, src)
	return err
}
