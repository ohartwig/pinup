// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package helmchart classifies what changed between two versions of a
// Helm chart, beyond what its version number says. A chart's version is
// the packager's word; some vendors raise the major on every release
// while the application inside and the values a consumer sets are what
// they were. The analyzer reads both charts and reports:
//
//   - appVersion: the application's own version and how far it moved -
//     that is the label's main input (a major appVersion is a major);
//   - values: the keys of values.yaml, flattened to paths; a key that the
//     new chart no longer has is a value a consumer may have set that now
//     does nothing - "breaking-values", the strictest label;
//   - kubeVersion and the subcharts: reported as evidence, not weighed.
//
// The chart comes from where the dependency does: a Helm repository's
// index.yaml names the archive per version, an OCI registry carries it as
// the layer of the tag's manifest. Both are public formats
// (helm.sh/docs/topics/chart_repository, helm.sh/docs/topics/registries);
// nothing here is derived from any dependency bot's tree.
package helmchart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/ohartwig/pinup/classify"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/semverx"
	"github.com/ohartwig/pinup/yamlx"
)

// Name is the analyzer's name, as the plan records it.
const Name = "helm-chart"

// maxChart bounds an archive read into memory: a chart is tens of
// kilobytes to a few megabytes; more is not a chart.
const maxChart = 32 << 20

// OCI media types of a chart pushed with `helm push`.
const (
	configMediaType = "application/vnd.cncf.helm.config.v1+json"
	chartMediaType  = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// OCIReader is what the docker datasource offers beyond lookup: a tag's
// manifest and a blob by digest, at the registry the dependency names.
type OCIReader interface {
	Manifest(ctx context.Context, packageName string, registryURLs []string, tag string) ([]byte, error)
	Blob(ctx context.Context, packageName string, registryURLs []string, digest string, limit int64) ([]byte, error)
}

// Analyzer reads charts from Helm repositories through client and from
// OCI registries through oci (nil: OCI charts are not analyzed).
type Analyzer struct {
	client *httpx.Client
	oci    OCIReader
}

// New returns the analyzer.
func New(client *httpx.Client, oci OCIReader) *Analyzer {
	return &Analyzer{client: client, oci: oci}
}

func (a *Analyzer) Name() string { return Name }

// Applies says yes to a helm dependency, and to a docker one with an OCI
// reader - whether the tag is a chart is only known from its manifest,
// which Analyze reports as ErrNotApplicable when it is an image.
func (a *Analyzer) Applies(dep model.Dependency) bool {
	switch dep.Datasource {
	case "helm":
		return len(dep.RegistryURLs) > 0 && dep.RegistryURLs[0] != ""
	case "docker":
		return a.oci != nil
	}
	return false
}

// chart is what the analyzer reads out of one archive.
type chart struct {
	appVersion   string
	kubeVersion  string
	dependencies map[string]string // subchart name -> version
	values       []string          // flattened value paths, sorted
}

// Analyze compares the chart at from with the chart at to.
func (a *Analyzer) Analyze(ctx context.Context, dep model.Dependency, from, to string) (classify.Effective, error) {
	var old, new chart
	var err error
	switch dep.Datasource {
	case "helm":
		old, new, err = a.fromRepository(ctx, dep, from, to)
	case "docker":
		old, new, err = a.fromOCI(ctx, dep, from, to)
	default:
		return classify.Effective{}, classify.ErrNotApplicable
	}
	if err != nil {
		return classify.Effective{}, err
	}
	return compare(old, new), nil
}

// compare is the verdict: values removed outrank everything; else the
// application's own move decides.
func compare(old, new chart) classify.Effective {
	var e classify.Effective
	risk := model.RiskUnknown

	removed, added := diffPaths(old.values, new.values)
	e.Evidence = append(e.Evidence, model.Evidence{Kind: "values", Note: fmt.Sprintf("%d keys removed, %d added%s", len(removed), len(added), listing(removed))})
	if len(removed) > 0 {
		risk = model.RiskBreakingValues
	}

	appNote := "unchanged"
	switch ov, nv := parseLoose(old.appVersion), parseLoose(new.appVersion); {
	case old.appVersion == "" || new.appVersion == "":
		appNote = "not stated"
	case ov == nil || nv == nil:
		appNote = "not comparable"
	case nv.Major != ov.Major:
		appNote = "major"
		risk = model.Stricter(risk, model.RiskMajor)
	case nv.Minor != ov.Minor:
		appNote = "minor"
		risk = model.Stricter(risk, model.RiskMinor)
	case nv.Patch != ov.Patch:
		appNote = "patch"
		risk = model.Stricter(risk, model.RiskPatch)
	default:
		risk = model.Stricter(risk, model.RiskPatch)
	}
	e.Evidence = append([]model.Evidence{{Kind: "appVersion", From: old.appVersion, To: new.appVersion, Note: appNote}}, e.Evidence...)

	if old.kubeVersion != new.kubeVersion {
		e.Evidence = append(e.Evidence, model.Evidence{Kind: "kubeVersion", From: old.kubeVersion, To: new.kubeVersion, Note: "constraint changed"})
	}
	names := map[string]bool{}
	for n := range old.dependencies {
		names[n] = true
	}
	for n := range new.dependencies {
		names[n] = true
	}
	for _, n := range slices.Sorted(mapKeys(names)) {
		o, ok1 := old.dependencies[n]
		nv, ok2 := new.dependencies[n]
		switch {
		case !ok1:
			e.Evidence = append(e.Evidence, model.Evidence{Kind: "dependencies", To: nv, Note: n + " added"})
		case !ok2:
			e.Evidence = append(e.Evidence, model.Evidence{Kind: "dependencies", From: o, Note: n + " removed"})
		case o != nv:
			e.Evidence = append(e.Evidence, model.Evidence{Kind: "dependencies", From: o, To: nv, Note: n})
		}
	}
	e.Risk = risk
	return e
}

func mapKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// listing spells the first few removed keys after the count.
func listing(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	shown := keys
	more := ""
	if len(shown) > 8 {
		more = fmt.Sprintf(", … %d more", len(shown)-8)
		shown = shown[:8]
	}
	return ": " + strings.Join(shown, ", ") + more
}

// parseLoose reads an application version the way a chart states it:
// "8.2.1", "v1.28.0", "2.4" (padded) - or nothing.
func parseLoose(s string) *semverx.Version {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if v, ok := semverx.Parse(s); ok {
		return &v
	}
	// Two components, or a suffix after the third: keep what parses.
	parts := strings.SplitN(strings.TrimLeft(s, "vV"), ".", 4)
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	trim := func(p string) string {
		for i, r := range p {
			if r < '0' || r > '9' {
				return p[:i]
			}
		}
		return p
	}
	v, ok := semverx.Parse(trim(parts[0]) + "." + trim(parts[1]) + "." + trim(parts[2]))
	if !ok {
		return nil
	}
	return &v
}

// diffPaths returns the paths only old has, and only new has.
func diffPaths(old, new []string) (removed, added []string) {
	o := map[string]bool{}
	for _, p := range old {
		o[p] = true
	}
	n := map[string]bool{}
	for _, p := range new {
		n[p] = true
	}
	for _, p := range old {
		if !n[p] {
			removed = append(removed, p)
		}
	}
	for _, p := range new {
		if !o[p] {
			added = append(added, p)
		}
	}
	return removed, added
}

// flatten lists the paths of every key in a values document: a map's keys
// joined with dots, a list or a scalar as a leaf. A consumer sets values by
// path, so paths are what a change is measured in.
func flatten(v any, prefix string, out *[]string) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		if prefix != "" {
			*out = append(*out, prefix)
		}
		return
	}
	for k, child := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		flatten(child, p, out)
	}
}

// readArchive reads Chart.yaml and values.yaml out of a chart archive.
func readArchive(data []byte) (chart, error) {
	var c chart
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return c, fmt.Errorf("chart archive: %w", err)
	}
	tr := tar.NewReader(gz)
	var chartYAML, valuesYAML []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return c, fmt.Errorf("chart archive: %w", err)
		}
		// The archive's top directory is the chart's name; only its own
		// two files count, not a subchart's under charts/.
		parts := strings.Split(strings.TrimPrefix(h.Name, "./"), "/")
		if len(parts) != 2 || h.Typeflag != tar.TypeReg {
			continue
		}
		var into *[]byte
		switch parts[1] {
		case "Chart.yaml":
			into = &chartYAML
		case "values.yaml":
			into = &valuesYAML
		default:
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxChart))
		if err != nil {
			return c, fmt.Errorf("chart archive: %s: %w", h.Name, err)
		}
		*into = b
	}
	if chartYAML == nil {
		return c, errors.New("chart archive: no Chart.yaml")
	}
	var meta map[string]any
	if err := yamlx.Unmarshal(chartYAML, &meta); err != nil {
		return c, fmt.Errorf("chart archive: Chart.yaml: %w", err)
	}
	c.appVersion, _ = meta["appVersion"].(string)
	if n, ok := meta["appVersion"].(float64); ok {
		c.appVersion = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", n), "0"), ".")
	}
	c.kubeVersion, _ = meta["kubeVersion"].(string)
	c.dependencies = map[string]string{}
	if deps, ok := meta["dependencies"].([]any); ok {
		for _, d := range deps {
			if m, ok := d.(map[string]any); ok {
				name, _ := m["name"].(string)
				ver, _ := m["version"].(string)
				if name != "" {
					c.dependencies[name] = ver
				}
			}
		}
	}
	if valuesYAML != nil {
		var values map[string]any
		if err := yamlx.Unmarshal(valuesYAML, &values); err != nil {
			return c, fmt.Errorf("values.yaml: %w", err)
		}
		flatten(values, "", &c.values)
		sort.Strings(c.values)
	}
	return c, nil
}

// fromRepository reads both versions from a Helm repository's index.
func (a *Analyzer) fromRepository(ctx context.Context, dep model.Dependency, from, to string) (chart, chart, error) {
	repo := strings.TrimRight(dep.RegistryURLs[0], "/")
	resp, err := a.client.Get(ctx, repo+"/index.yaml", httpx.ReqOptions{})
	if err != nil {
		return chart{}, chart{}, fmt.Errorf("helm-chart: %s: index: %w", repo, err)
	}
	var index map[string]any
	if err := yamlx.Unmarshal(resp.Body, &index); err != nil {
		return chart{}, chart{}, fmt.Errorf("helm-chart: %s: index: %w", repo, err)
	}
	entries, _ := index["entries"].(map[string]any)
	name := dep.PackageName
	if name == "" {
		name = dep.DepName
	}
	versions, _ := entries[name].([]any)
	urlOf := func(version string) (string, error) {
		for _, raw := range versions {
			e, _ := raw.(map[string]any)
			if v, _ := e["version"].(string); strings.TrimPrefix(v, "v") != strings.TrimPrefix(version, "v") {
				continue
			}
			urls, _ := e["urls"].([]any)
			if len(urls) == 0 {
				break
			}
			u, _ := urls[0].(string)
			if !strings.Contains(u, "://") {
				base, err := url.Parse(repo + "/")
				if err != nil {
					return "", err
				}
				rel, err := url.Parse(u)
				if err != nil {
					return "", err
				}
				u = base.ResolveReference(rel).String()
			}
			return u, nil
		}
		return "", fmt.Errorf("helm-chart: %s has no archive for %s %s", repo, dep.DepName, version)
	}
	read := func(version string) (chart, error) {
		u, err := urlOf(version)
		if err != nil {
			return chart{}, err
		}
		var data []byte
		if err := a.client.Stream(ctx, u, func(r io.Reader) error {
			var err error
			data, err = io.ReadAll(io.LimitReader(r, maxChart))
			return err
		}); err != nil {
			return chart{}, fmt.Errorf("helm-chart: %s: %w", u, err)
		}
		c, err := readArchive(data)
		if err != nil {
			return chart{}, fmt.Errorf("helm-chart: %s: %w", u, err)
		}
		return c, nil
	}
	old, err := read(from)
	if err != nil {
		return chart{}, chart{}, err
	}
	new, err := read(to)
	if err != nil {
		return chart{}, chart{}, err
	}
	return old, new, nil
}

// fromOCI reads both versions as the chart layer of each tag's manifest.
func (a *Analyzer) fromOCI(ctx context.Context, dep model.Dependency, from, to string) (chart, chart, error) {
	read := func(tag string) (chart, error) {
		raw, err := a.oci.Manifest(ctx, dep.DepName, dep.RegistryURLs, tag)
		if err != nil {
			return chart{}, fmt.Errorf("helm-chart: %s:%s: %w", dep.DepName, tag, err)
		}
		var m struct {
			Config struct {
				MediaType string `json:"mediaType"`
			} `json:"config"`
			Layers []struct {
				MediaType string `json:"mediaType"`
				Digest    string `json:"digest"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return chart{}, fmt.Errorf("helm-chart: %s:%s: manifest: %w", dep.DepName, tag, err)
		}
		if m.Config.MediaType != configMediaType {
			return chart{}, classify.ErrNotApplicable
		}
		for _, l := range m.Layers {
			if l.MediaType != chartMediaType {
				continue
			}
			data, err := a.oci.Blob(ctx, dep.DepName, dep.RegistryURLs, l.Digest, maxChart)
			if err != nil {
				return chart{}, fmt.Errorf("helm-chart: %s:%s: %w", dep.DepName, tag, err)
			}
			c, err := readArchive(data)
			if err != nil {
				return chart{}, fmt.Errorf("helm-chart: %s:%s: %w", dep.DepName, tag, err)
			}
			return c, nil
		}
		return chart{}, fmt.Errorf("helm-chart: %s:%s: the manifest carries no chart layer", dep.DepName, tag)
	}
	old, err := read(from)
	if err != nil {
		return chart{}, chart{}, err
	}
	new, err := read(to)
	if err != nil {
		return chart{}, chart{}, err
	}
	return old, new, nil
}
