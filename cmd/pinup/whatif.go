// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/apply"
	"git.ole-hartwig.eu/pinup/pinup/cache"
	"git.ole-hartwig.eu/pinup/pinup/config"
	"git.ole-hartwig.eu/pinup/pinup/config/preset"
	"git.ole-hartwig.eu/pinup/pinup/discover"
	"git.ole-hartwig.eu/pinup/pinup/extract"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/planner"
	"git.ole-hartwig.eu/pinup/pinup/rules"
	"git.ole-hartwig.eu/pinup/pinup/wire"
)

func cmdWhatif(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("whatif", flag.ContinueOnError)
	fs.SetOutput(errw)
	repo := fs.String("repo", ".", "path to the repository checkout")
	cfgPath := fs.String("config", "", "configuration file to resolve (required until preset resolution lands)")
	report := fs.String("report", "", "write the plan as JSON to this path instead of stdout")
	name := fs.String("name", "", "repository path to record in the plan, e.g. devops/images/ci-tools")
	cachePath := fs.String("cache", os.Getenv("PINUP_CACHE"), "path of the lookup cache file (bbolt); empty means every lookup is cold")
	cacheTTL := fs.Duration("cache-ttl", time.Hour, "how long a cached lookup counts as fresh")
	nowFlag := fs.String("now", "", "plan as if it were this moment (RFC 3339); schedules and release ages are judged against it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now()
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			return fmt.Errorf("--now: %w", err)
		}
		now = t
	}
	if *cfgPath == "" {
		return fmt.Errorf("whatif: --config is required")
	}

	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	opts := whatifOptions{
		Root: *repo, ConfigPath: *cfgPath, RepoName: *name, Now: now,
		Datasources: wire.Datasources(httpClient(env), datasourceOptions(env)),
		CacheTTL:    *cacheTTL,
	}
	if *cachePath != "" {
		store, err := cache.Open(*cachePath)
		if err != nil {
			return fmt.Errorf("cache: %w", err)
		}
		defer store.Close()
		opts.Cache = store
	}
	plan, err := whatif(context.Background(), opts)
	if err != nil {
		return err
	}

	w := out
	if *report != "" {
		f, err := os.Create(*report)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if err := model.WritePlan(w, plan); err != nil {
		return err
	}
	if *report != "" {
		fmt.Fprintf(errw, "%s: %d dependencies in %d files, %d lookups, %d updates, %d warnings\n",
			*report, plan.Stats.DepsExtracted, plan.Stats.FilesDiscovered,
			plan.Stats.LookupsIssued, plan.Stats.UpdatesFound, len(plan.Warnings))
	}
	return nil
}

// nearlyEmptyFirstSeen is the first-seen entry count below which a cache is
// warned about. The estate's smallest repository observes a few hundred
// versions on one run; a record below this has not seen a run at all.
const nearlyEmptyFirstSeen = 100

// whatifOptions is everything a plan run needs. Datasources is injected so a
// test can hand in a fake registry and prove the run touched no network.
type whatifOptions struct {
	Root        string
	ConfigPath  string
	RepoName    string
	Now         time.Time
	Datasources lookup.Registry
	// Cache is optional; nil means every lookup is cold.
	Cache    lookup.Cache
	CacheTTL time.Duration
	// Presets answers local> presets other than the runner's own file;
	// nil means only the builtin library is known.
	Presets preset.Source
}

// runnerAliases are the names the estate's repositories extend the runner
// configuration by. They resolve to the runner's own file without a fetch
// and without changing a byte in any renovate.json.
var runnerAliases = []string{
	"local>devops/renovate-runner",
	"local>devops/renovate-runner:default.json",
	"local>devops/renovate-runner:default",
}

// resolveConfig resolves the configuration a repository runs under.
//
// The runner's file is the global configuration. A repository that carries
// its own renovate.json (or .pinup.*) is resolved from that file, with the
// runner's file answering the local> alias its extends name - the way
// Renovate composes them in production, where the repository's own keys
// and rules come last and decide (testdata/parity/.../presets/README.md).
// A repository without one runs under the runner's file alone.
func resolveConfig(root, cfgPath string, remote preset.Source) (config.Decoded, *config.Resolved, []string, error) {
	global, err := config.LoadFile(cfgPath)
	if err != nil {
		return config.Decoded{}, nil, nil, err
	}
	aliases := preset.Aliases{}
	for _, name := range runnerAliases {
		aliases[name] = global.Raw
	}
	sources := preset.Chain{aliases}
	if remote != nil {
		sources = append(sources, remote)
	}
	sources = append(sources, preset.Builtin())

	repoCfg, err := config.FindConfigFile(root)
	if err != nil {
		return config.Decoded{}, nil, nil, err
	}
	layer := global
	if repoCfg != "" {
		layer, err = config.LoadFile(repoCfg)
		if err != nil {
			return config.Decoded{}, nil, nil, err
		}
	}
	r, warnings, err := config.ResolveLayer(layer, sources)
	if err != nil {
		return config.Decoded{}, nil, nil, err
	}
	d, err := config.Decode(r.Raw)
	return d, r, warnings, err
}

// whatif runs everything up to the plan. It writes nothing to the repository,
// which is what makes it safe to point at anything.
//
// config → discover → extract → rules → lookup → plan → rules. Rules run
// twice, as they do in Renovate: once per dependency before lookup, where
// they can disable it or change its versioning and registries, and once per
// update, where the update type is known and a rule can hold it.
func whatif(ctx context.Context, o whatifOptions) (*model.Plan, error) {
	root, cfgPath, repoName, now := o.Root, o.ConfigPath, o.RepoName, o.Now
	decoded, resolved, presetWarnings, err := resolveConfig(root, cfgPath, o.Presets)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	packageRules, _ := resolved.Raw["packageRules"].([]any)
	engine, err := rules.Compile(packageRules, wire.Versionings())
	if err != nil {
		return nil, fmt.Errorf("packageRules: %w", err)
	}

	managers := wire.Managers()
	req := discover.Request{
		Root:            root,
		EnabledManagers: wire.EnabledKeys(decoded),
		IgnorePaths:     decoded.IgnorePaths,
		Patterns:        wire.DiscoveryPatterns(decoded, managers),
	}
	found, err := discover.Discover(req)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}

	plan := &model.Plan{
		SchemaVersion: model.SchemaVersion,
		PinupVersion:  version,
		GeneratedAt:   now.UTC(),
		Repo:          model.RepoRef{Path: repoName},
		Warnings:      found.Warnings,
	}
	for _, w := range presetWarnings {
		plan.Warnings = append(plan.Warnings, model.Warning{Stage: "config", Msg: w})
	}
	for _, w := range engine.Warnings {
		plan.Warnings = append(plan.Warnings, model.Warning{Stage: "rules", Msg: w})
	}

	// One read per file, however many managers claim it. Reading a file once
	// per manager would multiply IO by the number of definitions that match
	// it, and several match every .gitlab-ci.yml in the estate.
	contents := map[string][]byte{}
	for _, m := range found.Matches {
		if _, ok := contents[m.Path]; ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, m.Path))
		if err != nil {
			plan.Warnings = append(plan.Warnings, model.Warning{
				Stage: "extract", File: m.Path, Msg: err.Error(),
			})
			continue
		}
		contents[m.Path] = b
	}

	for _, match := range found.Matches {
		body, ok := contents[match.Path]
		if !ok {
			continue
		}
		p, err := wire.Resolve(match.Manager, decoded, managers)
		if err != nil {
			plan.Warnings = append(plan.Warnings, model.Warning{
				Stage: "extract", File: match.Path, Msg: err.Error(),
			})
			continue
		}
		res, err := extract.Run(ctx, p.Manager,
			extract.File{Path: match.Path, Content: body}, p.Config)
		if err != nil {
			plan.Warnings = append(plan.Warnings, model.Warning{
				Stage: "extract", File: match.Path, Msg: err.Error(),
			})
			continue
		}
		plan.Warnings = append(plan.Warnings, res.Warnings...)
		for _, d := range res.Deps {
			d.Manager = wire.ManagerNameOf(match.Manager)
			plan.Deps = append(plan.Deps, applyDepRules(engine, resolved.Raw, d))
		}
	}

	// A nearly empty first-seen record makes every release look brand new,
	// and minimumReleaseAge then holds everything for the full duration.
	// That is the safe direction, but it is also a whole estate's worth of
	// updates arriving a day late without anyone knowing why - so say so.
	if counter, ok := o.Cache.(interface{ FirstSeenCount() (int, error) }); ok {
		if n, err := counter.FirstSeenCount(); err == nil && n < nearlyEmptyFirstSeen {
			plan.Warnings = append(plan.Warnings, model.Warning{
				Stage: "cache",
				Msg: fmt.Sprintf("the first-seen record holds only %d versions: releases without a published timestamp will look newly seen and be held for the full minimumReleaseAge; seed the cache from a previous run's export before trusting the hold times",
					n),
			})
		}
	}

	// Every unique (datasource, package, registry) once, however many
	// dependencies share it - the same component pinned in three jobs is
	// one round trip.
	fetcher := &lookup.Fetcher{Registry: o.Datasources, Cache: o.Cache, TTL: o.CacheTTL, Now: now}
	results := fetcher.Fetch(ctx, plan.Deps)
	fromCache := 0
	for _, r := range results {
		if r.Warning != nil {
			plan.Warnings = append(plan.Warnings, *r.Warning)
		}
		if r.Releases != nil && r.Releases.FromCache {
			fromCache++
		}
	}

	planned := planner.Plan(planner.Request{
		Deps: plan.Deps,
		Releases: func(d model.Dependency) *model.ReleaseSet {
			r, ok := results[lookup.RefOf(d).Key()]
			if !ok {
				return nil
			}
			return r.Releases
		},
		Versionings:       wire.Versionings(),
		DefaultVersioning: wire.DefaultVersioning(o.Datasources),
		Now:               now,
	})
	plan.Deps = planned.Deps
	plan.Warnings = append(plan.Warnings, planned.Warnings...)
	var named []planner.Named
	for _, u := range planned.Updates {
		decided, cfg, err := applyUpdateRules(engine, resolved.Raw, u, now)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u.DepKey, err)
		}
		plan.Updates = append(plan.Updates, decided)
		n, err := planner.Name(decided, cfg, wire.Versionings())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u.DepKey, err)
		}
		named = append(named, n)
	}
	branches, err := planner.Compose(named)
	if err != nil {
		return nil, fmt.Errorf("branches: %w", err)
	}
	// Every branch carries the byte-range edits that realise its updates,
	// produced by the manager that extracted each dependency against the
	// bytes it extracted from. A conflict - two managers claiming the same
	// bytes - is reported on the plan and the branch carries no edits, so
	// nothing downstream can write half of it.
	for i := range branches {
		edits, warnings := editsFor(ctx, branches[i], plan.Updates, contents, decoded, managers)
		plan.Warnings = append(plan.Warnings, warnings...)
		branches[i].Edits = edits
	}
	plan.Branches = branches

	blocked := 0
	for _, u := range plan.Updates {
		if u.Blocked() {
			blocked++
		}
	}
	plan.Stats = model.Stats{
		FilesDiscovered:  found.Stats.FilesMatched,
		DepsExtracted:    len(plan.Deps),
		LookupsIssued:    len(results),
		LookupsFromCache: fromCache,
		UpdatesFound:     len(plan.Updates),
		UpdatesBlocked:   blocked,
		BranchesPlanned:  len(plan.Branches),
	}
	plan.Sort()
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("the plan this run produced is not valid: %w", err)
	}
	return plan, nil
}

// editsFor asks each update's manager for its edit and checks the set for
// overlaps. The warnings name what could not be edited and why.
func editsFor(ctx context.Context, b model.Branch, updates []model.Update, contents map[string][]byte,
	decoded config.Decoded, managers extract.Registry) ([]model.Edit, []model.Warning) {
	var edits []model.Edit
	var warnings []model.Warning
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	for _, u := range updates {
		if !keys[u.DepKey] || u.Blocked() {
			continue
		}
		body, ok := contents[u.Dep.File]
		if !ok {
			continue
		}
		key := u.Dep.Manager
		if u.Dep.CustomManager != model.NoCustomManager {
			key = config.CustomManagerName(u.Dep.CustomManager)
		}
		p, err := wire.Resolve(key, decoded, managers)
		if err != nil {
			warnings = append(warnings, model.Warning{Stage: "apply", File: u.Dep.File, Msg: err.Error()})
			continue
		}
		e, err := p.Manager.Edit(ctx, extract.File{Path: u.Dep.File, Content: body}, u)
		if err != nil {
			warnings = append(warnings, model.Warning{Stage: "apply", File: u.Dep.File, Msg: err.Error()})
			continue
		}
		edits = append(edits, e)
	}
	if conflicts := apply.Check(edits); len(conflicts) > 0 {
		for _, c := range conflicts {
			warnings = append(warnings, model.Warning{Stage: "apply", File: c.A.File, Msg: b.Name + ": " + c.Error()})
		}
		return nil, warnings
	}
	return apply.Dedupe(edits), warnings
}

// applyDepRules is the pre-lookup pass: a rule may disable the dependency
// or change how it is looked up. The rule that decided is named, because
// "disabled" without a rule index is the question, not the answer.
func applyDepRules(engine *rules.Engine, base map[string]any, d model.Dependency) model.Dependency {
	if d.SkipReason != "" {
		return d
	}
	res := engine.Apply(base, rules.SubjectOf(d, ""))
	if res.SkipReason != "" {
		d.SkipReason = fmt.Sprintf("disabled by %s", lastWriter(res, "enabled"))
		return d
	}
	if v, ok := res.Config["versioning"].(string); ok && v != "" && len(res.Wrote["versioning"]) > 0 {
		d.Versioning = v
	}
	if urls, ok := res.Config["registryUrls"].([]any); ok && len(res.Wrote["registryUrls"]) > 0 {
		d.RegistryURLs = d.RegistryURLs[:0:0]
		for _, u := range urls {
			if s, ok := u.(string); ok {
				d.RegistryURLs = append(d.RegistryURLs, s)
			}
		}
	}
	return d
}

// applyUpdateRules is the per-update pass: the rules see the update type,
// and the policy they select decides whether the update is acted on now.
// Held, not deleted: the plan still says what would have happened and why
// not, with the thaw time and the rule that held it.
func applyUpdateRules(engine *rules.Engine, base map[string]any, u model.Update, now time.Time) (model.Update, map[string]any, error) {
	res := engine.Apply(base, rules.SubjectOf(u.Dep, u.Type.String()))
	policy := planner.PolicyOf(res.Config, func(key string) model.Origin { return origin(res, key) })
	if res.SkipReason != "" {
		policy.Enabled = false
	}
	decided, err := planner.Decide(u, policy, now)
	return decided, res.Config, err
}

func lastWriter(res rules.Resolution, key string) string {
	chain := res.Wrote[key]
	if len(chain) == 0 {
		return "the base configuration"
	}
	return fmt.Sprintf("packageRules[%d]", chain[len(chain)-1])
}

func origin(res rules.Resolution, key string) model.Origin {
	chain := res.Wrote[key]
	if len(chain) == 0 {
		return model.Origin{Source: "config", Rule: model.NoRule}
	}
	return model.Origin{Source: "packageRules", Rule: chain[len(chain)-1]}
}
