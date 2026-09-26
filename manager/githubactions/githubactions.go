// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package githubactions extracts the actions and reusable workflows a GitHub
// Actions workflow (or a composite action, or a Gitea/Forgejo workflow)
// references with `uses:`.
//
// The shapes, from GitHub's published workflow syntax - a public
// specification, not anything read out of the Renovate tree:
//
//	jobs.<id>.steps[].uses: owner/repo@ref            an action
//	jobs.<id>.steps[].uses: owner/repo/sub/path@ref   an action in a subdirectory
//	jobs.<id>.uses: owner/repo/.github/workflows/x.yml@ref   a reusable workflow
//	runs.steps[].uses: ...                            a composite action's step
//
// Every one is a dependency on the REPOSITORY owner/repo, datasource
// github-tags: its tags are what a ref names. depName is the path as
// written, so two actions of one repository stay two dependencies.
//
// The ref comes in two forms that matter:
//
//	uses: actions/checkout@v7.0.1
//	uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1  # v7.0.1
//
// The first is a version, taken as written. The second is a commit SHA - the
// form GitHub's security hardening guide recommends, because a tag can be
// moved and a commit cannot - with the version it stands for in a trailing
// comment. The SHA is the dependency's digest, the comment's version its
// value, and an update rewrites both in one edit: a SHA that moved without
// its comment would claim a version it does not run, and a comment that
// moved without its SHA would be worse. A SHA with no version beside it has
// no version to compare and is reported as such, not guessed at.
//
// Versioning is left to the datasource's default (github-tags: semver). The
// defaults this repository resolves from the pinned container
// (config/defaults.json, testdata/.../resolved-options.json) give the
// github-actions manager file patterns and nothing else, and no measurement
// says otherwise; a rule's `versioning` overrides it like for any manager.
//
// The file is walked as a yaml.v3 node tree and edited by byte range.
// Nothing is re-serialised.
package githubactions

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/yamlx"
)

const name = "github-actions"

type Manager struct{}

func New() *Manager { return &Manager{} }

func (*Manager) Name() string { return name }

// FilePatterns are the defaults the configuration carries for this manager
// (config/defaults.json): workflows and actions under .github, .gitea and
// .forgejo, workflow templates, and an action.yml anywhere.
func (*Manager) FilePatterns() []string {
	return []string{
		`/(^|/)(workflow-templates|\.(?:github|gitea|forgejo)/(?:workflows|actions))/.+\.ya?ml$/`,
		`/(^|/)action\.ya?ml$/`,
	}
}

func (*Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

var (
	// reSHA is a full commit id. GitHub resolves a 40-hex ref as a commit
	// and nothing shorter; a short hex ref is read as a tag or branch name.
	reSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	// reVersionComment reads the version out of the comment beside a SHA
	// pin: `# v7.0.1`, `# 7.0.1`, `# v7`, `#v7.0.1`, followed by the end of
	// the line or by more words (`# v7.0.1 - pinned for the scan`). The
	// version group is what the tag is called, v and all.
	reVersionComment = regexp.MustCompile(`^#[ \t]*(v?[0-9]+(?:\.[0-9]+)*(?:-[0-9A-Za-z.-]+)?)(?:[ \t]|$)`)
)

func (m *Manager) Extract(_ context.Context, f extract.File, _ extract.ManagerConfig) (extract.Result, error) {
	var res extract.Result
	root, err := yamlx.ParseTree(f.Content)
	if err != nil {
		// One broken workflow is not a reason to stop managing the rest.
		res.Warnings = append(res.Warnings, model.Warning{
			Stage: "extract", File: f.Path, Msg: "not valid YAML: " + err.Error(),
		})
		return res, nil
	}
	w := &walker{file: f.Path, src: f.Content}
	w.walk(root)
	res.Deps = w.deps
	res.Warnings = w.warnings
	extract.SortDeps(res.Deps)
	return res, nil
}

type walker struct {
	file     string
	src      []byte
	deps     []model.Dependency
	warnings []model.Warning
}

// walk looks for the two places a `uses:` means a dependency: an item of a
// `steps` sequence (a workflow job's or a composite action's) and a job
// under `jobs` (a reusable workflow). A `uses` key anywhere else - an
// input under `with:` that happens to be called that - is not one.
func (w *walker) walk(n *yamlx.Node) {
	switch n.Kind {
	case yamlx.DocumentNode, yamlx.SequenceNode:
		for _, c := range n.Content {
			w.walk(c)
		}
	case yamlx.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			switch key.Value {
			case "steps":
				if val.Kind == yamlx.SequenceNode {
					for _, step := range val.Content {
						w.usesOf(step)
					}
				}
			case "jobs":
				if val.Kind == yamlx.MappingNode {
					for j := 1; j < len(val.Content); j += 2 {
						w.usesOf(val.Content[j])
					}
				}
			}
			w.walk(val)
		}
	}
}

// usesOf records the `uses:` of one step or job, when it has one.
func (w *walker) usesOf(n *yamlx.Node) {
	if n.Kind != yamlx.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "uses" {
			continue
		}
		v := n.Content[i+1]
		if v.Kind != yamlx.ScalarNode {
			w.warnings = append(w.warnings, model.Warning{
				Stage: "extract", File: w.file,
				Msg: fmt.Sprintf("line %d: uses: is not a string; nothing to manage", v.Line),
			})
			return
		}
		w.uses(v)
		return
	}
}

// skipReason names why a `uses:` value is not looked up, or returns "".
func skipReason(ref string) string {
	switch {
	case ref == "":
		return "empty uses: reference"
	case strings.Contains(ref, "${{"):
		return "value is an expression, resolved by the runner at run time"
	case strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../"):
		return "local action: it lives in this repository and has no version of its own"
	case strings.HasPrefix(ref, "docker://"):
		return "docker:// action: a container image, which this manager does not look up yet (the docker datasource would; not implemented)"
	case strings.Contains(ref, "://"):
		return "action named by URL: only owner/repo references are looked up"
	}
	return ""
}

func (w *walker) uses(n *yamlx.Node) {
	ref := n.Value
	dep := model.Dependency{
		Manager:       name,
		File:          w.file,
		CustomManager: model.NoCustomManager,
		DepName:       ref,
		DepType:       "action",
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: n.Line},
	}
	if reason := skipReason(ref); reason != "" {
		dep.SkipReason = reason
		w.deps = append(w.deps, dep)
		return
	}
	path, version, ok := strings.CutLast(ref, "@")
	if !ok || version == "" {
		dep.SkipReason = "reference carries no @ref: nothing pins it to a version"
		w.deps = append(w.deps, dep)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		dep.SkipReason = `reference is not "owner/repo[/path]@ref"`
		w.deps = append(w.deps, dep)
		return
	}
	dep.DepName = path
	dep.PackageName = parts[0] + "/" + parts[1]
	dep.Datasource = "github-tags"

	base := scalarOffset(w.src, n)
	if base < 0 || base+len(ref) > len(w.src) || string(w.src[base:base+len(ref)]) != ref {
		// A folded or escaped scalar: the value is not spelled in the file
		// the way it parsed, so no byte range can be trusted to edit it.
		dep.SkipReason = "the reference is not written verbatim in the file (escaped or folded); no byte range to edit"
		w.deps = append(w.deps, dep)
		return
	}
	refStart := base + len(path) + 1

	if !reSHA.MatchString(version) {
		dep.CurrentValue = version
		dep.Locus.ValueStart = refStart
		dep.Locus.ValueEnd = refStart + len(version)
		w.deps = append(w.deps, dep)
		return
	}

	dep.CurrentDigest = version
	dep.Locus.DigestStart = refStart
	dep.Locus.DigestEnd = refStart + len(version)
	start, tag := versionComment(w.src, base+len(ref))
	if start < 0 {
		dep.SkipReason = fmt.Sprintf("pinned to commit %s with no version comment: nothing says which release it is, so there is no version to compare (write the tag beside it, `# v1.2.3`)", version[:7])
		w.deps = append(w.deps, dep)
		return
	}
	dep.CurrentValue = tag
	dep.Locus.ValueStart = start
	dep.Locus.ValueEnd = start + len(tag)
	w.deps = append(w.deps, dep)
}

// versionComment finds the version in a comment that follows a scalar
// ending at end, on the same line: its offset and text, or -1 when the line
// carries no comment or the comment names no version. A closing quote of
// the scalar is stepped over.
func versionComment(src []byte, end int) (int, string) {
	if end < len(src) && (src[end] == '"' || src[end] == '\'') {
		end++
	}
	line := src[end:]
	if nl := bytes.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	trimmed := bytes.TrimLeft(line, " \t")
	if len(trimmed) == len(line) {
		// YAML needs whitespace before a comment's #; without it the
		// scalar would have run on. Nothing follows the scalar here.
		return -1, ""
	}
	m := reVersionComment.FindSubmatchIndex(trimmed)
	if m == nil {
		return -1, ""
	}
	off := end + (len(line) - len(trimmed))
	return off + m[2], string(trimmed[m[2]:m[3]])
}

// scalarOffset returns the byte offset of a scalar's value, stepping over
// an opening quote: yaml.v3 positions a quoted scalar at the quote.
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

// Edit rewrites one reference. A tag reference moves its tag, through the
// same editor the other text managers use. A SHA pin moves its SHA and the
// version in its comment together, as one edit over both and the bytes
// between them, which are kept as they are:
//
//	uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1  # v7.0.1
//	                       ^------------------ one edit -------------------^
//
// A new version without a new SHA is refused, not written: the runner
// checks out the SHA and ignores the comment, so the file would claim a
// version it does not run.
func (*Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	d, l, src := up.Dep, up.Dep.Locus, f.Content
	if l.DigestStart == model.NoDigest || d.CurrentDigest == "" {
		if up.Type == model.UpdatePinDigest && up.NewDigest != "" {
			return pin(f, up)
		}
		return extract.EditRef(name, f, up)
	}
	if err := verify(f, l.DigestStart, l.DigestEnd, d.CurrentDigest); err != nil {
		return model.Edit{}, err
	}
	if err := verify(f, l.ValueStart, l.ValueEnd, d.CurrentValue); err != nil {
		return model.Edit{}, err
	}
	if l.DigestEnd > l.ValueStart {
		return model.Edit{}, fmt.Errorf("%s: %s: the version comment of %s does not follow its SHA", name, f.Path, d.DepName)
	}
	valueChanges := up.NewValue != "" && up.NewValue != d.CurrentValue
	digestChanges := up.NewDigest != "" && up.NewDigest != d.CurrentDigest
	switch {
	case valueChanges && up.NewDigest == "":
		return model.Edit{}, fmt.Errorf("%s: %s: %s to %s carries no commit SHA; a reference pinned by SHA is not moved by its comment alone",
			name, f.Path, d.DepName, up.NewValue)
	case valueChanges && digestChanges:
		between := string(src[l.DigestEnd:l.ValueStart])
		return model.Edit{
			File: f.Path, Start: l.DigestStart, End: l.ValueEnd,
			Old: d.CurrentDigest + between + d.CurrentValue, New: up.NewDigest + between + up.NewValue, Manager: name,
		}, nil
	case valueChanges:
		// The new tag names the commit already pinned: only the comment
		// was behind.
		return model.Edit{File: f.Path, Start: l.ValueStart, End: l.ValueEnd, Old: d.CurrentValue, New: up.NewValue, Manager: name}, nil
	case digestChanges:
		// The tag was moved to another commit, or the SHA had drifted from
		// its comment; the comment stays.
		return model.Edit{File: f.Path, Start: l.DigestStart, End: l.DigestEnd, Old: d.CurrentDigest, New: up.NewDigest, Manager: name}, nil
	}
	return model.Edit{}, fmt.Errorf("%s: update for %s names no change to apply", name, d.DepName)
}

// pin turns `owner/repo@v7` into `owner/repo@<sha> # v7` - the form a
// pinDigest update asks for. It is refused on a line that already carries
// a comment, which the pin's comment would have to be merged into.
func pin(f extract.File, up model.Update) (model.Edit, error) {
	d, l, src := up.Dep, up.Dep.Locus, f.Content
	if err := verify(f, l.ValueStart, l.ValueEnd, d.CurrentValue); err != nil {
		return model.Edit{}, err
	}
	if !reSHA.MatchString(up.NewDigest) {
		return model.Edit{}, fmt.Errorf("%s: %s: %q is not a commit SHA to pin %s to", name, f.Path, up.NewDigest, d.DepName)
	}
	end := l.ValueEnd
	if end < len(src) && (src[end] == '"' || src[end] == '\'') {
		end++
	}
	rest := src[end:]
	if nl := bytes.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return model.Edit{}, fmt.Errorf("%s: %s: %s is followed by %q on its line; not pinning over it", name, f.Path, d.DepName, bytes.TrimSpace(rest))
	}
	value := d.CurrentValue
	if up.NewValue != "" {
		value = up.NewValue
	}
	return model.Edit{
		File: f.Path, Start: l.ValueStart, End: end,
		Old: string(src[l.ValueStart:end]), New: up.NewDigest + string(src[l.ValueEnd:end]) + " # " + value, Manager: name,
	}, nil
}

// verify refuses a file that no longer holds what was extracted.
func verify(f extract.File, start, end int, want string) error {
	if start < 0 || end < start || end > len(f.Content) {
		return fmt.Errorf("%s: %s: locus [%d:%d] is out of range (file has %d bytes)", name, f.Path, start, end, len(f.Content))
	}
	if got := string(f.Content[start:end]); got != want {
		return fmt.Errorf("%s: %s changed since extraction: expected %q at [%d:%d], found %q", name, f.Path, want, start, end, got)
	}
	return nil
}
