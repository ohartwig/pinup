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

	"git.ole-hartwig.eu/pinup/pinup/config"
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("whatif: --config is required")
	}

	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	opts := whatifOptions{
		Root: *repo, ConfigPath: *cfgPath, RepoName: *name, Now: time.Now(),
		Datasources: wire.Datasources(httpClient(env), env.URL),
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

// whatifOptions is everything a plan run needs. Datasources is injected so a
// test can hand in a fake registry and prove the run touched no network.
type whatifOptions struct {
	Root        string
	ConfigPath  string
	RepoName    string
	Now         time.Time
	Datasources lookup.Registry
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
	decoded, resolved, err := config.DecodeFile(cfgPath)
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

	// Every unique (datasource, package, registry) once, however many
	// dependencies share it - the same component pinned in three jobs is
	// one round trip.
	fetcher := &lookup.Fetcher{Registry: o.Datasources}
	results := fetcher.Fetch(ctx, plan.Deps)
	for _, r := range results {
		if r.Warning != nil {
			plan.Warnings = append(plan.Warnings, *r.Warning)
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
	for _, u := range planned.Updates {
		plan.Updates = append(plan.Updates, applyUpdateRules(engine, resolved.Raw, u))
	}

	blocked := 0
	for _, u := range plan.Updates {
		if u.Blocked() {
			blocked++
		}
	}
	plan.Stats = model.Stats{
		FilesDiscovered: found.Stats.FilesMatched,
		DepsExtracted:   len(plan.Deps),
		LookupsIssued:   len(results),
		UpdatesFound:    len(plan.Updates),
		UpdatesBlocked:  blocked,
	}
	plan.Sort()
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("the plan this run produced is not valid: %w", err)
	}
	return plan, nil
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

// applyUpdateRules is the per-update pass. A rule that disables the update
// type, or demands dashboard approval, holds the update rather than
// deleting it: the plan still says what would have happened and why not.
func applyUpdateRules(engine *rules.Engine, base map[string]any, u model.Update) model.Update {
	res := engine.Apply(base, rules.SubjectOf(u.Dep, u.Type.String()))
	if res.SkipReason != "" {
		u.Blocks = append(u.Blocks, model.Block{
			Reason: model.BlockDisabled,
			Org:    origin(res, "enabled"),
			Note:   "enabled: false for this update type",
		})
	}
	if approval, ok := res.Config["dependencyDashboardApproval"].(bool); ok && approval {
		u.Blocks = append(u.Blocks, model.Block{
			Reason: model.BlockDashboardApproval,
			Org:    origin(res, "dependencyDashboardApproval"),
		})
	}
	if len(u.Blocks) > 0 {
		u.SuppressedBy = u.Blocks[0].Reason
	}
	return u
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
