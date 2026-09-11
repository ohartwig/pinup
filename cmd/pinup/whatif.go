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
	"git.ole-hartwig.eu/pinup/pinup/model"
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

	plan, err := whatif(context.Background(), *repo, *cfgPath, *name, time.Now())
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
		fmt.Fprintf(errw, "%s: %d dependencies in %d files, %d warnings\n",
			*report, plan.Stats.DepsExtracted, plan.Stats.FilesDiscovered, len(plan.Warnings))
	}
	return nil
}

// whatif runs everything up to the plan. It writes nothing to the repository,
// which is what makes it safe to point at anything.
//
// Lookup and classification are not wired yet, so the plan it produces records
// what is there rather than what should change. That is deliberately still a
// plan rather than a listing: the schema, the ordering and the skip reasons
// are the parts that have to be right before an update can be built on them.
func whatif(ctx context.Context, root, cfgPath, repoName string, now time.Time) (*model.Plan, error) {
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
			// Nothing looks anything up yet, so every dependency is skipped
			// with a reason rather than silently producing no update. A plan
			// whose dependencies simply had no updates would be
			// indistinguishable from one where lookup failed.
			if d.SkipReason == "" {
				d.SkipReason = "lookup is not wired yet, so no releases were fetched"
			}
			plan.Deps = append(plan.Deps, d)
		}
	}

	plan.Stats = model.Stats{
		FilesDiscovered: found.Stats.FilesMatched,
		DepsExtracted:   len(plan.Deps),
	}
	plan.Sort()
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("the plan this run produced is not valid: %w", err)
	}
	return plan, nil
}
