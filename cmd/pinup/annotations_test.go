// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

func TestOrphanAnnotations(t *testing.T) {
	// The three shapes measured on 2026-09-17: adjacent (claimed), a blank
	// line between (redis-exporter, mysqld-exporter), comment lines between
	// (ksops), and an annotation above a FROM another manager reads.
	body := "" +
		"# renovate: datasource=custom.koh-apk depName=adjacent versioning=apk\n" + // line 1
		"ARG ADJACENT_VERSION=1.0.0-r1\n" + // line 2, claimed
		"\n" +
		"# renovate: datasource=custom.koh-apk depName=blank versioning=apk\n" + // line 4
		"\n" +
		"ARG BLANK_VERSION=2.0.0-r0\n" + // line 6, not claimed
		"# renovate: datasource=custom.koh-apk depName=commented versioning=apk\n" + // line 7
		"# a remark that separates the two\n" +
		"ARG COMMENTED_VERSION=3.0.0-r0\n" + // line 9, not claimed
		"# renovate: datasource=docker depName=registry.example.org/base\n" + // line 10
		"FROM registry.example.org/base:1.2.3\n" // line 11, claimed by the dockerfile manager
	contents := map[string][]byte{"Containerfile": []byte(body)}
	deps := []model.Dependency{
		{File: "Containerfile", DepName: "adjacent", Locus: model.Locus{ValueStart: strings.Index(body, "1.0.0-r1")}},
		{File: "Containerfile", DepName: "registry.example.org/base", Locus: model.Locus{ValueStart: strings.Index(body, "1.2.3")}},
		{File: "other.yaml", DepName: "blank", Locus: model.Locus{ValueStart: 0}}, // another file never claims
	}
	got := orphanAnnotations(contents, deps)
	if len(got) != 2 {
		t.Fatalf("want 2 warnings (blank, commented), got %d: %+v", len(got), got)
	}
	for i, want := range []struct {
		line string
		name string
	}{{"line 4:", "depName=blank"}, {"line 7:", "depName=commented"}} {
		if got[i].Stage != "extract" || got[i].File != "Containerfile" {
			t.Errorf("warning %d: stage/file = %q/%q", i, got[i].Stage, got[i].File)
		}
		if !strings.Contains(got[i].Msg, want.line) || !strings.Contains(got[i].Msg, want.name) {
			t.Errorf("warning %d: %q does not name %s %s", i, got[i].Msg, want.line, want.name)
		}
	}
	if got := orphanAnnotations(map[string][]byte{"a.yaml": []byte("key: value\n")}, nil); got != nil {
		t.Errorf("a file without annotations warns: %+v", got)
	}
}
