// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package npmman

import (
	"context"
	"encoding/json/v2"
	"slices"
	"sort"
	"testing"

	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/model"
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
		if !slices.Equal(d.LockFiles, []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock"}) {
			t.Errorf("%s: LockFiles = %v, want npm's locks then yarn's", tc.depName, d.LockFiles)
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

	if res, err := (&Manager{}).Extract(context.Background(), f, extract.ManagerConfig{}); err != nil || len(res.LockFiles) != 3 || res.LockFiles[0] != "package-lock.json" || res.LockFiles[2] != "yarn.lock" {
		t.Errorf("Result.LockFiles = %v, err = %v, want npm's locks then yarn's", res.LockFiles, err)
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

// TestAgreesWithTheCorpus compares this manager's extraction of every
// package.json in the fixture root's corpus against what the pinned Renovate
// container extracted from the same file. A capture whose checkout is not on
// this machine is skipped; the public root's repositories are in the tree.
func TestAgreesWithTheCorpus(t *testing.T) {
	ran := 0
	for _, c := range fixture.Corpora(t) {
		for _, e := range c.Entries(t, "npm") {
			if c.Tree == "" {
				t.Logf("%s: checkout not present; %s skipped", c.Name, e.PackageFile)
				continue
			}
			ran++
			t.Run(c.Name+"/"+e.PackageFile, func(t *testing.T) {
				var deps []struct {
					DepName      string `json:"depName"`
					CurrentValue string `json:"currentValue"`
					DepType      string `json:"depType"`
				}
				if err := json.Unmarshal(e.Deps, &deps); err != nil {
					t.Fatal(err)
				}
				want := make(map[string]bool, len(deps))
				for _, d := range deps {
					want[d.DepName+"|"+d.CurrentValue+"|"+d.DepType] = true
				}

				f := extract.File{Path: e.PackageFile, Content: c.Read(t, e.PackageFile)}
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
				t.Logf("agreed with the corpus on %d of %d dependencies (%d invented)", len(want)-len(missing), len(want), len(invented))
			})
		}
	}
	if ran == 0 {
		t.Skip("no npm capture with its files on this machine")
	}
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

// A yarn.lock is read in both shapes: the classic v1 file the estate's blog
// carries, and berry's YAML. Every selector of an entry resolves to the
// entry's version; scoped names keep their scope.
func TestYarnLocksAreRead(t *testing.T) {
	classic := []byte(`# THIS IS AN AUTOGENERATED FILE. DO NOT EDIT THIS FILE DIRECTLY.
# yarn lockfile v1


"@babel/code-frame@^7.0.0", "@babel/code-frame@^7.10.4":
  version "7.29.7"
  resolved "https://registry.yarnpkg.com/@babel/code-frame/-/code-frame-7.29.7.tgz#f2fb"
  integrity sha512-x
  dependencies:
    js-tokens "^4.0.0"

datatables.net@^2.0.0:
  version "2.3.5"
  resolved "https://registry.yarnpkg.com/datatables.net/-/datatables.net-2.3.5.tgz#abcd"

js-tokens@^4.0.0:
  version "4.0.0"
`)
	got, err := LockedVersions(classic)
	if err != nil {
		t.Fatal(err)
	}
	if got["@babel/code-frame"] != "7.29.7" || got["datatables.net"] != "2.3.5" || got["js-tokens"] != "4.0.0" {
		t.Errorf("classic: %v", got)
	}
	if YarnLockKind(classic) != "classic" {
		t.Error("kind")
	}

	berry := []byte(`# This file is generated by running "yarn install" inside your project.

__metadata:
  version: 8
  cacheKey: 10c0

"@types/node@npm:^20.0.0":
  version: 20.14.2
  resolution: "@types/node@npm:20.14.2"
  checksum: 10c0/abc
  languageName: node
  linkType: hard

"lodash@npm:^4.17.21":
  version: 4.17.21
  resolution: "lodash@npm:4.17.21"
`)
	got, err = LockedVersions(berry)
	if err != nil {
		t.Fatal(err)
	}
	if got["@types/node"] != "20.14.2" || got["lodash"] != "4.17.21" {
		t.Errorf("berry: %v", got)
	}
	if YarnLockKind(berry) != "berry" {
		t.Error("kind")
	}
	if _, err := LockedVersions([]byte("not a lock at all\n")); err == nil {
		t.Error("garbage must be refused")
	}
}

// A workspace member reads its versions off the root lock: what is hoisted
// to node_modules/<name>, and where the member needs another version, its
// own <member>/node_modules/<name> entry - never a package nested inside a
// package. The manifest beside the lock reads it as before.
func TestLockedVersionsForAWorkspaceMember(t *testing.T) {
	const lock = `{
  "name": "platform",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "platform", "version": "0.0.0", "workspaces": ["apps/*"]},
    "apps/web": {"name": "web", "version": "0.0.0"},
    "node_modules/web": {"resolved": "apps/web", "link": true},
    "node_modules/@types/node": {"version": "22.20.1"},
    "node_modules/react": {"version": "19.1.0"},
    "apps/web/node_modules/react": {"version": "18.3.1"},
    "apps/web/node_modules/react/node_modules/loose-envify": {"version": "1.4.0"}
  }
}`
	for member, want := range map[string]map[string]string{
		"":         {"@types/node": "22.20.1", "react": "19.1.0"},
		"apps/web": {"@types/node": "22.20.1", "react": "18.3.1"},
		"apps/api": {"@types/node": "22.20.1", "react": "19.1.0"},
	} {
		got, err := LockedVersionsFor([]byte(lock), member)
		if err != nil {
			t.Fatal(err)
		}
		for name, version := range want {
			if got[name] != version {
				t.Errorf("member %q: %s = %q, want %q", member, name, got[name], version)
			}
		}
		if _, ok := got["loose-envify"]; ok {
			t.Errorf("member %q: a package nested inside react leaked", member)
		}
	}
}
