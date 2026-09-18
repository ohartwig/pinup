// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package lint

import (
	"os"
	"path/filepath"
	"testing"
)

// moduleRoot is the tree under test. Taking it as a value rather than assuming
// the working directory is what lets canfail_test.go point the same checks at
// a synthetic tree.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}
	return root
}

func TestTheTreeObeysItsOwnRules(t *testing.T) {
	root := moduleRoot(t)
	files, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	// A check that ran over nothing is indistinguishable from a check that
	// passed. Assert the denominator.
	if len(files) < 3 {
		t.Fatalf("loaded %d Go files; the walk did not work", len(files))
	}
	t.Logf("checking %d Go files", len(files))

	for _, c := range All() {
		vs := c.Run(files)
		for _, v := range vs {
			t.Errorf("%s: %s", c.Name, v)
		}
	}
}

func TestLayerAssignment(t *testing.T) {
	for _, c := range []struct {
		pkg   string
		want  int
		known bool
	}{
		{"model", 0, true},
		{"hbs", 0, true},
		{"config", 1, true},
		{"config/preset", 0, true}, // longer prefix wins over "config"
		{"versioning", 1, true},
		{"versioning/semver", 3, true}, // longest prefix wins over "versioning"
		{"extract", 2, true},
		{"manager/dockerfile", 3, true},
		{"datasource/docker", 3, true},
		{"runner", 4, true},
		{"advise", 4, true},
		{"wire", 5, true},
		{"cmd/pinup", 6, true},
		{"tools/lint", 0, false}, // exempt
		{"fake/clockfake", 0, false},
		{"nonesuch", 0, false},
	} {
		got, ok := Layer(c.pkg)
		if ok != c.known {
			t.Errorf("Layer(%q): known = %v, want %v", c.pkg, ok, c.known)
			continue
		}
		if ok && got != c.want {
			t.Errorf("Layer(%q) = L%d, want L%d", c.pkg, got, c.want)
		}
	}
}
