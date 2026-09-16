// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohartwig/pinup/apply"
	"github.com/ohartwig/pinup/cache"
	"github.com/ohartwig/pinup/changelog"
	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/discover"
	"github.com/ohartwig/pinup/extract"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/manager/terraform"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/planner"
	"github.com/ohartwig/pinup/plugin"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/versioning"
	"github.com/ohartwig/pinup/wire"
)

func cmdWhatif(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("whatif", flag.ContinueOnError)
	fs.SetOutput(errw)
	repo := fs.String("repo", ".", "path to the repository checkout")
	fs.StringVar(repo, "dir", ".", "alias for --repo")
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
	project := runnerProject(os.Getenv, *cfgPath)
	if strings.HasPrefix(*cfgPath, "local>") {
		if env.Host == "" {
			return fmt.Errorf("whatif: --config %s needs the instance: set PINUP_GITLAB_URL or CI_SERVER_URL", *cfgPath)
		}
		local, err := fetchConfig(context.Background(), wire.Platform(env.URL, env.Token, env.Header), *cfgPath)
		if err != nil {
			return err
		}
		defer os.Remove(local)
		*cfgPath = local
	}
	client := httpClient(env)
	dsOpts, err := datasourceOptions(env, os.Getenv)
	if err != nil {
		return err
	}
	opts := whatifOptions{
		Root: *repo, ConfigPath: *cfgPath, RepoName: *name, Now: now,
		Datasources:   wire.Datasources(client, dsOpts),
		CacheTTL:      *cacheTTL,
		RunnerProject: project,
	}
	opts.CustomDatasources = customDatasourcesHook(client, dsOpts)
	advisories := &osv.Client{}
	opts.Advisories = advisories
	notes := &changelog.Fetcher{Client: client, GitLabURL: env.URL, TTL: changelogTTL, Now: now, MaxBody: noteBodyLimit}
	opts.Changelog = notes
	opts.LookPath = exec.LookPath
	if opts.AllowedCommands, err = allowedCommands(os.Getenv); err != nil {
		return err
	}
	// A repository's own `local>` presets are read from the instance when
	// there is one to read from; without a token they stay unknown and the
	// resolution says so.
	if env.Host != "" {
		opts.Presets = preset.Remote{Reader: wire.Platform(env.URL, env.Token, env.Header), Ctx: context.Background()}
	}
	if *cachePath != "" {
		store, err := cache.Open(*cachePath)
		if err != nil {
			return fmt.Errorf("cache: %w", err)
		}
		defer store.Close()
		opts.Cache = store
		advisories.Store = advisoryStore{cache: store, now: now, warn: func(m string) { fmt.Fprintf(errw, "warning: %s\n", m) }}
		notes.Cache = store
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
		// The rendering sits beside the plan, for the job summary and
		// for a person reading the artefact.
		if err := writeMarkdown(*report, plan); err != nil {
			return err
		}
		fmt.Fprintf(errw, "%s: %d dependencies in %d files, %d lookups, %d updates, %d warnings\n",
			*report, plan.Stats.DepsExtracted, plan.Stats.FilesDiscovered,
			plan.Stats.LookupsIssued, plan.Stats.UpdatesFound, len(plan.Warnings))
	}
	return nil
}

// writeMarkdown writes the plan's rendering next to its JSON: plan.json
// gets plan.md, anything else gets .md appended.
func writeMarkdown(jsonPath string, plan *model.Plan) error {
	path := strings.TrimSuffix(jsonPath, ".json") + ".md"
	return os.WriteFile(path, []byte(report.Markdown(plan)), 0o644)
}

// nearlyEmptyFirstSeen is the first-seen entry count below which a cache is
// warned about. The estate's smallest repository observes a few hundred
// versions on one run; a record below this has not seen a run at all.
const nearlyEmptyFirstSeen = 100

// changelogTTL is how long a source's release list serves from the cache;
// noteBodyLimit caps one note's body in the plan and the description.
const (
	changelogTTL  = 6 * time.Hour
	noteBodyLimit = 4000
)

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
	// Released, when set, is the project path of a package that was just
	// released: only dependencies on it are planned, every other one is
	// skipped by name, and its lookups bypass the cache.
	Released string
	// Package, when set, is an index key "datasource|packageName" - an
	// external package an advisory names: the same narrowing as Released,
	// by exact key, for the targeted run the advisory watch starts.
	Package string
	// CustomDatasources builds the datasources a configuration declares;
	// nil means customDatasources are unknown. wire supplies it.
	CustomDatasources func(map[string]model.CustomDatasource) lookup.Registry
	// Checks are the dashboard's ticked boxes, read before the run; a
	// dry run has none.
	Checks report.Checks
	// Advisories asks the advisory database about current versions when
	// the configuration sets osvVulnerabilityAlerts; nil means it is never
	// asked, whatever the configuration says.
	Advisories advisoryChecker
	// Changelog reads the release notes between the current and the new
	// version for every update the run will act on, unless a rule sets
	// fetchChangeLogs "off" for it; nil means no update carries notes.
	Changelog noteFetcher
	// LookPath tells whether a task's toolchain is on this machine; nil
	// means the plan lists tasks without judging them, which is what a
	// plan produced away from the runner should do.
	LookPath func(string) (string, error)
	// AllowedCommands are the anchored patterns a postUpgradeTasks command
	// must match; a self-hosted setting, never a repository's. nil means
	// the configuration file's own allowedCommands, if any.
	AllowedCommands []string
	// RunnerDefault is the runner's default.json, what the repositories
	// extend as local><RunnerProject>. Empty means ConfigPath is that
	// file. It differs for the fast lane, whose --config is
	// release-fast.json and itself extends the default.
	RunnerDefault string
	// RunnerProject is the platform project the repositories extend the
	// runner configuration from; empty means defaultRunnerProject.
	RunnerProject string
}

// defaultRunnerProject is the project the estate's repositories extend -
// what --config local>... names, or PINUP_RUNNER_PROJECT, overrides it.
const defaultRunnerProject = "devops/renovate-runner"

// runnerAliases are the names the repositories extend the runner
// configuration by. They resolve to the runner's own file without a fetch
// and without changing a byte in any renovate.json - which is why the
// project the repositories were written against stays an alias after the
// configuration moved: the estate's renovate.json files extend
// devops/renovate-runner, and that project is archived (cutover step 12,
// 2026-09-16) while pinup/runner carries the file.
func runnerAliases(project string) []string {
	if project == "" {
		project = defaultRunnerProject
	}
	names := []string{"local>" + project, "local>" + project + ":default.json", "local>" + project + ":default"}
	if project != defaultRunnerProject {
		names = append(names, "local>"+defaultRunnerProject, "local>"+defaultRunnerProject+":default.json", "local>"+defaultRunnerProject+":default")
	}
	return names
}

// runnerProject is the project the runner configuration lives in: the one
// --config local>... names, else PINUP_RUNNER_PROJECT, else the default.
func runnerProject(getenv func(string) string, cfgPath string) string {
	if strings.HasPrefix(cfgPath, "local>") {
		if project, _, _, err := preset.ParseLocal(strings.TrimPrefix(cfgPath, "local>")); err == nil && project != "" {
			return project
		}
	}
	if v := getenv("PINUP_RUNNER_PROJECT"); v != "" {
		return v
	}
	return defaultRunnerProject
}

// resolveConfig resolves the configuration a repository runs under.
//
// The runner's file is the global configuration. A repository that carries
// its own renovate.json (or .pinup.*) is resolved from that file, with the
// runner's file answering the local> alias its extends name - the way
// Renovate composes them in production, where the repository's own keys
// and rules come last and decide (testdata/renovate/.../presets/README.md).
// A repository without one runs under the runner's file alone.
func resolveConfig(root, cfgPath, runnerDefault, runnerProject string, remote preset.Source) (config.Decoded, *config.Resolved, []string, error) {
	global, err := config.LoadFile(cfgPath)
	if err != nil {
		return config.Decoded{}, nil, nil, err
	}
	aliasDoc := global.Raw
	if runnerDefault != "" && runnerDefault != cfgPath {
		def, err := config.LoadFile(runnerDefault)
		if err != nil {
			return config.Decoded{}, nil, nil, err
		}
		aliasDoc = def.Raw
	}
	aliases := preset.Aliases{}
	for _, name := range runnerAliases(runnerProject) {
		aliases[name] = aliasDoc
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
	if repoCfg == "" {
		// A repository without a configuration file runs under the
		// onboarding default on top of the runner's file, and the one
		// key that changes is ignorePaths: the recommended list, not the
		// runner's own. Measured twice - print-config against a bare
		// repository (testdata/<root>/.../full-resolved.json) and the
		// pinned container on a repository with a committed
		// node_modules: one package file matched, not two - and on the
		// estate, where partner-a-jobs' vendored node_modules/dropzone/
		// package.json is not among Renovate's dependencies.
		if def, _, ok, err := preset.Builtin().Get(":ignoreModulesAndTests"); err == nil && ok {
			if paths, ok := def["ignorePaths"]; ok {
				r.Raw["ignorePaths"] = paths
				r.Prov["/ignorePaths"] = append(r.Prov["/ignorePaths"], model.Origin{Source: "preset::ignoreModulesAndTests", Rule: model.NoRule})
			}
		}
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
	r := &whatifRun{ctx: ctx, o: o, now: o.Now}
	for _, stage := range []func() error{r.configure, r.extractAll, r.lookupAll, r.planUpdates, r.realise} {
		if err := stage(); err != nil {
			return nil, err
		}
	}
	return r.finish()
}

// whatifRun is one run's state as it passes through the stages: the
// configuration as resolved, the files as read, the lookups as answered,
// and the plan as it fills.
type whatifRun struct {
	ctx context.Context
	o   whatifOptions
	now time.Time

	decoded  config.Decoded
	resolved *config.Resolved
	engine   *rules.Engine
	managers extract.Registry
	found    discover.Result
	plan     *model.Plan
	// contents is one read per file, however many managers claim it.
	contents map[string][]byte
	// locks maps "manager|dir" to the lock file present there, so the
	// refresh task and the maintenance branch name the one that exists.
	locks map[string]string
	// lockPaths maps "manager|dir" to the path of the lock a manifest's
	// versions were read from - beside it or, for terraform, an ancestor's
	// - for the edit that moves the lock with the manifest.
	lockPaths map[string]string

	datasources   lookup.Registry
	fetcher       *lookup.Fetcher
	results       map[string]lookup.Result
	fromCache     int
	digestLookups int

	// postUpgrade and changelogOff are per update key: the rule's
	// postUpgradeTasks and its fetchChangeLogs "off".
	postUpgrade  map[string]plugin.PostUpgrade
	changelogOff map[string]bool
	branches     []model.Branch
}

// configure resolves the configuration, compiles the rules, discovers the
// files and opens the plan.
func (r *whatifRun) configure() error {
	o := r.o
	decoded, resolved, presetWarnings, err := resolveConfig(o.Root, o.ConfigPath, o.RunnerDefault, o.RunnerProject, o.Presets)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	packageRules, _ := resolved.Raw["packageRules"].([]any)
	engine, err := rules.Compile(packageRules, wire.Versionings())
	if err != nil {
		return fmt.Errorf("packageRules: %w", err)
	}
	r.decoded, r.resolved, r.engine = decoded, resolved, engine
	r.managers = wire.Managers()
	found, err := discover.Discover(discover.Request{
		Root:            o.Root,
		EnabledManagers: wire.EnabledKeys(decoded),
		IgnorePaths:     decoded.IgnorePaths,
		Patterns:        wire.DiscoveryPatterns(decoded, r.managers),
	})
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	r.found = found

	plan := &model.Plan{
		SchemaVersion: model.SchemaVersion,
		PinupVersion:  version,
		GeneratedAt:   r.now.UTC(),
		Repo:          model.RepoRef{Path: o.RepoName},
		Warnings:      found.Warnings,
	}
	for _, w := range presetWarnings {
		plan.Warnings = append(plan.Warnings, model.Warning{Stage: "config", Msg: w})
	}
	plan.Limits = model.Limits{PRHourlyLimit: intOf(resolved.Raw["prHourlyLimit"]), PRConcurrentLimit: intOf(resolved.Raw["prConcurrentLimit"])}
	if on, _ := resolved.Raw["dependencyDashboard"].(bool); on {
		plan.Dashboard = model.Dashboard{Enabled: true, Title: stringOf(resolved.Raw["dependencyDashboardTitle"])}
		if plan.Dashboard.Title == "" {
			plan.Dashboard.Title = "Dependency Dashboard"
		}
	}
	for _, w := range engine.Warnings {
		plan.Warnings = append(plan.Warnings, model.Warning{Stage: "rules", Msg: w})
	}
	r.plan = plan
	return nil
}

// extractAll reads every discovered file once and runs each claiming
// manager over it, applying the pre-lookup rules to what comes out.
func (r *whatifRun) extractAll() error {
	o, plan, decoded, resolved := r.o, r.plan, r.decoded, r.resolved
	// ignoreDeps names dependencies that are never looked up.
	ignored := map[string]bool{}
	if list, ok := resolved.Raw["ignoreDeps"].([]any); ok {
		for _, e := range list {
			if name, ok := e.(string); ok {
				ignored[name] = true
			}
		}
	}

	// One read per file, however many managers claim it. Reading a file once
	// per manager would multiply IO by the number of definitions that match
	// it, and several match every .gitlab-ci.yml in the estate.
	r.contents = map[string][]byte{}
	for _, m := range r.found.Matches {
		if _, ok := r.contents[m.Path]; ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(o.Root, m.Path))
		if err != nil {
			plan.Warnings = append(plan.Warnings, model.Warning{
				Stage: "extract", File: m.Path, Msg: err.Error(),
			})
			continue
		}
		r.contents[m.Path] = b
	}

	// locks remembers the lock files the run read, by manager and
	// directory: a manifest edit there needs a lock refresh task.
	r.locks = map[string]string{}
	r.lockPaths = map[string]string{}
	for _, match := range r.found.Matches {
		body, ok := r.contents[match.Path]
		if !ok {
			continue
		}
		p, err := wire.Resolve(match.Manager, decoded, r.managers)
		if err != nil {
			plan.Warnings = append(plan.Warnings, model.Warning{
				Stage: "extract", File: match.Path, Msg: err.Error(),
			})
			continue
		}
		res, err := extract.Run(r.ctx, p.Manager,
			extract.File{Path: match.Path, Content: body}, p.Config)
		if err != nil {
			plan.Warnings = append(plan.Warnings, model.Warning{
				Stage: "extract", File: match.Path, Msg: err.Error(),
			})
			continue
		}
		plan.Warnings = append(plan.Warnings, res.Warnings...)
		lock := lockedVersions(o.Root, match.Path, wire.ManagerNameOf(match.Manager), lockFilesOf(res), plan)
		if lock.versions != nil {
			r.lockPaths[wire.ManagerNameOf(match.Manager)+"|"+filepath.Dir(match.Path)] = filepath.Join(lock.dir, lock.name)
		}
		if lock.versions != nil && lock.dir == filepath.Dir(match.Path) {
			// The lock beside the manifest is the one maintenance refreshes
			// and a task regenerates; a terraform ancestor's lock only says
			// what is pinned.
			r.locks[wire.ManagerNameOf(match.Manager)+"|"+filepath.Dir(match.Path)] = lock.name
		} else if lock.versions != nil && wire.ManagerNameOf(match.Manager) == "npm" {
			// An npm workspace: the root lock is the member's lock, and the
			// one a refresh regenerates - recorded by its path from the
			// member, so the task runs where the lock is.
			if rel, err := filepath.Rel(filepath.Dir(match.Path), lock.dir); err == nil {
				r.locks["npm|"+filepath.Dir(match.Path)] = filepath.Join(rel, lock.name)
			}
		}
		for _, d := range res.Deps {
			d.Manager = wire.ManagerNameOf(match.Manager)
			// A lock keys by whatever the lock file calls the package:
			// composer and npm by the name the manifest uses, terraform by
			// the registry source (`cloudflare/cloudflare`) behind the
			// local name (`cloudflare`).
			key := d.DepName
			if _, ok := lock.versions[d.PackageName]; ok && d.PackageName != "" {
				key = d.PackageName
			}
			if v, ok := lock.versions[key]; ok {
				d.LockedVersion = v
			}
			// A terraform lock also says where a provider was resolved from;
			// a registry other than the default is where the lookup goes. A
			// skipped dependency is never looked up and records none
			// (measured: the versionless legacy provider blocks).
			if reg, ok := lock.registries[key]; ok && len(d.RegistryURLs) == 0 && d.SkipReason == "" {
				d.RegistryURLs = []string{reg}
			}
			if d.SkipReason == "" && ignored[d.DepName] {
				d.SkipReason = "listed in ignoreDeps"
			}
			if o.Released != "" && d.SkipReason == "" && !report.RefersTo(report.Key(d), o.Released) {
				d.SkipReason = "not the released package " + o.Released + "; the scheduled run covers it"
			}
			if o.Package != "" && d.SkipReason == "" && report.Key(d) != o.Package {
				d.SkipReason = "not the package " + o.Package + " this run is for; the scheduled run covers it"
			}
			plan.Deps = append(plan.Deps, applyDepRules(r.engine, resolved.Raw, d))
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
	return nil
}

// lookupAll asks every unique (datasource, package, registry) once, then
// the advisory database, then the registries again for the disabled
// dependencies the database flagged.
func (r *whatifRun) lookupAll() error {
	o, plan, ctx := r.o, r.plan, r.ctx
	// The configuration's own datasources join the registry now that the
	// configuration is known; a name the registry already serves natively
	// stays native.
	r.datasources = lookup.Registry{}
	for k, v := range o.Datasources {
		r.datasources[k] = v
	}
	if o.CustomDatasources != nil {
		for k, v := range o.CustomDatasources(r.decoded.CustomDatasources) {
			if _, ok := r.datasources[k]; !ok {
				r.datasources[k] = v
			}
		}
	}

	// Every unique (datasource, package, registry) once, however many
	// dependencies share it - the same component pinned in three jobs is
	// one round trip.
	r.fetcher = &lookup.Fetcher{Registry: r.datasources, Cache: o.Cache, TTL: o.CacheTTL, Now: r.now}
	if o.Released != "" {
		r.fetcher.Bypass = func(ref lookup.Ref) bool { return report.RefersTo(ref.Datasource+"|"+ref.PackageName, o.Released) }
	}
	if o.Package != "" {
		r.fetcher.Bypass = func(ref lookup.Ref) bool { return ref.Datasource+"|"+ref.PackageName == o.Package }
	}
	// A disabled dependency is not looked up - unless the advisory
	// database says it is vulnerable, which is checked after the first
	// round of lookups (a range without a lock is asked about the lowest
	// release it admits, and that takes the registry's answer) and
	// followed by a second round for the vulnerable ones.
	for i := range plan.Deps {
		d := &plan.Deps[i]
		if d.Disabled != "" && d.SkipReason == "" {
			d.SkipReason = d.Disabled
		}
	}
	r.results = r.fetcher.Fetch(ctx, plan.Deps)
	if o.Advisories != nil {
		plan.Warnings = append(plan.Warnings, checkAdvisories(ctx, o.Advisories, r.resolved.Raw, plan.Deps, wire.DefaultVersioning(r.datasources), r.releasesOf)...)
		var vulnerable []model.Dependency
		for i := range plan.Deps {
			d := &plan.Deps[i]
			if d.Disabled != "" && d.SkipReason == d.Disabled && d.VulnerabilityBound != "" {
				d.SkipReason = ""
				vulnerable = append(vulnerable, *d)
			}
		}
		for k, res := range r.fetcher.Fetch(ctx, vulnerable) {
			r.results[k] = res
		}
	}
	for _, res := range r.results {
		if res.Warning != nil {
			plan.Warnings = append(plan.Warnings, *res.Warning)
		}
		if res.Releases != nil && res.Releases.FromCache {
			r.fromCache++
		}
	}
	return nil
}

// releasesOf answers a dependency's lookup result, nil when none was made.
func (r *whatifRun) releasesOf(d model.Dependency) *model.ReleaseSet {
	if res, ok := r.results[lookup.RefOf(d).Key()]; ok {
		return res.Releases
	}
	return nil
}

// planUpdates decides every dependency, applies the per-update rules and
// composes the branches.
func (r *whatifRun) planUpdates() error {
	o, plan, resolved := r.o, r.plan, r.resolved
	planned := planner.Plan(planner.Request{
		Deps:              plan.Deps,
		Releases:          r.releasesOf,
		Versionings:       wire.Versionings(),
		DefaultVersioning: wire.DefaultVersioning(r.datasources),
		Digest: func(d model.Dependency, version string) (string, error) {
			r.digestLookups++
			return r.fetcher.Digest(r.ctx, d, version)
		},
		Now: r.now,
	})
	plan.Deps = planned.Deps
	plan.Warnings = append(plan.Warnings, planned.Warnings...)
	// lockFileMaintenance: one refresh per lock file the run read, planned
	// as its own update so the branch Renovate opens for it exists here
	// too; the branch's task is the toolchain run, and a machine without
	// the toolchain holds it.
	planned.Updates = append(planned.Updates, lockMaintenance(resolved.Raw, plan.Deps, r.locks)...)

	var named []planner.Named
	r.postUpgrade = map[string]plugin.PostUpgrade{}
	r.changelogOff = map[string]bool{}
	for _, u := range planned.Updates {
		decided, cfg, err := applyUpdateRules(r.engine, resolved.Raw, u, r.now)
		if err != nil {
			return fmt.Errorf("%s: %w", u.DepKey, err)
		}
		if pu, ok := postUpgradeOf(cfg); ok {
			r.postUpgrade[u.Key()] = pu
		}
		if off, _ := cfg["fetchChangeLogs"].(string); off == "off" {
			r.changelogOff[u.Key()] = true
		}
		plan.Updates = append(plan.Updates, decided)
		n, err := planner.Name(decided, cfg, wire.Versionings())
		if err != nil {
			return fmt.Errorf("%s: %w", u.DepKey, err)
		}
		named = append(named, n)
	}
	branches, err := planner.Compose(named)
	if err != nil {
		return fmt.Errorf("branches: %w", err)
	}
	// The dashboard's ticked boxes lift the holds they name: an approval,
	// a schedule, a release age. The plan records the lift on the branch,
	// so "why did this open" has an answer.
	for i := range branches {
		liftByDashboard(&branches[i], plan.Updates, o.Checks)
	}
	r.branches = branches
	return nil
}

// realise gives every actionable branch its edits and tasks, and every
// held one the block a member brought onto it.
func (r *whatifRun) realise() error {
	o, plan, branches := r.o, r.plan, r.branches
	// Every branch carries the byte-range edits that realise its updates,
	// produced by the manager that extracted each dependency against the
	// bytes it extracted from. A conflict - two managers claiming the same
	// bytes - is reported on the plan and the branch carries no edits, so
	// nothing downstream can write half of it.
	allowed := o.AllowedCommands
	if allowed == nil {
		allowed = stringList(r.resolved.Raw["allowedCommands"])
	}
	for i := range branches {
		if branches[i].SuppressedBy != "" {
			if branches[i].HeldWith.Reason != "" {
				// The branch-level hold reaches the members that were
				// actionable on their own; the plan says which member
				// brought it.
				blk := branches[i].HeldWith
				if blk.Note == "" {
					blk.Note = "held with the branch: a member's " + string(blk.Reason) + " is the branch's"
				}
				holdBranch(&branches[i], plan.Updates, blk)
			}
			continue
		}
		edits, warnings := editsFor(r.ctx, branches[i], plan.Updates, r.contents, r.decoded, r.managers)
		plan.Warnings = append(plan.Warnings, warnings...)
		// A terraform provider bump moves its lock too, in the same branch:
		// the version and the hashes of the new release, computed here,
		// never by a tool on the checkout. A lock that cannot be moved
		// holds the branch - a manifest ahead of its lock is a branch
		// `tofu init` refuses (measured 2026-09-15: koh-infra).
		if lockEdits, err := r.terraformLockEdits(o.Root, branches[i], plan.Updates); err != nil {
			plan.Warnings = append(plan.Warnings, model.Warning{Stage: "plan", File: branches[i].Name, Msg: err.Error()})
			holdBranch(&branches[i], plan.Updates, model.Block{Reason: model.BlockTaskRefused, Org: model.Origin{Source: "pinup", Rule: model.NoRule}, Note: err.Error()})
			continue
		} else {
			edits = append(edits, lockEdits...)
		}
		branches[i].Edits = edits
		// The commands the branch needs beyond its edits: a lock refresh
		// where a manifest with a lock changed, and the configuration's
		// postUpgradeTasks. A command the allowlist refuses or a toolchain
		// this machine lacks holds the branch by name; the edits are then
		// dropped, since half a branch is worse than none.
		tasks, hold := tasksFor(branches[i], plan.Updates, r.postUpgrade, r.locks, allowed, o.LookPath)
		branches[i].Tasks = tasks
		if hold != nil {
			holdBranch(&branches[i], plan.Updates, *hold)
		}
	}
	plan.Branches = branches
	if o.Changelog != nil {
		plan.Warnings = append(plan.Warnings, fillNotes(r.ctx, o.Changelog, plan, r.changelogOff, wire.Versionings(), wire.DefaultVersioning(r.datasources))...)
	}
	return nil
}

// finish counts, sorts and validates: a plan that is not valid is not
// returned.
func (r *whatifRun) finish() (*model.Plan, error) {
	plan := r.plan
	blocked := 0
	for _, u := range plan.Updates {
		if u.Blocked() {
			blocked++
		}
	}
	actionable := 0
	for _, b := range plan.Branches {
		if b.SuppressedBy == "" {
			actionable++
		}
	}
	for _, msg := range r.fetcher.Problems() {
		plan.Warnings = append(plan.Warnings, model.Warning{Stage: "lookup", Msg: msg})
	}
	plan.Stats = model.Stats{
		FilesDiscovered:  r.found.Stats.FilesMatched,
		DepsExtracted:    len(plan.Deps),
		LookupsIssued:    len(r.results) + r.digestLookups,
		LookupsFromCache: r.fromCache,
		UpdatesFound:     len(plan.Updates),
		UpdatesBlocked:   blocked,
		BranchesPlanned:  actionable,
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
		if !keys[u.Key()] || u.Blocked() || u.LockOnly || u.Type == model.UpdateLockFileMaintenance {
			// A lock-only update writes no manifest byte; its lock
			// refresh is the branch's task. A maintenance update names
			// the lock itself and no range in it - asking its manager
			// for an edit reported "no editable range" on every run.
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
	// Two managers reading the same line and proposing the same bytes -
	// the estate's custom manager and gitlabci on a component include -
	// are one edit; only what remains distinct can conflict.
	edits = apply.Dedupe(edits)
	if conflicts := apply.Check(edits); len(conflicts) > 0 {
		for _, c := range conflicts {
			warnings = append(warnings, model.Warning{Stage: "apply", File: c.A.File, Msg: b.Name + ": " + c.Error()})
		}
		return nil, warnings
	}
	return edits, warnings
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
		// Disabled, not skipped: the advisory check still sees it, and a
		// security fix travels under vulnerabilityAlerts' own enabled.
		d.Disabled = fmt.Sprintf("disabled by %s", lastWriter(res, "enabled"))
		return d
	}
	if v, ok := res.Config["versioning"].(string); ok && v != "" && len(res.Wrote["versioning"]) > 0 {
		d.Versioning = v
	}
	if av, ok := res.Config["allowedVersions"].(string); ok && av != "" {
		d.AllowedVersions = av
	}
	if rs, ok := res.Config["rangeStrategy"].(string); ok && rs != "" {
		d.RangeStrategy = rs
	}
	if pin, ok := res.Config["pinDigests"].(bool); ok {
		d.PinDigests = pin
	}
	if f, ok := res.Config["internalChecksFilter"].(string); ok && f != "" && f != "none" {
		d.InternalChecksFilter = f
		if age, ok := res.Config["minimumReleaseAge"].(string); ok {
			d.MinimumReleaseAge = age
		}
		if b, ok := res.Config["minimumReleaseAgeBehaviour"].(string); ok {
			d.TimestampOptional = b == "timestamp-optional"
		}
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
	// The rules were written for Renovate and see the type Renovate would
	// report: a majorAvailable update is a major to them.
	res := engine.Apply(base, rules.SubjectOf(u.Dep, u.Type.Renovate().String()))
	// The update type's own object - lockFileMaintenance.schedule, say -
	// applies over the rules' result before the policy is read.
	cfg := planner.Overlay(res.Config, u.Type)
	if u.SecurityFix {
		// Measured: a security fix travels under the vulnerabilityAlerts
		// object - its own branch topic, the security label, no release
		// age, no schedule, no dashboard approval - forced over whatever
		// the rules said. The branch is named from the same view.
		cfg = planner.OverlayKey(cfg, "vulnerabilityAlerts")
		// Measured on the estate: a composer security fix is titled "to
		// ^11.5.50" - the manager's bump, not the object's update-lockfile
		// - so the strategy the pre-lookup rules gave the dependency
		// stands; the planner already used it to pick the fix.
	}
	policy := planner.PolicyOf(cfg, func(key string) model.Origin { return origin(res, key) })
	if res.SkipReason != "" {
		policy.Enabled = false
	}
	if u.SecurityFix {
		if enabled, ok := cfg["enabled"].(bool); ok {
			policy.Enabled = enabled
		}
	}
	decided, err := planner.Decide(u, policy, now)
	if u.SecurityFix {
		return decided, cfg, err
	}
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

// advisoryChecker is what the run asks about advisories; osv.Client is
// the one that talks to OSV, the golden harness replays recorded answers.
type advisoryChecker interface {
	Check(ctx context.Context, vs versioning.Registry, queries []osv.Query) ([]osv.Finding, error)
}

// checkAdvisories is the vulnerability fast path's first half: every
// dependency at a single version is asked about at the advisory database
// (osvVulnerabilityAlerts), and one that is affected gets its advisories
// and the highest fix version written on it for the planner. A database
// that cannot be reached is a warning, never a failed run - the ordinary
// updates still happen. Measured: Renovate queries npm and Packagist
// dependencies this way and skips a range like ^1.2.5.
func checkAdvisories(ctx context.Context, client advisoryChecker, cfg map[string]any, deps []model.Dependency, defaultVersioning func(string) string, releasesOf func(model.Dependency) *model.ReleaseSet) []model.Warning {
	if on, _ := cfg["osvVulnerabilityAlerts"].(bool); !on {
		return nil
	}
	if va, ok := cfg["vulnerabilityAlerts"].(map[string]any); ok {
		if enabled, ok := va["enabled"].(bool); ok && !enabled {
			return nil
		}
	}
	var queries []osv.Query
	var index []int
	schemes := wire.Versionings()
	for i, d := range deps {
		if (d.SkipReason != "" && d.SkipReason != d.Disabled) || d.CurrentValue == "" || osv.Ecosystem(d.Datasource) == "" {
			continue
		}
		name := d.PackageName
		if name == "" {
			name = d.DepName
		}
		scheme := d.Versioning
		if scheme == "" {
			scheme = defaultVersioning(d.Datasource)
		}
		// The version actually in use: the lock's when there is one -
		// a range like ^4.0.0 says nothing about what is installed, and
		// Renovate opened vitest's security fix from the lock. A range
		// without a lock is asked about the lowest release it admits.
		// Measured: sylius/sylius "^2.0" and no composer.lock gets
		// "update dependency sylius/sylius to ^2.0.18 [security]".
		version := d.CurrentValue
		if d.LockedVersion != "" {
			version = d.LockedVersion
		}
		v, err := schemes.Get(scheme)
		if err != nil {
			continue
		}
		if !v.IsVersion(version) {
			version = lowestAdmitted(v, version, releasesOf(d))
			if version == "" {
				continue
			}
		}
		queries = append(queries, osv.Query{Datasource: d.Datasource, PackageName: name, Version: version, Versioning: scheme})
		index = append(index, i)
	}
	if len(queries) == 0 {
		return nil
	}
	findings, err := client.Check(ctx, schemes, queries)
	if err != nil {
		return []model.Warning{{Stage: "lookup", Msg: fmt.Sprintf("advisory database: %v; updates are planned without vulnerability alerts", err)}}
	}
	var warns []model.Warning
	for k, f := range findings {
		d := &deps[index[k]]
		for _, w := range f.Warnings {
			warns = append(warns, model.Warning{Stage: "lookup", File: d.File, Msg: d.DepName + ": " + w})
		}
		if len(f.Advisories) == 0 {
			continue
		}
		for _, a := range f.Advisories {
			d.Advisories = append(d.Advisories, model.Advisory{
				ID: a.ID, Aliases: a.Aliases, Summary: a.Summary, Severity: a.Severity, Fixed: a.Fixed, Published: a.Published,
			})
		}
		d.VulnerabilityBound = f.Bound
	}
	return warns
}

// lowestAdmitted is the lowest release a range admits, "" when none is
// known: what a range without a lock is taken to be running. An OR of
// ranges is not asked at all - measured in the pinned container: "^2.0",
// ">=7.0 <7.5" and "3.0.*" resolve to their lowest release, "^6.4 || ^7.4"
// is skipped as an unsupported version, and the estate's symfony packages
// carry exactly that shape without a security branch.
func lowestAdmitted(v versioning.Versioning, rng string, rs *model.ReleaseSet) string {
	if rs == nil || strings.Contains(rng, "||") {
		return ""
	}
	var admitted []string
	for _, r := range rs.Releases {
		if v.IsVersion(r.Version) && v.Satisfies(r.Version, rng) {
			admitted = append(admitted, r.Version)
		}
	}
	if len(admitted) == 0 {
		return ""
	}
	return versioning.Sort(v, admitted)[0]
}

// advisoryStore adapts the lookup cache to the advisory client: a record
// is keyed by id and modification time, so it never goes stale, and thirty
// days is only how long an unreferenced one lingers.
type advisoryStore struct {
	cache lookup.Cache
	now   time.Time
	// warn receives a cache write failure; nil drops it.
	warn func(string)
}

func (s advisoryStore) Get(key string) ([]byte, bool) {
	payload, fresh, err := s.cache.GetReleases("advisory\x00"+key, 30*24*time.Hour, s.now)
	return payload, err == nil && fresh && len(payload) > 0
}

func (s advisoryStore) Put(key string, payload []byte) {
	if err := s.cache.PutReleases("advisory\x00"+key, payload, s.now); err != nil && s.warn != nil {
		s.warn(fmt.Sprintf("cache: advisory %s: %v", key, err))
	}
}

// customDatasourcesHook lets whatif build the datasources a configuration
// declares, with the same client the fixed ones use.
func customDatasourcesHook(client *httpx.Client, o wire.DatasourceOptions) func(map[string]model.CustomDatasource) lookup.Registry {
	views := o.ApkViews
	if views == nil {
		views = wire.DefaultApkViews()
	}
	return func(defs map[string]model.CustomDatasource) lookup.Registry {
		return wire.CustomDatasources(client, defs, views)
	}
}

// intOf reads a JSON number as an int; anything else is zero.
func intOf(v any) int {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int(f)
}

// lockedVersions reads the lock file next to a manifest, once, and returns
// the versions it pins by package name. A manager receives the manifest
// only; the lock is the run's to read. A missing lock is nothing pinned; an
// unreadable one is a warning, since "no locked version" is a different
// fact from "the lock could not be read".
// lockFilesOf is the lock files an extraction names: on the result, or on
// any of its dependencies - sorted first is not necessarily one that has a
// lock (go.mod's go directive has none, its modules do).
func lockFilesOf(res extract.Result) []string {
	if len(res.LockFiles) > 0 {
		return res.LockFiles
	}
	for _, d := range res.Deps {
		if len(d.LockFiles) > 0 {
			return d.LockFiles
		}
	}
	return nil
}

// lockFile is what a manifest's lock file pins: versions by package name,
// registries by package name where the lock names one (terraform), and the
// lock's name relative to the manifest's directory.
type lockFile struct {
	versions   map[string]string
	registries map[string]string
	name, dir  string
}

// lockedVersions reads the first lock file present of the candidates and
// returns what it pins. A composer or npm lock sits beside its manifest; a
// terraform lock sits wherever init ran, at the manifest's directory or an
// ancestor of it (measured: a module's versions.tf three directories down
// carries the root lock's versions), so the search walks up to the root.
func lockedVersions(root, manifest, manager string, lockFiles []string, plan *model.Plan) lockFile {
	if len(lockFiles) == 0 {
		return lockFile{}
	}
	dirs := []string{filepath.Dir(manifest)}
	// terraform: init ran at the manifest's directory or an ancestor. npm:
	// a workspace member's versions are in the root's lock (measured:
	// nozzleops/platform, four package.json under one package-lock.json).
	if manager == "terraform" || manager == "npm" {
		for d := dirs[0]; d != "." && d != "/" && d != ""; {
			d = filepath.Dir(d)
			dirs = append(dirs, d)
		}
	}
	for _, dir := range dirs {
		for _, name := range lockFiles {
			path := filepath.Join(dir, name)
			raw, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				continue
			}
			member := ""
			if dir != filepath.Dir(manifest) {
				member, _ = filepath.Rel(dir, filepath.Dir(manifest))
			}
			locked, err := wire.LockedVersionsFor(manager, raw, member)
			if err != nil {
				plan.Warnings = append(plan.Warnings, model.Warning{Stage: "extract", File: path, Msg: err.Error()})
				continue
			}
			return lockFile{versions: locked, registries: wire.LockedRegistries(manager, raw), name: name, dir: dir}
		}
	}
	return lockFile{}
}

// postUpgradeOf reads an update's resolved postUpgradeTasks object.
func postUpgradeOf(cfg map[string]any) (plugin.PostUpgrade, bool) {
	obj, ok := cfg["postUpgradeTasks"].(map[string]any)
	if !ok {
		return plugin.PostUpgrade{}, false
	}
	pu := plugin.PostUpgrade{
		Commands:      stringList(obj["commands"]),
		ExecutionMode: model.ExecutionMode(stringOf(obj["executionMode"])),
		FileFilters:   stringList(obj["fileFilters"]),
		Origin:        model.Origin{Source: "config", Rule: model.NoRule},
	}
	return pu, len(pu.Commands) > 0
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// tasksFor composes a branch's tasks: one lock refresh per (manager,
// directory) whose lock the run read and whose manifest the branch edits,
// then the postUpgradeTasks of its updates, deduplicated by command. The
// hold, when set, names the first thing that stops the branch: a refused
// command or a missing toolchain.
func tasksFor(b model.Branch, updates []model.Update, postUpgrade map[string]plugin.PostUpgrade, locks map[string]string, allowed []string, lookPath func(string) (string, error)) ([]model.Task, *model.Block) {
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	var mine []model.Update
	for _, u := range updates {
		if keys[u.Key()] && !u.Blocked() {
			mine = append(mine, u)
		}
	}
	var tasks []model.Task
	// Lock refreshes, one per manager and directory, in a stable order.
	type lockKey struct{ manager, dir string }
	names := map[lockKey][]string{}
	maintenance := map[lockKey]bool{}
	var order []lockKey
	for _, u := range mine {
		dir := filepath.Dir(u.Dep.File)
		if dir == "." {
			dir = ""
		}
		k := lockKey{u.Dep.Manager, dir}
		if u.Type == model.UpdateLockFileMaintenance {
			if !maintenance[k] {
				maintenance[k] = true
				order = append(order, k)
			}
			continue
		}
		if locks[u.Dep.Manager+"|"+filepath.Dir(u.Dep.File)] == "" {
			continue
		}
		if _, seen := names[k]; !seen {
			order = append(order, k)
		}
		names[k] = append(names[k], u.Dep.DepName)
	}
	// One refresh per lock, not per manifest: a workspace member's lock
	// is the root's, so the refresh runs where the lock is and regenerates
	// it whole, as every manifest under it now reads - naming the member's
	// packages there would add them to the root instead. A root manifest's
	// own refresh on the same lock folds into that one.
	type refresh struct {
		lockFile    string
		names       []string
		maintenance bool
		whole       bool
	}
	byLock := map[lockKey]*refresh{}
	var lockOrder []lockKey
	for _, k := range order {
		lockFile := locks[k.manager+"|"+k.dir]
		if lockFile == "" && k.dir == "" {
			lockFile = locks[k.manager+"|."]
		}
		dir, whole := k.dir, false
		if strings.Contains(lockFile, "/") {
			dir = filepath.Clean(filepath.Join(k.dir, filepath.Dir(lockFile)))
			if dir == "." {
				dir = ""
			}
			lockFile, whole = filepath.Base(lockFile), true
		}
		at := lockKey{k.manager, dir}
		r, seen := byLock[at]
		if !seen {
			r = &refresh{lockFile: lockFile}
			byLock[at] = r
			lockOrder = append(lockOrder, at)
		}
		r.names = append(r.names, names[k]...)
		r.maintenance = r.maintenance || maintenance[k]
		r.whole = r.whole || whole
	}
	for _, at := range lockOrder {
		r := byLock[at]
		refreshNames := r.names
		if r.whole {
			refreshNames = nil
		}
		t, ok := plugin.LockRefresh(at.manager, at.dir, r.lockFile, refreshNames, r.maintenance)
		if !ok && r.maintenance {
			// A maintenance branch is nothing but its refresh; without a
			// refresh for this manager's lock it would be a branch with
			// no edit and no task, which the runner skips without a word
			// and the comparison reads as planned and never opened
			// (measured 2026-09-16: koh-infra's .terraform.lock.hcl).
			return tasks, &model.Block{Reason: model.BlockPluginRequired, Org: model.Origin{Source: "pinup", Rule: model.NoRule},
				Note: fmt.Sprintf("no lock refresh for %s (%s)", at.manager, filepath.Join(at.dir, r.lockFile))}
		}
		if ok {
			tasks = append(tasks, t)
		}
	}
	// postUpgradeTasks: grouped by the rule object that set them, so a
	// branch-mode command carries every update the same object applies to.
	type puKey struct{ commands, mode, filters string }
	groups := map[puKey][]model.Update{}
	confs := map[puKey]plugin.PostUpgrade{}
	var puOrder []puKey
	for _, u := range mine {
		pu, ok := postUpgrade[u.Key()]
		if !ok {
			continue
		}
		k := puKey{strings.Join(pu.Commands, "\x00"), string(pu.ExecutionMode), strings.Join(pu.FileFilters, "\x00")}
		if _, seen := groups[k]; !seen {
			puOrder = append(puOrder, k)
			confs[k] = pu
		}
		groups[k] = append(groups[k], u)
	}
	for _, k := range puOrder {
		compiled, err := plugin.Compile(confs[k], groups[k], allowed)
		if err != nil {
			return tasks, &model.Block{Reason: model.BlockTaskRefused, Org: confs[k].Origin, Note: err.Error()}
		}
		tasks = append(tasks, compiled...)
	}
	if lookPath != nil && len(tasks) > 0 {
		r := &plugin.Runner{LookPath: lookPath}
		if err := r.Available(tasks); err != nil {
			return tasks, &model.Block{Reason: model.BlockPluginRequired, Org: model.Origin{Source: "pinup", Rule: model.NoRule}, Note: err.Error()}
		}
	}
	return tasks, nil
}

// noteFetcher is what the run asks for release notes; changelog.Fetcher
// is the one implementation, and a test hands in a fake.
type noteFetcher interface {
	Notes(ctx context.Context, sourceURL string, v versioning.Versioning, current, target string) ([]model.ReleaseNote, string, error)
}

// fillNotes gives every update on a branch the run will push the release
// notes between its current and its new version, and the forge's compare
// link. Held updates get none: their merge request does not exist yet,
// and the notes are fetched the run it does. A digest move has no
// versions to span; a lock-file maintenance names no dependency. A fetch
// that fails leaves the update without notes and says so in a warning -
// the update itself is fine, only the description is poorer.
func fillNotes(ctx context.Context, f noteFetcher, plan *model.Plan, off map[string]bool, schemes versioning.Registry, defaultVersioning func(string) string) []model.Warning {
	index := map[string]int{}
	for i, u := range plan.Updates {
		index[u.Key()] = i
	}
	var warnings []model.Warning
	fetched := map[string]bool{}
	for _, b := range plan.Branches {
		if b.SuppressedBy != "" {
			continue
		}
		for _, k := range b.UpdateKeys {
			i, ok := index[k]
			if !ok || fetched[k] || off[k] {
				continue
			}
			fetched[k] = true
			u := &plan.Updates[i]
			current, target := u.Dep.CurrentValue, u.NewVersion
			if target == "" {
				target = u.NewValue
			}
			if u.Dep.LockedVersion != "" {
				current = u.Dep.LockedVersion
			}
			switch {
			case u.Dep.SourceURL == "", current == "", target == "", current == target:
				continue
			case u.Type == model.UpdateDigest, u.Type == model.UpdatePinDigest, u.Type == model.UpdateLockFileMaintenance:
				continue
			}
			scheme := u.Dep.Versioning
			if scheme == "" {
				scheme = defaultVersioning(u.Dep.Datasource)
			}
			v, err := schemes.Get(scheme)
			if err != nil {
				continue
			}
			notes, compare, err := f.Notes(ctx, u.Dep.SourceURL, v, current, target)
			u.Notes, u.CompareURL = notes, compare
			if err != nil {
				warnings = append(warnings, model.Warning{Stage: "changelog", Msg: fmt.Sprintf("%s: %v", u.DepKey, err)})
			}
		}
	}
	return warnings
}

// liftByDashboard clears a branch's hold when the dashboard ticked the box
// for it: the named block leaves every member update, the branch's
// suppression is recomputed from what remains, and the branch carries the
// dashboard as an origin.
func liftByDashboard(b *model.Branch, updates []model.Update, checks report.Checks) {
	if b.SuppressedBy == "" || !checks.Lifted(b.Name, b.SuppressedBy) {
		return
	}
	reason := b.SuppressedBy
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	remaining := model.BlockReason("")
	for i := range updates {
		u := &updates[i]
		if !keys[u.Key()] {
			continue
		}
		kept := u.Blocks[:0]
		for _, blk := range u.Blocks {
			if blk.Reason != reason {
				kept = append(kept, blk)
			}
		}
		u.Blocks = kept
		u.SuppressedBy = ""
		if len(kept) > 0 {
			u.SuppressedBy = kept[0].Reason
			if remaining == "" {
				remaining = kept[0].Reason
			}
		}
	}
	b.SuppressedBy = remaining
	b.HeldWith = model.Block{}
	if reason == model.BlockSchedule {
		b.Schedule.Active = true
	}
	b.Prov = append(b.Prov, model.Origin{Source: "dashboard", Pointer: string(reason), Rule: model.NoRule})
}

// holdBranch marks a branch held for one reason: every update on it gets
// the block, the branch records it and carries no edits.
func holdBranch(b *model.Branch, updates []model.Update, block model.Block) {
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	for i := range updates {
		if keys[updates[i].Key()] && !updates[i].Blocked() {
			updates[i].Blocks = append(updates[i].Blocks, block)
			updates[i].SuppressedBy = block.Reason
		}
	}
	b.SuppressedBy = block.Reason
	b.Edits = nil
}

// lockMaintenance plans the lock-file refreshes lockFileMaintenance asks
// for: one per (manager, lock file) among the dependencies that carry a
// locked version, each an update of type lockFileMaintenance.
func lockMaintenance(cfg map[string]any, deps []model.Dependency, locks map[string]string) []model.Update {
	lfm, ok := cfg["lockFileMaintenance"].(map[string]any)
	if !ok {
		return nil
	}
	if enabled, _ := lfm["enabled"].(bool); !enabled {
		return nil
	}
	seen := map[string]bool{}
	var out []model.Update
	for _, d := range deps {
		if d.LockedVersion == "" || len(d.LockFiles) == 0 {
			continue
		}
		// A version pinned by an ancestor's lock names nothing to maintain
		// beside this manifest.
		name := locks[d.Manager+"|"+filepath.Dir(d.File)]
		if name == "" {
			continue
		}
		lock := filepath.Join(filepath.Dir(d.File), name)
		key := d.Manager + "|" + lock
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model.Update{
			DepKey: "lockFileMaintenance|" + key,
			Dep: model.Dependency{
				Manager: d.Manager, File: lock, DepName: "lock file", CustomManager: model.NoCustomManager,
				Locus: model.Locus{DigestStart: model.NoDigest, DigestEnd: model.NoDigest},
			},
			Type: model.UpdateLockFileMaintenance, TimeSource: model.TimeUnknown,
		})
	}
	return out
}

// providerHasher is what the terraform-provider datasource offers beyond
// lookup.Datasource: the hashes a lock records for a version.
type providerHasher interface {
	ProviderHashes(ctx context.Context, ref lookup.Ref, version string) ([]string, error)
}

// terraformLockEdits returns the edits that move the .terraform.lock.hcl
// entries of a branch's terraform provider updates, one per provider. A
// provider without a lock in its manifest's reach needs none.
func (r *whatifRun) terraformLockEdits(root string, b model.Branch, updates []model.Update) ([]model.Edit, error) {
	keys := map[string]bool{}
	for _, k := range b.UpdateKeys {
		keys[k] = true
	}
	var edits []model.Edit
	done := map[string]bool{}
	for _, u := range updates {
		if !keys[u.Key()] || u.Blocked() || u.Dep.Manager != "terraform" || u.Dep.Datasource != "terraform-provider" {
			continue
		}
		lockPath := r.lockPaths["terraform|"+filepath.Dir(u.Dep.File)]
		if lockPath == "" {
			continue
		}
		name := u.Dep.PackageName
		if name == "" {
			name = u.Dep.DepName
		}
		if done[lockPath+"|"+name] {
			continue
		}
		done[lockPath+"|"+name] = true
		hasher, ok := r.datasources["terraform-provider"].(providerHasher)
		if !ok {
			return nil, fmt.Errorf("%s: the terraform-provider datasource computes no lock hashes", lockPath)
		}
		hashes, err := hasher.ProviderHashes(r.ctx, lookup.Ref{Datasource: "terraform-provider", PackageName: name, RegistryURLs: u.Dep.RegistryURLs}, u.NewValue)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", lockPath, name, err)
		}
		lock, err := os.ReadFile(filepath.Join(root, lockPath))
		if err != nil {
			return nil, err
		}
		es, err := terraform.LockEdits(lockPath, lock, name, u.NewValue, hashes)
		if err != nil {
			return nil, err
		}
		edits = append(edits, es...)
	}
	return edits, nil
}
