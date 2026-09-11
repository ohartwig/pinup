// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package lint

import (
	"fmt"
	"go/ast"
	"regexp"
	"strings"
)

// Identifiers this package must look for are assembled from pieces, because a
// file that spelled them out would trip its own check. The trick is worth the
// ugliness: the alternative is an exemption, and an exemption is a hole.
var (
	nowCall     = "time" + "." + "Now"
	writeFile   = "os" + "." + "WriteFile"
	createFile  = "os" + "." + "Create"
	removeAll   = "os" + "." + "RemoveAll"
	renameFile  = "os" + "." + "Rename"
	httpGet     = "http" + "." + "Get"
	httpPost    = "http" + "." + "Post"
	defaultClnt = "http" + "." + "DefaultClient"
)

// CheckSPDXHeader requires the two-line header on every Go file. Licensing
// completeness is checked in CI by lint-reuse; this catches it at the point
// where it is cheap to fix.
func CheckSPDXHeader(files []File) []Violation {
	const (
		wantCopyright = "// SPDX-FileCopyrightText:"
		wantLicense   = "// SPDX-License-Identifier: MIT"
	)
	var vs []Violation
	for _, f := range files {
		lines := strings.SplitN(string(f.Src), "\n", 3)
		if len(lines) < 2 ||
			!strings.HasPrefix(strings.TrimSpace(lines[0]), wantCopyright) ||
			strings.TrimSpace(lines[1]) != wantLicense {
			vs = append(vs, Violation{f.Path, 1,
				"missing the two-line SPDX header (copyright line, then " + wantLicense + ")"})
		}
	}
	return vs
}

// CheckNoFakesInBinary keeps test doubles out of anything that ships. They
// live in their own package precisely so this check is possible.
//
// A fake may import another fake: the conformance runner is built on the
// recording T, and both are test infrastructure. What matters is that nothing
// reachable from cmd/ imports either.
func CheckNoFakesInBinary(files []File) []Violation {
	var vs []Violation
	for _, f := range files {
		if f.IsTest || f.Pkg == "fake" || strings.HasPrefix(f.Pkg, "fake/") {
			continue
		}
		for _, im := range imports(f) {
			to, ok := internal(im.Path)
			if !ok {
				continue
			}
			if to == "fake" || strings.HasPrefix(to, "fake/") {
				vs = append(vs, Violation{f.Path, im.Line,
					fmt.Sprintf("non-test file imports the test double %q", to)})
			}
		}
	}
	return vs
}

// CheckNoUpdateFlag enforces the estate rule that golden files are read and
// never rewritten. A -update flag turns a failing assertion into a diff nobody
// reads; captured snapshots are versioned by directory instead.
func CheckNoUpdateFlag(files []File) []Violation {
	// Matches flag registrations whose name suggests golden rewriting.
	re := regexp.MustCompile(`flag\.(Bool|String)\w*\(\s*"(update|regold|regenerate|rewrite|golden)`)
	var vs []Violation
	for _, f := range files {
		for i, line := range strings.Split(string(f.Src), "\n") {
			if re.MatchString(line) {
				vs = append(vs, Violation{f.Path, i + 1,
					"golden files are read, never rewritten - no -update flag (see docs/plan.md section 5.1)"})
			}
		}
	}
	return vs
}

// CheckClockInjected keeps the clock out of the logic. Scheduling,
// minimumReleaseAge and the firstseen cache all depend on `now` being a
// parameter; a single stray call makes a whole class of tests unreproducible.
func CheckClockInjected(files []File) []Violation {
	var vs []Violation
	for _, f := range files {
		if f.IsTest {
			continue
		}
		if group(f.Pkg) == "cmd" || strings.HasPrefix(f.Pkg, "fake/clockfake") {
			continue
		}
		ast.Inspect(f.AST, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name+"."+sel.Sel.Name == nowCall {
				vs = append(vs, Violation{f.Path, f.FileSet.Position(sel.Pos()).Line,
					"the clock is injected: take `now time.Time` as a parameter instead"})
			}
			return true
		})
	}
	return vs
}

// CheckNoTestWritesTestdata enforces the read-only-golden rule mechanically,
// rather than by memory. Mutation testing mutates in memory for this reason.
func CheckNoTestWritesTestdata(files []File) []Violation {
	calls := []string{writeFile, createFile, removeAll, renameFile}
	var vs []Violation
	for _, f := range files {
		if !f.IsTest {
			continue
		}
		for i, line := range strings.Split(string(f.Src), "\n") {
			if !strings.Contains(line, "testdata") {
				continue
			}
			for _, c := range calls {
				if strings.Contains(line, c) {
					vs = append(vs, Violation{f.Path, i + 1,
						"a test must not write into testdata/ - mutate in memory instead"})
				}
			}
		}
	}
	return vs
}

// CheckNoNetworkInTests backs the refusing transport with a static check. Only
// the capture tooling, which is manual and deliberate, may reach out.
func CheckNoNetworkInTests(files []File) []Violation {
	banned := []string{httpGet, httpPost, defaultClnt}
	var vs []Violation
	for _, f := range files {
		if !f.IsTest || strings.HasPrefix(f.Pkg, "tools/capture") {
			continue
		}
		for i, line := range strings.Split(string(f.Src), "\n") {
			for _, b := range banned {
				if strings.Contains(line, b) {
					vs = append(vs, Violation{f.Path, i + 1,
						"tests do not reach the network: use a protocol-speaking httptest server and the refusing transport"})
				}
			}
		}
	}
	return vs
}

// German function words, and English ones as the tie-breaker. A comment is
// flagged only when German words outnumber English ones, because a false alarm
// in a check that gates the pipeline costs more than a missed comment.
var (
	germanWords  = regexp.MustCompile(`(?i)\b(und|oder|nicht|wird|werden|wurde|ist|sind|sein|eine|einen|einem|einer|nach|über|durch|damit|dann|weil|dass|kein|keine|noch|schon|beim|vom|zur|zum|auf|aus|für|mit|sich|man|hier|auch)\b`)
	englishWords = regexp.MustCompile(`(?i)\b(and|or|not|the|is|are|was|were|be|a|an|to|of|in|for|with|that|this|it|as|by|from|on|at|if|so|but|which|when|because|so that)\b`)
)

// CheckCommentsAreEnglish enforces "the code is English" from CLAUDE.md.
func CheckCommentsAreEnglish(files []File) []Violation {
	var vs []Violation
	for _, f := range files {
		for _, cg := range f.AST.Comments {
			for _, c := range cg.List {
				text := strings.TrimLeft(c.Text, "/* \t")
				de := len(germanWords.FindAllString(text, -1))
				en := len(englishWords.FindAllString(text, -1))
				if de > en && de >= 2 {
					vs = append(vs, Violation{f.Path, f.FileSet.Position(c.Pos()).Line,
						"comment reads as German; identifiers, comments and test messages are English"})
				}
			}
		}
	}
	return vs
}

// CheckYAMLConfined keeps gopkg.in/yaml.v3 inside yamlx. The dependency is
// meant to be swappable, and it is only swappable if exactly one package names
// it; yamlx re-exports the node vocabulary so nobody else needs to.
func CheckYAMLConfined(files []File) []Violation {
	const dep = "gopkg.in/" + "yaml.v3"
	var vs []Violation
	for _, f := range files {
		if f.Pkg == "yamlx" {
			continue
		}
		for _, im := range imports(f) {
			if im.Path == dep {
				vs = append(vs, Violation{f.Path, im.Line,
					"yaml.v3 is confined to yamlx; use yamlx.Node and the re-exported kinds"})
			}
		}
	}
	return vs
}

// Check is one named rule, so the test table and the mutation suite can
// address them uniformly.
type Check struct {
	Name string
	Run  func([]File) []Violation
}

// All returns every check in this package.
func All() []Check {
	return []Check{
		{"ImportLayering", CheckImportLayering},
		{"RunnerHasNoImplementations", CheckRunnerHasNoImplementations},
		{"WireIsNotImported", CheckWireIsNotImported},
		{"SPDXHeader", CheckSPDXHeader},
		{"NoFakesInBinary", CheckNoFakesInBinary},
		{"NoUpdateFlag", CheckNoUpdateFlag},
		{"ClockInjected", CheckClockInjected},
		{"NoTestWritesTestdata", CheckNoTestWritesTestdata},
		{"NoNetworkInTests", CheckNoNetworkInTests},
		{"CommentsAreEnglish", CheckCommentsAreEnglish},
		{"YAMLConfined", CheckYAMLConfined},
	}
}
