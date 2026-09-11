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

	env := platformFromEnv(os.Getenv)
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
// config → discover → extract → lookup → plan. Rules are not applied yet, so
// what comes out is the default policy's view: every newer release the
// scheme accepts, separated into a minor and a major update. Once the rules
// engine exists it filters and reshapes this; it does not replace it.
func whatif(ctx context.Context, o whatifOptions) (*model.Plan, error) {
	root, cfgPath, repoName, now := o.Root, o.ConfigPath, o.RepoName, o.Now
	decoded, resolved, err := config.DecodeFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	_ = resolved

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
			plan.Deps = append(plan.Deps, d)
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
	plan.Updates = planned.Updates
	plan.Warnings = append(plan.Warnings, planned.Warnings...)

	plan.Stats = model.Stats{
		FilesDiscovered: found.Stats.FilesMatched,
		DepsExtracted:   len(plan.Deps),
		LookupsIssued:   len(results),
		UpdatesFound:    len(plan.Updates),
	}
	plan.Sort()
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("the plan this run produced is not valid: %w", err)
	}
	return plan, nil
}
