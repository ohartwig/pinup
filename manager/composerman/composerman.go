// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package composerman extracts and rewrites version constraints in PHP
// Composer's composer.json.
//
// Three shapes, measured against the extraction corpus
// (testdata/parity/renovate-43.288.0/extract/*.json, key "composer"):
//
//	"require": { "vendor/name": "^7.0" }         depType require      packagist
//	"require-dev": { "vendor/name": "*" }        depType require-dev  packagist
//	"require": { "php": "^8.5" }                 datasource github-tags,
//	                                              packageName containerbase/php-prebuild,
//	                                              (looked up; the runner carries a GitHub token)
//
// "ext-*" and "lib-*" keys name a PHP extension or system library the
// platform provides rather than a package Packagist can resolve; they carry
// skipReason "platform package" and no datasource, the same "skip, don't
// invent a lookup" contract a platform package carries.
//
// composer.json is plain JSON, but offsets into the file as read are the
// contract here (model.Locus), so this package does not decode it with
// encoding/json - that would lose the byte positions and force a
// re-serialization on write. Instead it walks the bytes with a small
// tolerant JSON scanner (jsonParser below) that records, for every string
// literal, the byte span of its content excluding the surrounding quotes.
// Nothing is ever re-serialized: Edit replaces exactly the recorded bytes.
//
// A composer.json's own "repositories" entries of type "composer" name
// additional package sources; their urls become each dependency's
// RegistryURLs, followed by Packagist itself unless a "packagist.org": false
// entry disables it - the estate runs a group-scoped GitLab package registry
// this way. Repositories of other types (vcs, path, ...) carry no version
// information a datasource could use and are ignored.
//
// composer.lock, which records the version actually installed, is a
// separate file Extract never sees - a manager touches only the file
// discovery handed it. LockedVersions is exported so the caller that does
// have both files can populate model.Dependency.LockedVersion itself.
package composerman

import (
	"context"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

// name is both the registry key (extract.Registry) and model.Dependency.Manager.
const name = "composer"

// defaultPackagistURL is Packagist itself, appended after every repository
// this file declares unless a "packagist.org": false entry says otherwise.
const defaultPackagistURL = "https://repo.packagist.org"

// Manager implements extract.Manager for composer.json. It carries no
// state: every call is a pure function of the file it is given.
type Manager struct{}

// New returns a ready-to-use Manager.
func New() *Manager { return &Manager{} }

// Name implements extract.Manager.
func (m *Manager) Name() string { return name }

// FilePatterns implements extract.Manager. This is the regex form Renovate's
// own composer manager matches by default: composer.json at any depth,
// optionally prefixed (docker-composer.json and the like).
func (m *Manager) FilePatterns() []string {
	return []string{`/(^|/)([\w-]*)composer\.json$/`}
}

// NeedsPlugin implements extract.Manager. Extraction is plain text scanning;
// nothing here needs a toolchain of its own. Applying an update still needs
// `composer update` to refresh composer.lock, but that is apply's concern,
// not extract's.
func (m *Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Extract implements extract.Manager. A file that is not a JSON object
// yields a warning and no dependencies - the same "warn, don't stop"
// contract every text manager in this estate follows.
func (m *Manager) Extract(_ context.Context, f extract.File, cfg extract.ManagerConfig) (extract.Result, error) {
	src := f.Content
	root, err := (&jsonParser{src: src}).parseValue()
	if err != nil || root.kind != jsonObject {
		msg := "root value is not a JSON object"
		if err != nil {
			msg = err.Error()
		}
		return extract.Result{Warnings: []model.Warning{{
			Stage: "extract", File: f.Path, Msg: "not valid JSON: " + msg,
		}}}, nil
	}

	urls := registryURLs(root, src)

	var deps []model.Dependency
	for _, section := range []string{"require", "require-dev"} {
		obj := lookupMember(root, section, src)
		if obj == nil || obj.kind != jsonObject {
			continue
		}
		for _, mem := range obj.members {
			if mem.key.kind != jsonString || mem.val.kind != jsonString {
				// Composer version constraints are always strings; anything
				// else is not a dependency this manager understands.
				continue
			}
			deps = append(deps, dependency(f.Path, section, mem, src, urls))
		}
	}

	applyManagerDefaults(deps, cfg)
	extract.SortDeps(deps)
	// LockFiles is reported both per dependency (below) and for the file as
	// a whole, so the planner knows a composer plugin will be needed for
	// this file even when every dependency in it turns out to be held.
	return extract.Result{Deps: deps, LockFiles: []string{"composer.lock"}}, nil
}

// dependency builds one model.Dependency from a "name": "value" member of
// require or require-dev.
func dependency(file, depType string, mem jsonMember, src []byte, urls []string) model.Dependency {
	depName := mem.key.raw(src)
	dep := model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       depName,
		DepType:       depType,
		CurrentValue:  mem.val.raw(src),
		Locus: model.Locus{
			ValueStart:  mem.val.contentStart,
			ValueEnd:    mem.val.contentEnd,
			DigestStart: model.NoDigest,
			DigestEnd:   model.NoDigest,
			Line:        lineOf(src, mem.val.contentStart),
		},
		// Renovate reports composer.lock alongside every dependency it
		// extracts here, whether or not that dependency turns out to be
		// actionable.
		LockFiles: []string{"composer.lock"},
	}

	switch {
	case depName == "php":
		// The runtime itself: not a Packagist package. containerbase's
		// prebuild tags are what resolves a PHP version (measured with the
		// runner's GitHub token in place; without one Renovate skips it as
		// github-token-required, which the first capture recorded).
		dep.Datasource = "github-tags"
		dep.PackageName = "containerbase/php-prebuild"
		// The manager's scheme, not the datasource's: "^8.5" is a
		// composer range, and github-tags would read it as semver and
		// refuse it (measured live: "current value ^8.5 is not a valid
		// semver version" against Renovate's "update dependency php to
		// ^8.5.10").
		dep.Versioning = "composer"
	case strings.HasPrefix(depName, "ext-"), strings.HasPrefix(depName, "lib-"):
		// A platform requirement: a PHP extension or system library the
		// runtime image provides, not a package any datasource can look up.
		dep.SkipReason = "platform package"
	default:
		dep.Datasource = "packagist"
		if len(urls) > 0 {
			dep.RegistryURLs = urls
		}
	}
	return dep
}

// applyManagerDefaults stamps ManagerConfig's fallback registry URLs and
// versioning onto an actionable dependency whose own extraction named none -
// the same contract extract.ManagerConfig documents and manager/dockerfile
// follows. A skipped dependency (php, a platform package) is never looked
// up, so it is left alone: stamping a registry onto it would invent
// information the corpus does not record.
func applyManagerDefaults(deps []model.Dependency, cfg extract.ManagerConfig) {
	for i := range deps {
		if deps[i].SkipReason != "" {
			continue
		}
		if deps[i].Versioning == "" && cfg.Versioning != "" {
			deps[i].Versioning = cfg.Versioning
		}
		if len(deps[i].RegistryURLs) == 0 && len(cfg.RegistryURLs) > 0 {
			deps[i].RegistryURLs = cfg.RegistryURLs
		}
	}
}

// registryURLs reads the "repositories" member, if any, and returns the
// registries a Packagist lookup for this file should consult: every
// "type": "composer" entry's url, in file order, followed by Packagist
// itself unless a "packagist.org": false entry disables it. It returns nil
// when the file declares no "repositories" at all, leaving the decision to
// ManagerConfig's default.
func registryURLs(root jsonNode, src []byte) []string {
	repos := lookupMember(root, "repositories", src)
	if repos == nil {
		return nil
	}

	var composerURLs []string
	disablePackagist := false

	visit := func(entry jsonNode) {
		if entry.kind != jsonObject {
			return
		}
		var typ, url string
		for _, mem := range entry.members {
			if mem.key.kind != jsonString {
				continue
			}
			switch mem.key.raw(src) {
			case "type":
				if mem.val.kind == jsonString {
					typ = mem.val.raw(src)
				}
			case "url":
				if mem.val.kind == jsonString {
					url = mem.val.raw(src)
				}
			case "packagist.org":
				if mem.val.kind == jsonOther && isFalse(mem.val, src) {
					disablePackagist = true
				}
			}
		}
		if typ == "composer" && url != "" {
			composerURLs = append(composerURLs, url)
		}
	}

	switch repos.kind {
	case jsonArray:
		for _, item := range repos.items {
			visit(item)
		}
	case jsonObject:
		// The named-map form: "repositories": {"packagist.org": false, "my-repo": {...}}.
		for _, mem := range repos.members {
			if mem.key.kind == jsonString && mem.key.raw(src) == "packagist.org" &&
				mem.val.kind == jsonOther && isFalse(mem.val, src) {
				disablePackagist = true
				continue
			}
			visit(mem.val)
		}
	}

	urls := append([]string{}, composerURLs...)
	if !disablePackagist {
		urls = append(urls, defaultPackagistURL)
	}
	return urls
}

// isFalse reports whether a jsonOther value (a bare literal: true, false,
// null or a number) is the literal "false".
func isFalse(n jsonNode, src []byte) bool {
	return string(src[n.start:n.end]) == "false"
}

// lookupMember returns the value of one member of a JSON object, or nil when
// obj is not an object or has no such member.
func lookupMember(obj jsonNode, key string, src []byte) *jsonNode {
	if obj.kind != jsonObject {
		return nil
	}
	for i := range obj.members {
		if obj.members[i].key.kind == jsonString && obj.members[i].key.raw(src) == key {
			return &obj.members[i].val
		}
	}
	return nil
}

// lineOf counts newlines up to offset, for the human-readable Locus.Line -
// offsets, not lines, are what Edit uses to locate bytes.
func lineOf(src []byte, offset int) int {
	line := 1
	for i := range min(offset, len(src)) {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

// Edit implements extract.Manager. It refuses, rather than guesses, when the
// bytes it recorded at extraction time no longer match what is on disk now -
// that is the file having changed underneath the run, and writing anyway
// would corrupt it.
func (m *Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	l := up.Dep.Locus
	if l.ValueStart < 0 || l.ValueEnd > len(f.Content) || l.ValueStart >= l.ValueEnd {
		return model.Edit{}, fmt.Errorf("composer: %s: no editable range recorded for %s", f.Path, up.Dep.DepName)
	}
	got := string(f.Content[l.ValueStart:l.ValueEnd])
	if got != up.Dep.CurrentValue {
		return model.Edit{}, fmt.Errorf("composer: %s changed since extraction: expected %q at [%d:%d], found %q",
			f.Path, up.Dep.CurrentValue, l.ValueStart, l.ValueEnd, got)
	}
	return model.Edit{
		File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
		Old: got, New: up.NewValue, Manager: name,
	}, nil
}

// LockedVersions reads a composer.lock and returns the installed version of
// every package named in its "packages" and "packages-dev" sections, keyed
// by package name.
//
// Extract never sees composer.lock - a manager touches only the one file
// discovery handed it - so this is exported for the caller that does have
// both files (checkout knows the directory layout; a manager does not) to
// populate model.Dependency.LockedVersion itself after calling Extract.
func LockedVersions(lock []byte) (map[string]string, error) {
	root, err := (&jsonParser{src: lock}).parseValue()
	if err != nil {
		return nil, fmt.Errorf("composer: lock file is not valid JSON: %w", err)
	}
	if root.kind != jsonObject {
		return nil, fmt.Errorf("composer: lock file root is not a JSON object")
	}

	out := make(map[string]string)
	for _, section := range []string{"packages", "packages-dev"} {
		arr := lookupMember(root, section, lock)
		if arr == nil || arr.kind != jsonArray {
			continue
		}
		for _, item := range arr.items {
			if item.kind != jsonObject {
				continue
			}
			nameNode := lookupMember(item, "name", lock)
			verNode := lookupMember(item, "version", lock)
			if nameNode == nil || verNode == nil || nameNode.kind != jsonString || verNode.kind != jsonString {
				continue
			}
			out[nameNode.raw(lock)] = verNode.raw(lock)
		}
	}
	return out, nil
}

// jsonKind is the shape of one scanned JSON value.
type jsonKind int

const (
	jsonString jsonKind = iota
	jsonObject
	jsonArray
	jsonOther // a number, true, false or null - scanned but never interpreted
)

// jsonNode is one JSON value with its byte span in the source it was parsed
// from. For jsonString, contentStart/contentEnd bracket the value between
// its quotes - the exact span model.Locus needs, and the exact span a raw
// slice of the source reproduces byte for byte.
type jsonNode struct {
	kind                     jsonKind
	start, end               int // the full value, quotes/brackets included
	contentStart, contentEnd int // jsonString only: between the quotes
	members                  []jsonMember
	items                    []jsonNode
}

// jsonMember is one "key": value pair of a jsonObject, in file order.
type jsonMember struct {
	key jsonNode
	val jsonNode
}

// raw returns a jsonString node's content exactly as written, escapes and
// all. composer.json version constraints and package names never contain an
// escape sequence in practice, so this is not unescaped - doing so would
// break the invariant that model.Locus brackets exactly model.Dependency's
// CurrentValue.
func (n jsonNode) raw(src []byte) string {
	return string(src[n.contentStart:n.contentEnd])
}

// jsonParser is a tolerant, allocation-light scanner over JSON bytes. It
// exists instead of encoding/json because encoding/json discards the byte
// offsets a text manager needs to report a model.Locus and to Edit without
// re-serializing the file.
//
// It accepts exactly the JSON grammar; a composer.json that fails to parse
// yields a warning from Extract, not a panic.
type jsonParser struct {
	src []byte
	pos int
}

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) parseValue() (jsonNode, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return jsonNode{}, fmt.Errorf("unexpected end of input")
	}
	switch p.src[p.pos] {
	case '"':
		return p.parseString()
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	default:
		return p.parseOther()
	}
}

func (p *jsonParser) parseString() (jsonNode, error) {
	start := p.pos
	if p.pos >= len(p.src) || p.src[p.pos] != '"' {
		return jsonNode{}, fmt.Errorf("expected a string at byte %d", p.pos)
	}
	p.pos++
	contentStart := p.pos
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\\':
			// Skip the escaped byte without decoding it: this parser reports
			// spans, not values, and an escape is exceedingly rare in a
			// composer.json version constraint or package name.
			p.pos += 2
		case '"':
			contentEnd := p.pos
			p.pos++
			return jsonNode{kind: jsonString, start: start, end: p.pos, contentStart: contentStart, contentEnd: contentEnd}, nil
		default:
			p.pos++
		}
	}
	return jsonNode{}, fmt.Errorf("unterminated string starting at byte %d", start)
}

func (p *jsonParser) parseObject() (jsonNode, error) {
	start := p.pos
	p.pos++ // consume '{'
	var members []jsonMember

	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		return jsonNode{kind: jsonObject, start: start, end: p.pos, members: members}, nil
	}

	for {
		p.skipSpace()
		key, err := p.parseString()
		if err != nil {
			return jsonNode{}, fmt.Errorf("object key: %w", err)
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return jsonNode{}, fmt.Errorf("expected ':' at byte %d", p.pos)
		}
		p.pos++
		val, err := p.parseValue()
		if err != nil {
			return jsonNode{}, err
		}
		members = append(members, jsonMember{key: key, val: val})

		p.skipSpace()
		if p.pos >= len(p.src) {
			return jsonNode{}, fmt.Errorf("unterminated object starting at byte %d", start)
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return jsonNode{kind: jsonObject, start: start, end: p.pos, members: members}, nil
		default:
			return jsonNode{}, fmt.Errorf("expected ',' or '}' at byte %d", p.pos)
		}
	}
}

func (p *jsonParser) parseArray() (jsonNode, error) {
	start := p.pos
	p.pos++ // consume '['
	var items []jsonNode

	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		return jsonNode{kind: jsonArray, start: start, end: p.pos, items: items}, nil
	}

	for {
		val, err := p.parseValue()
		if err != nil {
			return jsonNode{}, err
		}
		items = append(items, val)

		p.skipSpace()
		if p.pos >= len(p.src) {
			return jsonNode{}, fmt.Errorf("unterminated array starting at byte %d", start)
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return jsonNode{kind: jsonArray, start: start, end: p.pos, items: items}, nil
		default:
			return jsonNode{}, fmt.Errorf("expected ',' or ']' at byte %d", p.pos)
		}
	}
}

// parseOther scans a bare literal - a number, true, false or null - up to
// the next delimiter. Its content is never interpreted; the only literal
// this package inspects the bytes of is "false", for "packagist.org": false.
func (p *jsonParser) parseOther() (jsonNode, error) {
	start := p.pos
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			if p.pos == start {
				return jsonNode{}, fmt.Errorf("unexpected byte %q at %d", p.src[p.pos], p.pos)
			}
			return jsonNode{kind: jsonOther, start: start, end: p.pos}, nil
		default:
			p.pos++
		}
	}
	if p.pos == start {
		return jsonNode{}, fmt.Errorf("unexpected end of input at byte %d", start)
	}
	return jsonNode{kind: jsonOther, start: start, end: p.pos}, nil
}
