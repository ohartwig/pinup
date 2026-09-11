// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package gitlabci extracts image and component references from
// .gitlab-ci.yml.
//
// Three shapes, measured across the extraction corpus:
//
//	image: registry/name:tag              depType image        docker
//	image: { name: registry/name:tag }    depType image-name   docker
//	services: [ registry/name:tag ]       depType image-name   docker
//	include: [ component: host/path/name@1 ]
//	                                      depType repository   gitlab-tags,
//	                                                           semver-partial
//
// The component form is the one that carries the estate's CI pins. Its depName
// is the PROJECT path - "devops/ci-cd-components/lint-tools" - with the host
// variable stripped from the front and the component name from the back, and
// its version is whatever follows the @, which for a rolling pin is a bare
// major.
//
// The file is walked as a yaml.v3 node tree so every image: at every nesting
// level is found, and positions come back as byte offsets so an Edit replaces
// exactly the tag. Nothing is re-serialised: anchors, comments and the
// !reference tags the estate's pipelines lean on all survive because no code
// path here can lose them.
package gitlabci

import (
	"context"
	"fmt"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/yamlx"
)

type Manager struct{}

func New() *Manager { return &Manager{} }

func (*Manager) Name() string { return "gitlabci" }

func (*Manager) FilePatterns() []string { return []string{`/\.gitlab-ci\.ya?ml$/`} }

func (*Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

func (m *Manager) Extract(_ context.Context, f extract.File, _ extract.ManagerConfig) (extract.Result, error) {
	var res extract.Result
	root, err := yamlx.ParseTree(f.Content)
	if err != nil {
		// A file that does not parse yields a warning and nothing else. One
		// broken pipeline file is not a reason to stop managing the rest of
		// the repository.
		res.Warnings = append(res.Warnings, model.Warning{
			Stage: "extract", File: f.Path, Msg: "not valid YAML: " + err.Error(),
		})
		return res, nil
	}
	if root.Kind == yamlx.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}

	w := &walker{file: f.Path, src: f.Content}
	w.walk(root, "")
	res.Deps = w.deps
	res.Warnings = append(res.Warnings, w.warnings...)
	extract.SortDeps(res.Deps)
	return res, nil
}

type walker struct {
	file     string
	src      []byte
	deps     []model.Dependency
	warnings []model.Warning
}

// walk visits every mapping in the tree. The key names that matter - image,
// services, include - can appear at any depth: on a job, in a hidden template,
// under default:, or inside a component spec.
func (w *walker) walk(n *yamlx.Node, path string) {
	switch n.Kind {
	case yamlx.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			child := path + "/" + key.Value
			switch key.Value {
			case "image":
				w.image(val, child)
			case "services":
				if val.Kind == yamlx.SequenceNode {
					for _, item := range val.Content {
						w.image(item, child)
					}
				}
			case "include":
				w.include(val)
			}
			w.walk(val, child)
		}
	case yamlx.SequenceNode:
		for i, item := range n.Content {
			w.walk(item, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// image handles both spellings: a bare string, and an object with a name key.
func (w *walker) image(n *yamlx.Node, path string) {
	switch n.Kind {
	case yamlx.ScalarNode:
		w.imageRef(n, "image")
	case yamlx.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "name" && n.Content[i+1].Kind == yamlx.ScalarNode {
				w.imageRef(n.Content[i+1], "image-name")
			}
		}
	}
}

func (w *walker) imageRef(n *yamlx.Node, depType string) {
	ref := n.Value
	if n.Tag == "!reference" || n.Tag == "!!merge" {
		return
	}
	dep := model.Dependency{
		Manager:       "gitlabci",
		File:          w.file,
		CustomManager: model.NoCustomManager,
		DepType:       depType,
		Datasource:    "docker",
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: n.Line},
	}

	if reason := unresolvable(ref); reason != "" {
		dep.DepName = ref
		dep.SkipReason = reason
		w.deps = append(w.deps, dep)
		return
	}

	name, tag, digest := splitImage(ref)
	dep.DepName = name
	dep.CurrentValue = tag
	dep.CurrentDigest = digest

	base := scalarOffset(w.src, n)
	if base < 0 {
		w.warnings = append(w.warnings, model.Warning{
			Stage: "extract", File: w.file,
			Msg: fmt.Sprintf("line %d: could not locate the image value in the file bytes", n.Line),
		})
		return
	}
	if tag != "" {
		if i := strings.Index(ref, ":"+tag); i >= 0 {
			// The first ":tag" after the last slash is the tag; a registry
			// port sits before the last slash and is not it.
			i = indexAfterLastSlash(ref, ":"+tag)
			dep.Locus.ValueStart = base + i + 1
			dep.Locus.ValueEnd = dep.Locus.ValueStart + len(tag)
		}
	}
	if digest != "" {
		if i := strings.Index(ref, "@"+digest); i >= 0 {
			dep.Locus.DigestStart = base + i + 1
			dep.Locus.DigestEnd = dep.Locus.DigestStart + len(digest)
		}
	}
	if tag == "" && digest == "" {
		dep.SkipReason = "no tag and no digest: a bare image name pins nothing to update"
	} else if tag == "" {
		dep.SkipReason = "digest-only reference with no tag; a tag-less lookup would fall back to latest"
	}
	w.deps = append(w.deps, dep)
}

// include handles the component form. Other include kinds - local, project,
// remote, template - are a different manager's business or carry no version.
func (w *walker) include(n *yamlx.Node) {
	items := []*yamlx.Node{n}
	if n.Kind == yamlx.SequenceNode {
		items = n.Content
	}
	for _, item := range items {
		if item.Kind != yamlx.MappingNode {
			continue
		}
		for i := 0; i+1 < len(item.Content); i += 2 {
			if item.Content[i].Value != "component" || item.Content[i+1].Kind != yamlx.ScalarNode {
				continue
			}
			w.component(item.Content[i+1])
		}
	}
}

// component reads "host/group/project/name@version" into a dependency on the
// PROJECT, which is what has tags.
func (w *walker) component(n *yamlx.Node) {
	ref := n.Value
	dep := model.Dependency{
		Manager:       "gitlabci",
		File:          w.file,
		CustomManager: model.NoCustomManager,
		DepType:       "repository",
		Datasource:    "gitlab-tags",
		Versioning:    "semver-partial",
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: n.Line},
	}

	at := strings.LastIndex(ref, "@")
	if at < 0 {
		dep.DepName = ref
		dep.SkipReason = "component reference carries no @version"
		w.deps = append(w.deps, dep)
		return
	}
	version := ref[at+1:]
	pathPart := ref[:at]

	// Strip the host, whether written as a variable or literally. What is
	// left is group/.../project/component-name. Either host names the
	// registry, a variable one (`${CI_SERVER_HOST}`, the estate's form)
	// verbatim, as Renovate records it. A rule rewrites it to the real
	// host before lookup - the estate's does, for every gitlab-*
	// datasource - and a configuration without such a rule has the lookup
	// declined by datasource/gitlabds rather than resolved by guesswork.
	if i := strings.Index(pathPart, "/"); i >= 0 {
		host := pathPart[:i]
		switch {
		case strings.HasPrefix(host, "$"):
			dep.RegistryURLs = []string{"https://" + host}
			pathPart = pathPart[i+1:]
		case strings.Contains(host, "."):
			dep.RegistryURLs = []string{"https://" + host}
			pathPart = pathPart[i+1:]
		}
	}
	// The last segment is the component's name inside the project; the
	// project path is everything before it.
	slash := strings.LastIndex(pathPart, "/")
	if slash < 0 {
		dep.DepName = pathPart
		dep.SkipReason = "component reference has no project path"
		w.deps = append(w.deps, dep)
		return
	}
	dep.DepName = pathPart[:slash]
	dep.PackageName = pathPart[:slash]
	dep.CurrentValue = version

	base := scalarOffset(w.src, n)
	if base >= 0 {
		dep.Locus.ValueStart = base + at + 1
		dep.Locus.ValueEnd = dep.Locus.ValueStart + len(version)
	}
	w.deps = append(w.deps, dep)
}

// unresolvable names why an image reference cannot be looked up, or returns
// "" when it can.
func unresolvable(ref string) string {
	switch {
	case ref == "":
		return "empty image reference"
	case strings.Contains(ref, "$[[ inputs."):
		return "value is a component input placeholder, resolved by GitLab at include time"
	case strings.HasPrefix(ref, "$"):
		return "value is a CI variable, resolved at pipeline time"
	case strings.Contains(ref, "${"):
		return "value contains a CI variable, resolved at pipeline time"
	}
	return ""
}

// splitImage separates name, tag and digest. The tag is the part after the
// first colon FOLLOWING the last slash - a registry port comes before it and
// must not be read as a tag.
func splitImage(ref string) (name, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		digest = ref[i+1:]
		ref = ref[:i]
	}
	lastSlash := strings.LastIndex(ref, "/")
	if i := strings.Index(ref[lastSlash+1:], ":"); i >= 0 {
		tag = ref[lastSlash+1+i+1:]
		name = ref[:lastSlash+1+i]
	} else {
		name = ref
	}
	return name, tag, digest
}

func indexAfterLastSlash(ref, sub string) int {
	lastSlash := strings.LastIndex(ref, "/")
	i := strings.Index(ref[lastSlash+1:], sub)
	if i < 0 {
		return -1
	}
	return lastSlash + 1 + i
}

// scalarOffset returns the byte offset of a scalar's VALUE, skipping an
// opening quote when the scalar was quoted. yaml.v3 positions a quoted scalar
// at the quote, and an Edit that replaced the quote along with the tag would
// break the file.
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

// Edit replaces the recorded bytes, refusing when they have moved - and
// refusing a new tag for an image pinned by digest unless the digest moves
// with it.
func (*Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	return extract.EditRef("gitlabci", f, up)
}
