// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gomod

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

func extractAll(t *testing.T, path, src string) (extract.File, []model.Dependency) {
	t.Helper()
	f := extract.File{Path: path, Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
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

// assertLocus checks the contract every dependency with a CurrentValue must
// satisfy: the Locus brackets exactly those bytes, quotes are never part of
// it, and no digest group is ever set - this manager never reports one.
func assertLocus(t *testing.T, f extract.File, d model.Dependency) {
	t.Helper()
	if d.Locus.ValueStart < 0 || d.Locus.ValueEnd > len(f.Content) || d.Locus.ValueStart > d.Locus.ValueEnd {
		t.Fatalf("%s/%s: locus [%d:%d] out of range for a %d-byte file", d.DepName, d.DepType, d.Locus.ValueStart, d.Locus.ValueEnd, len(f.Content))
	}
	if got := string(f.Content[d.Locus.ValueStart:d.Locus.ValueEnd]); got != d.CurrentValue {
		t.Errorf("%s/%s: bytes at locus = %q, want CurrentValue %q", d.DepName, d.DepType, got, d.CurrentValue)
	}
	if d.Locus.DigestStart != model.NoDigest || d.Locus.DigestEnd != model.NoDigest {
		t.Errorf("%s/%s: digest locus set on a manager with no digests", d.DepName, d.DepType)
	}
}

// synthetic exercises every shape the package comment documents, plus
// comments and blank lines, in one file with plain LF endings.
// TestCRLFGoMod below repeats the require/go-directive shape with CRLF line
// endings.
const synthetic = `module git.ole-hartwig.eu/pinup/synthetic

go 1.27.0

toolchain go1.27.1

require (
	go.etcd.io/bbolt v1.5.0
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/sys v0.45.0 // indirect

require github.com/aws/aws-sdk-go-v2 v1.43.8

replace github.com/aws/aws-sdk-go-v2 => github.com/aws/aws-sdk-go-v2 v1.43.7

exclude golang.org/x/text v0.3.0

tool golang.org/x/tools/cmd/stringer
`

func TestSyntheticGoMod(t *testing.T) {
	f, deps := extractAll(t, "go.mod", synthetic)
	if len(deps) != 7 {
		t.Fatalf("got %d dependencies, want 7: %+v", len(deps), deps)
	}

	goDep := depNamed(t, deps, "go", "golang")
	if goDep.CurrentValue != "1.27.0" || goDep.Datasource != "golang-version" || goDep.Versioning != "go-mod-directive" || goDep.SkipReason != "" {
		t.Errorf("go directive = %+v, want the measured shape", goDep)
	}
	assertLocus(t, f, goDep)

	tc := depNamed(t, deps, "go", "toolchain")
	if tc.CurrentValue != "1.27.1" || tc.Datasource != "golang-version" || tc.Versioning != "" {
		t.Errorf("toolchain directive = %+v, want currentValue 1.27.1, datasource golang-version, no versioning", tc)
	}
	assertLocus(t, f, tc)
	if got := string(f.Content[tc.Locus.ValueStart-2 : tc.Locus.ValueStart]); got != "go" {
		t.Errorf("toolchain locus starts at %q, want it to sit right after the literal \"go\"", got)
	}

	bolt := depNamed(t, deps, "go.etcd.io/bbolt", "require")
	if bolt.CurrentValue != "v1.5.0" || bolt.PackageName != "go.etcd.io/bbolt" || bolt.Datasource != "go" || bolt.SkipReason != "" {
		t.Errorf("bbolt require = %+v", bolt)
	}
	assertLocus(t, f, bolt)

	yaml := depNamed(t, deps, "gopkg.in/yaml.v3", "require")
	if yaml.CurrentValue != "v3.0.1" {
		t.Errorf("yaml require = %+v", yaml)
	}
	assertLocus(t, f, yaml)

	sys := depNamed(t, deps, "golang.org/x/sys", "indirect")
	if sys.CurrentValue != "v0.45.0" || sys.Disabled != "indirect dependency" || sys.SkipReason != "" || sys.Datasource != "go" {
		t.Errorf("sys indirect require = %+v, want it disabled, not skipped", sys)
	}
	assertLocus(t, f, sys)

	awsReq := depNamed(t, deps, "github.com/aws/aws-sdk-go-v2", "require")
	if awsReq.CurrentValue != "v1.43.8" {
		t.Errorf("aws-sdk-go-v2 require = %+v", awsReq)
	}
	assertLocus(t, f, awsReq)

	awsRepl := depNamed(t, deps, "github.com/aws/aws-sdk-go-v2", "replace")
	if awsRepl.CurrentValue != "v1.43.7" || awsRepl.PackageName != "github.com/aws/aws-sdk-go-v2" {
		t.Errorf("aws-sdk-go-v2 replace = %+v, want the right-hand version", awsRepl)
	}
	assertLocus(t, f, awsRepl)

	for _, name := range []string{"golang.org/x/text", "golang.org/x/tools/cmd/stringer", "git.ole-hartwig.eu/pinup/synthetic"} {
		for _, d := range deps {
			if d.DepName == name {
				t.Errorf("exclude/tool/module line produced a dependency: %+v", d)
			}
		}
	}
}

// TestReplaceToALocalPathYieldsNothing: unmeasured (not in the corpus), but
// the natural reading of Go's own module grammar - a version-less right-hand
// side is only legal for a local filesystem path.
func TestReplaceToALocalPathYieldsNothing(t *testing.T) {
	const src = "module m\n\ngo 1.27\n\nreplace example.com/a => ../local\n"
	_, deps := extractAll(t, "go.mod", src)
	for _, d := range deps {
		if d.DepType == "replace" {
			t.Errorf("a local replace produced a dependency: %+v", d)
		}
	}
}

// TestCRLFGoMod repeats the require/go-directive shape with CRLF line
// endings, which must not shift a single byte of any Locus.
func TestCRLFGoMod(t *testing.T) {
	src := strings.ReplaceAll(`module m

go 1.27.0

require (
	example.com/a v1.0.0
)
`, "\n", "\r\n")

	f, deps := extractAll(t, "go.mod", src)
	if len(deps) != 2 {
		t.Fatalf("got %d dependencies, want 2: %+v", len(deps), deps)
	}
	assertLocus(t, f, depNamed(t, deps, "go", "golang"))
	assertLocus(t, f, depNamed(t, deps, "example.com/a", "require"))
}

func TestEditReplacesExactlyTheRequireVersionBytes(t *testing.T) {
	f, deps := extractAll(t, "go.mod", synthetic)
	dep := depNamed(t, deps, "go.etcd.io/bbolt", "require")

	up := model.Update{Dep: dep, NewValue: "v1.6.0"}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	if edit.Start != dep.Locus.ValueStart || edit.End != dep.Locus.ValueEnd {
		t.Fatalf("edit range = [%d:%d], want [%d:%d]", edit.Start, edit.End, dep.Locus.ValueStart, dep.Locus.ValueEnd)
	}
	if edit.Old != "v1.5.0" || edit.New != "v1.6.0" {
		t.Fatalf("edit = %+v, want Old=v1.5.0 New=v1.6.0", edit)
	}

	rewritten := string(f.Content[:edit.Start]) + edit.New + string(f.Content[edit.End:])
	_, rewrittenDeps := extractAll(t, f.Path, rewritten)
	if got := depNamed(t, rewrittenDeps, "go.etcd.io/bbolt", "require").CurrentValue; got != "v1.6.0" {
		t.Errorf("after applying the edit, bbolt = %q, want v1.6.0", got)
	}
	// Every other dependency must be untouched.
	for _, tc := range []struct{ name, depType string }{
		{"go", "golang"}, {"go", "toolchain"}, {"gopkg.in/yaml.v3", "require"},
		{"golang.org/x/sys", "indirect"}, {"github.com/aws/aws-sdk-go-v2", "require"},
		{"github.com/aws/aws-sdk-go-v2", "replace"},
	} {
		before := depNamed(t, deps, tc.name, tc.depType)
		after := depNamed(t, rewrittenDeps, tc.name, tc.depType)
		if before.CurrentValue != after.CurrentValue {
			t.Errorf("%s/%s changed from %q to %q; the edit touched bytes it should not have", tc.name, tc.depType, before.CurrentValue, after.CurrentValue)
		}
	}
}

func TestEditReplacesExactlyTheGoDirectiveBytes(t *testing.T) {
	f, deps := extractAll(t, "go.mod", synthetic)
	dep := depNamed(t, deps, "go", "golang")

	up := model.Update{Dep: dep, NewValue: "1.28.0"}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	rewritten := string(f.Content[:edit.Start]) + edit.New + string(f.Content[edit.End:])
	if !strings.Contains(rewritten, "go 1.28.0\n") {
		t.Errorf("rewritten file does not contain the new go directive:\n%s", rewritten)
	}
}

// TestEditReplacesOnlyTheToolchainNumber checks the one Locus this manager
// must get exactly right: the "go" literal in "toolchain go1.27.1" is never
// part of the edit, only the version after it.
func TestEditReplacesOnlyTheToolchainNumber(t *testing.T) {
	f, deps := extractAll(t, "go.mod", synthetic)
	dep := depNamed(t, deps, "go", "toolchain")

	up := model.Update{Dep: dep, NewValue: "1.28.2"}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	rewritten := string(f.Content[:edit.Start]) + edit.New + string(f.Content[edit.End:])
	if !strings.Contains(rewritten, "toolchain go1.28.2\n") {
		t.Errorf("rewritten file does not contain the new toolchain directive:\n%s", rewritten)
	}
}

func TestEditRefusesAChangedFile(t *testing.T) {
	f, deps := extractAll(t, "go.mod", synthetic)
	dep := depNamed(t, deps, "go.etcd.io/bbolt", "require")

	mutated := make([]byte, len(f.Content))
	copy(mutated, f.Content)
	mutated[dep.Locus.ValueStart] = 'X'
	mutatedFile := extract.File{Path: f.Path, Content: mutated}

	up := model.Update{Dep: dep, NewValue: "v1.6.0"}
	if _, err := (&Manager{}).Edit(context.Background(), mutatedFile, up); err == nil {
		t.Fatal("Edit did not refuse a file that changed since extraction")
	}
}

// depKey is the tuple the corpus comparison below is made on: packageFile,
// depName, depType, datasource, currentValue, versioning, and whether the
// dependency is skipped - which stands in for the corpus's "enabled: false",
// the only field in the task's comparison list that this manager expresses
// differently (via SkipReason) than Renovate's own recording does (via
// enabled). managerData and commitMessageTopic are deliberately not part of
// this tuple, per the corpus's own instructions.
type depKey struct {
	packageFile, depName, depType, datasource, currentValue, versioning string
	skipped                                                             bool
}

func (k depKey) String() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|skipped=%v",
		k.packageFile, k.depName, k.depType, k.datasource, k.currentValue, k.versioning, k.skipped)
}

// TestAgreesWithTheCorpus is the acceptance test: this manager's extraction
// of the synthetic tree's two go.mod files must agree with the Renovate
// corpus recording (testdata/parity/renovate-43.288.0/extract/gomod.json,
// key "gomod") on all 10 dependencies.
func TestAgreesWithTheCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/parity/renovate-43.288.0/extract/gomod.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Gomod []struct {
			PackageFile string `json:"packageFile"`
			Deps        []struct {
				DepName      string `json:"depName"`
				DepType      string `json:"depType"`
				Datasource   string `json:"datasource"`
				CurrentValue string `json:"currentValue"`
				Versioning   string `json:"versioning"`
				Enabled      *bool  `json:"enabled"`
			} `json:"deps"`
		} `json:"gomod"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("corpus fixture does not parse: %v", err)
	}

	want := make(map[string]int)
	total := 0
	for _, pf := range corpus.Gomod {
		for _, d := range pf.Deps {
			k := depKey{
				packageFile: pf.PackageFile, depName: d.DepName, depType: d.DepType,
				datasource: d.Datasource, currentValue: d.CurrentValue, versioning: d.Versioning,
				skipped: d.Enabled != nil && !*d.Enabled,
			}
			want[k.String()]++
			total++
		}
	}
	if total != 10 {
		t.Fatalf("corpus fixture has %d gomod dependencies, want 10 - the fixture itself changed", total)
	}

	got := make(map[string]int)
	for _, pf := range corpus.Gomod {
		content, err := os.ReadFile(filepath.Join("../../testdata/parity/synthetic/gomod", pf.PackageFile))
		if err != nil {
			t.Fatal(err)
		}
		f := extract.File{Path: pf.PackageFile, Content: content}
		res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
		if err != nil {
			t.Fatalf("Extract(%s) returned an error: %v", pf.PackageFile, err)
		}
		for _, d := range res.Deps {
			k := depKey{
				packageFile: pf.PackageFile, depName: d.DepName, depType: d.DepType,
				datasource: d.Datasource, currentValue: d.CurrentValue, versioning: d.Versioning,
				// Renovate's enabled:false is pinup's Disabled: recorded,
				// still asked about advisories, not updated on its own.
				skipped: d.Disabled != "",
			}
			got[k.String()]++
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
	t.Logf("agreed with the corpus on %d of %d dependencies", agreed, total)
	if agreed != total {
		t.Errorf("agreed on %d of %d, want all %d", agreed, total, total)
	}
}

// A pseudo-version's commit is the dependency's digest, the locus stays on
// the whole value, and every module requirement names go.sum as its lock.
func TestPseudoVersionsCarryTheirCommitAndModulesNameGoSum(t *testing.T) {
	src := "module m\n\ngo 1.27.0\n\nrequire golang.org/x/mobile v0.0.0-20260821190718-4776eadac327\n"
	res, err := (&Manager{}).Extract(context.Background(), extract.File{Path: "go.mod", Content: []byte(src)}, extract.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.LockFiles) != 1 || res.LockFiles[0] != "go.sum" {
		t.Errorf("result lock files = %v, want go.sum", res.LockFiles)
	}
	mobile := depNamed(t, res.Deps, "golang.org/x/mobile", "require")
	if mobile.CurrentDigest != "4776eadac327" {
		t.Errorf("currentDigest = %q, want the pseudo-version's commit", mobile.CurrentDigest)
	}
	if mobile.Locus.DigestStart != model.NoDigest || string(src[mobile.Locus.ValueStart:mobile.Locus.ValueEnd]) != mobile.CurrentValue {
		t.Errorf("locus = %+v, want the whole value and no digest span", mobile.Locus)
	}
	if len(mobile.LockFiles) != 1 || mobile.LockFiles[0] != "go.sum" {
		t.Errorf("lock files = %v, want go.sum", mobile.LockFiles)
	}
	goDirective := depNamed(t, res.Deps, "go", "golang")
	if len(goDirective.LockFiles) != 0 {
		t.Errorf("the go directive names lock files %v, want none", goDirective.LockFiles)
	}
	if locked, err := LockedVersions([]byte("a/b v1.0.0 h1:x=\n")); err != nil || locked == nil || len(locked) != 0 {
		t.Errorf("LockedVersions = %v, %v; want an empty, non-nil map", locked, err)
	}
}
