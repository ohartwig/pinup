// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package discover walks a repository checkout and decides which files each
// enabled manager should look at.
//
// It is layer 2, sitting next to extract rather than on top of it: discover
// decides WHICH files matter, extract reads and parses them. Accordingly this
// package never opens a file - it returns paths, relative to the checkout
// root and slash-separated, and leaves reading bytes to extract. That keeps a
// malformed or huge file from costing anything during discovery, and it means
// a Match is cheap enough to sort and log wholesale.
//
// Pattern form matters: the estate's configuration uses two dialects for a
// manager's file patterns. A pattern wrapped in slashes, e.g.
// `/(^|/)composer\.json$/`, is a JavaScript-flavored regular expression (as
// Renovate's customManagers author them) applied to the whole relative path.
// Anything else is a glob, matched with this repository's own glob package.
// Measured against the estate config, the regex form is the overwhelming
// majority (63 of 63 patterns); the glob branch exists for compatibility with
// the handful of built-in managers that still use fileMatch-style globs.
package discover

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/re2x"
)

// Request describes one discovery run.
type Request struct {
	// Root is the checkout directory to walk.
	Root string
	// EnabledManagers is the closed set of manager names to consider, taken
	// from resolved configuration. Empty means "all registered" - since
	// discover has no registry of its own (that lives in wire, three layers
	// up), "registered" here means every manager named as a key in Patterns.
	EnabledManagers []string
	// IgnorePaths are glob patterns, matched against the slash-separated
	// relative path of both files and directories. A match excludes the file
	// (or the whole subtree, for a directory) from every manager, regardless
	// of whether any manager's own patterns would otherwise have matched it.
	IgnorePaths []string
	// Patterns maps a manager name to its file patterns, in the `/regex/` or
	// glob form described in the package doc. A manager named in
	// EnabledManagers with no entry here produces a Warning, not an error.
	Patterns map[string][]string
}

// Match is one file offered to one manager.
type Match struct {
	Manager string
	// Path is relative to Request.Root and slash-separated.
	Path string
}

// Result is the outcome of a discovery run.
type Result struct {
	Matches  []Match
	Warnings []model.Warning
	Stats    Stats
}

// Stats are denominators, so a run that discovered nothing is distinguishable
// from a run that never walked anything.
type Stats struct {
	// FilesWalked counts every regular, non-symlink file the walk visited
	// outside .git, before ignore filtering or pattern matching - the
	// denominator that says the walk actually happened.
	FilesWalked int
	// FilesMatched counts distinct files that matched at least one manager's
	// patterns. A file offered to three managers still counts once here;
	// len(Result.Matches) is the total offer count, this is the file count.
	FilesMatched int
}

// compiledManager is one enabled manager's patterns, ready to test against a
// path.
type compiledManager struct {
	name    string
	regexes []*re2x.Regexp
	globs   []*glob.Matcher
}

func (c compiledManager) match(rel string) bool {
	for _, re := range c.regexes {
		if _, ok := re.Find(rel); ok {
			return true
		}
	}
	for _, g := range c.globs {
		if g.Match(rel) {
			return true
		}
	}
	return false
}

// Discover walks req.Root and reports, for every enabled manager, the files
// whose relative path matches one of its patterns.
func Discover(req Request) (Result, error) {
	if req.Root == "" {
		return Result{}, fmt.Errorf("discover: empty Root")
	}

	managers := req.EnabledManagers
	if len(managers) == 0 {
		managers = sortedKeys(req.Patterns)
	}

	var warnings []model.Warning
	var compiled []compiledManager
	for _, name := range managers {
		pats, ok := req.Patterns[name]
		if !ok {
			// An unimplemented or misspelled manager must not stop the run -
			// the estate config names `nix`, which pinup does not implement,
			// and one bad name in a long EnabledManagers list is not worth
			// losing every other manager's results over.
			warnings = append(warnings, model.Warning{
				Stage: "discover",
				Msg:   fmt.Sprintf("manager %q is enabled but has no file patterns configured", name),
			})
			continue
		}
		cm := compiledManager{name: name}
		for _, p := range pats {
			if body, isRegex := regexBody(p); isRegex {
				re, err := re2x.Compile(body)
				if err != nil {
					// Same reasoning as the unknown-manager case: a single bad
					// pattern costs that pattern, not the manager or the run.
					warnings = append(warnings, model.Warning{
						Stage: "discover",
						Msg:   fmt.Sprintf("manager %q: invalid pattern %q: %v", name, p, err),
					})
					continue
				}
				cm.regexes = append(cm.regexes, re)
				continue
			}
			cm.globs = append(cm.globs, glob.Compile(p))
		}
		compiled = append(compiled, cm)
	}

	// glob.IgnoreSet rather than glob.Set: the two answer opposite questions
	// about an empty list, and that difference is a type precisely so no
	// caller has to remember it.
	ignore := glob.NewIgnoreSet(req.IgnorePaths)

	var stats Stats
	matchedFiles := map[string]bool{}
	var matches []Match

	walkErr := filepath.WalkDir(req.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == req.Root {
			return nil
		}

		rel, err := filepath.Rel(req.Root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		// Never follow a symlink: a loop back into an ancestor directory must
		// not hang the walk, and discovery has no business dereferencing
		// links to decide what a file is. os.ReadDir (which WalkDir uses)
		// reports a symlink's own type without following it, so this is
		// enough to keep the walk from ever entering one.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			if ignore.Ignores(rel) {
				return fs.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		stats.FilesWalked++

		if ignore.Ignores(rel) {
			return nil
		}

		for _, cm := range compiled {
			if cm.match(rel) {
				matches = append(matches, Match{Manager: cm.name, Path: rel})
				matchedFiles[rel] = true
			}
		}
		return nil
	})
	if walkErr != nil {
		return Result{}, fmt.Errorf("discover: walking %s: %w", req.Root, walkErr)
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Manager != matches[j].Manager {
			return matches[i].Manager < matches[j].Manager
		}
		return matches[i].Path < matches[j].Path
	})
	stats.FilesMatched = len(matchedFiles)

	return Result{Matches: matches, Warnings: warnings, Stats: stats}, nil
}

// regexBody reports whether pattern uses the "/regex/" form and, if so,
// returns the pattern with its wrapping slashes stripped.
func regexBody(pattern string) (body string, ok bool) {
	if len(pattern) < 2 || !strings.HasPrefix(pattern, "/") || !strings.HasSuffix(pattern, "/") {
		return "", false
	}
	return pattern[1 : len(pattern)-1], true
}

// sortedKeys returns m's keys in sorted order, so "EnabledManagers empty
// means all registered" walks its managers in a deterministic sequence
// instead of Go's randomised map order.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
