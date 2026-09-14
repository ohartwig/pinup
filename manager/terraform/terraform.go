// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package terraform extracts and rewrites provider and module versions in
// Terraform/OpenTofu configuration.
//
// Four shapes, measured across the extraction corpus
// (testdata/<root>/renovate-43.288.0/extract/koh-infra.json, key "terraform",
// 56 dependencies over 22 files):
//
//	terraform {
//	  required_providers {
//	    cloudflare = { source = "cloudflare/cloudflare", version = ">= 5.22" }
//	  }
//	}
//	                                       depType required_provider
//	                                       datasource terraform-provider
//	                                       registryUrls [registry.opentofu.org]
//
//	terraform { required_version = ">= 1.6" }
//	                                       depType required_version
//	                                       depName hashicorp/terraform
//	                                       datasource github-releases
//	                                       (looked up; the runner carries a GitHub token)
//
//	provider "aws" { ... }                 depType provider (the pre-1.0 form
//	                                       with no version pinned in the block
//	                                       itself)
//	                                       packageName hashicorp/aws
//	                                       skipReason unspecified-version
//
//	module "x" { source = "./network" }    depType module
//	                                       skipReason local
//
// The estate mirrors every provider through the OpenTofu registry rather than
// HashiCorp's - .tofurc excludes registry.opentofu.org's upstream fallback, so
// an unmirrored provider fails init with nothing to fall back to. That is why
// every terraform-provider dependency here carries exactly one registry URL,
// https://registry.opentofu.org, rather than the datasource's own default.
//
// A required_providers entry with no version attribute, and a registry module
// source (`namespace/name/provider` with a version constraint), are not in
// the corpus; they are extrapolated from the wording the corpus does record
// for a versionless legacy provider block and are marked unmeasured where
// they are built, below. A git or HTTP module source is not in the corpus
// either and is reported with skipReason "unsupported-source" rather than
// guessed at.
//
// No HCL library exists in this estate and none may be added: this package
// hand-rolls a small recursive-descent scanner over the HCL subset Terraform
// files in this shape actually use - blocks with zero or more string labels,
// `key = "string"` and `key = { ... }` attributes, `#`, `//` and `/* */`
// comments, and heredoc bodies skipped opaquely. It does not attempt the full
// HCL expression grammar: anything else - lists, function calls, references,
// ternaries - is scanned only far enough to find its end, never interpreted,
// exactly as the package comment on extract.Manager.Extract requires for a
// file this package cannot fully parse.
//
// Like every text manager in this estate, nothing is re-serialized. Every
// dependency reports a model.Locus - the byte span of its version string,
// quotes excluded - and Edit turns one model.Update into a model.Edit, a
// byte-range replacement, refusing when the bytes it recorded have moved.
package terraform

import (
	"context"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

// name is both the registry key (extract.Registry) and model.Dependency.Manager.
const name = "terraform"

// openTofuRegistry is the only provider registry this estate's mirrors serve.
// Measured: every terraform-provider dependency in the corpus carries exactly
// this URL, not the datasource's own hashicorp/registry.terraform.io default.
const openTofuRegistry = "https://registry.opentofu.org"

// lockFileName is the OpenTofu/Terraform provider lock file. It sits next to
// the .tf files it locks (or above them, at the nearest ancestor that ran
// init) - resolving that path is a checkout concern, not this manager's, so
// every dependency that carries a datasource simply names it here for the
// planner to find.
const lockFileName = ".terraform.lock.hcl"

// requiredVersionExtractVersion strips the leading "v" a GitHub release tag
// carries before comparison - the same pattern manager/dockerfile and
// manager/composerman apply for a github-releases datasource.
const requiredVersionExtractVersion = `v(?<version>.*)$`

// Manager implements extract.Manager for Terraform and OpenTofu configuration.
// It carries no state: every call is a pure function of the file it is given.
type Manager struct{}

// New returns a ready-to-use Manager.
func New() *Manager { return &Manager{} }

// Name implements extract.Manager.
func (m *Manager) Name() string { return name }

// FilePatterns implements extract.Manager. Both extensions the estate uses,
// at any depth - the ".tofu" suffix predates nothing here; the files are
// terraform syntax read by OpenTofu either way.
func (m *Manager) FilePatterns() []string {
	return []string{"**/*.tf", "**/*.tofu"}
}

// NeedsPlugin implements extract.Manager. Extraction and the edit it produces
// are plain text scanning; nothing here needs a toolchain of its own.
func (m *Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Extract implements extract.Manager. A file this scanner cannot make sense
// of yields fewer dependencies, never an error - the same "warn, don't stop"
// contract every text manager in this estate follows, made trivial here
// because the scanner never rejects a file outright: it resynchronizes past
// whatever it does not recognize and keeps going.
func (m *Manager) Extract(_ context.Context, f extract.File, cfg extract.ManagerConfig) (extract.Result, error) {
	items := parseBody(f.Content)

	var deps []model.Dependency
	for _, it := range items {
		if !it.isBlock {
			continue
		}
		switch it.blockType {
		case "terraform":
			deps = append(deps, terraformBlockDeps(f.Path, f.Content, it)...)
		case "provider":
			if d, ok := legacyProviderDep(f.Path, f.Content, it); ok {
				deps = append(deps, d)
			}
		case "module":
			if d, ok := moduleDep(f.Path, f.Content, it); ok {
				deps = append(deps, d)
			}
		}
	}

	applyManagerDefaults(deps, cfg)
	stampLockFiles(deps)
	extract.SortDeps(deps)
	return extract.Result{Deps: deps}, nil
}

// terraformBlockDeps reads the two shapes that live directly inside a
// `terraform { ... }` block: the required_version attribute, and the nested
// required_providers block.
func terraformBlockDeps(file string, src []byte, blk node) []model.Dependency {
	var deps []model.Dependency
	for _, it := range blk.body {
		switch {
		case !it.isBlock && it.attrIsString && it.attrName == "required_version":
			deps = append(deps, requiredVersionDep(file, src, it))
		case it.isBlock && it.blockType == "required_providers":
			deps = append(deps, requiredProvidersDeps(file, src, it)...)
		}
	}
	return deps
}

// valueLocus turns a strSpan - a version or source string's content span -
// into the model.Locus that brackets it: no digest group, and the line the
// opening quote sits on.
func valueLocus(s strSpan) model.Locus {
	return model.Locus{
		ValueStart: s.start, ValueEnd: s.end,
		DigestStart: model.NoDigest, DigestEnd: model.NoDigest,
		Line: s.line,
	}
}

// pointLocus is the zero-width Locus for a dependency with no version string
// to bracket - a skip reason names why, and the zero-width range on its own
// makes Edit refuse rather than guess, the same convention
// manager/dockerfile's internalReference uses.
func pointLocus(line int) model.Locus {
	return model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: line}
}

// requiredVersionDep builds the dependency on the tofu/terraform binary
// itself. It is reported and immediately skipped: resolving it needs a GitHub
// token this estate does not hand to every manager, the same shape
// manager/composerman uses for the PHP runtime.
func requiredVersionDep(file string, src []byte, it node) model.Dependency {
	return model.Dependency{
		Manager:        name,
		File:           file,
		CustomManager:  model.NoCustomManager,
		DepName:        "hashicorp/terraform",
		DepType:        "required_version",
		CurrentValue:   it.str.raw(src),
		Datasource:     "github-releases",
		Versioning:     "hashicorp",
		ExtractVersion: requiredVersionExtractVersion,
		Locus:          valueLocus(it.str),
	}
}

// requiredProvidersDeps reads every `name = { source = "...", version =
// "..." }` member of a required_providers block. Anything else in that
// block's body - a comment-only entry, an object this scanner mis-scanned -
// is silently not a provider and is skipped.
func requiredProvidersDeps(file string, src []byte, blk node) []model.Dependency {
	var deps []model.Dependency
	for _, it := range blk.body {
		if it.isBlock || !it.attrIsObject {
			continue
		}
		deps = append(deps, requiredProviderDep(file, src, it))
	}
	return deps
}

// requiredProviderDep builds one required_providers entry. packageName falls
// back to "hashicorp/NAME" when the entry names no source, which is
// Terraform's own default for an unqualified provider.
//
// A missing version attribute is not in the corpus for this shape; the
// skipReason wording is carried over unchanged from the legacy provider block
// case the corpus does record (see legacyProviderDep) rather than invented.
func requiredProviderDep(file string, src []byte, it node) model.Dependency {
	depName := it.attrName
	source, _, hasSource := findStringAttr(src, it.body, "source")
	packageName := source
	if !hasSource {
		packageName = "hashicorp/" + depName
	}

	dep := model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       depName,
		DepType:       "required_provider",
		PackageName:   packageName,
		Datasource:    "terraform-provider",
		RegistryURLs:  []string{openTofuRegistry},
	}

	if version, span, ok := findStringAttr(src, it.body, "version"); ok {
		dep.CurrentValue = version
		dep.Locus = valueLocus(span)
	} else {
		dep.SkipReason = "unspecified-version"
		dep.Locus = pointLocus(it.line)
	}
	return dep
}

// legacyProviderDep builds the dependency for a top-level `provider "name" {
// ... }` block - Terraform's pre-1.0 form, still used here for provider
// configuration (aliases, credentials) alongside a required_providers entry
// that names the real source.
//
// Measured: the corpus's packageName is always "hashicorp/NAME" here, even
// for cloudflare, whose actual source (cloudflare/cloudflare) is declared
// elsewhere in the same file's required_providers block. This block carries
// no source of its own to read, so it is never consulted - and never joined
// up with required_providers, which is a different dependency entirely.
func legacyProviderDep(file string, src []byte, blk node) (model.Dependency, bool) {
	if len(blk.labels) == 0 {
		return model.Dependency{}, false
	}
	depName := blk.labels[0].raw(src)

	dep := model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       depName,
		DepType:       "provider",
		PackageName:   "hashicorp/" + depName,
		Datasource:    "terraform-provider",
	}

	// Unmeasured: the corpus has no provider block that carries a version
	// attribute. If one is ever seen, this is the natural reading - a real
	// currentValue and no skip - rather than a guess made up for this report.
	if version, span, ok := findStringAttr(src, blk.body, "version"); ok {
		dep.CurrentValue = version
		dep.Locus = valueLocus(span)
	} else {
		dep.SkipReason = "unspecified-version"
		dep.Locus = pointLocus(blk.line)
	}
	return dep, true
}

// moduleDep builds the dependency for a `module "label" { source = ... }`
// block. A block with no source attribute is not valid Terraform and reports
// nothing - there is no version reference to carry.
func moduleDep(file string, src []byte, blk node) (model.Dependency, bool) {
	if len(blk.labels) == 0 {
		return model.Dependency{}, false
	}
	depName := blk.labels[0].raw(src)

	source, sourceSpan, hasSource := findStringAttr(src, blk.body, "source")
	if !hasSource {
		return model.Dependency{}, false
	}

	dep := model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       depName,
		DepType:       "module",
	}

	switch classifyModuleSource(source) {
	case moduleSourceLocal:
		// Measured: every module source in the corpus is this shape, and
		// every one of them carries this skipReason with no datasource.
		dep.SkipReason = "local"
		dep.Locus = pointLocus(sourceSpan.line)

	case moduleSourceRegistry:
		// Unmeasured: no registry module source appears in the corpus. This
		// is the natural reading of Terraform's own registry module
		// convention (source is "namespace/name/provider", version is a
		// constraint on it), built the same way a required_provider entry is.
		dep.Datasource = "terraform-module"
		dep.PackageName = source
		if version, span, ok := findStringAttr(src, blk.body, "version"); ok {
			dep.CurrentValue = version
			dep.Locus = valueLocus(span)
		} else {
			dep.SkipReason = "unspecified-version"
			dep.Locus = pointLocus(sourceSpan.line)
		}

	default: // moduleSourceUnsupported
		// Unmeasured: no git or HTTP module source appears in the corpus
		// either. Named plainly rather than folded into "local", which it is
		// not - a git or HTTP source is a real external dependency this
		// manager simply does not resolve yet.
		dep.SkipReason = "unsupported-source"
		dep.Locus = pointLocus(sourceSpan.line)
	}
	return dep, true
}

// moduleSourceKind classifies a module block's source string.
type moduleSourceKind int

const (
	moduleSourceLocal moduleSourceKind = iota
	moduleSourceRegistry
	moduleSourceUnsupported
)

// classifyModuleSource tells a local path, a registry source and everything
// else apart. A registry source is "namespace/name/provider", optionally
// prefixed with a "host/" - three or four non-empty slash-separated segments
// and nothing that looks like a VCS or URL scheme.
func classifyModuleSource(source string) moduleSourceKind {
	switch {
	case strings.HasPrefix(source, "./"), strings.HasPrefix(source, "../"):
		return moduleSourceLocal
	case strings.Contains(source, "://"),
		strings.HasPrefix(source, "git::"),
		strings.HasPrefix(source, "hg::"),
		strings.HasPrefix(source, "git@"),
		strings.HasSuffix(source, ".git"),
		strings.HasPrefix(source, "github.com/"),
		strings.HasPrefix(source, "bitbucket.org/"):
		return moduleSourceUnsupported
	}
	segs := strings.Split(source, "/")
	if len(segs) == 3 || len(segs) == 4 {
		for _, s := range segs {
			if s == "" {
				return moduleSourceUnsupported
			}
		}
		return moduleSourceRegistry
	}
	return moduleSourceUnsupported
}

// findStringAttr returns the raw content, and the strSpan it came from, of
// the first `name = "value"` attribute in body, or ok=false when body has
// none by that name - covering both "no such key" and "the value there is
// not a plain string".
func findStringAttr(src []byte, body []node, wantName string) (value string, span strSpan, ok bool) {
	for _, it := range body {
		if it.isBlock || !it.attrIsString || it.attrName != wantName {
			continue
		}
		return it.str.raw(src), it.str, true
	}
	return "", strSpan{}, false
}

// applyManagerDefaults stamps ManagerConfig's fallback versioning onto an
// actionable dependency whose own extraction named none - the same contract
// extract.ManagerConfig documents and manager/dockerfile and
// manager/composerman follow. A skipped dependency is never looked up, so it
// is left alone: stamping a registry or versioning onto it would invent
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

// stampLockFiles records .terraform.lock.hcl against every dependency that
// carries a datasource. Extract never sees that file - a manager touches only
// the one file discovery handed it - so LockedVersion itself stays empty
// here; the caller that does have both files populates it after calling
// Extract, the same division manager/composerman uses for composer.lock.
func stampLockFiles(deps []model.Dependency) {
	for i := range deps {
		if deps[i].Datasource != "" {
			deps[i].LockFiles = []string{lockFileName}
		}
	}
}

// Edit implements extract.Manager. It refuses, rather than guesses, when the
// bytes it recorded at extraction time no longer match what is on disk now -
// that is the file having changed underneath the run, and writing anyway
// would corrupt it.
func (m *Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	l := up.Dep.Locus
	if l.ValueStart < 0 || l.ValueEnd > len(f.Content) || l.ValueStart >= l.ValueEnd {
		return model.Edit{}, fmt.Errorf("terraform: %s: no editable range recorded for %s", f.Path, up.Dep.DepName)
	}
	got := string(f.Content[l.ValueStart:l.ValueEnd])
	if got != up.Dep.CurrentValue {
		return model.Edit{}, fmt.Errorf("terraform: %s changed since extraction: expected %q at [%d:%d], found %q",
			f.Path, up.Dep.CurrentValue, l.ValueStart, l.ValueEnd, got)
	}
	return model.Edit{
		File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
		Old: got, New: up.NewValue, Manager: name,
	}, nil
}

// LockedVersions reads a .terraform.lock.hcl and returns the version tofu
// init pinned for every provider it records, keyed by "namespace/name" - the
// last two slash-separated segments of the provider's registry source
// ("registry.opentofu.org/cloudflare/cloudflare" -> "cloudflare/cloudflare"),
// which is what a required_provider dependency's PackageName also holds.
//
// Extract never sees this file - a manager touches only the one file
// discovery handed it - so this is exported for the caller that does have
// both files (checkout knows the directory layout; a manager does not) to
// populate model.Dependency.LockedVersion itself after calling Extract.
func LockedVersions(lock []byte) (map[string]string, error) {
	items := parseBody(lock)

	out := make(map[string]string)
	for _, it := range items {
		if !it.isBlock || it.blockType != "provider" || len(it.labels) != 1 {
			continue
		}
		key := lastTwoSegments(it.labels[0].raw(lock))
		if key == "" {
			continue
		}
		if version, _, ok := findStringAttr(lock, it.body, "version"); ok {
			out[key] = version
		}
	}
	return out, nil
}

// lastTwoSegments returns the last two "/"-separated segments of s, or "" if
// s has fewer than two.
func lastTwoSegments(s string) string {
	segs := strings.Split(s, "/")
	if len(segs) < 2 {
		return ""
	}
	return segs[len(segs)-2] + "/" + segs[len(segs)-1]
}
