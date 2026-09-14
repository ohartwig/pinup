// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package terraform

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
)

// depNamed finds the one dependency with the given (DepName, DepType) pair,
// or fails the test - several fixtures below repeat a depName across
// depTypes (a required_provider and a legacy provider block for the same
// provider), so DepName alone would not be unique.
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

func extractAll(t *testing.T, path, src string) (extract.File, []model.Dependency) {
	t.Helper()
	f := extract.File{Path: path, Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	return f, res.Deps
}

// assertLocus checks the contract every dependency with a CurrentValue must
// satisfy: the Locus brackets exactly those bytes, quotes excluded, and no
// digest group is ever set - this manager never reports one.
func assertLocus(t *testing.T, f extract.File, d model.Dependency) {
	t.Helper()
	if d.Locus.ValueStart < 0 || d.Locus.ValueEnd > len(f.Content) || d.Locus.ValueStart > d.Locus.ValueEnd {
		t.Fatalf("%s/%s: locus [%d:%d] out of range for a %d-byte file", d.DepName, d.DepType, d.Locus.ValueStart, d.Locus.ValueEnd, len(f.Content))
	}
	got := string(f.Content[d.Locus.ValueStart:d.Locus.ValueEnd])
	if got != d.CurrentValue {
		t.Errorf("%s/%s: bytes at locus = %q, want CurrentValue %q", d.DepName, d.DepType, got, d.CurrentValue)
	}
	if d.Locus.DigestStart != model.NoDigest || d.Locus.DigestEnd != model.NoDigest {
		t.Errorf("%s/%s: digest locus set on a manager with no digests", d.DepName, d.DepType)
	}
}

// synthetic exercises every shape terraform.go's package comment documents,
// plus comments between attributes and a /* */ block, in one file with plain
// LF endings. TestCRLFVersionsFile below repeats the required_providers shape
// with CRLF line endings.
const synthetic = `# Root module.
terraform {
  required_version = ">= 1.6" // pinned to the estate's minimum

  required_providers {
    /* the registry-sourced provider */
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = ">= 5.22"
    }
    # a provider with no source: falls back to hashicorp/NAME
    archive = {
      version = ">= 2.7"
    }
    # a provider with no version at all
    tls = {
      source = "hashicorp/tls"
    }
  }
}

provider "aws" {
  region = var.region
}

provider "cloudflare" {}

module "network" {
  source = "./network"
}

module "web_green" {
  count  = var.enable_web_green ? 1 : 0
  source = "./web-green"

  systems = ["a", "b"]
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.8.1"
}

module "remote" {
  source = "git::https://example.com/vpc.git"
}
`

func TestSyntheticTerraformFile(t *testing.T) {
	f, deps := extractAll(t, "versions.tf", synthetic)

	if len(deps) != 10 {
		t.Fatalf("got %d dependencies, want 10: %+v", len(deps), deps)
	}

	rv := depNamed(t, deps, "hashicorp/terraform", "required_version")
	if rv.CurrentValue != ">= 1.6" || rv.Datasource != "github-releases" ||
		rv.Versioning != "hashicorp" || rv.SkipReason != "" ||
		rv.ExtractVersion != `v(?<version>.*)$` {
		t.Errorf("required_version dependency = %+v, want the measured shape", rv)
	}
	assertLocus(t, f, rv)

	cf := depNamed(t, deps, "cloudflare", "required_provider")
	if cf.CurrentValue != ">= 5.22" || cf.PackageName != "cloudflare/cloudflare" ||
		cf.Datasource != "terraform-provider" || cf.SkipReason != "" {
		t.Errorf("cloudflare required_provider = %+v", cf)
	}
	if len(cf.RegistryURLs) != 1 || cf.RegistryURLs[0] != openTofuRegistry {
		t.Errorf("cloudflare registryUrls = %v, want [%s]", cf.RegistryURLs, openTofuRegistry)
	}
	if len(cf.LockFiles) != 1 || cf.LockFiles[0] != lockFileName {
		t.Errorf("cloudflare lockFiles = %v, want [%s]", cf.LockFiles, lockFileName)
	}
	assertLocus(t, f, cf)

	archive := depNamed(t, deps, "archive", "required_provider")
	if archive.PackageName != "hashicorp/archive" || archive.CurrentValue != ">= 2.7" {
		t.Errorf("archive required_provider = %+v, want packageName hashicorp/archive (no source given)", archive)
	}
	assertLocus(t, f, archive)

	tls := depNamed(t, deps, "tls", "required_provider")
	if tls.PackageName != "hashicorp/tls" || tls.SkipReason != "unspecified-version" || tls.CurrentValue != "" {
		t.Errorf("tls required_provider = %+v, want an unspecified-version skip", tls)
	}
	if tls.Locus.ValueStart != tls.Locus.ValueEnd {
		t.Errorf("tls required_provider locus = %+v, want a zero-width point (no version to bracket)", tls.Locus)
	}

	awsProvider := depNamed(t, deps, "aws", "provider")
	if awsProvider.PackageName != "hashicorp/aws" || awsProvider.SkipReason != "unspecified-version" {
		t.Errorf("aws provider block = %+v, want the measured unspecified-version shape", awsProvider)
	}

	cfProvider := depNamed(t, deps, "cloudflare", "provider")
	if cfProvider.PackageName != "hashicorp/cloudflare" || cfProvider.SkipReason != "unspecified-version" {
		t.Errorf("cloudflare provider block = %+v, want packageName hashicorp/cloudflare, not cloudflare/cloudflare", cfProvider)
	}

	network := depNamed(t, deps, "network", "module")
	if network.SkipReason != "local" || network.Datasource != "" || network.CurrentValue != "" {
		t.Errorf("network module = %+v, want a local skip with no datasource", network)
	}

	webGreen := depNamed(t, deps, "web_green", "module")
	if webGreen.SkipReason != "local" {
		t.Errorf("web_green module = %+v, want a local skip (count meta-argument must not confuse the scanner)", webGreen)
	}

	vpc := depNamed(t, deps, "vpc", "module")
	if vpc.Datasource != "terraform-module" || vpc.PackageName != "terraform-aws-modules/vpc/aws" || vpc.CurrentValue != "5.8.1" || vpc.SkipReason != "" {
		t.Errorf("vpc registry module = %+v", vpc)
	}
	assertLocus(t, f, vpc)

	remote := depNamed(t, deps, "remote", "module")
	if remote.SkipReason != "unsupported-source" {
		t.Errorf("remote module = %+v, want skipReason unsupported-source", remote)
	}
}

// TestCRLFVersionsFile repeats the required_providers/required_version shape
// with CRLF line endings, which must not shift a single byte of any Locus:
// the file is never normalized before its offsets are reported.
func TestCRLFVersionsFile(t *testing.T) {
	src := strings.ReplaceAll(`terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.40"
    }
  }
}
`, "\n", "\r\n")

	f, deps := extractAll(t, "versions.tf", src)
	if len(deps) != 2 {
		t.Fatalf("got %d dependencies, want 2: %+v", len(deps), deps)
	}
	aws := depNamed(t, deps, "aws", "required_provider")
	if aws.CurrentValue != ">= 5.40" {
		t.Fatalf("aws.CurrentValue = %q, want >= 5.40", aws.CurrentValue)
	}
	assertLocus(t, f, aws)

	rv := depNamed(t, deps, "hashicorp/terraform", "required_version")
	assertLocus(t, f, rv)
}

func TestEditReplacesExactlyTheValueBytes(t *testing.T) {
	f, deps := extractAll(t, "versions.tf", synthetic)
	dep := depNamed(t, deps, "cloudflare", "required_provider")

	up := model.Update{Dep: dep, NewValue: ">= 5.0"}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	if edit.Start != dep.Locus.ValueStart || edit.End != dep.Locus.ValueEnd {
		t.Fatalf("edit range = [%d:%d], want [%d:%d]", edit.Start, edit.End, dep.Locus.ValueStart, dep.Locus.ValueEnd)
	}
	if edit.Old != ">= 5.22" || edit.New != ">= 5.0" {
		t.Fatalf("edit = %+v, want Old=>= 5.22 New=>= 5.0", edit)
	}

	rewritten := string(f.Content[:edit.Start]) + edit.New + string(f.Content[edit.End:])
	_, rewrittenDeps := extractAll(t, f.Path, rewritten)
	if got := depNamed(t, rewrittenDeps, "cloudflare", "required_provider").CurrentValue; got != ">= 5.0" {
		t.Errorf("after applying the edit, cloudflare = %q, want >= 5.0", got)
	}
	// Every other dependency must be untouched.
	for _, tc := range []struct{ name, depType string }{
		{"hashicorp/terraform", "required_version"},
		{"archive", "required_provider"},
		{"tls", "required_provider"},
		{"aws", "provider"},
		{"network", "module"},
	} {
		before := depNamed(t, deps, tc.name, tc.depType)
		after := depNamed(t, rewrittenDeps, tc.name, tc.depType)
		if before.CurrentValue != after.CurrentValue {
			t.Errorf("%s/%s changed from %q to %q; the edit touched bytes it should not have", tc.name, tc.depType, before.CurrentValue, after.CurrentValue)
		}
	}
}

func TestEditRefusesAChangedFile(t *testing.T) {
	f, deps := extractAll(t, "versions.tf", synthetic)
	dep := depNamed(t, deps, "cloudflare", "required_provider")

	mutated := make([]byte, len(f.Content))
	copy(mutated, f.Content)
	mutated[dep.Locus.ValueStart] = 'X'
	mutatedFile := extract.File{Path: f.Path, Content: mutated}

	up := model.Update{Dep: dep, NewValue: ">= 5.0"}
	if _, err := (&Manager{}).Edit(context.Background(), mutatedFile, up); err == nil {
		t.Fatal("Edit did not refuse a file that changed since extraction")
	}
}

func TestEditRefusesASkippedDependencyWithNoLocus(t *testing.T) {
	f, deps := extractAll(t, "versions.tf", synthetic)
	dep := depNamed(t, deps, "network", "module")

	up := model.Update{Dep: dep, NewValue: "1.0.0"}
	if _, err := (&Manager{}).Edit(context.Background(), f, up); err == nil {
		t.Fatal("Edit did not refuse a dependency with no editable range")
	}
}

// depKey is the tuple this manager's own corpus comparison and Renovate's
// recording are compared on - everything the task description names except
// lockedVersion (Extract deliberately leaves LockedVersion empty; see
// stampLockFiles) and lockFiles (not part of Renovate's own recording).
type depKey struct {
	packageFile, depName, depType, datasource, currentValue, packageName, skipReason, registryURLs, extractVersion, versioning string
}

func (k depKey) String() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s|%s",
		k.packageFile, k.depName, k.depType, k.datasource, k.currentValue,
		k.packageName, k.skipReason, k.registryURLs, k.extractVersion, k.versioning)
}

// TestAgainstRealKohInfraCheckout compares this manager's extraction of the
// koh-infra checkout's 22 terraform packageFiles against the Renovate corpus
// recording for the same repository (testdata/<root>/renovate-43.288.0/extract,
// key "terraform"). It is skipped, not failed, when either side is
// unavailable - the checkout is a local mirror this repository does not own.
//
// The corpus was captured against the checkout's committed HEAD, not
// whatever happens to be in the working tree, so every file is read with
// `git show HEAD:<path>` - the same approach manager/npmman's real-checkout
// test uses.
func TestAgainstRealKohInfraCheckout(t *testing.T) {
	const repoDir = "/Volumes/Samsung_X5/Projects/koh-infra"
	if _, err := os.Stat(repoDir); err != nil {
		t.Skipf("real checkout not available: %v", err)
	}

	corpusBytes, err := os.ReadFile(fixture.Captured(t, "extract", "koh-infra.json"))
	if err != nil {
		t.Skipf("corpus fixture not available: %v", err)
	}

	var fixture struct {
		Terraform []struct {
			PackageFile string `json:"packageFile"`
			Deps        []struct {
				DepName        string   `json:"depName"`
				DepType        string   `json:"depType"`
				Datasource     string   `json:"datasource"`
				CurrentValue   string   `json:"currentValue"`
				PackageName    string   `json:"packageName"`
				SkipReason     string   `json:"skipReason"`
				RegistryURLs   []string `json:"registryUrls"`
				ExtractVersion string   `json:"extractVersion"`
				Versioning     string   `json:"versioning"`
			} `json:"deps"`
		} `json:"terraform"`
	}
	if err := json.Unmarshal(corpusBytes, &fixture); err != nil {
		t.Fatalf("corpus fixture does not parse: %v", err)
	}

	// Counts, not a set: versions.tf legitimately records the same tuple
	// twice (two `provider "aws" { ... }` blocks - the default and the
	// us_east_1 alias - carry no field this comparison distinguishes), so
	// deduplicating would silently drop one entry on both sides.
	want := make(map[string]int)
	total := 0
	for _, pf := range fixture.Terraform {
		for _, d := range pf.Deps {
			k := depKey{
				packageFile: pf.PackageFile, depName: d.DepName, depType: d.DepType,
				datasource: d.Datasource, currentValue: d.CurrentValue, packageName: d.PackageName,
				skipReason: d.SkipReason, registryURLs: strings.Join(d.RegistryURLs, ","),
				extractVersion: d.ExtractVersion, versioning: d.Versioning,
			}
			want[k.String()]++
			total++
		}
	}
	if total != 56 {
		t.Fatalf("corpus fixture has %d terraform dependencies, want 56 - the fixture itself changed", total)
	}

	got := make(map[string]int)
	skippedFiles := 0
	for _, pf := range fixture.Terraform {
		out, err := exec.Command("git", "-C", repoDir, "show", "HEAD:"+pf.PackageFile).Output()
		if err != nil {
			t.Logf("could not read %s from the checkout's HEAD commit: %v", pf.PackageFile, err)
			skippedFiles++
			continue
		}
		f := extract.File{Path: pf.PackageFile, Content: out}
		res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
		if err != nil {
			t.Fatalf("Extract(%s) returned an error: %v", pf.PackageFile, err)
		}
		for _, d := range res.Deps {
			k := depKey{
				packageFile: pf.PackageFile, depName: d.DepName, depType: d.DepType,
				datasource: d.Datasource, currentValue: d.CurrentValue, packageName: d.PackageName,
				skipReason: d.SkipReason, registryURLs: strings.Join(d.RegistryURLs, ","),
				extractVersion: d.ExtractVersion, versioning: d.Versioning,
			}
			got[k.String()]++
		}
	}
	if skippedFiles == len(fixture.Terraform) {
		t.Skip("none of the corpus's packageFiles could be read from the checkout")
	}

	var missing, invented []string
	agreed := 0
	for k, wantN := range want {
		gotN := got[k]
		switch {
		case gotN < wantN:
			missing = append(missing, fmt.Sprintf("%s (want %d, got %d)", k, wantN, gotN))
			agreed += gotN
		default:
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
	t.Logf("agreed with the corpus on %d of %d dependencies", agreed, total)
}

func TestLockedVersions(t *testing.T) {
	const lock = `# This file is maintained automatically by "tofu init".
provider "registry.opentofu.org/cloudflare/cloudflare" {
  version     = "5.24.0"
  constraints = ">= 5.22.0"
  hashes = [
    "h1:abc=",
  ]
}

provider "registry.opentofu.org/hashicorp/aws" {
  version     = "6.63.0"
  constraints = ">= 5.40.0, >= 6.55.0"
  hashes = [
    "h1:def=",
  ]
}
`
	got, err := LockedVersions([]byte(lock))
	if err != nil {
		t.Fatalf("LockedVersions returned an error: %v", err)
	}
	want := map[string]string{
		"cloudflare/cloudflare": "5.24.0",
		"hashicorp/aws":         "6.63.0",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
