// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package helmchart

import (
	"fmt"
	"sort"
	"strings"
)

// A chart places container images its consumer never wrote down.
//
// WHY THIS IS HERE. Every manager reads a file: dockerfile reads FROM,
// gitlabci reads image:, kustomize reads images:. An image a chart carries in
// its OWN values is in none of them, so nothing updates it, nothing pins it,
// and nothing reports it. The consumer sees a chart version and gets a set of
// images decided by the vendor -- which is the same blind spot the values
// analysis next door exists for, one level down: there a key the consumer set
// stops working, here an image the consumer never set starts running.
//
// It found its first case in a repository whose charts placed ten images, all
// by tag, none of them written anywhere in that repository.
//
// WHAT THIS DOES NOT SEE, and the evidence says so on every run rather than
// leaving it to be discovered: a chart may compose a reference in its
// TEMPLATES -- `{{ .Values.global.registry }}/{{ .Values.image.name }}` -- and
// values.yaml then holds the parts and not the reference. Reading the values
// finds what the values hold. Full fidelity needs a render, which needs Helm,
// which this binary deliberately does not have. Under-reporting is the safe
// direction: what is reported is real, and the count is a floor.

// placedImage is one image reference a chart's values carry.
type placedImage struct {
	path   string // the values path, e.g. "image" or "controller.image"
	ref    string // the reference as the values spell it
	pinned bool   // carries an @sha256: digest
}

// imageFields are the key names whose STRING value is a reference on its own.
// Exact `image`, or a camelCase name ending in `Image` (initImage, busyboxImage).
func isImageField(key string) bool {
	if key == "image" {
		return true
	}
	return len(key) > 5 && strings.HasSuffix(key, "Image")
}

// looksLikeRef keeps a string that can be a reference and drops prose. A
// reference has a path separator or a tag separator, no whitespace, and no Go
// template -- a templated value is a part, not a reference, and reporting it
// would be reporting a fragment as a fact.
func looksLikeRef(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") || strings.Contains(s, "{{") {
		return false
	}
	return strings.Contains(s, "/") || strings.Contains(s, ":")
}

// join builds the reference from the parts a values map spells separately.
// registry and repository are joined with "/", tag with ":", digest with "@" --
// and a digest beside a tag keeps both, because that is how a pinned reference
// is written and how the registry resolves it.
func join(registry, repository, tag, digest string) string {
	ref := repository
	if registry != "" {
		ref = strings.TrimRight(registry, "/") + "/" + repository
	}
	if tag != "" {
		ref += ":" + tag
	}
	if digest != "" {
		ref += "@" + digest
	}
	return ref
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// findImages walks a values document and collects every image reference it
// spells out. Two shapes, and both are in the wild:
//
//	image: "repo:tag"                        a string under an image field
//	image: {registry, repository, tag, …}    the parts, as most charts do it
//
// A map counts as an image when it names a `repository`; that is the one key
// every variant of the second shape has, and prose does not have it.
func findImages(v any, prefix string, out *[]placedImage) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if repo := str(m, "repository"); repo != "" {
		digest := str(m, "digest")
		ref := join(str(m, "registry"), repo, str(m, "tag"), digest)
		if looksLikeRef(ref) {
			*out = append(*out, placedImage{
				path:   prefix,
				ref:    ref,
				pinned: strings.Contains(digest, "sha256:") || strings.Contains(str(m, "tag"), "@sha256:"),
			})
		}
	}
	for k, child := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if s, ok := child.(string); ok {
			if isImageField(k) && looksLikeRef(s) {
				*out = append(*out, placedImage{path: p, ref: s, pinned: strings.Contains(s, "@sha256:")})
			}
			continue
		}
		findImages(child, p, out)
	}
}

// collectImages returns the images a values document places, by path.
func collectImages(values map[string]any) []placedImage {
	var out []placedImage
	findImages(values, "", &out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].ref < out[j].ref
	})
	return out
}

// imagesEvidence reports what the two charts place: every reference that moved,
// and how many of the new chart's are unpinned.
//
// IT CONTRIBUTES NO RISK, deliberately. Whether a chart pins its images is a
// standing property of that chart, true of every version of it, and letting it
// colour the label would put nearly every chart in the estate on the strictest
// one -- a verdict that says the same thing about everything says nothing, and
// gets switched off. What a bump DID move is evidence a reviewer can act on;
// what the chart has always done belongs in a count.
func imagesEvidence(old, new []placedImage) []string {
	byPath := func(in []placedImage) map[string]placedImage {
		m := make(map[string]placedImage, len(in))
		for _, i := range in {
			m[i.path] = i
		}
		return m
	}
	o, n := byPath(old), byPath(new)

	var notes []string
	paths := map[string]bool{}
	for p := range o {
		paths[p] = true
	}
	for p := range n {
		paths[p] = true
	}
	var sorted []string
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	for _, p := range sorted {
		a, inOld := o[p]
		b, inNew := n[p]
		switch {
		case !inOld:
			notes = append(notes, fmt.Sprintf("%s added %s", p, b.ref))
		case !inNew:
			notes = append(notes, fmt.Sprintf("%s removed (was %s)", p, a.ref))
		case a.ref != b.ref:
			notes = append(notes, fmt.Sprintf("%s %s -> %s", p, a.ref, b.ref))
		}
	}

	unpinned := 0
	for _, i := range new {
		if !i.pinned {
			unpinned++
		}
	}
	summary := fmt.Sprintf("%d image(s) placed by the chart's own values, %d without a digest", len(new), unpinned)
	if len(new) == 0 {
		// Silence here would read as "this chart places nothing", which is a
		// different statement from "the values name nothing" -- see the package
		// note on templated references.
		summary = "no image reference in the chart's values (a template may still compose one)"
	}
	return append([]string{summary}, notes...)
}
