// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A check that has never failed has not been tested. This file plants one
// deliberate violation per check in a synthetic tree and asserts the check
// names it.
//
// The planted source is assembled from fragments rather than written out,
// because several checks read raw source lines - a literal here would trip the
// very check it is testing, in this file, on the real tree.

const hdr = "// SPDX-FileCopyrightText: 2026 Test\n// SPDX-License-Identifier: Apache-2.0\n\n"

// plant writes files into a fresh tree and returns its root.
func plant(t *testing.T, files map[string]string) []File {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module "+Module+"\n\ngo 1.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, src := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(files) {
		t.Fatalf("planted %d files, loaded %d - the walk missed some", len(files), len(loaded))
	}
	return loaded
}

func imp(pkg, path string) string {
	return hdr + "package " + pkg + "\n\nimport _ \"" + Module + "/" + path + "\"\n"
}

func TestEveryCheckCanFail(t *testing.T) {
	// Fragments assembled so this file does not trip its own checks.
	var (
		nowExpr    = hdr + "package lookup\n\nimport \"time\"\n\nfunc f() { _ = " + "time" + "." + "Now" + "() }\n"
		updFlag    = hdr + "package model\n\nimport \"flag\"\n\nvar u = " + "flag" + "." + "Bool" + "(\"update\", false, \"rewrite goldens\")\n"
		writesGold = hdr + "package model\n\nimport \"os\"\n\nfunc f() { _ = " + "os" + "." + "WriteFile" + "(\"testdata/x.json\", nil, 0o644) }\n"
		netCall    = hdr + "package model\n\nimport \"net/http\"\n\nfunc f() { _, _ = " + "http" + "." + "Get" + "(\"https://example.invalid\") }\n"
		germanCmt  = hdr + "// Diese Funktion wird nicht aufgerufen und ist auch sonst\n// nicht weiter von Belang, weil sie nur ein Beispiel sein soll.\npackage model\n"
	)

	for _, c := range []struct {
		check   string
		files   map[string]string
		wantIn  string // the file the violation must name
		wantMsg string // a fragment of the message
	}{
		{
			check:   "ImportLayering",
			files:   map[string]string{"model/a.go": imp("model", "lookup")},
			wantIn:  "model/a.go",
			wantMsg: "strictly lower layers",
		},
		{
			// An L3 package importing an L2 package that is NOT the one
			// declaring its interface.
			check:   "ImportLayering",
			files:   map[string]string{"manager/dockerfile/a.go": imp("dockerfile", "lookup")},
			wantIn:  "manager/dockerfile/a.go",
			wantMsg: "the only L2 import permitted here is \"extract\"",
		},
		{
			check:   "RunnerHasNoImplementations",
			files:   map[string]string{"runner/a.go": imp("runner", "manager/dockerfile")},
			wantIn:  "runner/a.go",
			wantMsg: "receive registries from wire",
		},
		{
			check:   "WireIsNotImported",
			files:   map[string]string{"model/a.go": imp("model", "wire")},
			wantIn:  "model/a.go",
			wantMsg: "only cmd/ and wire/",
		},
		{
			check:   "SPDXHeader",
			files:   map[string]string{"model/a.go": "package model\n"},
			wantIn:  "model/a.go",
			wantMsg: "SPDX",
		},
		{
			check:   "NoFakesInBinary",
			files:   map[string]string{"lookup/a.go": imp("lookup", "fake/dsfake")},
			wantIn:  "lookup/a.go",
			wantMsg: "test double",
		},
		{
			check:   "NoUpdateFlag",
			files:   map[string]string{"model/a_test.go": updFlag},
			wantIn:  "model/a_test.go",
			wantMsg: "never rewritten",
		},
		{
			check:   "ClockInjected",
			files:   map[string]string{"lookup/a.go": nowExpr},
			wantIn:  "lookup/a.go",
			wantMsg: "clock is injected",
		},
		{
			check:   "NoTestWritesTestdata",
			files:   map[string]string{"model/a_test.go": writesGold},
			wantIn:  "model/a_test.go",
			wantMsg: "must not write into testdata",
		},
		{
			check:   "NoNetworkInTests",
			files:   map[string]string{"model/a_test.go": netCall},
			wantIn:  "model/a_test.go",
			wantMsg: "do not reach the network",
		},
		{
			check:   "YAMLConfined",
			files:   map[string]string{"model/a.go": hdr + "package model\n\nimport _ \"gopkg.in/yaml.v3\"\n"},
			wantIn:  "model/a.go",
			wantMsg: "confined to yamlx",
		},
		{
			check:   "CommentsAreEnglish",
			files:   map[string]string{"model/a.go": germanCmt},
			wantIn:  "model/a.go",
			wantMsg: "German",
		},
	} {
		files := plant(t, c.files)

		var run func([]File) []Violation
		for _, chk := range All() {
			if chk.Name == c.check {
				run = chk.Run
			}
		}
		if run == nil {
			t.Fatalf("no check named %q", c.check)
		}

		vs := run(files)
		if len(vs) == 0 {
			t.Errorf("%s: planted violation in %s was NOT detected", c.check, c.wantIn)
			continue
		}
		var found bool
		for _, v := range vs {
			if v.File == c.wantIn && strings.Contains(v.Msg, c.wantMsg) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected a violation in %s containing %q, got %v",
				c.check, c.wantIn, c.wantMsg, vs)
		}
	}
}

// TestTheExceptionIsAllowed is the other half: the one permitted L3 -> L2
// import must NOT be reported, or every implementation package would be red.
func TestTheExceptionIsAllowed(t *testing.T) {
	for _, c := range []struct{ pkg, file, imports string }{
		{"dockerfile", "manager/dockerfile/a.go", "extract"},
		{"docker", "datasource/docker/a.go", "lookup"},
		{"gitlab", "platform/gitlab/a.go", "publish"},
	} {
		files := plant(t, map[string]string{c.file: imp(c.pkg, c.imports)})
		if vs := CheckImportLayering(files); len(vs) != 0 {
			t.Errorf("%s importing %s should be allowed, got %v", c.file, c.imports, vs)
		}
	}
}
