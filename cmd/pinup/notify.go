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

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/wire"
)

// rollingMajorTitle is the one issue the notice lives in; the exact title
// is how it is found again on the next run.
const rollingMajorTitle = "Rolling major references (@N) with a newer major available"

// estateTitle is the issue the estate overview lives in.
const estateTitle = "pinup Estate: every dependency, every version in use"

// cmdNotify renders the estate-wide rolling-major notice from the plans of
// a run and keeps it in one issue of the runner project. It fails when the
// plans contain no bare-major reference at all: a scan that saw none is a
// broken scan, not a clean estate (the verify:rolling-major check).
func cmdNotify(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(errw)
	plansGlob := fs.String("plans", "", "plan reports of the run, a glob such as 'reports/*.json' (required)")
	project := fs.String("project", "", "project that carries the issue, e.g. pinup/runner (required unless --dry-run)")
	dryRun := fs.Bool("dry-run", false, "print the notice; write no issue")
	minRefs := fs.Int("min-refs", 1, "fail when fewer bare-major references than this were found")
	outPath := fs.String("out", "", "with estate: also write the overview to this file")
	// The notice's name comes first, the flags after: `notify rolling-major --plans …`.
	if len(args) == 0 || (args[0] != "rolling-major" && args[0] != "estate") {
		return fmt.Errorf("notify: usage: pinup notify rolling-major|estate --plans 'reports/*.json' --project pinup/runner")
	}
	kind := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *plansGlob == "" {
		return fmt.Errorf("notify: --plans is required")
	}
	paths, err := filepath.Glob(*plansGlob)
	if err != nil {
		return err
	}
	var plans []*model.Plan
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		plan, err := model.ReadPlan(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		plans = append(plans, plan)
	}
	title := rollingMajorTitle
	var body string
	if kind == "estate" {
		// The estate overview: every dependency, every version in use,
		// every repository, from the same plans.
		e := report.EstateOf(plans)
		version := ""
		if len(plans) > 0 {
			version = plans[0].PinupVersion
		}
		body = report.EstateMarkdown(e, version, time.Now())
		title = estateTitle
		fmt.Fprintf(errw, "notify: %d plans, %d repositories, %d dependencies, %d uses (%d current, %d behind, %d held, %d with advisories)\n",
			len(plans), e.Repos, e.Deps, e.Uses, e.Current, e.Behind, e.Held, e.Vulnerable)
		if e.Deps < *minRefs {
			return fmt.Errorf("notify: %d dependencies found, fewer than %d; the scan is not looking at the estate", e.Deps, *minRefs)
		}
		if *outPath != "" {
			if err := os.WriteFile(*outPath, []byte(body), 0o644); err != nil {
				return err
			}
		}
	} else {
		refs, notices := report.RollingMajors(plans)
		body = report.RollingMajorIssue(refs, notices)
		fmt.Fprintf(errw, "notify: %d plans, %d bare-major references, %d with a newer major\n", len(plans), len(refs), notices)
		if len(refs) < *minRefs {
			return fmt.Errorf("notify: %d bare-major references found, fewer than %d; the scan is not looking at the estate", len(refs), *minRefs)
		}
	}
	if *dryRun {
		fmt.Fprint(out, body)
		return nil
	}
	if *project == "" {
		return fmt.Errorf("notify: --project is required to write the issue")
	}
	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if env.Host == "" {
		return fmt.Errorf("notify: the GitLab instance is not known; set PINUP_GITLAB_URL or CI_SERVER_URL")
	}
	platform := wire.Platform(env.URL, env.Token, env.Header)
	ctx := context.Background()
	proj, err := platform.Project(ctx, *project)
	if err != nil {
		return err
	}
	labels := []string{"pinup", "rolling-major"}
	if kind == "estate" {
		labels = []string{"pinup", "estate"}
	}
	issue, changed, err := platform.UpsertIssue(ctx, proj, title, body, labels)
	if err != nil {
		return err
	}
	state := "unchanged"
	if changed {
		state = "updated"
	}
	fmt.Fprintf(out, "%s: issue #%d %s\n", *project, issue.IID, state)
	return nil
}
