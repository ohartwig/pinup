// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package dockerfile extracts and rewrites image references in Dockerfiles
// and Containerfiles.
//
// It never re-serializes a file: every dependency it reports carries a
// model.Locus - byte offsets into the file exactly as read - and Edit turns
// one model.Update into a model.Edit, a byte-range replacement. That is what
// lets comments and indentation survive untouched.
package dockerfile

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/model"
)

// name is both the registry key (extract.Registry) and model.Dependency.Manager.
const name = "dockerfile"

// Manager implements extract.Manager for Dockerfile and Containerfile syntax.
// It carries no state: every call is a pure function of the file it is given.
type Manager struct{}

// New returns a ready-to-use Manager.
func New() *Manager { return &Manager{} }

// Name implements extract.Manager.
func (m *Manager) Name() string { return name }

// FilePatterns implements extract.Manager. These are the estate's two names,
// bare or with a suffix (Dockerfile.dev, Containerfile.ci) - the same set
// Renovate's own dockerfile manager matches by default.
func (m *Manager) FilePatterns() []string {
	return []string{
		"**/Dockerfile",
		"**/Dockerfile.*",
		"**/Containerfile",
		"**/Containerfile.*",
	}
}

// NeedsPlugin implements extract.Manager. Dockerfile syntax is plain text;
// nothing here needs a toolchain of its own.
func (m *Manager) NeedsPlugin() *extract.PluginRequirement { return nil }

// Line-level patterns. Each is matched against one line with its trailing
// "\r" already stripped (see bareLine), so none of them need to account for
// CRLF line endings themselves.
var (
	// reFrom captures the image/stage reference and an optional stage name.
	// --platform is accepted and skipped: it names an architecture, never a
	// dependency.
	reFrom = regexp.MustCompile(`(?i)^[ \t]*FROM[ \t]+(?:--platform=\S+[ \t]+)?(?P<ref>\S+)(?:[ \t]+AS[ \t]+(?P<stage>\S+))?[ \t]*$`)
	// reCopyFrom finds --from= anywhere on a COPY line; a real COPY carries
	// other flags and two paths besides it.
	reCopyFrom = regexp.MustCompile(`(?i)^[ \t]*COPY\b.*?--from=(?P<from>\S+)`)
	// reArg matches both `ARG NAME` and `ARG NAME=value`; the val group does
	// not participate (index -1) when there is no default.
	// The value stops at the first whitespace. Docker itself would take the
	// rest of the line - a # is only a comment at the start of one - but
	// Renovate stops at the space, measured against the pinned container:
	//
	//     ARG GO_APK_VERSION=1.27.0-r1\t# a trailing comment
	//
	// yields "1.27.0-r1", not the comment with it. Taking Docker's reading
	// would produce a version no datasource can resolve, and an Edit that
	// replaced the comment along with it.
	reArg = regexp.MustCompile(`(?i)^[ \t]*ARG[ \t]+(?P<name>[A-Za-z_][A-Za-z0-9_]*)(?:=(?P<val>[^ \t]*))?[ \t]*.*$`)
	// reSyntax is the BuildKit parser directive naming the frontend image,
	// which Renovate reports as a dependency of depType "syntax" (measured:
	// a customer repository's scheduler Containerfile). It is a comment to
	// Docker, so it is read before the first instruction only.
	reSyntax = regexp.MustCompile(`(?i)^[ \t]*#[ \t]*syntax[ \t]*=[ \t]*(?P<ref>\S+)[ \t]*$`)
	// reArgRef finds the ${NAME}, ${NAME:-default} and $NAME references a
	// FROM line may carry; the default form is measured nowhere and reported
	// as unresolved.
	reArgRef = regexp.MustCompile(`\$\{(?P<braced>[A-Za-z_][A-Za-z0-9_]*)(?P<def>:-[^}]*)?\}|\$(?P<bare>[A-Za-z_][A-Za-z0-9_]*)`)
)

// argValue is a global ARG's default and where its bytes sit in the file,
// so a FROM that interpolates it can be edited at the ARG line - which is
// where Renovate edits it (measured: replaceString "ARG X_TAG=1.0.5\n").
type argValue struct {
	val        string
	start, end int
}

// Extract implements extract.Manager. It never returns an error for a
// malformed file - a Dockerfile that does not match a pattern here simply
// yields no dependency for that line, which is the same "warn, don't stop"
// contract the interface documents.
func (m *Manager) Extract(_ context.Context, f extract.File, cfg extract.ManagerConfig) (extract.Result, error) {
	lines := splitLines(string(f.Content))

	var deps []model.Dependency
	// stages holds every name introduced by "AS <name>" seen so far, in file
	// order - that is what lets a later FROM or COPY --from tell a stage
	// reference apart from a real external dependency.
	stages := make(map[string]bool)
	// globalArgs holds the default value of every ARG seen before the first
	// FROM. Those are the only ARGs Docker itself lets a FROM line
	// interpolate, so they are the only ones this manager tries to resolve a
	// `FROM img:${TAG}`-style reference against.
	globalArgs := make(map[string]argValue)
	sawFrom := false
	sawInstruction := false

	for i, ln := range lines {
		bare := strings.TrimSuffix(ln.text, "\r")

		if idx, ok := matchNamed(reSyntax, bare); ok && !sawInstruction {
			refSpan := idx["ref"]
			dep := refDependency(f.Path, i+1, bare[refSpan[0]:refSpan[1]], ln.start+refSpan[0], nil)
			dep.DepType = "syntax"
			deps = append(deps, dep)
			continue
		}
		if t := strings.TrimSpace(bare); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		sawInstruction = true

		if idx, ok := matchNamed(reArg, bare); ok {
			nameSpan := idx["name"]
			argName := bare[nameSpan[0]:nameSpan[1]]

			valSpan, hasVal := idx["val"]
			hasVal = hasVal && valSpan[0] >= 0
			if !hasVal {
				continue // "ARG NAME" with no default carries nothing to track
			}
			vs, ve := trimValueSpan(bare, valSpan[0], valSpan[1])

			if !sawFrom {
				globalArgs[argName] = argValue{val: bare[vs:ve], start: ln.start + vs, end: ln.start + ve}
			}
			// An ARG is never a dependency of this manager, annotated or not.
			// Measured: Renovate's dockerfile manager reports FROM references
			// only; a "# renovate:" annotated ARG belongs to the
			// customManagers:dockerfileVersions preset, which resolves into
			// every estate configuration and extracts it as a custom regex
			// dependency. Extracting it here as well produced every such
			// dependency twice.
			continue
		}

		if idx, ok := matchNamed(reFrom, bare); ok {
			refSpan := idx["ref"]
			ref := bare[refSpan[0]:refSpan[1]]
			refAbs := ln.start + refSpan[0]

			deps = append(deps, fromDependency(f.Path, i+1, ref, refAbs, stages, globalArgs))

			if stageSpan, ok := idx["stage"]; ok && stageSpan[0] >= 0 {
				stages[bare[stageSpan[0]:stageSpan[1]]] = true
			}
			sawFrom = true
			continue
		}

		if idx, ok := matchNamed(reCopyFrom, bare); ok {
			fromSpan := idx["from"]
			val := bare[fromSpan[0]:fromSpan[1]]
			valAbs := ln.start + fromSpan[0]
			deps = append(deps, copyFromDependency(f.Path, i+1, val, valAbs, stages))
			continue
		}
	}

	applyManagerDefaults(deps, cfg)
	return extract.Result{Deps: deps}, nil
}

// applyManagerDefaults stamps ManagerConfig's fallback registry URLs and
// versioning onto a dependency whose own extraction named none - exactly the
// contract extract.ManagerConfig documents.
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

// fromDependency builds the Dependency for one FROM instruction's reference.
// ref is either a stage name, "scratch", or an image reference - possibly
// through the ARGs declared above the first FROM, which Docker lets a FROM
// interpolate and Renovate resolves (measured: FROM ${IMG}:${TAG} with
// both ARGs global is the image at the tag, edited on the tag's ARG line).
func fromDependency(file string, lineNo int, ref string, refAbs int, stages map[string]bool, globalArgs map[string]argValue) model.Dependency {
	if stages[ref] {
		return internalReference(file, lineNo, ref, refAbs,
			fmt.Sprintf("FROM %s refers to an earlier build stage in this file, not an external dependency", ref))
	}
	if strings.EqualFold(ref, "scratch") {
		return internalReference(file, lineNo, ref, refAbs,
			"FROM scratch is the empty pseudo-image, not a dependency")
	}
	if strings.ContainsRune(ref, '$') {
		if dep, ok := interpolatedDependency(file, lineNo, ref, refAbs, globalArgs); ok {
			return dep
		}
	}
	return refDependency(file, lineNo, ref, refAbs, globalArgs)
}

// interpolatedDependency resolves a FROM reference through the global ARGs
// and builds the dependency on the resolved image, with the tag's and the
// digest's Locus wherever their bytes actually sit: on the FROM line when
// literal, on the ARG line when interpolated. ok is false when a reference
// does not resolve - an ARG with no default, one declared after the first
// FROM, or an inline default - or when the tag or digest would straddle
// two places, which nothing can edit as one span.
func interpolatedDependency(file string, lineNo int, ref string, refAbs int, globalArgs map[string]argValue) (model.Dependency, bool) {
	var resolved strings.Builder
	var origin []int // absolute source offset of each resolved byte
	last := 0
	for _, m := range reArgRef.FindAllStringSubmatchIndex(ref, -1) {
		for k := last; k < m[0]; k++ {
			origin = append(origin, refAbs+k)
		}
		resolved.WriteString(ref[last:m[0]])
		name := ""
		switch {
		case m[2] >= 0 && m[4] < 0:
			name = ref[m[2]:m[3]]
		case m[6] >= 0:
			name = ref[m[6]:m[7]]
		default:
			return model.Dependency{}, false // ${NAME:-default}: unmeasured
		}
		a, ok := globalArgs[name]
		if !ok {
			return model.Dependency{}, false
		}
		for k := range len(a.val) {
			origin = append(origin, a.start+k)
		}
		resolved.WriteString(a.val)
		last = m[1]
	}
	for k := last; k < len(ref); k++ {
		origin = append(origin, refAbs+k)
	}
	resolved.WriteString(ref[last:])
	full := resolved.String()
	if strings.ContainsRune(full, '$') {
		return model.Dependency{}, false
	}

	contiguous := func(start, end int) (int, int, bool) {
		for k := start + 1; k < end; k++ {
			if origin[k] != origin[k-1]+1 {
				return 0, 0, false
			}
		}
		if start == end {
			return origin[start-1] + 1, origin[start-1] + 1, true
		}
		return origin[start], origin[end-1] + 1, true
	}

	imgName, tagStart, tagEnd, digestStart, digestEnd := splitRef(full)
	dep := model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       imgName,
		Datasource:    "docker",
		Versioning:    "docker",
		CurrentValue:  full[tagStart:tagEnd],
		Locus:         model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest, Line: lineNo},
	}
	if tagStart == tagEnd && digestStart < 0 {
		dep.SkipReason = "reference carries no tag; a lookup would fall back to latest"
		dep.Locus.ValueStart, dep.Locus.ValueEnd = refAbs+len(ref), refAbs+len(ref)
		return dep, true
	}
	vs, ve, ok := contiguous(tagStart, tagEnd)
	if !ok {
		return model.Dependency{}, false
	}
	dep.Locus.ValueStart, dep.Locus.ValueEnd = vs, ve
	if digestStart >= 0 {
		ds, de, ok := contiguous(digestStart, digestEnd)
		if !ok {
			return model.Dependency{}, false
		}
		dep.CurrentDigest = full[digestStart:digestEnd]
		dep.Locus.DigestStart, dep.Locus.DigestEnd = ds, de
	}
	return dep, true
}

// copyFromDependency builds the Dependency for one COPY --from= value, which
// is either a stage name, a positional stage index, or an image reference.
func copyFromDependency(file string, lineNo int, val string, valAbs int, stages map[string]bool) model.Dependency {
	if stages[val] {
		return internalReference(file, lineNo, val, valAbs,
			fmt.Sprintf("COPY --from=%s references a build stage in this file, not a dependency", val))
	}
	if isDigits(val) {
		return internalReference(file, lineNo, val, valAbs,
			fmt.Sprintf("COPY --from=%s references a build stage by index, not a dependency", val))
	}
	return refDependency(file, lineNo, val, valAbs, nil)
}

// internalReference builds a skipped Dependency for a reference that names
// something inside this file rather than an external image: a build stage, by
// name or index, or the scratch pseudo-image. None of these carry a version,
// so the Locus is the zero-width point right after the name, keeping the
// "slice the source and compare to CurrentValue" invariant trivially true.
func internalReference(file string, lineNo int, ref string, refAbs int, reason string) model.Dependency {
	end := refAbs + len(ref)
	return model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       ref,
		Locus: model.Locus{
			ValueStart:  end,
			ValueEnd:    end,
			DigestStart: model.NoDigest,
			DigestEnd:   model.NoDigest,
			Line:        lineNo,
		},
		SkipReason: reason,
	}
}

// refDependency builds the Dependency for a real image reference: FROM's
// argument when it is not a stage or scratch, or COPY --from's when it is not
// a stage or an index. globalArgs may be nil - it is only consulted to make a
// skip reason more specific, never to change what gets skipped.
func refDependency(file string, lineNo int, ref string, refAbs int, globalArgs map[string]argValue) model.Dependency {
	imgName, tagStart, tagEnd, digestStart, digestEnd := splitRef(ref)
	tag := ref[tagStart:tagEnd]

	dep := model.Dependency{
		Manager:       name,
		File:          file,
		CustomManager: model.NoCustomManager,
		DepName:       imgName,
		Datasource:    "docker",
		Versioning:    "docker",
		CurrentValue:  tag,
		Locus: model.Locus{
			ValueStart:  refAbs + tagStart,
			ValueEnd:    refAbs + tagEnd,
			DigestStart: model.NoDigest,
			DigestEnd:   model.NoDigest,
			Line:        lineNo,
		},
	}

	if digestStart >= 0 {
		dep.CurrentDigest = ref[digestStart:digestEnd]
		dep.Locus.DigestStart = refAbs + digestStart
		dep.Locus.DigestEnd = refAbs + digestEnd
	}

	switch {
	case strings.ContainsRune(tag, '$'):
		reason := fmt.Sprintf("tag %q is a build argument that is not resolvable in this file", tag)
		if v, ok := resolveArgRef(tag, globalArgs); ok {
			reason = fmt.Sprintf("tag %q resolves to ARG default %q, but that is not an independently editable reference here", tag, v)
		}
		dep.SkipReason = reason
	case tag == "" && digestStart < 0:
		// A bare image name with neither tag nor digest: a lookup with no
		// tag to compare against would silently fall back to latest, and
		// what Renovate pins there is not measured.
		dep.SkipReason = "reference carries no tag; a lookup would fall back to latest"
	}
	// A reference by digest alone - `image@sha256:…` - is a digest pin of
	// the tag it implies, latest, and the digest moves with it. Measured:
	// devops/wolfi-packages !426, "update cgr.dev/chainguard/wolfi-base
	// docker digest to 9a8d954" on a tagless FROM.

	return dep
}

// resolveArgRef reports the value a "${NAME}", "${NAME:-default}" or "$NAME"
// tag would resolve to, using only ARGs declared before the first FROM -
// Docker's own scoping rule for what a FROM line may interpolate. It exists
// to make a skip reason more informative; it never turns a variable
// reference into an editable one, since the bytes to edit would then live in
// the ARG line, not here.
func resolveArgRef(tag string, globalArgs map[string]argValue) (string, bool) {
	inner := tag
	switch {
	case strings.HasPrefix(inner, "${") && strings.HasSuffix(inner, "}"):
		inner = inner[2 : len(inner)-1]
	case strings.HasPrefix(inner, "$"):
		inner = inner[1:]
	default:
		return "", false
	}
	if def := strings.Index(inner, ":-"); def >= 0 {
		return inner[def+2:], true // an inline default needs no ARG lookup
	}
	if globalArgs == nil {
		return "", false
	}
	v, ok := globalArgs[inner]
	return v.val, ok
}

// splitRef splits a raw reference token into its image name and the byte
// spans (relative to the token itself) of its tag and digest.
//
// The tag separator is only recognized after the last '/': a registry host
// may itself carry a port ("host:5000/name:tag"), so scanning for the first
// ':' in the whole token would misread the port as a tag. This is the same
// rule the docker CLI and Renovate's own reference parser use.
//
// tagStart/tagEnd are always valid (zero-width at the end of the name when
// there is no tag); digestStart/digestEnd are both -1 when there is no
// digest.
func splitRef(ref string) (name string, tagStart, tagEnd, digestStart, digestEnd int) {
	digestStart, digestEnd = -1, -1

	rest := ref
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		digestStart, digestEnd = at+1, len(ref)
		rest = ref[:at]
	}

	searchFrom := 0
	if slash := strings.LastIndexByte(rest, '/'); slash >= 0 {
		searchFrom = slash + 1
	}
	if colon := strings.IndexByte(rest[searchFrom:], ':'); colon >= 0 {
		abs := searchFrom + colon
		return rest[:abs], abs + 1, len(rest), digestStart, digestEnd
	}
	return rest, len(rest), len(rest), digestStart, digestEnd
}

// trimValueSpan narrows an ARG value's [start, end) span to exclude trailing
// blanks and a matching pair of quotes, so the reported Locus brackets
// exactly the version text and nothing else.
func trimValueSpan(s string, start, end int) (int, int) {
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	if end-start >= 2 {
		if (s[start] == '"' && s[end-1] == '"') || (s[start] == '\'' && s[end-1] == '\'') {
			start++
			end--
		}
	}
	return start, end
}

// isDigits reports whether s is a non-empty run of ASCII digits, which is how
// COPY --from refers to an earlier stage positionally instead of by name.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// matchNamed runs re against s and returns its named groups' byte spans. A
// group that did not participate reports [-1, -1], mirroring
// regexp.FindStringSubmatchIndex; ok is false only when re did not match s at
// all.
func matchNamed(re *regexp.Regexp, s string) (map[string][2]int, bool) {
	loc := re.FindStringSubmatchIndex(s)
	if loc == nil {
		return nil, false
	}
	names := re.SubexpNames()
	out := make(map[string][2]int, len(names))
	for i, n := range names {
		if i == 0 || n == "" {
			continue
		}
		out[n] = [2]int{loc[2*i], loc[2*i+1]}
	}
	return out, true
}

// lineInfo is one line of a file, its "\n"-delimited text and the absolute
// byte offset of its first byte in the source.
type lineInfo struct {
	text  string
	start int
}

// splitLines splits src into lines on '\n' without discarding anything but
// that separator, so start+i indexing into a line's text always yields the
// correct absolute offset into src - the whole reason a text manager can
// report a Locus without re-serializing.
func splitLines(src string) []lineInfo {
	var out []lineInfo
	start := 0
	for i := 0; i <= len(src); i++ {
		if i == len(src) || src[i] == '\n' {
			out = append(out, lineInfo{text: src[start:i], start: start})
			start = i + 1
		}
	}
	return out
}

// Edit implements extract.Manager. It refuses, rather than guesses, when the
// bytes it recorded at extraction time no longer match what is on disk now -
// that is the file having changed underneath the run, and writing anyway
// would corrupt it.
func (m *Manager) Edit(_ context.Context, f extract.File, up model.Update) (model.Edit, error) {
	return extract.EditRef(name, f, up)
}
