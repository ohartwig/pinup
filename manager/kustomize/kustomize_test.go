// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package kustomize

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
)

func extractAll(t *testing.T, path, src string) (extract.File, []model.Dependency) {
	t.Helper()
	f := extract.File{Path: path, Content: []byte(src)}
	res, err := New().Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	return f, res.Deps
}

func depNamed(t *testing.T, deps []model.Dependency, depName, depType string) model.Dependency {
	t.Helper()
	var matches []model.Dependency
	for _, d := range deps {
		if d.DepName == depName && d.DepType == depType {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("found %d dependencies named %q/%q, want exactly 1 (deps: %+v)", len(matches), depName, depType, deps)
	}
	return matches[0]
}

// assertValueLocus checks the contract every dependency with a CurrentValue
// must satisfy: the value span brackets exactly those bytes.
func assertValueLocus(t *testing.T, f extract.File, d model.Dependency) {
	t.Helper()
	if d.Locus.ValueStart < 0 || d.Locus.ValueEnd > len(f.Content) || d.Locus.ValueStart > d.Locus.ValueEnd {
		t.Fatalf("%s/%s: value locus [%d:%d] out of range for a %d-byte file", d.DepName, d.DepType, d.Locus.ValueStart, d.Locus.ValueEnd, len(f.Content))
	}
	if got := string(f.Content[d.Locus.ValueStart:d.Locus.ValueEnd]); got != d.CurrentValue {
		t.Errorf("%s/%s: bytes at value locus = %q, want CurrentValue %q", d.DepName, d.DepType, got, d.CurrentValue)
	}
}

// assertDigestLocus checks the same contract for a digest span.
func assertDigestLocus(t *testing.T, f extract.File, d model.Dependency) {
	t.Helper()
	if d.Locus.DigestStart < 0 || d.Locus.DigestEnd > len(f.Content) || d.Locus.DigestStart > d.Locus.DigestEnd {
		t.Fatalf("%s/%s: digest locus [%d:%d] out of range for a %d-byte file", d.DepName, d.DepType, d.Locus.DigestStart, d.Locus.DigestEnd, len(f.Content))
	}
	if got := string(f.Content[d.Locus.DigestStart:d.Locus.DigestEnd]); got != d.CurrentDigest {
		t.Errorf("%s/%s: bytes at digest locus = %q, want CurrentDigest %q", d.DepName, d.DepType, got, d.CurrentDigest)
	}
}

// TestResourcesAndComponents exercises every resources/components shape the
// corpus records, plus a local path (nothing to extract) and an entry with
// no "?ref=" at all.
func TestResourcesAndComponents(t *testing.T) {
	src := `resources:
  - ../../base
  - deployment.yaml
  - github.com/kubernetes-sigs/kustomize/examples/multibases/dev/?ref=v3.3.1
  - https://github.com/argoproj/argo-cd/manifests/cluster-install?ref=v2.12.3
  - git::https://git.example.com/group/project.git//apps?ref=1.4.0
  - ssh://git@git.example.com/group/project.git//monitoring?ref=v0.9.1
components:
  - github.com/kubernetes-sigs/kustomize/examples/components/example?ref=v5.0.0
`
	f, deps := extractAll(t, "kustomization.yaml", src)
	if len(deps) != 5 {
		for _, d := range deps {
			t.Logf("  got %-8s %-45s %q skip=%q", d.DepType, d.DepName, d.CurrentValue, d.SkipReason)
		}
		t.Fatalf("got %d deps, want 5 (2 local/no-ref entries yield nothing)", len(deps))
	}

	// Both github.com entries share a depName; disambiguate on CurrentValue.
	var ghDeps []model.Dependency
	for _, d := range deps {
		if d.DepName == "kubernetes-sigs/kustomize" {
			ghDeps = append(ghDeps, d)
		}
	}
	if len(ghDeps) != 2 {
		t.Fatalf("got %d kubernetes-sigs/kustomize deps, want 2 (resources + components)", len(ghDeps))
	}
	for _, d := range ghDeps {
		if d.Datasource != "github-tags" || d.SkipReason != "" || d.PackageName != "" {
			t.Errorf("github form %+v, want datasource github-tags, no skip, no packageName", d)
		}
		assertValueLocus(t, f, d)
	}

	argo := depNamed(t, deps, "argoproj/argo-cd", "Kustomization")
	if argo.CurrentValue != "v2.12.3" || argo.Datasource != "github-tags" || argo.SkipReason != "" {
		t.Errorf("argo dep = %+v", argo)
	}
	assertValueLocus(t, f, argo)

	// Two "git.example.com/group/project" deps exist (git:: and ssh);
	// disambiguate on CurrentValue rather than looking either up by name.
	var gitDouble, sshDep model.Dependency
	var foundGit, foundSSH bool
	for _, d := range deps {
		if d.DepName != "git.example.com/group/project" {
			continue
		}
		switch d.CurrentValue {
		case "1.4.0":
			gitDouble, foundGit = d, true
		case "v0.9.1":
			sshDep, foundSSH = d, true
		}
	}
	if !foundGit {
		t.Fatalf("git:: form not found among: %+v", deps)
	}
	if gitDouble.Datasource != "git-tags" ||
		gitDouble.PackageName != "https://git.example.com/group/project.git" || gitDouble.SkipReason != "" {
		t.Errorf("git:: form = %+v", gitDouble)
	}
	assertValueLocus(t, f, gitDouble)

	if !foundSSH {
		t.Fatalf("ssh:// form not found among: %+v", deps)
	}
	if sshDep.Datasource != "git-tags" || sshDep.PackageName != "ssh://**redacted**@git.example.com/group/project.git" {
		t.Errorf("ssh:// form = %+v, want packageName with the user redacted", sshDep)
	}
	assertValueLocus(t, f, sshDep)
}

// TestImages exercises the four measured images: shapes plus the invalid
// combination and the no-op (name only) case.
func TestImages(t *testing.T) {
	src := `images:
  - name: registry.example/wolfi-base
    newTag: "2"
  - name: registry.example/golang
    newTag: 1.27.0
  - name: nginx
    newName: registry.example/ci-mirrors/nginx
    newTag: 1.27.2
  - name: alpine
    digest: sha256:0000000000000000000000000000000000000000000000000000000000000000
  - name: busybox
    newTag: "1.36"
    digest: sha256:1111111111111111111111111111111111111111111111111111111111111111
  - name: unpatched
`
	f, deps := extractAll(t, "kustomization.yaml", src)
	if len(deps) != 5 {
		for _, d := range deps {
			t.Logf("  got %-45s %q digest=%q skip=%q", d.DepName, d.CurrentValue, d.CurrentDigest, d.SkipReason)
		}
		t.Fatalf("got %d deps, want 5 (the name-only entry yields nothing)", len(deps))
	}

	// Quoted newTag.
	wolfi := depNamed(t, deps, "registry.example/wolfi-base", "Kustomization")
	if wolfi.CurrentValue != "2" || wolfi.Datasource != "docker" || wolfi.PackageName != "registry.example/wolfi-base" {
		t.Errorf("wolfi-base = %+v", wolfi)
	}
	assertValueLocus(t, f, wolfi)

	// Unquoted newTag.
	golang := depNamed(t, deps, "registry.example/golang", "Kustomization")
	if golang.CurrentValue != "1.27.0" || golang.Datasource != "docker" {
		t.Errorf("golang = %+v", golang)
	}
	assertValueLocus(t, f, golang)

	// name + newName + newTag: depName/packageName is newName, not name.
	nginx := depNamed(t, deps, "registry.example/ci-mirrors/nginx", "Kustomization")
	if nginx.CurrentValue != "1.27.2" || nginx.PackageName != "registry.example/ci-mirrors/nginx" {
		t.Errorf("nginx = %+v", nginx)
	}
	assertValueLocus(t, f, nginx)

	// digest-only.
	alpine := depNamed(t, deps, "alpine", "Kustomization")
	if alpine.CurrentValue != "" || alpine.CurrentDigest == "" || alpine.Datasource != "docker" || alpine.PackageName != "alpine" {
		t.Errorf("alpine = %+v", alpine)
	}
	assertDigestLocus(t, f, alpine)
	if alpine.Locus.ValueStart != alpine.Locus.DigestStart || alpine.Locus.ValueEnd != alpine.Locus.DigestEnd {
		t.Errorf("alpine value locus %+v does not mirror the digest locus", alpine.Locus)
	}

	// newTag AND digest: invalid, no datasource.
	busybox := depNamed(t, deps, "busybox", "Kustomization")
	if busybox.SkipReason != "invalid-dependency-specification" || busybox.Datasource != "" ||
		busybox.CurrentValue != "1.36" || busybox.CurrentDigest == "" {
		t.Errorf("busybox = %+v", busybox)
	}
	assertValueLocus(t, f, busybox)
	assertDigestLocus(t, f, busybox)
}

// TestHelmCharts exercises the two measured repo shapes.
func TestHelmCharts(t *testing.T) {
	src := `helmCharts:
  - name: grafana
    repo: https://grafana.github.io/helm-charts
    version: 8.5.1
  - name: prometheus
    repo: oci://registry.example/charts
    version: 25.8.0
`
	f, deps := extractAll(t, "kustomization.yaml", src)
	if len(deps) != 2 {
		t.Fatalf("got %d deps, want 2", len(deps))
	}

	grafana := depNamed(t, deps, "grafana", "HelmChart")
	if grafana.CurrentValue != "8.5.1" || grafana.Datasource != "helm" || grafana.PackageName != "" ||
		len(grafana.RegistryURLs) != 1 || grafana.RegistryURLs[0] != "https://grafana.github.io/helm-charts" {
		t.Errorf("grafana = %+v", grafana)
	}
	assertValueLocus(t, f, grafana)

	prometheus := depNamed(t, deps, "prometheus", "HelmChart")
	if prometheus.CurrentValue != "25.8.0" || prometheus.Datasource != "docker" ||
		prometheus.PackageName != "registry.example/charts/prometheus" || len(prometheus.RegistryURLs) != 0 {
		t.Errorf("prometheus = %+v", prometheus)
	}
	assertValueLocus(t, f, prometheus)
}

// TestKustomizationFileNoExtension checks that a bare "Kustomization" file
// (no .yaml/.yml suffix, one of Renovate's two fileMatch spellings) is read
// the same way - Extract does not look at the path at all.
func TestKustomizationFileNoExtension(t *testing.T) {
	src := "images:\n  - name: alpine\n    newTag: \"3.20\"\n"
	f, deps := extractAll(t, "overlays/prod/Kustomization", src)
	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1", len(deps))
	}
	assertValueLocus(t, f, deps[0])
}

// TestKustomizationYmlExtension checks the short ".yml" spelling.
func TestKustomizationYmlExtension(t *testing.T) {
	_, deps := extractAll(t, "base/kustomization.yml", "images:\n  - name: alpine\n    newTag: \"3.20\"\n")
	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1", len(deps))
	}
}

// TestNotAMappingYieldsNothing covers a file that parses as YAML but is not
// a mapping at the top level - kustomize itself would refuse it.
func TestNotAMappingYieldsNothing(t *testing.T) {
	for _, src := range []string{
		"- a\n- b\n",
		"just a scalar\n",
		"",
	} {
		_, deps := extractAll(t, "kustomization.yaml", src)
		if len(deps) != 0 {
			t.Errorf("src %q: got %d deps, want 0", src, len(deps))
		}
	}
}

// TestNoneOfTheThreeKeysYieldsNothing covers a mapping with none of
// resources, components, images or helmCharts.
func TestNoneOfTheThreeKeysYieldsNothing(t *testing.T) {
	_, deps := extractAll(t, "kustomization.yaml", "namespace: prod\n")
	if len(deps) != 0 {
		t.Errorf("got %d deps, want 0", len(deps))
	}
}

func TestInvalidYAMLIsAWarningNotAFailure(t *testing.T) {
	f := extract.File{Path: "kustomization.yaml", Content: []byte("images: [unclosed\n")}
	res, err := New().Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || len(res.Deps) != 0 {
		t.Errorf("warnings=%d deps=%d; a broken file should warn and yield nothing", len(res.Warnings), len(res.Deps))
	}
	if res.Warnings[0].Stage != "extract" {
		t.Errorf("warning stage = %q, want extract", res.Warnings[0].Stage)
	}
}

// TestEditReplacesExactlyTheValueBytes round-trips a tag update.
func TestEditReplacesExactlyTheValueBytes(t *testing.T) {
	f, deps := extractAll(t, "kustomization.yaml", "images:\n  - name: alpine\n    newTag: \"3.20\"\n")
	dep := depNamed(t, deps, "alpine", "Kustomization")

	up := model.Update{Dep: dep, NewValue: "3.21"}
	edit, err := New().Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	if edit.Old != "3.20" || edit.New != "3.21" {
		t.Fatalf("edit = %+v, want Old=3.20 New=3.21", edit)
	}
	rewritten := string(f.Content[:edit.Start]) + edit.New + string(f.Content[edit.End:])
	want := "images:\n  - name: alpine\n    newTag: \"3.21\"\n"
	if rewritten != want {
		t.Errorf("rewritten = %q, want %q", rewritten, want)
	}
}

// TestEditReplacesExactlyTheDigestBytes round-trips a digest-only update.
func TestEditReplacesExactlyTheDigestBytes(t *testing.T) {
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	src := "images:\n  - name: alpine\n    digest: " + digest + "\n"
	f, deps := extractAll(t, "kustomization.yaml", src)
	dep := depNamed(t, deps, "alpine", "Kustomization")

	newDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	up := model.Update{Dep: dep, NewDigest: newDigest}
	edit, err := New().Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	if edit.Old != digest || edit.New != newDigest {
		t.Fatalf("edit = %+v", edit)
	}
	rewritten := string(f.Content[:edit.Start]) + edit.New + string(f.Content[edit.End:])
	want := "images:\n  - name: alpine\n    digest: " + newDigest + "\n"
	if rewritten != want {
		t.Errorf("rewritten = %q, want %q", rewritten, want)
	}
}

func TestEditRefusesAChangedFile(t *testing.T) {
	f, deps := extractAll(t, "kustomization.yaml", "images:\n  - name: alpine\n    newTag: \"3.20\"\n")
	dep := depNamed(t, deps, "alpine", "Kustomization")

	mutated := make([]byte, len(f.Content))
	copy(mutated, f.Content)
	mutated[dep.Locus.ValueStart] = 'X'
	mutatedFile := extract.File{Path: f.Path, Content: mutated}

	up := model.Update{Dep: dep, NewValue: "3.21"}
	if _, err := New().Edit(context.Background(), mutatedFile, up); err == nil {
		t.Fatal("Edit did not refuse a file that changed since extraction")
	}
}

func TestEditRefusesAChangedDigest(t *testing.T) {
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	f, deps := extractAll(t, "kustomization.yaml", "images:\n  - name: alpine\n    digest: "+digest+"\n")
	dep := depNamed(t, deps, "alpine", "Kustomization")

	mutated := make([]byte, len(f.Content))
	copy(mutated, f.Content)
	mutated[dep.Locus.DigestStart] = 'X'
	mutatedFile := extract.File{Path: f.Path, Content: mutated}

	up := model.Update{Dep: dep, NewDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111"}
	if _, err := New().Edit(context.Background(), mutatedFile, up); err == nil {
		t.Fatal("Edit did not refuse a file that changed since extraction")
	}
}

// depKey is the tuple the task names as the acceptance comparison, minus the
// corpus fields this manager deliberately does not reproduce
// (autoReplaceStringTemplate, replaceString, pinDigests - pinup edits by
// byte span, not by template) and versioning/extractVersion, which this
// manager's corpus entries never set.
type depKey struct {
	packageFile, depName, depType, datasource, currentValue, currentDigest, packageName, skipReason, registryURLs string
}

func (k depKey) String() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s",
		k.packageFile, k.depName, k.depType, k.datasource, k.currentValue,
		k.currentDigest, k.packageName, k.skipReason, k.registryURLs)
}

// TestAgreesWithTheCorpus extracts every kustomize packageFile in
// testdata/renovate/synthetic/tfversion and compares the result, as a
// multiset, against testdata/<root>/renovate-43.288.0/extract/tfversion.json
// key "kustomize" - 12 dependencies over two files. This is the manager's
// acceptance test.
func TestAgreesWithTheCorpus(t *testing.T) {
	corpusPath := fixture.Captured(t, "extract", "tfversion.json")
	treeDir := fixture.Shared(t, "synthetic", "tfversion")

	corpusBytes, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("reading corpus fixture: %v", err)
	}
	var fixture struct {
		Kustomize []struct {
			PackageFile string `json:"packageFile"`
			Deps        []struct {
				DepName       string   `json:"depName"`
				DepType       string   `json:"depType"`
				Datasource    string   `json:"datasource"`
				CurrentValue  string   `json:"currentValue"`
				CurrentDigest string   `json:"currentDigest"`
				PackageName   string   `json:"packageName"`
				SkipReason    string   `json:"skipReason"`
				RegistryURLs  []string `json:"registryUrls"`
			} `json:"deps"`
		} `json:"kustomize"`
	}
	if err := json.Unmarshal(corpusBytes, &fixture); err != nil {
		t.Fatalf("corpus fixture does not parse: %v", err)
	}

	want := make(map[string]int)
	total := 0
	for _, pf := range fixture.Kustomize {
		for _, d := range pf.Deps {
			k := depKey{
				packageFile: pf.PackageFile, depName: d.DepName, depType: d.DepType,
				datasource: d.Datasource, currentValue: d.CurrentValue, currentDigest: d.CurrentDigest,
				packageName: d.PackageName, skipReason: d.SkipReason,
				registryURLs: strings.Join(d.RegistryURLs, ","),
			}
			want[k.String()]++
			total++
		}
	}
	if total != 12 {
		t.Fatalf("corpus fixture has %d kustomize dependencies, want 12 - the fixture itself changed", total)
	}

	got := make(map[string]int)
	for _, pf := range fixture.Kustomize {
		src, err := os.ReadFile(filepath.Join(treeDir, pf.PackageFile))
		if err != nil {
			t.Fatalf("reading %s: %v", pf.PackageFile, err)
		}
		f := extract.File{Path: pf.PackageFile, Content: src}
		res, err := New().Extract(context.Background(), f, extract.ManagerConfig{})
		if err != nil {
			t.Fatalf("Extract(%s) returned an error: %v", pf.PackageFile, err)
		}
		for _, d := range res.Deps {
			k := depKey{
				packageFile: pf.PackageFile, depName: d.DepName, depType: d.DepType,
				datasource: d.Datasource, currentValue: d.CurrentValue, currentDigest: d.CurrentDigest,
				packageName: d.PackageName, skipReason: d.SkipReason,
				registryURLs: strings.Join(d.RegistryURLs, ","),
			}
			got[k.String()]++

			// Every dependency this manager reports must bracket its own
			// CurrentValue/CurrentDigest exactly - a corpus agreement with an
			// unusable Locus would not be worth having.
			if d.CurrentValue != "" {
				if d.Locus.ValueStart < 0 || d.Locus.ValueEnd > len(src) || d.Locus.ValueStart > d.Locus.ValueEnd {
					t.Errorf("%s/%s: value locus %+v out of range", pf.PackageFile, d.DepName, d.Locus)
				} else if slice := string(src[d.Locus.ValueStart:d.Locus.ValueEnd]); slice != d.CurrentValue {
					t.Errorf("%s/%s: value locus points at %q, not CurrentValue %q", pf.PackageFile, d.DepName, slice, d.CurrentValue)
				}
			}
			if d.CurrentDigest != "" {
				if d.Locus.DigestStart < 0 || d.Locus.DigestEnd > len(src) || d.Locus.DigestStart > d.Locus.DigestEnd {
					t.Errorf("%s/%s: digest locus %+v out of range", pf.PackageFile, d.DepName, d.Locus)
				} else if slice := string(src[d.Locus.DigestStart:d.Locus.DigestEnd]); slice != d.CurrentDigest {
					t.Errorf("%s/%s: digest locus points at %q, not CurrentDigest %q", pf.PackageFile, d.DepName, slice, d.CurrentDigest)
				}
			}
		}
	}

	var missing, invented []string
	agreed := 0
	for k, wantN := range want {
		gotN := got[k]
		if gotN < wantN {
			missing = append(missing, fmt.Sprintf("%s (want %d, got %d)", k, wantN, gotN))
			agreed += gotN
		} else {
			agreed += wantN
		}
	}
	for k, gotN := range got {
		if wantN := want[k]; gotN > wantN {
			invented = append(invented, fmt.Sprintf("%s (got %d, want %d)", k, gotN, wantN))
		}
	}
	sort.Strings(missing)
	sort.Strings(invented)

	if len(missing) > 0 {
		t.Errorf("missing dependencies the corpus recorded:\n%s", strings.Join(missing, "\n"))
	}
	if len(invented) > 0 {
		t.Errorf("invented dependencies the corpus did not record:\n%s", strings.Join(invented, "\n"))
	}
	t.Logf("agreed on %d of %d", agreed, total)
}
