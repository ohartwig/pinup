// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package discover_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/discover"
)

// write creates a file with some content at rel (slash-separated) under root,
// creating parent directories as needed.
func write(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscover covers the pattern-matching and filtering behaviour with one
// tree built per case in t.TempDir(), never under testdata.
func TestDiscover(t *testing.T) {
	for _, tc := range []struct {
		name    string
		build   func(t *testing.T, root string)
		req     func(root string) discover.Request
		want    []discover.Match
		wantWc  int    // want warning count
		wantWMs string // a fragment every warning message must contain, when wantWc == 1
	}{
		{
			// Real pattern from testdata/parity/config/default.json: the
			// regex form, anchored so it matches both a root-level file and
			// one nested arbitrarily deep.
			name: "regex pattern matches nested file",
			build: func(t *testing.T, root string) {
				write(t, root, "composer.json")
				write(t, root, "sub/dir/composer.json")
				write(t, root, "composer.lock") // must NOT match
			},
			req: func(root string) discover.Request {
				return discover.Request{
					Root:            root,
					EnabledManagers: []string{"composer"},
					Patterns: map[string][]string{
						"composer": {`/(^|/)composer\.json$/`},
					},
				}
			},
			want: []discover.Match{
				{Manager: "composer", Path: "composer.json"},
				{Manager: "composer", Path: "sub/dir/composer.json"},
			},
		},
		{
			// Real pattern from testdata/parity/renovate-43.288.0/full-resolved.json:
			// a glob with a character class and no leading slashes, proving
			// the compatibility branch (the non-regex path) works.
			name: "glob pattern compatibility branch",
			build: func(t *testing.T, root string) {
				write(t, root, "Dockerfile")
				write(t, root, "services/app/Dockerfile")
				write(t, root, "README.md") // must NOT match
			},
			req: func(root string) discover.Request {
				return discover.Request{
					Root:            root,
					EnabledManagers: []string{"dockerfile"},
					Patterns: map[string][]string{
						"dockerfile": {"**/[Dd]ockerfile*"},
					},
				}
			},
			want: []discover.Match{
				{Manager: "dockerfile", Path: "Dockerfile"},
				{Manager: "dockerfile", Path: "services/app/Dockerfile"},
			},
		},
		{
			// IgnorePaths wins over every manager, not just the one whose
			// pattern would otherwise have matched.
			name: "ignore paths exclude a subtree from every manager",
			build: func(t *testing.T, root string) {
				write(t, root, "prometheus-exporter/composer.json")
				write(t, root, "prometheus-exporter/Dockerfile")
				write(t, root, "composer.json")
			},
			req: func(root string) discover.Request {
				return discover.Request{
					Root:            root,
					EnabledManagers: []string{"composer", "dockerfile"},
					IgnorePaths:     []string{"**/prometheus-exporter/**"},
					Patterns: map[string][]string{
						"composer":   {`/(^|/)composer\.json$/`},
						"dockerfile": {"**/[Dd]ockerfile*"},
					},
				}
			},
			want: []discover.Match{
				{Manager: "composer", Path: "composer.json"},
			},
		},
		{
			// A manager enabled but absent from Patterns produces a warning,
			// not an error, and the other managers still get their matches -
			// the estate config names `nix`, which pinup does not implement.
			name: "enabled manager with no patterns configured warns",
			build: func(t *testing.T, root string) {
				write(t, root, "composer.json")
			},
			req: func(root string) discover.Request {
				return discover.Request{
					Root:            root,
					EnabledManagers: []string{"composer", "nix"},
					Patterns: map[string][]string{
						"composer": {`/(^|/)composer\.json$/`},
					},
				}
			},
			want: []discover.Match{
				{Manager: "composer", Path: "composer.json"},
			},
			wantWc:  1,
			wantWMs: "nix",
		},
		{
			// .git/ contents never appear in the results, even when a
			// pattern would otherwise match them.
			name: "git directory contents never appear",
			build: func(t *testing.T, root string) {
				write(t, root, ".git/composer.json")
				write(t, root, ".git/objects/pack/composer.json")
				write(t, root, "composer.json")
			},
			req: func(root string) discover.Request {
				return discover.Request{
					Root:            root,
					EnabledManagers: []string{"composer"},
					Patterns: map[string][]string{
						"composer": {`/(^|/)composer\.json$/`},
					},
				}
			},
			want: []discover.Match{
				{Manager: "composer", Path: "composer.json"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.build(t, root)

			res, err := discover.Discover(tc.req(root))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}

			if len(res.Matches) != len(tc.want) {
				t.Fatalf("got %d matches, want %d\n got: %+v\nwant: %+v", len(res.Matches), len(tc.want), res.Matches, tc.want)
			}
			for i, m := range res.Matches {
				if m != tc.want[i] {
					t.Errorf("match[%d] = %+v, want %+v", i, m, tc.want[i])
				}
			}

			if len(res.Warnings) != tc.wantWc {
				t.Fatalf("got %d warnings, want %d: %+v", len(res.Warnings), tc.wantWc, res.Warnings)
			}
			if tc.wantWc == 1 {
				if got := res.Warnings[0].Msg; !strings.Contains(got, tc.wantWMs) {
					t.Errorf("warning message %q does not mention %q", got, tc.wantWMs)
				}
			}

			// Denominator: a walk that found nothing must still be provably a
			// walk, not a silent no-op.
			if res.Stats.FilesWalked <= 0 {
				t.Errorf("Stats.FilesWalked = %d, want > 0", res.Stats.FilesWalked)
			}
		})
	}
}

// TestDiscover_Deterministic runs the same request twice over the same tree
// and requires byte-for-byte identical results, including order - a
// non-deterministic walk would make golden plans uncomparable.
func TestDiscover_Deterministic(t *testing.T) {
	root := t.TempDir()
	write(t, root, "composer.json")
	write(t, root, "a/composer.json")
	write(t, root, "b/composer.json")
	write(t, root, "Dockerfile")
	write(t, root, "a/Dockerfile")

	req := discover.Request{
		Root:            root,
		EnabledManagers: []string{"composer", "dockerfile", "nix"},
		Patterns: map[string][]string{
			"composer":   {`/(^|/)composer\.json$/`},
			"dockerfile": {"**/[Dd]ockerfile*"},
		},
	}

	first, err := discover.Discover(req)
	if err != nil {
		t.Fatalf("Discover (first run): %v", err)
	}
	second, err := discover.Discover(req)
	if err != nil {
		t.Fatalf("Discover (second run): %v", err)
	}

	if len(first.Matches) != len(second.Matches) {
		t.Fatalf("match count differs across runs: %d vs %d", len(first.Matches), len(second.Matches))
	}
	for i := range first.Matches {
		if first.Matches[i] != second.Matches[i] {
			t.Errorf("match[%d] differs across runs: %+v vs %+v", i, first.Matches[i], second.Matches[i])
		}
	}
	if len(first.Warnings) != len(second.Warnings) {
		t.Fatalf("warning count differs across runs: %d vs %d", len(first.Warnings), len(second.Warnings))
	}
	for i := range first.Warnings {
		if first.Warnings[i] != second.Warnings[i] {
			t.Errorf("warning[%d] differs across runs: %+v vs %+v", i, first.Warnings[i], second.Warnings[i])
		}
	}
	if first.Stats != second.Stats {
		t.Errorf("stats differ across runs: %+v vs %+v", first.Stats, second.Stats)
	}
}

// TestDiscover_SymlinkLoopTerminates plants a symlink that points back at its
// own parent directory. If the walk ever followed it, this test would hang
// rather than fail - which is exactly why it must not follow it.
func TestDiscover_SymlinkLoopTerminates(t *testing.T) {
	root := t.TempDir()
	write(t, root, "loop/composer.json")

	// loop/self -> loop (its own parent): following it would recurse forever.
	if err := os.Symlink(filepath.Join(root, "loop"), filepath.Join(root, "loop", "self")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	res, err := discover.Discover(discover.Request{
		Root:            root,
		EnabledManagers: []string{"composer"},
		Patterns: map[string][]string{
			"composer": {`/(^|/)composer\.json$/`},
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(res.Matches) != 1 || res.Matches[0].Path != "loop/composer.json" {
		t.Errorf("got %+v, want exactly loop/composer.json", res.Matches)
	}
}
