// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohartwig/pinup/fake/fixture"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/versioning"
	"github.com/ohartwig/pinup/wire"
)

// Golden repositories (H.5): <root>/golden/<name>/ holds a repository
// tree, the lookups a live run answered (recorded once, then canned), and
// the plan pinup produced from them. The test re-plans each tree from the
// canned lookups at the recorded moment and compares byte for byte.
//
// Golden files are read, never rewritten. A new golden repository is
// recorded by running this test with PINUP_GOLDEN_RECORD=<name> and a
// live token, which refuses to touch a directory that already has a plan;
// changing behaviour means a new directory reviewed as a diff, or deleting
// the old plan deliberately in the same commit that explains why.
//
// golden.json per repository:
//
//	{"repo": "<name recorded in the plan>", "now": "<RFC 3339>",
//	 "config": "<path relative to <root>/config, default default.json>",
//	 "covers": {"managers": [...], "datasources": [...], "versionings": [...]}}
//
// covers is asserted against the plan, both ways: everything listed must
// appear, and everything that appears must be listed.
func goldenRoot(t *testing.T) string { return fixture.Path(t, "golden") }

type goldenMeta struct {
	Repo   string `json:"repo"`
	Now    string `json:"now"`
	Config string `json:"config"`
	Covers struct {
		Managers    []string `json:"managers"`
		Datasources []string `json:"datasources"`
		Versionings []string `json:"versionings"`
	} `json:"covers"`
}

// cannedLookups is the recorded answer set: release sets by ref key and
// digests by ref key and version.
type cannedLookups struct {
	Releases   map[string]*model.ReleaseSet `json:"releases"`
	Digests    map[string]string            `json:"digests"`
	Advisories map[string]osv.Finding       `json:"advisories,omitempty"`
	// Failures are the lookups that answered an error live; a declined
	// one is replayed as declined, since the plan treats the two apart.
	Failures map[string]cannedFailure `json:"failures,omitempty"`
}

type cannedFailure struct {
	Msg      string `json:"msg"`
	Declined bool   `json:"declined,omitempty"`
}

func advisoryKey(q osv.Query) string {
	return q.Datasource + "\x00" + q.PackageName + "\x00" + q.Version
}

// cannedAdvisories replays recorded findings; a query that was never asked
// live is an error, so a golden repository cannot quietly widen.
type cannedAdvisories struct{ answers *cannedLookups }

func (c cannedAdvisories) Check(_ context.Context, _ versioning.Registry, queries []osv.Query) ([]osv.Finding, error) {
	out := make([]osv.Finding, len(queries))
	for i, q := range queries {
		f, ok := c.answers.Advisories[advisoryKey(q)]
		if !ok {
			return nil, fmt.Errorf("golden: no recorded advisory answer for %s %s %s", q.Datasource, q.PackageName, q.Version)
		}
		out[i] = f
	}
	return out, nil
}

// recordingAdvisories asks OSV and remembers every finding by query.
type recordingAdvisories struct {
	inner   *osv.Client
	answers *cannedLookups
}

func (r recordingAdvisories) Check(ctx context.Context, vs versioning.Registry, queries []osv.Query) ([]osv.Finding, error) {
	fs, err := r.inner.Check(ctx, vs, queries)
	if err != nil {
		return nil, err
	}
	for i, q := range queries {
		r.answers.Advisories[advisoryKey(q)] = fs[i]
	}
	return fs, nil
}

// cannedRegistry serves the recorded answers; every datasource name in the
// live registry is present so the run sees the same names.
type cannedRegistry struct {
	name    string
	scheme  string
	answers *cannedLookups
}

func (c cannedRegistry) Name() string              { return c.name }
func (c cannedRegistry) DefaultVersioning() string { return c.scheme }
func (c cannedRegistry) Releases(_ context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	if f, ok := c.answers.Failures[ref.Key()]; ok {
		if f.Declined {
			return nil, &lookup.DeclinedError{Reason: f.Msg}
		}
		return nil, errors.New(f.Msg)
	}
	rs, ok := c.answers.Releases[ref.Key()]
	if !ok {
		return nil, fmt.Errorf("golden: no recorded lookup for %s %s", ref.Datasource, ref.PackageName)
	}
	cp := *rs
	return &cp, nil
}
func (c cannedRegistry) Digest(_ context.Context, ref lookup.Ref, version string) (string, error) {
	if f, ok := c.answers.Failures["digest\x00"+ref.Key()+"\x00"+version]; ok {
		return "", errors.New(f.Msg)
	}
	d, ok := c.answers.Digests[ref.Key()+"\x00"+version]
	if !ok {
		return "", fmt.Errorf("golden: no recorded digest for %s %s %s", ref.Datasource, ref.PackageName, version)
	}
	return d, nil
}

// recordingDS wraps a live datasource and remembers what it answered.
type recordingDS struct {
	inner   lookup.Datasource
	mu      *sync.Mutex
	answers *cannedLookups
}

func (r recordingDS) Name() string              { return r.inner.Name() }
func (r recordingDS) DefaultVersioning() string { return r.inner.DefaultVersioning() }
func (r recordingDS) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	rs, err := r.inner.Releases(ctx, ref)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		var declined *lookup.DeclinedError
		r.answers.Failures[ref.Key()] = cannedFailure{Msg: err.Error(), Declined: errors.As(err, &declined)}
		return nil, err
	}
	cp := *rs
	cp.FetchedAt, cp.FromCache = time.Time{}, false
	r.answers.Releases[ref.Key()] = &cp
	return rs, nil
}
func (r recordingDS) Digest(ctx context.Context, ref lookup.Ref, version string) (string, error) {
	src, ok := r.inner.(lookup.DigestSource)
	if !ok {
		return "", fmt.Errorf("%s offers no digests", ref.Datasource)
	}
	d, err := src.Digest(ctx, ref, version)
	r.mu.Lock()
	if err == nil {
		r.answers.Digests[ref.Key()+"\x00"+version] = d
	} else {
		r.answers.Failures["digest\x00"+ref.Key()+"\x00"+version] = cannedFailure{Msg: err.Error()}
	}
	r.mu.Unlock()
	return d, err
}

func goldenOptions(t *testing.T, dir string, meta goldenMeta, ds lookup.Registry, now time.Time) whatifOptions {
	t.Helper()
	cfg := meta.Config
	if cfg == "" {
		cfg = "default.json"
	}
	// The toolchain is "present" for every task: the golden plan records
	// the tasks, not this machine's PATH.
	return whatifOptions{
		Root: filepath.Join(dir, "repo"), ConfigPath: fixture.Path(t, "config", cfg),
		RunnerDefault: fixture.Config(t),
		RepoName:      meta.Repo, Now: now, Datasources: ds,
		LookPath: func(string) (string, error) { return "/usr/bin/true", nil },
		AllowedCommands: []string{
			`^node scripts/update-pass-cli-hashes\.mjs$`, `^node tools/update-expected-commit\.mjs [^;&|]+$`, `^composer update [^;&|]+$`,
		},
		CustomDatasources: func(map[string]model.CustomDatasource) lookup.Registry { return nil },
	}
}

func TestGoldenRepositories(t *testing.T) {
	entries, err := os.ReadDir(goldenRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	record := os.Getenv("PINUP_GOLDEN_RECORD")
	seen := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(goldenRoot(t), name)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "golden.json"))
			if err != nil {
				t.Fatal(err)
			}
			var meta goldenMeta
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Fatal(err)
			}
			now, err := time.Parse(time.RFC3339, meta.Now)
			if err != nil {
				t.Fatal(err)
			}
			if record == name {
				recordGolden(t, dir, meta, now)
			}
			raw, err = os.ReadFile(filepath.Join(dir, "lookups.json"))
			if err != nil {
				t.Fatal(err)
			}
			answers := &cannedLookups{}
			if err := json.Unmarshal(raw, answers); err != nil {
				t.Fatal(err)
			}
			ds := lookup.Registry{}
			for n, live := range wire.Datasources(nil, wire.DatasourceOptions{}) {
				ds[n] = cannedRegistry{name: n, scheme: live.DefaultVersioning(), answers: answers}
			}
			opts := goldenOptions(t, dir, meta, ds, now)
			opts.Advisories = cannedAdvisories{answers}
			// The configuration's own datasources answer from the same
			// record - a custom index that was unreachable when recorded
			// stays unreachable, in the same words.
			opts.CustomDatasources = func(defs map[string]model.CustomDatasource) lookup.Registry {
				r := lookup.Registry{}
				for n, live := range wire.CustomDatasources(nil, defs, wire.DefaultApkViews()) {
					r[n] = cannedRegistry{name: n, scheme: live.DefaultVersioning(), answers: answers}
				}
				return r
			}
			plan, err := whatif(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			plan.PinupVersion = "golden"
			var got strings.Builder
			if err := model.WritePlan(&got, plan); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(dir, "plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != string(want) {
				t.Errorf("plan differs from the golden plan.json; a behaviour change is a new golden directory, not a rewrite\n%s", firstDiff(got.String(), string(want)))
			}
			// The rendering is golden too: the merge-request body and
			// the job summary are what a person reads.
			md, err := os.ReadFile(filepath.Join(dir, "plan.md"))
			if err != nil {
				t.Fatal(err)
			}
			if rendered := report.Markdown(plan); rendered != string(md) {
				t.Errorf("rendering differs from the golden plan.md\n%s", firstDiff(rendered, string(md)))
			}
			checkCovers(t, meta, plan)
			seen++
		})
	}
	if seen == 0 {
		t.Fatal("no golden repositories; the harness checked nothing")
	}
}

// checkCovers asserts COVERS both ways.
func checkCovers(t *testing.T, meta goldenMeta, plan *model.Plan) {
	t.Helper()
	managers, datasources, versionings := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, d := range plan.Deps {
		managers[d.Manager] = true
		if d.Datasource != "" {
			datasources[d.Datasource] = true
		}
		if d.Versioning != "" {
			versionings[d.Versioning] = true
		}
	}
	for _, c := range []struct {
		what   string
		listed []string
		found  map[string]bool
	}{{"managers", meta.Covers.Managers, managers}, {"datasources", meta.Covers.Datasources, datasources}, {"versionings", meta.Covers.Versionings, versionings}} {
		l := map[string]bool{}
		for _, x := range c.listed {
			l[x] = true
			if !c.found[x] {
				t.Errorf("golden.json lists %s %q, the plan has none", c.what, x)
			}
		}
		for x := range c.found {
			if !l[x] {
				t.Errorf("the plan covers %s %q, golden.json does not list it", c.what, x)
			}
		}
	}
}

// recordGolden runs live once and writes lookups.json and plan.json - and
// refuses when a plan already exists.
func recordGolden(t *testing.T, dir string, meta goldenMeta, now time.Time) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "plan.json")); err == nil {
		t.Fatalf("%s already has a plan.json; a golden plan is not rewritten", dir)
	}
	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	answers := &cannedLookups{Releases: map[string]*model.ReleaseSet{}, Digests: map[string]string{}, Advisories: map[string]osv.Finding{}, Failures: map[string]cannedFailure{}}
	mu := &sync.Mutex{}
	live := lookup.Registry{}
	dsOpts, err := datasourceOptions(env, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	for n, ds := range wire.Datasources(httpClient(env), dsOpts) {
		live[n] = recordingDS{inner: ds, mu: mu, answers: answers}
	}
	opts := goldenOptions(t, dir, meta, live, now)
	opts.Advisories = recordingAdvisories{inner: &osv.Client{}, answers: answers}
	opts.CustomDatasources = func(defs map[string]model.CustomDatasource) lookup.Registry {
		r := lookup.Registry{}
		for n, ds := range wire.CustomDatasources(httpClient(env), defs, dsOpts.ApkViews) {
			r[n] = recordingDS{inner: ds, mu: mu, answers: answers}
		}
		return r
	}
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	plan.PinupVersion = "golden"
	raw, _ := json.MarshalIndent(answers, "", " ")
	if err := os.WriteFile(filepath.Join(dir, "lookups.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := model.WritePlan(f, plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(report.Markdown(plan)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %s: %d lookups, %d digests, %d deps, %d branches", dir, len(answers.Releases), len(answers.Digests), len(plan.Deps), len(plan.Branches))
}

func firstDiff(got, want string) string {
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range min(len(gl), len(wl)) {
		if gl[i] != wl[i] {
			return fmt.Sprintf("line %d:\n got: %s\nwant: %s", i+1, gl[i], wl[i])
		}
	}
	return fmt.Sprintf("lengths differ: got %d lines, want %d", len(gl), len(wl))
}

// The union of what the golden repositories cover, against what wire
// knows: every manager and datasource is either covered or named in
// UNCOVERED.json with an owner and a reason.
func TestGoldenCoverageIsComplete(t *testing.T) {
	entries, err := os.ReadDir(goldenRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(goldenRoot(t), e.Name(), "golden.json"))
		if err != nil {
			t.Fatal(err)
		}
		var meta goldenMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		for _, m := range meta.Covers.Managers {
			covered["manager:"+m] = true
		}
		for _, d := range meta.Covers.Datasources {
			covered["datasource:"+d] = true
		}
		for _, v := range meta.Covers.Versionings {
			// A regex scheme is named with its pattern in the plan;
			// the registry knows it as "regex".
			if strings.HasPrefix(v, "regex:") {
				v = "regex"
			}
			covered["versioning:"+v] = true
		}
	}
	raw, err := os.ReadFile(filepath.Join(goldenRoot(t), "UNCOVERED.json"))
	if err != nil {
		t.Fatal(err)
	}
	var uncovered map[string]struct {
		Owner  string `json:"owner"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &uncovered); err != nil {
		t.Fatal(err)
	}
	var want []string
	for m := range wire.Managers() {
		want = append(want, "manager:"+m)
	}
	want = append(want, "manager:custom.regex")
	for d := range wire.Datasources(nil, wire.DatasourceOptions{}) {
		want = append(want, "datasource:"+d)
	}
	for v := range wire.Versionings() {
		want = append(want, "versioning:"+v)
	}
	sort.Strings(want)
	for _, w := range want {
		u, listed := uncovered[w]
		switch {
		case covered[w] && listed:
			t.Errorf("%s is covered and still listed in UNCOVERED.json", w)
		case !covered[w] && !listed:
			t.Errorf("%s is neither covered by a golden repository nor listed in UNCOVERED.json", w)
		case listed && (u.Owner == "" || u.Reason == ""):
			t.Errorf("%s: UNCOVERED.json entries need an owner and a reason", w)
		}
	}
	for w := range uncovered {
		if !slices.Contains(want, w) {
			t.Errorf("UNCOVERED.json names %s, which wire does not know", w)
		}
	}
}
