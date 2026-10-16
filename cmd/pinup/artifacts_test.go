// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/report"
)

// The shape of devops/wolfi-packages on 2026-10-16: a melange recipe whose
// koh/go-raise block pins two modules, and the SBOM of the package it
// built, which carries a third module no line names - kin-openapi
// v0.144.0, six advisories, found by a scanner eleven hours after OSV had
// them. The recipe's custom manager is the estate's: one module per line.
const recipeYAML = `package:
  name: grafana
  epoch: 1
pipeline:
  - uses: koh/go-raise
    with:
      deps: |-
        golang.org/x/crypto@v0.58.0
        google.golang.org/grpc@v1.83.2
`

const grafanaSBOM = `{"bomFormat":"CycloneDX","specVersion":"1.7","components":[
 {"type":"library","name":"golang.org/x/crypto","version":"v0.58.0","purl":"pkg:golang/golang.org/x/crypto@v0.58.0"},
 {"type":"library","name":"github.com/getkin/kin-openapi","version":"v0.144.0","purl":"pkg:golang/github.com/getkin/kin-openapi@v0.144.0"},
 {"type":"library","name":"stdlib","version":"v1.26.9","purl":"pkg:golang/stdlib@v1.26.9"},
 {"type":"library","name":"busybox","version":"1.37.0-r51","purl":"pkg:apk/wolfi/busybox@1.37.0-r51?arch=aarch64"}]}`

// pinAfter is the pin's anchor and depsLine the manager's pattern, both as
// JSON string contents: the same line shape, the manager's with the groups.
const pinAfter = `\\n(?<indent>[ \\t]+)[a-z0-9.-]+\\.[a-z]{2,}/[^\\s@]+@v[0-9][^\\s]*`
const depsLine = `\\n[ \\t]+(?<depName>[a-z0-9.-]+\\.[a-z]{2,}/[^\\s@]+)@(?<currentValue>v[0-9][^\\s]*)`

type manifestTree struct {
	// pin is the pin object's JSON, "" for a manifest without one.
	pin string
	// source is the source key and value, e.g. `"path": "sbom/{{{fileStem}}}.cdx.json"`.
	source string
	// vulnerable answers kin-openapi's advisory.
	vulnerable bool
	// pkg narrows the run (--package).
	pkg string
	// readArtifact answers the platform's artifacts; nil leaves the
	// option unset.
	readArtifact func(ctx context.Context, project, ref, job, path string) ([]byte, error)
}

func manifestPlan(t *testing.T, mt manifestTree) (*model.Plan, *askedAdvisories) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(root+"/grafana.yaml", []byte(recipeYAML), 0o644)
	os.MkdirAll(root+"/sbom", 0o755)
	os.WriteFile(root+"/sbom/grafana.cdx.json", []byte(grafanaSBOM), 0o644)
	pin := ""
	if mt.pin != "" {
		pin = `, "pin": ` + mt.pin
	}
	source := mt.source
	if source == "" {
		source = `"format": "cyclonedx", "path": "sbom/{{{fileStem}}}.cdx.json"`
	}
	cfg := fmt.Sprintf(`{"extends": ["local>devops/renovate-runner"], "osvVulnerabilityAlerts": true, "timezone": "Europe/Berlin",
		"vulnerabilityAlerts": {"enabled": true, "labels": ["security"], "schedule": ["at any time"], "minimumReleaseAge": null, "automerge": true},
		"customManagers": [{"customType": "regex", "managerFilePatterns": ["/^[^/]+\\.yaml$/"],
			"matchStrings": ["%s"], "datasourceTemplate": "go", "versioningTemplate": "semver"}],
		"artifactManifests": [{"matchFileNames": ["/^[^/]+\\.yaml$/"], %s%s}]}`, depsLine, source, pin)
	os.WriteFile(root+"/renovate.json", []byte(cfg), 0o644)
	opts := treeOptions(t, root, "devops/wolfi-packages", time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	opts.Datasources["go"] = cannedDS{name: "go", scheme: "semver", releases: map[string][]string{
		"github.com/getkin/kin-openapi": {"v0.144.0", "v0.147.0", "v0.151.0"},
		"golang.org/x/crypto":           {"v0.58.0"},
		"google.golang.org/grpc":        {"v1.83.2"},
	}}
	opts.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
	client := &askedAdvisories{answers: map[string][]osv.Advisory{}}
	if mt.vulnerable {
		client.answers["github.com/getkin/kin-openapi"] = []osv.Advisory{
			{ID: "GHSA-23vg-4cjm-66q5", Aliases: []string{"CVE-2026-100351"}, Fixed: "v0.147.0", Published: time.Date(2026, 10, 15, 20, 19, 20, 0, time.UTC)},
		}
	}
	opts.Advisories = client
	opts.Package = mt.pkg
	opts.ReadArtifact = mt.readArtifact
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return plan, client
}

const estatePin = `{"after": "` + pinAfter + `", "line": "{{{indent}}}{{{depName}}}@{{{newValue}}}"}`

func artifactDep(t *testing.T, plan *model.Plan, name string) *model.Dependency {
	t.Helper()
	for i := range plan.Deps {
		if plan.Deps[i].Manager == model.ArtifactManager && plan.Deps[i].DepName == name {
			return &plan.Deps[i]
		}
	}
	return nil
}

// The manifest's components become dependencies of the recipe: watched,
// not updated; the one a line already declares stays with its line; the
// apk package is counted, not read. Every count has its denominator.
func TestAManifestsComponentsAreWatchedNotUpdated(t *testing.T) {
	plan, client := manifestPlan(t, manifestTree{pin: estatePin})
	kin := artifactDep(t, plan, "github.com/getkin/kin-openapi")
	if kin == nil {
		t.Fatalf("kin-openapi is not a dependency: %+v", plan.Deps)
	}
	if kin.CurrentValue != "v0.144.0" || kin.Datasource != "go" || kin.File != "grafana.yaml" || kin.Artifact == nil || !kin.Artifact.Pinnable() {
		t.Errorf("kin-openapi = %+v", *kin)
	}
	if !strings.Contains(kin.SkipReason, "shipped by the build") || kin.Disabled == "" {
		t.Errorf("skipReason %q, disabled %q", kin.SkipReason, kin.Disabled)
	}
	if d := artifactDep(t, plan, "golang.org/x/crypto"); d != nil {
		t.Errorf("x/crypto is declared by a line and still read from the manifest: %+v", *d)
	}
	if std := artifactDep(t, plan, "go"); std == nil || std.Datasource != "golang-version" || std.CurrentValue != "1.26.9" {
		t.Errorf("stdlib = %+v", std)
	}
	if len(plan.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v", plan.Artifacts)
	}
	a := plan.Artifacts[0]
	if a.File != "grafana.yaml" || a.Source != "sbom/grafana.cdx.json" || a.Components != 4 || a.Watched != 2 || a.Declared != 1 || a.Skipped["apk"] != 1 || a.Err != "" {
		t.Errorf("artifact report = %+v", a)
	}
	asked := slices.IndexFunc(client.asked, func(q osv.Query) bool {
		return q.PackageName == "github.com/getkin/kin-openapi" && q.Version == "v0.144.0" && q.Datasource == "go"
	})
	if asked < 0 {
		t.Errorf("OSV was not asked about kin-openapi v0.144.0: %+v", client.asked)
	}
	if kin.AdvisoryCoverage == nil || kin.AdvisoryCoverage.State != model.CoveredByAdvisories {
		t.Errorf("coverage = %+v", kin.AdvisoryCoverage)
	}
	for _, b := range plan.Branches {
		for _, k := range b.UpdateKeys {
			if strings.Contains(k, "kin-openapi") {
				t.Errorf("a branch moves kin-openapi without an advisory: %s", b.Name)
			}
		}
	}
	if line := report.ArtifactsLine(plan.Artifacts); line != "Shipped by the build: 4 components from 1 manifest, 2 watched, 1 declared, 1 skipped (apk 1)." {
		t.Errorf("dashboard line = %q", line)
	}
}

// An advisory with a fix turns the component into a security update whose
// edit is the pin line, written after the block's last module, under the
// vulnerabilityAlerts branch with its label. The version is the lowest
// release at or above the fix (vulnerabilityFixStrategy lowest), as for
// every security fix pinup plans.
func TestAnAdvisoryWritesThePin(t *testing.T) {
	plan, _ := manifestPlan(t, manifestTree{pin: estatePin, vulnerable: true})
	var branch *model.Branch
	for i := range plan.Branches {
		for _, k := range plan.Branches[i].UpdateKeys {
			if strings.Contains(k, "kin-openapi") {
				branch = &plan.Branches[i]
			}
		}
	}
	if branch == nil {
		t.Fatalf("no branch for kin-openapi; warnings %+v", plan.Warnings)
	}
	if branch.SuppressedBy != "" || !slices.Contains(branch.Labels, "security") || !strings.Contains(branch.Name, "vulnerability") {
		t.Errorf("branch = %s suppressed %q labels %v", branch.Name, branch.SuppressedBy, branch.Labels)
	}
	if len(branch.Edits) != 1 {
		t.Fatalf("edits = %+v", branch.Edits)
	}
	e := branch.Edits[0]
	want := "\n        github.com/getkin/kin-openapi@v0.147.0"
	if e.File != "grafana.yaml" || e.Old != "" || e.Start != e.End || e.New != want || e.Manager != model.ArtifactManager {
		t.Errorf("edit = %+v, want insertion of %q", e, want)
	}
	if recipeYAML[:e.Start] != strings.TrimSuffix(recipeYAML, "\n") {
		t.Errorf("inserted at %d, after %q", e.Start, recipeYAML[:e.Start])
	}
	for _, u := range plan.Updates {
		if strings.Contains(u.DepKey, "kin-openapi") && (!u.SecurityFix || u.NewVersion != "v0.147.0" || u.Type != model.UpdateMinor) {
			t.Errorf("update = securityFix %t %s %s", u.SecurityFix, u.NewVersion, u.Type)
		}
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w.Msg, "read by no manager") {
			t.Errorf("the estate's manager reads the line, yet: %s", w.Msg)
		}
	}
}

// Without a pin the finding is a statement: the reason names the advisory
// and the fix, the plan warns, and nothing is written.
func TestWithoutAPinTheFindingIsStated(t *testing.T) {
	plan, _ := manifestPlan(t, manifestTree{vulnerable: true})
	kin := artifactDep(t, plan, "github.com/getkin/kin-openapi")
	if kin == nil || !strings.Contains(kin.SkipReason, "GHSA-23vg-4cjm-66q5") || !strings.Contains(kin.SkipReason, "fixed in v0.147.0") || !strings.Contains(kin.SkipReason, "no pin configured") {
		t.Fatalf("kin-openapi = %+v", kin)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w model.Warning) bool {
		return w.File == "grafana.yaml" && strings.Contains(w.Msg, "GHSA-23vg-4cjm-66q5") && strings.Contains(w.Msg, "no pin configured")
	}) {
		t.Errorf("no warning names the finding: %+v", plan.Warnings)
	}
	for _, b := range plan.Branches {
		if slices.ContainsFunc(b.UpdateKeys, func(k string) bool { return strings.Contains(k, "kin-openapi") }) {
			t.Errorf("a branch was planned without a pin: %s", b.Name)
		}
	}
}

// The index carries the component with its manifest and without a
// consumer entry; the targeted run the watch starts writes the pin.
func TestTheIndexAndTheTargetedRunCarryTheComponent(t *testing.T) {
	plan, _ := manifestPlan(t, manifestTree{pin: estatePin})
	idx := report.NewIndex()
	idx.Record("devops/wolfi-packages", plan, time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	var got *report.Dependency
	for i, d := range idx.Dependencies["devops/wolfi-packages"] {
		if d.PackageName == "github.com/getkin/kin-openapi" {
			got = &idx.Dependencies["devops/wolfi-packages"][i]
		}
	}
	if got == nil || got.Version != "v0.144.0" || got.Artifact != "sbom/grafana.cdx.json" || got.Datasource != "go" || got.File != "grafana.yaml" {
		t.Fatalf("index entry = %+v", got)
	}
	if _, listed := idx.Consumers["go|github.com/getkin/kin-openapi"]; listed {
		t.Errorf("a manifest component is a consumer entry")
	}
	if _, listed := idx.Consumers["go|golang.org/x/crypto"]; !listed {
		t.Errorf("the declared line lost its consumer entry")
	}

	narrowed, _ := manifestPlan(t, manifestTree{pin: estatePin, vulnerable: true, pkg: "go|github.com/getkin/kin-openapi"})
	var found bool
	for _, b := range narrowed.Branches {
		if slices.ContainsFunc(b.UpdateKeys, func(k string) bool { return strings.Contains(k, "kin-openapi") }) && b.SuppressedBy == "" && len(b.Edits) == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("the targeted run planned no pin: %+v", narrowed.Branches)
	}
	if d := artifactDep(t, narrowed, "go"); d == nil || !strings.Contains(d.SkipReason, "not the package") {
		t.Errorf("the other component is not narrowed out: %+v", d)
	}
}

// A pin whose line no manager reads is written and warned about: the
// configuration, not the run, is what has to change.
func TestAPinNoManagerReadsIsWarnedAbout(t *testing.T) {
	plan, _ := manifestPlan(t, manifestTree{vulnerable: true, pin: `{"after": "` + pinAfter + `", "line": "{{{indent}}}# raise {{{depName}}} to {{{newValue}}}"}`})
	if !slices.ContainsFunc(plan.Warnings, func(w model.Warning) bool {
		return strings.Contains(w.Msg, "read by no manager") && strings.Contains(w.Msg, "kin-openapi")
	}) {
		t.Errorf("no proof warning: %+v", plan.Warnings)
	}
	var written bool
	for _, b := range plan.Branches {
		if slices.ContainsFunc(b.UpdateKeys, func(k string) bool { return strings.Contains(k, "kin-openapi") }) && b.SuppressedBy == "" {
			written = true
		}
	}
	if !written {
		t.Errorf("the branch was held although the warning is the configuration's: %+v", plan.Branches)
	}
}

// An artifact source reads through the platform, named by job and path
// with the file's stem; the repository is the project when none is named.
func TestAnArtifactSourceReadsThroughThePlatform(t *testing.T) {
	var asked []string
	read := func(_ context.Context, project, ref, job, path string) ([]byte, error) {
		asked = append(asked, project+"|"+ref+"|"+job+"|"+path)
		if path == "scan-reports/grafana.trivy.json" {
			return []byte(`{"SchemaVersion":2,"Results":[{"Packages":[{"Name":"github.com/getkin/kin-openapi","Version":"v0.144.0","Identifier":{"PURL":"pkg:golang/github.com/getkin/kin-openapi@v0.144.0"}}]}]}`), nil
		}
		return nil, fmt.Errorf("no such artifact")
	}
	plan, _ := manifestPlan(t, manifestTree{readArtifact: read, source: `"format": "trivy", "artifact": {"job": "audit:published:aarch64", "path": "scan-reports/{{{fileStem}}}.trivy.json"}`})
	if !slices.Contains(asked, "devops/wolfi-packages||audit:published:aarch64|scan-reports/grafana.trivy.json") {
		t.Fatalf("asked = %v", asked)
	}
	kin := artifactDep(t, plan, "github.com/getkin/kin-openapi")
	if kin == nil || kin.Artifact.Source != "artifact devops/wolfi-packages audit:published:aarch64:scan-reports/grafana.trivy.json" {
		t.Errorf("kin-openapi = %+v", kin)
	}
	if len(plan.Artifacts) != 1 || plan.Artifacts[0].Format != "trivy" || plan.Artifacts[0].Watched != 1 {
		t.Errorf("artifacts = %+v", plan.Artifacts)
	}

	// Without a platform the manifest is a stated gap, and the rest of the
	// run goes on.
	plan, _ = manifestPlan(t, manifestTree{source: `"format": "trivy", "artifact": {"job": "audit:published:aarch64", "path": "x.json"}`})
	if len(plan.Artifacts) != 1 || !strings.Contains(plan.Artifacts[0].Err, "no platform") {
		t.Errorf("artifacts = %+v", plan.Artifacts)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w model.Warning) bool {
		return strings.Contains(w.Msg, "artifactManifests[0]") && strings.Contains(w.Msg, "not watched this run")
	}) {
		t.Errorf("no warning: %+v", plan.Warnings)
	}
	if len(plan.Deps) == 0 {
		t.Errorf("the run lost the recipe's own lines")
	}
}

// A manifest that is not there is an error on that manifest alone.
func TestAMissingManifestIsAWarningNotAFailure(t *testing.T) {
	plan, _ := manifestPlan(t, manifestTree{source: `"format": "cyclonedx", "path": "sbom/{{{fileStem}}}.missing.json"`})
	if len(plan.Artifacts) != 1 || plan.Artifacts[0].Err == "" || plan.Artifacts[0].Watched != 0 {
		t.Fatalf("artifacts = %+v", plan.Artifacts)
	}
	if artifactDep(t, plan, "github.com/getkin/kin-openapi") != nil {
		t.Errorf("a component came out of a manifest that was not read")
	}
	if !strings.Contains(report.ArtifactsLine(plan.Artifacts), "1 manifest could not be read") {
		t.Errorf("line = %q", report.ArtifactsLine(plan.Artifacts))
	}
}
