// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/hbs"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/sbom"
	"github.com/ohartwig/pinup/wire"
)

// purlDatasources maps a purl type to the datasource pinup looks it up in.
// Only datasources pinup serves are here: a component OSV knows but pinup
// cannot look up (maven, crates.io, RubyGems, NuGet) would be asked and
// never planned, and a finding nothing can move is a warning without a
// remedy. Those types are skipped and counted instead.
var purlDatasources = map[string]string{
	"golang":   "go",
	"npm":      "npm",
	"composer": "packagist",
	"pypi":     "pypi",
}

// componentDependency names the datasource, package and version a manifest
// component is asked about. The Go standard library is OSV's package
// "stdlib" in the Go ecosystem, which pinup asks under the toolchain
// datasource at the toolchain's version - the same way a go.mod's
// toolchain line is asked. ok is false for a type pinup does not serve.
func componentDependency(c sbom.Component) (datasource, name, version string, ok bool) {
	ds, ok := purlDatasources[c.Type]
	if !ok {
		return "", "", "", false
	}
	if c.Type == "golang" && c.Name == "stdlib" {
		return "golang-version", "go", strings.TrimPrefix(c.Version, "v"), true
	}
	return ds, c.Name, c.Version, true
}

// readArtifacts is the manifest stage, between extraction and lookup: for
// every artifactManifests entry and every file its patterns matched, the
// manifest is read, and each component pinup can look up becomes a
// disabled dependency of that file - asked about advisories, carried into
// the index for the watch, moved only by a fix and only where the
// configuration says how (docs/artifact-manifests.md).
func (r *whatifRun) readArtifacts() error {
	o, plan, decoded := r.o, r.plan, r.decoded
	if len(decoded.ArtifactManifests) == 0 {
		return nil
	}
	byManifest := map[int][]string{}
	for _, m := range r.found.Matches {
		if i, ok := wire.ArtifactIndex(m.Manager); ok {
			byManifest[i] = append(byManifest[i], m.Path)
		}
	}
	// A component the file already declares - the same datasource and
	// package on a line a manager read - is that line's business.
	declared := map[string]bool{}
	for _, d := range plan.Deps {
		declared[d.File+"|"+report.Key(d)] = true
	}
	seen := map[string]bool{}
	for i, am := range decoded.ArtifactManifests {
		files := byManifest[i]
		sort.Strings(files)
		for _, file := range files {
			rep := model.ArtifactReport{File: file, Manifest: i, Format: am.Format}
			source, data, err := r.readManifest(am, file)
			rep.Source = source
			var comps []sbom.Component
			if err == nil {
				comps, err = sbom.Parse(am.Format, data)
			}
			if err != nil {
				rep.Err = err.Error()
				plan.Warnings = append(plan.Warnings, model.Warning{Stage: "extract", File: file, Msg: fmt.Sprintf("artifactManifests[%d]: %v; the components of this build are not watched this run", i, err)})
				plan.Artifacts = append(plan.Artifacts, rep)
				continue
			}
			comps = sbom.Lowest(comps)
			rep.Components = len(comps)
			skip := func(kind string) {
				if rep.Skipped == nil {
					rep.Skipped = map[string]int{}
				}
				rep.Skipped[kind]++
			}
			for _, c := range comps {
				ds, name, version, ok := componentDependency(c)
				if !ok {
					kind := c.Type
					if kind == "" {
						kind = "no-purl"
					}
					skip(kind)
					continue
				}
				if version == "" {
					skip("no-version")
					continue
				}
				if len(am.Datasources) > 0 && !slices.Contains(am.Datasources, ds) {
					skip(ds)
					continue
				}
				key := file + "|" + ds + "|" + name
				if declared[key] {
					rep.Declared++
					continue
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				d := model.Dependency{
					Manager: model.ArtifactManager, File: file, CustomManager: model.NoCustomManager,
					DepName: name, PackageName: name, CurrentValue: version, Datasource: ds,
					Locus:    model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest},
					Artifact: &model.ArtifactRef{Manifest: i, Source: source},
					Disabled: fmt.Sprintf("shipped by the build of %s, not written in it (artifactManifests[%d]): watched for advisories, not updated on its own", file, i),
				}
				if am.Pin.Covers(ds) {
					d.Artifact.After, d.Artifact.Line = am.Pin.After, am.Pin.Line
				}
				// The same narrowing extraction applies: a run for one
				// released package or one advisory plans that and nothing
				// else.
				if o.Released != "" && !report.RefersTo(report.Key(d), o.Released) {
					d.SkipReason = "not the released package " + o.Released + "; the scheduled run covers it"
				}
				if o.Package != "" && d.SkipReason == "" && report.Key(d) != o.Package {
					d.SkipReason = "not the package " + o.Package + " this run is for; the scheduled run covers it"
				}
				d = applyDepRules(r.engine, r.resolved.Raw, d)
				plan.Deps = append(plan.Deps, d)
				rep.Watched++
			}
			plan.Artifacts = append(plan.Artifacts, rep)
		}
	}
	return nil
}

// readManifest fetches one manifest for one file, naming where it came
// from in the plan's words.
func (r *whatifRun) readManifest(am model.ArtifactManifest, file string) (source string, data []byte, err error) {
	env := hbs.MapEnv{Values: map[string]string{
		"file":     file,
		"fileName": filepath.Base(file),
		"fileStem": strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)),
		"fileDir":  filepath.ToSlash(filepath.Dir(file)),
	}}
	render := func(what, tmpl string) (string, error) {
		v, _, err := hbs.RenderString(tmpl, env)
		if err != nil {
			return "", fmt.Errorf("%s template: %w", what, err)
		}
		return v, nil
	}
	switch {
	case am.Path != "":
		p, err := render("path", am.Path)
		if err != nil {
			return am.Path, nil, err
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if strings.HasPrefix(p, "../") || filepath.IsAbs(p) {
			return p, nil, fmt.Errorf("path %q leaves the repository", p)
		}
		data, err := os.ReadFile(filepath.Join(r.o.Root, filepath.FromSlash(p)))
		return p, data, err
	case am.URL != "":
		u, err := render("url", am.URL)
		if err != nil {
			return am.URL, nil, err
		}
		if r.o.Fetch == nil {
			return u, nil, fmt.Errorf("no HTTP client to read %s with", u)
		}
		data, err := r.o.Fetch(r.ctx, u)
		return u, data, err
	case am.Artifact != nil:
		project, err := render("artifact.project", am.Artifact.Project)
		if err != nil {
			return "", nil, err
		}
		if project == "" {
			project = r.o.RepoName
		}
		ref, err := render("artifact.ref", am.Artifact.Ref)
		if err != nil {
			return "", nil, err
		}
		path, err := render("artifact.path", am.Artifact.Path)
		if err != nil {
			return "", nil, err
		}
		source := "artifact " + project
		if ref != "" {
			source += "@" + ref
		}
		source += " " + am.Artifact.Job + ":" + path
		if r.o.ReadArtifact == nil {
			return source, nil, fmt.Errorf("no platform to read the artifact %s from", source)
		}
		if project == "" {
			return source, nil, fmt.Errorf("artifact names no project and the run knows no repository name")
		}
		data, err := r.o.ReadArtifact(r.ctx, project, ref, am.Artifact.Job, path)
		return source, data, err
	}
	return "", nil, fmt.Errorf("artifactManifests[%d] names no source", am.Index)
}

// stateArtifactFindings turns an advisory against a component nothing can
// pin into a statement: the dependency stays skipped, its reason names the
// advisories and the fix, and the plan warns on every run while it lasts.
// A component a pin covers and a fix exists for is left for the planner,
// which reactivates it as it reactivates every vulnerable disabled
// dependency.
func stateArtifactFindings(deps []model.Dependency) []model.Warning {
	var warns []model.Warning
	for i := range deps {
		d := &deps[i]
		if d.Artifact == nil || len(d.Advisories) == 0 || d.SkipReason != d.Disabled {
			continue
		}
		ids := make([]string, 0, len(d.Advisories))
		for _, a := range d.Advisories {
			ids = append(ids, a.ID)
		}
		fix := "no fixed version"
		if d.VulnerabilityBound != "" {
			fix = "fixed in " + d.VulnerabilityBound
		}
		switch {
		case d.VulnerabilityBound == "":
			d.SkipReason = fmt.Sprintf("vulnerable (%s): %s; shipped by the build, nothing to raise it to yet", strings.Join(ids, ", "), fix)
		case !d.Artifact.Pinnable():
			d.SkipReason = fmt.Sprintf("vulnerable (%s): %s; shipped by the build and no pin configured (artifactManifests[%d].pin), so nothing here can write the fix", strings.Join(ids, ", "), fix, d.Artifact.Manifest)
		default:
			continue
		}
		warns = append(warns, model.Warning{Stage: "lookup", File: d.File, Msg: fmt.Sprintf("%s %s: %s (%s); %s", d.DepName, d.CurrentValue, strings.Join(ids, ", "), fix, afterSemicolon(d.SkipReason))})
	}
	return warns
}

func afterSemicolon(s string) string {
	if _, rest, ok := strings.Cut(s, "; "); ok {
		return rest
	}
	return s
}

// pinProof checks that the line a pin writes is one a manager of the
// repository reads: the edited file is extracted again and the dependency
// the line should have become is looked for. A line no manager reads would
// be written once and never moved; the branch is still written, and the
// warning names the manifest to fix.
func (r *whatifRun) pinProof(b model.Branch, updates []model.Update, edits []model.Edit) []model.Warning {
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	var warns []model.Warning
	for _, u := range updates {
		if !keys[u.Key()] || u.Blocked() || u.Dep.Artifact == nil {
			continue
		}
		body, ok := r.contents[u.Dep.File]
		if !ok {
			raw, err := os.ReadFile(filepath.Join(r.o.Root, filepath.FromSlash(u.Dep.File)))
			if err != nil {
				continue
			}
			body = raw
		}
		var own []model.Edit
		for _, e := range edits {
			if e.File == u.Dep.File && e.Manager == model.ArtifactManager && strings.Contains(e.New, u.Dep.DepName) {
				own = append(own, e)
			}
		}
		if len(own) == 0 {
			continue
		}
		edited := applyAll(body, own)
		if r.readsLine(u.Dep.File, edited, u.Dep.DepName, u.NewValue) {
			continue
		}
		warns = append(warns, model.Warning{Stage: "plan", File: u.Dep.File, Msg: fmt.Sprintf(
			"the line artifactManifests[%d].pin writes for %s is read by no manager; it would be written once and never moved", u.Dep.Artifact.Manifest, u.Dep.DepName)})
	}
	return warns
}

// applyAll inserts zero-width edits from the end of the file backwards; a
// pin edit is never anything else, so no conflict check is needed here.
func applyAll(content []byte, edits []model.Edit) []byte {
	sorted := append([]model.Edit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start > sorted[j].Start })
	out := append([]byte(nil), content...)
	for _, e := range sorted {
		if e.Start < 0 || e.End > len(out) || e.Start > e.End {
			continue
		}
		out = append(out[:e.Start], append([]byte(e.New), out[e.End:]...)...)
	}
	return out
}

// readsLine runs every manager that claims file over content and reports
// whether one of them extracts depName at value.
func (r *whatifRun) readsLine(file string, content []byte, depName, value string) bool {
	for _, m := range r.found.Matches {
		if m.Path != file {
			continue
		}
		if _, isManifest := wire.ArtifactIndex(m.Manager); isManifest {
			continue
		}
		p, err := wire.Resolve(m.Manager, r.decoded, r.managers)
		if err != nil {
			continue
		}
		res, err := extract.Run(r.ctx, p.Manager, extract.File{Path: file, Content: content}, p.Config)
		if err != nil {
			continue
		}
		for _, d := range res.Deps {
			if (d.DepName == depName || d.PackageName == depName) && d.CurrentValue == value {
				return true
			}
		}
	}
	return false
}
