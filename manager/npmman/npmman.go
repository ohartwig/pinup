// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package npmman extracts and rewrites dependency versions in package.json.
//
// Behaviour is measured against the pinned Renovate container's own npm
// manager output, recorded under testdata/<root> - nothing is copied from the
// Renovate tree itself (AGPL-3.0; see CLAUDE.md's licensing section).
// Measured: depType is the section name verbatim ("dependencies",
// "devDependencies", "optionalDependencies", "peerDependencies"); Renovate
// additionally records a human-readable "prettyDepType" ("dependency",
// "devDependency", ...), which model.Dependency has no field for and this
// package does not invent one for.
//
// package.json is scanned byte by byte rather than decoded with
// encoding/json: a Locus must bracket the exact bytes of a version string as
// they sit in the file, with no re-serialization, so that Edit can replace
// only those bytes and leave comments, key order and formatting untouched. The
// scanner below is a small, string-aware recursive-descent walk - just enough
// JSON to find the four dependency sections and skip everything else
// correctly, including nested objects and arrays that are not dependencies.
//
// Beyond the four sections, three more shapes are measured (the public
// fixture root's frontend, 2026-09-14): engines.node is a dependency "node"
// on the node-version datasource with depType "engines"; packageManager
// ("pnpm@10.15.0") is a dependency on the tool with depType "packageManager"
// and the version after the "@"; and pnpm.overrides is a section with
// depType "pnpm.overrides" whose entries carry packageName as well. Other
// engines, volta, and the top-level overrides and resolutions blocks are not
// measured and are left undone rather than guessed at.
//
// yarn.lock and pnpm-lock.yaml are a different lockfile format entirely and
// are out of scope; only package-lock.json is read, through LockedVersions.
package npmman

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

// name is both the registry key (extract.Registry) and model.Dependency.Manager.
const name = "npm"

// lockFileName is the sibling file every dependency this manager reports
// names in its LockFiles - the manager itself is handed only package.json,
// never the lock, so the planner is what pairs them up. LockFiles lists the
// candidates in the order the run tries them: npm's, then yarn's.
const lockFileName = "package-lock.json"

// yarnLockFileName is yarn's lock, read in both its shapes: the classic
// "# yarn lockfile v1" and the berry "__metadata" YAML. Measured on the
// estate: development/external-ext/blog runs yarn classic, and Renovate
// refreshes its yarn.lock the way it refreshes a package-lock.json.
const yarnLockFileName = "yarn.lock"

// lockFileNames are the lock files this manager's dependencies may be
// paired with, in the order tried.
var lockFileNames = []string{lockFileName, "npm-shrinkwrap.json", yarnLockFileName}

// sectionDepTypes are the four object keys package.json uses for a versioned
// dependency, and become DepType verbatim.
var sectionDepTypes = map[string]bool{
	"dependencies":         true,
	"devDependencies":      true,
	"optionalDependencies": true,
	"peerDependencies":     true,
}

// engineDepTypes is the engines block's depType; engineDatasources maps the
// engine names with a measured datasource. An engine not listed is recorded
// and held, not resolved against a guessed registry.
const enginesDepType = "engines"

var engineDatasources = map[string]string{"node": "node-version"}

// packageManagerDepType is the depType of the packageManager field.
const packageManagerDepType = "packageManager"

// pnpmOverridesDepType is the depType of entries under pnpm.overrides.
const pnpmOverridesDepType = "pnpm.overrides"

// Manager implements extract.Manager for package.json. It carries no state:
// every call is a pure function of the file it is given.
type Manager struct{}

// New returns a ready-to-use Manager.
func New() *Manager { return &Manager{} }

// Name implements extract.Manager.
func (m *Manager) Name() string { return name }

// FilePatterns implements extract.Manager. package.json at any depth - a
// monorepo's per-package manifests included.
func (m *Manager) FilePatterns() []string { return []string{`/(^|/)package\.json$/`} }

// NeedsPlugin implements extract.Manager. Reading and rewriting package.json
// is plain text work; nothing here needs a toolchain of its own. (Resolving
// the lock file back into place after an edit is apply's business, not
// extraction's.)
func (m *Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Extract implements extract.Manager. A file that is not a JSON object, or
// that the scanner cannot walk to its end, yields a warning and no
// dependencies - the same "warn, don't stop the run" contract the interface
// documents.
func (m *Manager) Extract(_ context.Context, f extract.File, cfg extract.ManagerConfig) (extract.Result, error) {
	sc := &scanner{file: f.Path, src: f.Content}
	if err := sc.run(); err != nil {
		return extract.Result{Warnings: []model.Warning{{
			Stage: "extract", File: f.Path, Msg: "not valid JSON: " + err.Error(),
		}}}, nil
	}

	applyManagerDefaults(sc.deps, cfg)
	extract.SortDeps(sc.deps)

	res := extract.Result{Deps: sc.deps}
	if len(sc.deps) > 0 {
		res.LockFiles = lockFileNames
	}
	return res, nil
}

// applyManagerDefaults stamps ManagerConfig's fallback registry URLs and
// versioning onto a dependency whose own extraction named none - the same
// contract extract.ManagerConfig documents and manager/dockerfile follows.
func applyManagerDefaults(deps []model.Dependency, cfg extract.ManagerConfig) {
	for i := range deps {
		if deps[i].Versioning == "" && cfg.Versioning != "" {
			deps[i].Versioning = cfg.Versioning
		}
		if len(deps[i].RegistryURLs) == 0 && len(cfg.RegistryURLs) > 0 {
			deps[i].RegistryURLs = cfg.RegistryURLs
		}
	}
}

// scanner walks one package.json's bytes once, top to bottom, collecting a
// Dependency for every name found in one of the four dependency sections.
type scanner struct {
	file string
	src  []byte
	deps []model.Dependency
}

// run walks the top-level object. Any key that is not one of the four
// dependency sections is skipped generically via skipValue - a "scripts"
// block, an author string, a nested "peerDependenciesMeta" - none of it is a
// dependency and none of it may be misread as one.
func (sc *scanner) run() error {
	i := skipWS(sc.src, 0)
	if i >= len(sc.src) || sc.src[i] != '{' {
		return fmt.Errorf("expected a top-level JSON object")
	}
	i++

	for {
		i = skipWS(sc.src, i)
		if i >= len(sc.src) {
			return fmt.Errorf("unexpected end of file inside the top-level object")
		}
		if sc.src[i] == '}' {
			return nil
		}

		keyStart, keyEnd, next, ok := scanString(sc.src, i)
		if !ok {
			return fmt.Errorf("expected a key at offset %d", i)
		}
		key := string(sc.src[keyStart:keyEnd])

		i = skipWS(sc.src, next)
		if i >= len(sc.src) || sc.src[i] != ':' {
			return fmt.Errorf("expected ':' after key %q", key)
		}
		i = skipWS(sc.src, i+1)

		switch {
		case sectionDepTypes[key] || key == enginesDepType:
			n, err := sc.section(key, i)
			if err != nil {
				return err
			}
			i = n
		case key == packageManagerDepType:
			n, err := sc.packageManager(i)
			if err != nil {
				return err
			}
			i = n
		case key == "pnpm":
			n, err := sc.pnpm(i)
			if err != nil {
				return err
			}
			i = n
		default:
			n, ok := skipValue(sc.src, i)
			if !ok {
				return fmt.Errorf("could not skip the value of key %q", key)
			}
			i = n
		}

		i = skipWS(sc.src, i)
		if i >= len(sc.src) {
			return fmt.Errorf("unexpected end of file after key %q", key)
		}
		switch sc.src[i] {
		case ',':
			i++
		case '}':
			return nil
		default:
			return fmt.Errorf("expected ',' or '}' after key %q, found %q", key, sc.src[i])
		}
	}
}

// section walks one dependency section's object, appending a Dependency for
// every "name": "value" pair. i is positioned at the section's value, right
// after the key's colon.
//
// A section whose value is not an object (a malformed package.json, or a
// preset that put something else there) is skipped like any other value:
// there is nothing inside it shaped like a dependency.
func (sc *scanner) section(depType string, i int) (int, error) {
	if i >= len(sc.src) || sc.src[i] != '{' {
		n, ok := skipValue(sc.src, i)
		if !ok {
			return 0, fmt.Errorf("could not skip the non-object value of %q", depType)
		}
		return n, nil
	}
	i++

	for {
		i = skipWS(sc.src, i)
		if i >= len(sc.src) {
			return 0, fmt.Errorf("unexpected end of file inside %q", depType)
		}
		if sc.src[i] == '}' {
			return i + 1, nil
		}

		nameStart, nameEnd, next, ok := scanString(sc.src, i)
		if !ok {
			return 0, fmt.Errorf("expected a dependency name inside %q at offset %d", depType, i)
		}
		depName := string(sc.src[nameStart:nameEnd])

		i = skipWS(sc.src, next)
		if i >= len(sc.src) || sc.src[i] != ':' {
			return 0, fmt.Errorf("expected ':' after %q in %q", depName, depType)
		}
		i = skipWS(sc.src, i+1)

		dep, n := sc.dependency(depType, depName, i)
		sc.deps = append(sc.deps, dep)
		i = n

		i = skipWS(sc.src, i)
		if i >= len(sc.src) {
			return 0, fmt.Errorf("unexpected end of file after %q in %q", depName, depType)
		}
		switch sc.src[i] {
		case ',':
			i++
		case '}':
			return i + 1, nil
		default:
			return 0, fmt.Errorf("expected ',' or '}' after %q in %q, found %q", depName, depType, sc.src[i])
		}
	}
}

// packageManager reads the packageManager field: "<tool>@<version>", the
// tool a dependency on the npm registry and the Locus around the version
// alone. i is positioned at the value. A value of another shape - no "@",
// or a "+<hash>" suffix, which is not measured - is recorded and held.
func (sc *scanner) packageManager(i int) (int, error) {
	dep := model.Dependency{
		Manager:       name,
		File:          sc.file,
		CustomManager: model.NoCustomManager,
		DepType:       packageManagerDepType,
		Datasource:    name,
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: lineAt(sc.src, i)},
	}
	if i >= len(sc.src) || sc.src[i] != '"' {
		n, ok := skipValue(sc.src, i)
		if !ok {
			return 0, fmt.Errorf("could not skip the non-string value of %q", packageManagerDepType)
		}
		return n, nil
	}
	valStart, valEnd, next, ok := scanString(sc.src, i)
	if !ok {
		return 0, fmt.Errorf("malformed %q value", packageManagerDepType)
	}
	raw := string(sc.src[valStart:valEnd])
	tool, version, found := strings.Cut(raw, "@")
	if tool == "" || !found {
		return next, nil
	}
	dep.DepName = tool
	dep.CurrentValue = version
	dep.Locus.ValueStart = valStart + len(tool) + 1
	dep.Locus.ValueEnd = valEnd
	if strings.Contains(version, "+") {
		dep.SkipReason = "packageManager with an integrity hash is not handled"
	}
	sc.deps = append(sc.deps, dep)
	return next, nil
}

// pnpm walks the pnpm block for its overrides, which are a dependency
// section under the depType "pnpm.overrides"; every other key in the block
// is skipped. i is positioned at the block's value.
func (sc *scanner) pnpm(i int) (int, error) {
	if i >= len(sc.src) || sc.src[i] != '{' {
		n, ok := skipValue(sc.src, i)
		if !ok {
			return 0, fmt.Errorf("could not skip the non-object value of \"pnpm\"")
		}
		return n, nil
	}
	i++
	for {
		i = skipWS(sc.src, i)
		if i >= len(sc.src) {
			return 0, fmt.Errorf("unexpected end of file inside \"pnpm\"")
		}
		if sc.src[i] == '}' {
			return i + 1, nil
		}
		keyStart, keyEnd, next, ok := scanString(sc.src, i)
		if !ok {
			return 0, fmt.Errorf("expected a key inside \"pnpm\" at offset %d", i)
		}
		key := string(sc.src[keyStart:keyEnd])
		i = skipWS(sc.src, next)
		if i >= len(sc.src) || sc.src[i] != ':' {
			return 0, fmt.Errorf("expected ':' after %q in \"pnpm\"", key)
		}
		i = skipWS(sc.src, i+1)
		var err error
		if key == "overrides" {
			i, err = sc.section(pnpmOverridesDepType, i)
			if err != nil {
				return 0, err
			}
		} else {
			n, ok := skipValue(sc.src, i)
			if !ok {
				return 0, fmt.Errorf("could not skip the value of \"pnpm\".%q", key)
			}
			i = n
		}
		i = skipWS(sc.src, i)
		if i >= len(sc.src) {
			return 0, fmt.Errorf("unexpected end of file after \"pnpm\".%q", key)
		}
		switch sc.src[i] {
		case ',':
			i++
		case '}':
			return i + 1, nil
		default:
			return 0, fmt.Errorf("expected ',' or '}' after \"pnpm\".%q, found %q", key, sc.src[i])
		}
	}
}

// dependency builds one Dependency from a "name": <value> pair and returns
// the byte offset right after the value, so the caller can resume scanning.
//
// i is positioned at the value, right after the name's colon.
func (sc *scanner) dependency(depType, depName string, i int) (model.Dependency, int) {
	dep := model.Dependency{
		Manager:       name,
		File:          sc.file,
		CustomManager: model.NoCustomManager,
		DepName:       depName,
		DepType:       depType,
		Datasource:    name,
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: lineAt(sc.src, i)},
		LockFiles:     lockFileNames,
	}
	switch depType {
	case enginesDepType:
		dep.LockFiles = nil
		if ds, ok := engineDatasources[depName]; ok {
			dep.Datasource = ds
		} else {
			dep.Datasource = ""
			dep.SkipReason = "engine " + depName + " has no measured datasource"
		}
	case pnpmOverridesDepType:
		dep.PackageName = depName
	}

	if i >= len(sc.src) || sc.src[i] != '"' {
		// package.json only ever puts a version range in a dependency
		// section as a JSON string; anything else (an object, a number) is
		// not something a Locus can be drawn around.
		dep.SkipReason = "dependency value is not a JSON string"
		n, ok := skipValue(sc.src, i)
		if !ok {
			n = len(sc.src)
		}
		return dep, n
	}

	valStart, valEnd, next, ok := scanString(sc.src, i)
	if !ok {
		dep.SkipReason = "malformed string value"
		return dep, len(sc.src)
	}

	raw := string(sc.src[valStart:valEnd])
	dep.CurrentValue = raw
	dep.Locus.ValueStart = valStart
	dep.Locus.ValueEnd = valEnd
	if reason, skip := unresolvable(raw); skip {
		dep.SkipReason = reason
	}
	return dep, next
}

// unresolvable names why a dependency value is not a registry version, or
// returns ok=false when it is one. Everything that is not one of these
// prefixes is treated as a version or range verbatim - including dist-tags
// ("latest", "next") and wildcards ("*", "1.x"), which the npm datasource
// resolves on its own terms, not this manager's.
//
// npm alias syntax ("npm:other-package@^1.0.0") is not handled: none of the
// corpus vectors exercise it, and resolving it correctly means renaming
// DepName to the aliased package, which is a bigger change than a skip
// reason.
func unresolvable(v string) (reason string, ok bool) {
	switch {
	case strings.HasPrefix(v, "file:"):
		return "file reference is not a registry version", true
	case strings.HasPrefix(v, "link:"):
		return "link reference is not a registry version", true
	case strings.HasPrefix(v, "workspace:"):
		return "workspace reference is not a registry version", true
	case strings.HasPrefix(v, "git+"), strings.HasPrefix(v, "git://"):
		return "git reference is not a registry version", true
	case strings.HasPrefix(v, "github:"):
		return "github reference is not a registry version", true
	case strings.HasPrefix(v, "http://"), strings.HasPrefix(v, "https://"):
		return "URL reference is not a registry version", true
	}
	return "", false
}

// Edit implements extract.Manager. It refuses, rather than guesses, when the
// bytes it recorded at extraction time no longer match what is on disk now -
// that is the file having changed underneath the run, and writing anyway
// would corrupt it.
func (m *Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	l := up.Dep.Locus
	if l.ValueStart < 0 || l.ValueEnd > len(f.Content) || l.ValueStart >= l.ValueEnd {
		return model.Edit{}, fmt.Errorf("npmman: %s: no editable range recorded for %s", f.Path, up.Dep.DepName)
	}
	got := string(f.Content[l.ValueStart:l.ValueEnd])
	if got != up.Dep.CurrentValue {
		return model.Edit{}, fmt.Errorf("npmman: %s changed since extraction: expected %q at [%d:%d], found %q",
			f.Path, up.Dep.CurrentValue, l.ValueStart, l.ValueEnd, got)
	}
	return model.Edit{
		File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
		Old: got, New: up.NewValue, Manager: name,
	}, nil
}

// LockedVersions reads a package-lock.json and reports the installed version
// of every package it names, keyed by package name.
//
// The manager itself never sees this file - extract.Manager.Extract is
// handed one file at a time, and LockFiles only tells the planner that a lock
// exists beside package.json. Reading it back is this exported helper's job,
// for whichever stage is positioned to open a second file.
//
// lockfileVersion 2 and 3 name a package's version at
// packages["node_modules/<name>"].version; lockfileVersion 1 (or a lock with
// no lockfileVersion at all) names it at dependencies[<name>].version
// instead. Only the top-level entry for each name is reported: a
// "node_modules/foo/node_modules/bar" key is bar nested inside foo, which may
// carry a different version than the one actually resolved for a direct
// dependency named bar, so it is excluded rather than silently overwriting
// the real answer depending on map iteration order.
//
// A yarn.lock is read too, classic and berry; pnpm-lock.yaml is not.
func LockedVersions(lock []byte) (map[string]string, error) {
	return LockedVersionsFor(lock, "")
}

// LockedVersionsFor reads a lock for the manifest at member, a directory
// relative to the lock's own - "" for the manifest beside it, "apps/web"
// for a workspace member whose versions the root lock carries. npm hoists
// what it can to node_modules/<name>; a member that needs another version
// of a package gets it under <member>/node_modules/<name>, and that entry
// is the member's answer (lockfileVersion 2 and 3; the v1 shape knows no
// members). Measured 2026-09-15 on nozzleops/platform, whose four
// package.json share one root package-lock.json and carried no locked
// versions at all.
func LockedVersionsFor(lock []byte, member string) (map[string]string, error) {
	if trimmed := bytes.TrimSpace(lock); len(trimmed) > 0 && trimmed[0] != '{' {
		return yarnLockedVersions(lock)
	}
	var doc struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(lock, &doc); err != nil {
		return nil, fmt.Errorf("npmman: parsing package-lock.json: %w", err)
	}

	out := make(map[string]string)
	if doc.LockfileVersion >= 2 {
		nested := ""
		if member = strings.Trim(filepath.ToSlash(member), "/"); member != "" && member != "." {
			nested = member + "/node_modules/"
		}
		own := map[string]bool{}
		for key, pkg := range doc.Packages {
			if pkg.Version == "" {
				continue
			}
			if nested != "" {
				if depName, ok := strings.CutPrefix(key, nested); ok && !strings.Contains(depName, "/node_modules/") {
					out[depName] = pkg.Version
					own[depName] = true
					continue
				}
			}
			depName, ok := strings.CutPrefix(key, "node_modules/")
			if !ok {
				continue
			}
			if strings.Contains(depName, "/node_modules/") {
				continue // a transitive dependency nested inside another package
			}
			if !own[depName] {
				out[depName] = pkg.Version
			}
		}
		return out, nil
	}
	for depName, pkg := range doc.Dependencies {
		if pkg.Version == "" {
			continue
		}
		out[depName] = pkg.Version
	}
	return out, nil
}

// yarnEntryRE matches an entry header of either yarn lock format:
//
//	"@babel/code-frame@^7.0.0", "@babel/code-frame@^7.1.0":   (classic)
//	"@babel/code-frame@npm:^7.0.0":                            (berry)
//	lodash@^4.17.20:                                           (classic, unquoted)
//
// The dependency's name is everything before the last "@" of one selector,
// the "npm:" protocol stripped from what follows.
var yarnEntryRE = regexp.MustCompile(`^"?((?:@[^@"]+/)?[^@"\s,]+)@`)

// yarnLockedVersions reads the versions a yarn.lock resolves each direct
// selector to. Every selector of an entry maps to the entry's version; a
// package that appears under several ranges resolves to one version per
// range, and the last written wins, which for a lock is the same version.
func yarnLockedVersions(lock []byte) (map[string]string, error) {
	out := map[string]string{}
	var names []string
	for line := range strings.Lines(string(lock)) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line[0] != ' ' && strings.HasSuffix(line, ":") {
			// An entry header: one or more comma-separated selectors.
			names = names[:0]
			for _, sel := range strings.Split(strings.TrimSuffix(line, ":"), ",") {
				sel = strings.TrimSpace(sel)
				if m := yarnEntryRE.FindStringSubmatch(sel); m != nil {
					names = append(names, m[1])
				}
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "version") && len(names) > 0 {
			v := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "version:"), "version"))
			v = strings.Trim(v, `"`)
			for _, n := range names {
				out[n] = v
			}
			names = names[:0]
		}
	}
	if len(out) == 0 && !bytes.Contains(lock, []byte("yarn lockfile")) && !bytes.Contains(lock, []byte("__metadata")) {
		return nil, fmt.Errorf("npmman: yarn.lock: neither a classic nor a berry lock")
	}
	return out, nil
}

// YarnLockKind reports whether a yarn.lock is the classic v1 format or
// berry, from its header.
func YarnLockKind(lock []byte) string {
	if bytes.Contains(lock, []byte("__metadata")) {
		return "berry"
	}
	return "classic"
}

// --- a small, string-aware JSON scanner -------------------------------
//
// Just enough of the grammar to find object keys and string values and to
// skip everything else - numbers, booleans, null, nested objects and arrays
// of any depth - without ever building a document tree. Offsets returned are
// always byte offsets into the original source, never adjusted, which is what
// lets a Locus point straight at the file on disk.

// skipWS advances past JSON whitespace.
func skipWS(src []byte, i int) int {
	for i < len(src) {
		switch src[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString reads a JSON string literal starting at src[i] == '"'. It
// returns the byte span of the content between the quotes exactly as
// written - escape sequences are walked over, not decoded, because a Locus
// must bracket the real bytes of the file, not an unescaped copy of them.
// next is the offset just past the closing quote.
func scanString(src []byte, i int) (contentStart, contentEnd, next int, ok bool) {
	if i >= len(src) || src[i] != '"' {
		return 0, 0, i, false
	}
	contentStart = i + 1
	for j := contentStart; j < len(src); {
		switch src[j] {
		case '\\':
			j += 2 // skip the escape and whatever it escapes, "\"" included
		case '"':
			return contentStart, j, j + 1, true
		default:
			j++
		}
	}
	return 0, 0, len(src), false
}

// skipValue advances past one JSON value of any kind, starting at i (which
// may still be looking at leading whitespace).
func skipValue(src []byte, i int) (int, bool) {
	i = skipWS(src, i)
	if i >= len(src) {
		return i, false
	}
	switch src[i] {
	case '"':
		_, _, next, ok := scanString(src, i)
		return next, ok
	case '{':
		return skipContainer(src, i, '{', '}')
	case '[':
		return skipContainer(src, i, '[', ']')
	default:
		// A number, true, false or null: run to the next structural byte.
		j := i
		for j < len(src) && !isStructural(src[j]) {
			j++
		}
		if j == i {
			return i, false
		}
		return j, true
	}
}

// skipContainer advances past a bracketed value from its opening byte to
// just past its matching close, honouring string literals along the way so a
// brace or bracket inside a string is never mistaken for a structural one.
// Depth is tracked only for the (open, close) pair asked for: a differently
// bracketed value nested inside (an array inside an object, say) is walked
// over byte by byte without needing its own depth counter, because JSON
// brackets always balance textually once strings are excluded.
func skipContainer(src []byte, i int, open, close byte) (int, bool) {
	depth := 0
	for j := i; j < len(src); {
		switch src[j] {
		case '"':
			_, _, next, ok := scanString(src, j)
			if !ok {
				return j, false
			}
			j = next
			continue
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
		j++
	}
	return len(src), false
}

// isStructural reports whether b ends an unquoted JSON value (a number,
// true, false or null).
func isStructural(b byte) bool {
	switch b {
	case ',', '}', ']', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// lineAt reports the 1-based line containing byte offset pos, for humans
// reading a report; nothing here uses it to compute an edit.
func lineAt(src []byte, pos int) int {
	if pos > len(src) {
		pos = len(src)
	}
	return 1 + bytes.Count(src[:pos], []byte{'\n'})
}
