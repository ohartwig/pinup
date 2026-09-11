// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package lint

import (
	"fmt"
	"sort"
	"strings"
)

// The six layers from docs/plan.md section 3.1. A package may import strictly
// lower layers only.
//
// The table is keyed by package directory prefix, longest match wins - so
// `versioning` sits at L1 (three stages consume its vocabulary) while
// `versioning/semver` sits at L3 as an implementation.
var layerOf = map[string]int{
	// L0 - pure. stdlib only, plus yaml.v3 inside yamlx.
	"model": 0, "hbs": 0, "glob": 0, "sched": 0, "jsonata": 0,
	"jsonc": 0, "yamlx": 0, "apkindex": 0, "re2x": 0,

	// L1 - services.
	"config": 1, "config/preset": 1, "versioning": 1,
	"httpx": 1, "cache": 1, "git": 1,

	// L2 - stages. Each declares the interface its implementations satisfy.
	"discover": 2, "extract": 2, "lookup": 2, "rules": 2, "classify": 2,
	"planner": 2, "apply": 2, "publish": 2, "osv": 2,

	// L3 - implementations.
	"manager": 3, "datasource": 3, "versioning/": 3, "platform": 3,
	"plugin": 3, "analyzer": 3,

	// L4 - orchestration.
	"runner": 4, "report": 4,

	// L5 - wiring. The only place that knows every implementation.
	"wire": 5, "cmd": 5,
}

// declaredBy names, for each L3 group, the single L2 package that declares the
// interface it implements. That import is the one permitted exception to the
// layering rule: `manager/dockerfile` imports `extract` for the type alone.
//
// It must not import `lookup`, `planner`, or another `manager/*`.
var declaredBy = map[string]string{
	"manager":    "extract",
	"datasource": "lookup",
	"platform":   "publish",
	"analyzer":   "classify",
	// `plugin` implements no L2 interface of its own; it is driven by `apply`.
	"plugin": "apply",
}

// exempt packages are outside the layering: test doubles (policed by
// TestNoFakesInBinary instead) and this package itself.
func exempt(pkg string) bool {
	return pkg == "." ||
		pkg == "tools" ||
		strings.HasPrefix(pkg, "tools/") ||
		pkg == "fake" ||
		strings.HasPrefix(pkg, "fake/")
}

// Layer returns the layer of a package directory, and whether it has one.
func Layer(pkg string) (int, bool) {
	if exempt(pkg) {
		return 0, false
	}
	best, bestLen, found := 0, -1, false
	for prefix, l := range layerOf {
		var match bool
		if strings.HasSuffix(prefix, "/") {
			match = strings.HasPrefix(pkg, prefix)
		} else {
			match = pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
		}
		if match && len(prefix) > bestLen {
			best, bestLen, found = l, len(prefix), true
		}
	}
	return best, found
}

// group returns the top path element, which is how an L3 package finds its
// declaring L2 package.
func group(pkg string) string {
	if i := strings.IndexByte(pkg, '/'); i >= 0 {
		return pkg[:i]
	}
	return pkg
}

// CheckImportLayering enforces the layer rule and its single exception.
func CheckImportLayering(files []File) []Violation {
	var vs []Violation
	for _, f := range files {
		from, ok := Layer(f.Pkg)
		if !ok {
			continue
		}
		for _, im := range imports(f) {
			to, ok := internal(im.Path)
			if !ok {
				continue
			}
			toLayer, ok := Layer(to)
			if !ok {
				continue
			}
			// A package may import itself (same dir, different file).
			if to == f.Pkg {
				continue
			}
			// L3 -> L2 is the narrow exception, and it is narrow: an
			// implementation may import the ONE stage package that declares
			// its interface, for the type alone. Any other stage is out of
			// bounds even though it sits lower - `manager/dockerfile` has no
			// business knowing about `lookup`. This case is therefore decided
			// before the general "strictly lower is fine" rule, not after it.
			if from == 3 && toLayer == 2 {
				if declaredBy[group(f.Pkg)] == to {
					continue
				}
			} else if toLayer < from {
				continue // strictly lower: always fine
			}
			vs = append(vs, Violation{
				File: f.Path,
				Line: im.Line,
				Msg: fmt.Sprintf("L%d package %q imports L%d package %q: a package may import strictly lower layers only%s",
					from, f.Pkg, toLayer, to, exceptionHint(from, toLayer, f.Pkg, to)),
			})
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].String() < vs[j].String() })
	return vs
}

func exceptionHint(from, to int, pkg, imported string) string {
	if from == 3 && to == 2 {
		if want := declaredBy[group(pkg)]; want != "" {
			return fmt.Sprintf(" (the only L2 import permitted here is %q, which declares the interface)", want)
		}
	}
	return ""
}

// CheckRunnerHasNoImplementations keeps the orchestrator honest: it receives
// registries from `wire` and must not know which implementations exist.
func CheckRunnerHasNoImplementations(files []File) []Violation {
	var vs []Violation
	for _, f := range files {
		if group(f.Pkg) != "runner" || f.IsTest {
			continue
		}
		for _, im := range imports(f) {
			to, ok := internal(im.Path)
			if !ok {
				continue
			}
			if l, ok := Layer(to); ok && l == 3 {
				vs = append(vs, Violation{f.Path, im.Line,
					fmt.Sprintf("runner imports the implementation %q; it must receive registries from wire", to)})
			}
		}
	}
	return vs
}

// CheckWireIsNotImported stops the wiring package leaking into the tree. Only
// cmd/ and wire/ itself may reference it.
func CheckWireIsNotImported(files []File) []Violation {
	var vs []Violation
	for _, f := range files {
		if group(f.Pkg) == "wire" || group(f.Pkg) == "cmd" {
			continue
		}
		for _, im := range imports(f) {
			to, ok := internal(im.Path)
			if !ok {
				continue
			}
			if group(to) == "wire" {
				vs = append(vs, Violation{f.Path, im.Line,
					"only cmd/ and wire/ may import wire"})
			}
		}
	}
	return vs
}
