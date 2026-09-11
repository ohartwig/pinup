// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package lint holds the rules that no linter knows: the ones this repository
// decided for itself. They are written as checks over a source tree rather
// than as comments, because a comment does not fail a pipeline.
//
// Every check takes the tree root as a parameter. That is what lets
// canfail_test.go point them at a synthetic tree with a planted violation and
// prove the check can go red - a check that has never failed has not been
// tested.
package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Module is the import path this repository publishes under. Imports that do
// not carry this prefix are somebody else's problem.
const Module = "git.ole-hartwig.eu/pinup/pinup"

// Violation is one broken rule, located well enough to fix without searching.
type Violation struct {
	File string
	Line int
	Msg  string
}

func (v Violation) String() string {
	if v.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Msg)
	}
	return fmt.Sprintf("%s: %s", v.File, v.Msg)
}

// File is one parsed Go file plus the bookkeeping every check needs.
type File struct {
	Path    string // relative to root, slash-separated
	Pkg     string // directory relative to root, slash-separated; "." for root
	IsTest  bool
	Src     []byte
	AST     *ast.File
	FileSet *token.FileSet
}

// Load parses every Go file under root, skipping directories that are not ours
// to police.
func Load(root string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".go", "dist", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		// ParseComments: the SPDX and language checks read comments.
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments)
		if err != nil {
			// A file that does not parse is a compile error, which `go build`
			// reports better than this package would. Skip rather than
			// duplicate the message.
			return nil
		}
		out = append(out, File{
			Path:    rel,
			Pkg:     path.Dir(rel),
			IsTest:  strings.HasSuffix(d.Name(), "_test.go"),
			Src:     src,
			AST:     f,
			FileSet: fset,
		})
		return nil
	})
	return out, err
}

// imports returns the import paths of f, with their positions.
func imports(f File) []struct {
	Path string
	Line int
} {
	var out []struct {
		Path string
		Line int
	}
	for _, im := range f.AST.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, struct {
			Path string
			Line int
		}{p, f.FileSet.Position(im.Pos()).Line})
	}
	return out
}

// internal reports whether an import path is one of ours, and returns the
// package directory relative to the module root.
func internal(importPath string) (string, bool) {
	if importPath == Module {
		return ".", true
	}
	if !strings.HasPrefix(importPath, Module+"/") {
		return "", false
	}
	return strings.TrimPrefix(importPath, Module+"/"), true
}
