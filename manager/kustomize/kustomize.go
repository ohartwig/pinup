// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package kustomize extracts and rewrites the versioned references a
// kustomization file carries.
//
// Four shapes, measured across the extraction corpus
// (testdata/parity/renovate-43.288.0/extract/tfversion.json, key "kustomize",
// 12 dependencies over two files):
//
//	resources: [ github.com/OWNER/REPO/path?ref=V ]
//	components: [ https://github.com/OWNER/REPO/path?ref=V ]
//	                                       depType Kustomization
//	                                       datasource github-tags
//	                                       skipReason github-token-required
//	                                       (the estate carries no GitHub
//	                                       token, so Renovate skips these at
//	                                       extract time already)
//
//	resources: [ git::https://HOST/PATH.git//sub?ref=V ]
//	resources: [ ssh://git@HOST/PATH.git//sub?ref=V ]
//	                                       depType Kustomization
//	                                       datasource git-tags
//	                                       packageName the URL, minus the
//	                                       "git::" prefix, the "//sub" part
//	                                       and the query - with a userinfo
//	                                       redacted to "**redacted**", the
//	                                       same way Renovate's own logging
//	                                       redacts a URL's credentials
//
//	images: [ { name, newTag } ]
//	images: [ { name, newName, newTag } ]
//	images: [ { name, digest } ]
//	                                       depType Kustomization
//	                                       datasource docker
//	                                       depName/packageName newName if
//	                                       given, else name
//
//	images: [ { name, newTag, digest } ]  both a fixed tag and a fixed
//	                                       digest is not an editable
//	                                       reference Renovate can express as
//	                                       one value; reported and skipped
//	                                       rather than guessed at
//	                                       skipReason invalid-dependency-specification
//
//	helmCharts: [ { name, repo: https://..., version } ]
//	                                       depType HelmChart
//	                                       datasource helm
//	                                       registryUrls [repo]
//
//	helmCharts: [ { name, repo: oci://HOST/PATH, version } ]
//	                                       depType HelmChart
//	                                       datasource docker
//	                                       packageName HOST/PATH/NAME
//
// A local resources/components entry (a relative path, no "?ref=") carries
// no version and yields nothing - the same "not a dependency" reading
// manager/terraform gives a local module source. A resources/components form
// this package does not recognize (unmeasured: anything that is neither the
// github.com shape nor a "scheme://host/path.git" shape) is treated the same
// way, rather than guessed at.
//
// An images entry with only a name and neither a newTag nor a digest does
// nothing to a build and is not in the corpus; it is read the same way -
// nothing to look up, nothing reported. This is the one shape in this file
// extrapolated beyond what the corpus records.
//
// The file is read through yamlx as a node tree, exactly as
// manager/gitlabci does, so every offset it reports is a byte range into the
// file as read: nothing here is ever re-serialized, and an Edit replaces
// exactly the value (or, for a digest-only image, exactly the digest).
package kustomize

import (
	"context"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/yamlx"
)

// name is both the registry key (extract.Registry) and model.Dependency.Manager.
const name = "kustomize"

// Manager implements extract.Manager for kustomize.config.k8s.io manifests.
// It carries no state: every call is a pure function of the file it is given.
type Manager struct{}

// New returns a ready-to-use Manager.
func New() *Manager { return &Manager{} }

// Name implements extract.Manager.
func (*Manager) Name() string { return name }

// FilePatterns implements extract.Manager. Renovate's own kustomize manager
// matches "(^|/)kustomization\.ya?ml$" and "(^|/)Kustomization$"; these are
// the glob equivalent, at any depth.
func (*Manager) FilePatterns() []string {
	return []string{
		"**/kustomization.yaml",
		"**/kustomization.yml",
		"**/Kustomization",
	}
}

// NeedsPlugin implements extract.Manager. A kustomization file is read and
// rewritten as plain YAML text; nothing here needs a toolchain of its own.
func (*Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Extract implements extract.Manager.
func (m *Manager) Extract(_ context.Context, f extract.File, _ extract.ManagerConfig) (extract.Result, error) {
	var res extract.Result
	root, err := yamlx.ParseTree(f.Content)
	if err != nil {
		// A file that does not parse yields a warning and nothing else. One
		// broken manifest is not a reason to stop managing the rest of the
		// repository.
		res.Warnings = append(res.Warnings, model.Warning{
			Stage: "extract", File: f.Path, Msg: "not valid YAML: " + err.Error(),
		})
		return res, nil
	}
	if root.Kind == yamlx.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yamlx.MappingNode {
		// Not a mapping at all - an empty file, a bare scalar, a sequence.
		// Kustomize itself would refuse this; there is nothing to extract.
		return res, nil
	}

	w := &walker{file: f.Path, src: f.Content}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "resources", "components":
			w.refList(val)
		case "images":
			w.imageList(val)
		case "helmCharts":
			w.helmChartList(val)
		}
	}
	res.Deps = w.deps
	extract.SortDeps(res.Deps)
	return res, nil
}

type walker struct {
	file string
	src  []byte
	deps []model.Dependency
}

// refList reads one resources: or components: sequence. Both keys carry the
// same shape - a list of local paths and remote references - and are read
// identically.
func (w *walker) refList(n *yamlx.Node) {
	if n.Kind != yamlx.SequenceNode {
		return
	}
	for _, item := range n.Content {
		if item.Kind == yamlx.ScalarNode {
			w.ref(item)
		}
	}
}

// ref reads one resources/components entry. Only an entry carrying "?ref="
// names a version; everything else - a relative path, a plain URL with no
// ref - is not a dependency this manager can look up or edit.
func (w *walker) ref(n *yamlx.Node) {
	before, version, ok := strings.Cut(n.Value, "?ref=")
	if !ok {
		return
	}

	dep := model.Dependency{
		Manager:       name,
		File:          w.file,
		CustomManager: model.NoCustomManager,
		DepType:       "Kustomization",
		CurrentValue:  version,
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: n.Line},
	}

	switch {
	case isGitHubForm(before):
		dep.DepName = githubRepo(before)
		dep.Datasource = "github-tags"
		dep.SkipReason = "github-token-required"

	default:
		depName, packageName, ok := gitRepoRef(before)
		if !ok {
			return
		}
		dep.DepName = depName
		dep.PackageName = packageName
		dep.Datasource = "git-tags"
	}

	if base := scalarOffset(w.src, n); base >= 0 {
		dep.Locus.ValueStart = base + len(before) + len("?ref=")
		dep.Locus.ValueEnd = dep.Locus.ValueStart + len(version)
	}
	w.deps = append(w.deps, dep)
}

// isGitHubForm reports whether s is "github.com/..." or
// "https://github.com/...", the shape a GitHub-hosted resource or component
// takes.
func isGitHubForm(s string) bool {
	s, _ = strings.CutPrefix(s, "https://")
	return strings.HasPrefix(s, "github.com/")
}

// githubRepo reads "OWNER/REPO" off the front of a GitHub resource path -
// the segment after "github.com/" that has tags, not the sub-path a
// kustomize overlay may point at within it.
func githubRepo(s string) string {
	s, _ = strings.CutPrefix(s, "https://")
	s = strings.TrimPrefix(s, "github.com/")
	owner, rest, ok := strings.Cut(s, "/")
	if !ok {
		return s
	}
	repo, _, _ := strings.Cut(rest, "/")
	return owner + "/" + repo
}

// gitRepoRef reads a generic git source: an optional "git::" prefix, a
// scheme, an optional redacted userinfo, a host/path ending in ".git", and an
// optional "//sub" directory that is stripped along with the query before
// this function ever sees it.
//
// depName is the host/path with no scheme, no userinfo and no ".git" suffix -
// what the datasource actually keys tags on. packageName is the full URL
// Renovate would resolve against, with any userinfo replaced by
// "**redacted**" the way Renovate's own logging redacts credentials, and
// with the "//sub" part and query already gone.
func gitRepoRef(s string) (depName, packageName string, ok bool) {
	stripped, _ := strings.CutPrefix(s, "git::")
	if !strings.Contains(stripped, "://") || !strings.Contains(stripped, ".git") {
		return "", "", false
	}

	beforeGit, _, found := strings.Cut(stripped, ".git")
	if !found {
		return "", "", false
	}
	withGit := beforeGit + ".git"

	scheme, hostAndPath, ok := strings.Cut(withGit, "://")
	if !ok {
		return "", "", false
	}

	packageName = withGit
	if user, rest, hasAt := strings.Cut(hostAndPath, "@"); hasAt && !strings.Contains(user, "/") {
		packageName = scheme + "://**redacted**@" + rest
		hostAndPath = rest
	}
	depName = strings.TrimSuffix(hostAndPath, ".git")
	return depName, packageName, true
}

// imageList reads the images: sequence.
func (w *walker) imageList(n *yamlx.Node) {
	if n.Kind != yamlx.SequenceNode {
		return
	}
	for _, item := range n.Content {
		if item.Kind == yamlx.MappingNode {
			w.image(item)
		}
	}
}

// image reads one images: entry - a name, and any of newName, newTag or
// digest.
func (w *walker) image(item *yamlx.Node) {
	var nameNode, newNameNode, newTagNode, digestNode *yamlx.Node
	for i := 0; i+1 < len(item.Content); i += 2 {
		key, val := item.Content[i], item.Content[i+1]
		if val.Kind != yamlx.ScalarNode {
			continue
		}
		switch key.Value {
		case "name":
			nameNode = val
		case "newName":
			newNameNode = val
		case "newTag":
			newTagNode = val
		case "digest":
			digestNode = val
		}
	}
	if nameNode == nil {
		return
	}

	depName := nameNode.Value
	if newNameNode != nil {
		depName = newNameNode.Value
	}

	dep := model.Dependency{
		Manager:       name,
		File:          w.file,
		CustomManager: model.NoCustomManager,
		DepType:       "Kustomization",
		DepName:       depName,
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: nameNode.Line},
	}

	switch {
	case newTagNode != nil && digestNode != nil:
		// A fixed tag and a fixed digest together is not one editable
		// reference: which one is Renovate meant to bump? Reported, with
		// both spans recorded for a human to look at, and skipped rather
		// than guessed at.
		dep.CurrentValue = newTagNode.Value
		dep.CurrentDigest = digestNode.Value
		dep.SkipReason = "invalid-dependency-specification"
		setValueLocus(&dep, w.src, newTagNode)
		setDigestLocus(&dep, w.src, digestNode)

	case newTagNode != nil:
		dep.PackageName = depName
		dep.Datasource = "docker"
		dep.CurrentValue = newTagNode.Value
		setValueLocus(&dep, w.src, newTagNode)

	case digestNode != nil:
		dep.PackageName = depName
		dep.Datasource = "docker"
		dep.CurrentDigest = digestNode.Value
		setDigestLocus(&dep, w.src, digestNode)
		// A digest-only reference has no separate value text to bracket; the
		// value locus points at the digest too, so Edit can still find
		// something to replace.
		dep.Locus.ValueStart = dep.Locus.DigestStart
		dep.Locus.ValueEnd = dep.Locus.DigestEnd

	default:
		// A name with neither a newTag nor a digest changes nothing kustomize
		// would apply; not in the corpus, and read here as no dependency
		// rather than a skip with an invented reason.
		return
	}

	w.deps = append(w.deps, dep)
}

// helmChartList reads the helmCharts: sequence.
func (w *walker) helmChartList(n *yamlx.Node) {
	if n.Kind != yamlx.SequenceNode {
		return
	}
	for _, item := range n.Content {
		if item.Kind == yamlx.MappingNode {
			w.helmChart(item)
		}
	}
}

// helmChart reads one helmCharts: entry. A entry missing name, repo or
// version carries nothing this manager can look up.
func (w *walker) helmChart(item *yamlx.Node) {
	var nameNode, repoNode, versionNode *yamlx.Node
	for i := 0; i+1 < len(item.Content); i += 2 {
		key, val := item.Content[i], item.Content[i+1]
		if val.Kind != yamlx.ScalarNode {
			continue
		}
		switch key.Value {
		case "name":
			nameNode = val
		case "repo":
			repoNode = val
		case "version":
			versionNode = val
		}
	}
	if nameNode == nil || repoNode == nil || versionNode == nil {
		return
	}

	dep := model.Dependency{
		Manager:       name,
		File:          w.file,
		CustomManager: model.NoCustomManager,
		DepType:       "HelmChart",
		DepName:       nameNode.Value,
		CurrentValue:  versionNode.Value,
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: versionNode.Line},
	}
	setValueLocus(&dep, w.src, versionNode)

	if hostAndPath, ok := strings.CutPrefix(repoNode.Value, "oci://"); ok {
		dep.Datasource = "docker"
		dep.PackageName = hostAndPath + "/" + nameNode.Value
	} else {
		dep.Datasource = "helm"
		dep.RegistryURLs = []string{repoNode.Value}
	}

	w.deps = append(w.deps, dep)
}

// setValueLocus points dep's value span at n's scalar bytes.
func setValueLocus(dep *model.Dependency, src []byte, n *yamlx.Node) {
	base := scalarOffset(src, n)
	if base < 0 {
		return
	}
	dep.Locus.ValueStart = base
	dep.Locus.ValueEnd = base + len(n.Value)
}

// setDigestLocus points dep's digest span at n's scalar bytes.
func setDigestLocus(dep *model.Dependency, src []byte, n *yamlx.Node) {
	base := scalarOffset(src, n)
	if base < 0 {
		return
	}
	dep.Locus.DigestStart = base
	dep.Locus.DigestEnd = base + len(n.Value)
}

// scalarOffset returns the byte offset of a scalar's VALUE, skipping an
// opening quote when the scalar was quoted. yaml.v3 positions a quoted
// scalar at the quote, and an Edit that replaced the quote along with the
// value would break the file. The same helper as manager/gitlabci's, kept
// local rather than shared - no L0/L1 package offers it, and an L3 package
// may not import another L3 package for it.
func scalarOffset(src []byte, n *yamlx.Node) int {
	off := yamlx.Offset(src, n.Line, n.Column)
	if off < 0 || off >= len(src) {
		return -1
	}
	if src[off] == '"' || src[off] == '\'' {
		off++
	}
	return off
}

// Edit implements extract.Manager: the value span, the digest span, or
// both, through the shared reference editor - which also refuses a new
// tag for an image pinned by digest unless the digest moves with it.
func (*Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	return extract.EditRef(name, f, up)
}
