// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package npmman

import (
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"slices"
	"sort"
	"testing"

	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// depNamed finds the one dependency with the given DepName, or fails the
// test - most of these tests care about one specific entry in a multi-entry
// fixture, not the whole slice.
func depNamed(t *testing.T, deps []model.Dependency, depName string) model.Dependency {
	t.Helper()
	var matches []model.Dependency
	for _, d := range deps {
		if d.DepName == depName {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("found %d dependencies named %q, want exactly 1 (deps: %+v)", len(matches), depName, deps)
	}
	return matches[0]
}

func extractAll(t *testing.T, src string) (extract.File, []model.Dependency) {
	t.Helper()
	f := extract.File{Path: "package.json", Content: []byte(src)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	return f, res.Deps
}

// synthetic exercises all four dependency sections, a scoped name, a "file:"
// reference, a bare wildcard, and a key order where devDependencies comes
// before dependencies - the order the estate's own template-2024 checkout
// happens to use, and a case Renovate's manager does not care about either.
const synthetic = `{
  "name": "synthetic-fixture",
  "version": "0.0.0",
  "devDependencies": {
    "eslint": "^8.55.0",
    "@moselwal/tooling": "~2.3.4"
  },
  "dependencies": {
    "left-pad": "*",
    "local-thing": "file:../local-thing",
    "lodash": "^4.17.21"
  },
  "optionalDependencies": {
    "fsevents": "^2.3.3"
  },
  "peerDependencies": {
    "react": "1.x"
  },
  "scripts": {
    "build": "webpack --mode=production"
  }
}
`

func TestSyntheticPackageJSON(t *testing.T) {
	f, deps := extractAll(t, synthetic)

	if len(deps) != 7 {
		t.Fatalf("got %d dependencies, want 7: %+v", len(deps), deps)
	}

	for _, tc := range []struct {
		depName    string
		depType    string
		want       string
		skipReason string
	}{
		{depName: "eslint", depType: "devDependencies", want: "^8.55.0"},
		{depName: "@moselwal/tooling", depType: "devDependencies", want: "~2.3.4"},
		{depName: "left-pad", depType: "dependencies", want: "*"},
		{depName: "local-thing", depType: "dependencies", want: "file:../local-thing", skipReason: "file reference is not a registry version"},
		{depName: "lodash", depType: "dependencies", want: "^4.17.21"},
		{depName: "fsevents", depType: "optionalDependencies", want: "^2.3.3"},
		{depName: "react", depType: "peerDependencies", want: "1.x"},
	} {
		d := depNamed(t, deps, tc.depName)
		if d.DepType != tc.depType {
			t.Errorf("%s: DepType = %q, want %q", tc.depName, d.DepType, tc.depType)
		}
		if d.CurrentValue != tc.want {
			t.Errorf("%s: CurrentValue = %q, want %q", tc.depName, d.CurrentValue, tc.want)
		}
		if d.Datasource != "npm" {
			t.Errorf("%s: Datasource = %q, want %q", tc.depName, d.Datasource, "npm")
		}
		if d.SkipReason != tc.skipReason {
			t.Errorf("%s: SkipReason = %q, want %q", tc.depName, d.SkipReason, tc.skipReason)
		}
		if !slices.Equal(d.LockFiles, []string{"package-lock.json"}) {
			t.Errorf("%s: LockFiles = %v, want [package-lock.json]", tc.depName, d.LockFiles)
		}

		// The Locus is the contract: it must bracket exactly CurrentValue,
		// with no surrounding quotes, in every case - skipped or not.
		if d.Locus.ValueStart < 0 || d.Locus.ValueEnd > len(f.Content) {
			t.Fatalf("%s: locus [%d:%d] out of range for a %d-byte file", tc.depName, d.Locus.ValueStart, d.Locus.ValueEnd, len(f.Content))
		}
		got := string(f.Content[d.Locus.ValueStart:d.Locus.ValueEnd])
		if got != tc.want {
			t.Errorf("%s: bytes at locus = %q, want %q", tc.depName, got, tc.want)
		}
		if d.Locus.DigestStart != model.NoDigest || d.Locus.DigestEnd != model.NoDigest {
			t.Errorf("%s: digest locus set on a manager with no digests", tc.depName)
		}
	}

	if res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{}); err != nil || len(res.LockFiles) != 1 || res.LockFiles[0] != "package-lock.json" {
		t.Errorf("Result.LockFiles = %v, err = %v, want [package-lock.json]", res.LockFiles, err)
	}
}

func TestEditReplacesExactlyTheValueBytes(t *testing.T) {
	f, deps := extractAll(t, synthetic)
	dep := depNamed(t, deps, "lodash")

	up := model.Update{Dep: dep, NewValue: "^4.18.0"}
	edit, err := (&Manager{}).Edit(context.Background(), f, up)
	if err != nil {
		t.Fatalf("Edit returned an error: %v", err)
	}
	if edit.Start != dep.Locus.ValueStart || edit.End != dep.Locus.ValueEnd {
		t.Fatalf("edit range = [%d:%d], want [%d:%d]", edit.Start, edit.End, dep.Locus.ValueStart, dep.Locus.ValueEnd)
	}
	if edit.Old != "^4.17.21" || edit.New != "^4.18.0" {
		t.Fatalf("edit = %+v, want Old=^4.17.21 New=^4.18.0", edit)
	}

	// Applying the edit must touch nothing outside [Start:End).
	rewritten := string(f.Content[:edit.Start]) + edit.New + string(f.Content[edit.End:])
	rewrittenFile := extract.File{Path: f.Path, Content: []byte(rewritten)}
	_, rewrittenDeps := extractAll(t, string(rewrittenFile.Content))
	if got := depNamed(t, rewrittenDeps, "lodash").CurrentValue; got != "^4.18.0" {
		t.Errorf("after applying the edit, lodash = %q, want ^4.18.0", got)
	}
	// Every other dependency must be untouched.
	for _, name := range []string{"eslint", "@moselwal/tooling", "left-pad", "local-thing", "fsevents", "react"} {
		before := depNamed(t, deps, name)
		after := depNamed(t, rewrittenDeps, name)
		if before.CurrentValue != after.CurrentValue {
			t.Errorf("%s changed from %q to %q; the edit touched bytes it should not have", name, before.CurrentValue, after.CurrentValue)
		}
	}
}

func TestEditRefusesAChangedFile(t *testing.T) {
	f, deps := extractAll(t, synthetic)
	dep := depNamed(t, deps, "lodash")

	// Mutate the file underneath the recorded Locus without re-extracting -
	// simulating a repository that moved on between plan and apply.
	mutated := make([]byte, len(f.Content))
	copy(mutated, f.Content)
	mutated[dep.Locus.ValueStart] = 'X'
	mutatedFile := extract.File{Path: f.Path, Content: mutated}

	up := model.Update{Dep: dep, NewValue: "^4.18.0"}
	if _, err := (&Manager{}).Edit(context.Background(), mutatedFile, up); err == nil {
		t.Fatal("Edit did not refuse a file that changed since extraction")
	}
}

func TestNonObjectPackageJSONYieldsAWarningNotAnError(t *testing.T) {
	f := extract.File{Path: "package.json", Content: []byte(`["not", "an", "object"]`)}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract returned an error for a malformed file: %v", err)
	}
	if len(res.Deps) != 0 {
		t.Errorf("got %d dependencies from a non-object file, want 0", len(res.Deps))
	}
	if len(res.Warnings) != 1 {
		t.Errorf("got %d warnings, want 1", len(res.Warnings))
	}
}

// TestAgainstRealTemplate2024Checkout compares this manager's extraction of
// the estate's own template-2024 package.json against the Renovate corpus
// recording for the same file (testdata/parity/renovate-43.288.0/extract,
// key "npm"). It is skipped, not failed, when either side is unavailable -
// the checkout is a local mirror this repository does not own.
//
// The corpus was captured against the checkout's committed HEAD, not
// whatever happens to be in the working tree - this local mirror carries
// uncommitted work (an eslint flat-config migration, at the time this test
// was written) that the corpus knows nothing about. `git show HEAD:` is what
// gets back the exact bytes Renovate saw.
func TestAgainstRealTemplate2024Checkout(t *testing.T) {
	const repoDir = "/Volumes/Samsung_X5/Projects/moselwal/development/moselwal/template-2024"
	if _, err := os.Stat(repoDir); err != nil {
		t.Skipf("real checkout not available: %v", err)
	}
	src, err := exec.Command("git", "-C", repoDir, "show", "HEAD:package.json").Output()
	if err != nil {
		t.Skipf("could not read package.json from the checkout's HEAD commit: %v", err)
	}

	corpusBytes, err := os.ReadFile("../../testdata/parity/renovate-43.288.0/extract/template-2024.json")
	if err != nil {
		t.Skipf("corpus fixture not available: %v", err)
	}

	var fixture struct {
		Npm []struct {
			PackageFile string `json:"packageFile"`
			Deps        []struct {
				DepName      string `json:"depName"`
				CurrentValue string `json:"currentValue"`
				DepType      string `json:"depType"`
			} `json:"deps"`
		} `json:"npm"`
	}
	if err := json.Unmarshal(corpusBytes, &fixture); err != nil {
		t.Fatalf("corpus fixture does not parse: %v", err)
	}
	if len(fixture.Npm) != 1 {
		t.Fatalf("expected exactly one npm packageFile entry in the corpus, got %d", len(fixture.Npm))
	}
	if fixture.Npm[0].PackageFile != "package.json" {
		t.Fatalf("corpus packageFile = %q, want %q", fixture.Npm[0].PackageFile, "package.json")
	}

	want := make(map[string]bool, len(fixture.Npm[0].Deps))
	for _, d := range fixture.Npm[0].Deps {
		want[d.DepName+"|"+d.CurrentValue+"|"+d.DepType] = true
	}

	f := extract.File{Path: "package.json", Content: src}
	res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}

	got := make(map[string]bool, len(res.Deps))
	for _, d := range res.Deps {
		got[d.DepName+"|"+d.CurrentValue+"|"+d.DepType] = true
	}

	var missing, invented []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			invented = append(invented, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(invented)

	if len(missing) > 0 {
		t.Errorf("missing %d dependencies the corpus recorded: %v", len(missing), missing)
	}
	if len(invented) > 0 {
		t.Errorf("invented %d dependencies the corpus did not record: %v", len(invented), invented)
	}
	agreed := len(want) - len(missing)
	t.Logf("agreed with the corpus on %d of %d dependencies (%d invented)", agreed, len(want), len(invented))
}

func TestLockedVersionsV3Lock(t *testing.T) {
	const lock = `{
  "name": "example",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "example", "version": "1.0.0"},
    "node_modules/lodash": {"version": "4.17.21"},
    "node_modules/@moselwal/tooling": {"version": "2.3.4"},
    "node_modules/eslint": {"version": "8.57.0"},
    "node_modules/eslint/node_modules/globals": {"version": "13.24.0"}
  }
}`
	got, err := LockedVersions([]byte(lock))
	if err != nil {
		t.Fatalf("LockedVersions returned an error: %v", err)
	}
	want := map[string]string{
		"lodash":            "4.17.21",
		"@moselwal/tooling": "2.3.4",
		"eslint":            "8.57.0",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for name, version := range want {
		if got[name] != version {
			t.Errorf("%s = %q, want %q", name, got[name], version)
		}
	}
	if v, ok := got["globals"]; ok {
		t.Errorf("globals nested under eslint's own node_modules leaked into the result as %q", v)
	}
}

func TestLockedVersionsV1Lock(t *testing.T) {
	const lock = `{
  "name": "example",
  "version": "1.0.0",
  "lockfileVersion": 1,
  "requires": true,
  "dependencies": {
    "lodash": {"version": "4.17.21", "requires": {}},
    "eslint": {"version": "8.57.0", "dependencies": {"globals": {"version": "13.24.0"}}}
  }
}`
	got, err := LockedVersions([]byte(lock))
	if err != nil {
		t.Fatalf("LockedVersions returned an error: %v", err)
	}
	want := map[string]string{"lodash": "4.17.21", "eslint": "8.57.0"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for name, version := range want {
		if got[name] != version {
			t.Errorf("%s = %q, want %q", name, got[name], version)
		}
	}
	if v, ok := got["globals"]; ok {
		t.Errorf("globals nested under eslint's v1 entry leaked into the result as %q", v)
	}
}
