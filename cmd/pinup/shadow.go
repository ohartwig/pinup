// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/publish"
	"git.ole-hartwig.eu/pinup/pinup/report"
	"git.ole-hartwig.eu/pinup/pinup/wire"
)

// cmdShadow compares plan reports with the merge requests Renovate has
// open, joined on the branch name. It exits non-zero on any of the
// failures report.Compare names, and prints matched over total either way.
func cmdShadow(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("shadow", flag.ContinueOnError)
	fs.SetOutput(errw)
	plansGlob := fs.String("plans", "", "plan reports to compare, a glob such as 'reports/*.json' (required)")
	suppressions := fs.String("suppressions", "", "suppressions file: triaged only-pinup entries with reason, owner and expiry")
	controls := fs.String("controls", "pinup/shadow-fixture", "comma-separated projects that must yield exactly one only-pinup entry")
	reportPath := fs.String("report", "", "write the comparison as JSON to this path")
	prefix := fs.String("prefix", "renovate/", "branch prefix of the other tool's merge requests")
	statePath := fs.String("state", "", "file carrying the previous comparison's differences; a difference fails only when it persists from one run to the next")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *plansGlob == "" {
		return fmt.Errorf("shadow: --plans is required")
	}
	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if env.Host == "" {
		return fmt.Errorf("shadow: the GitLab instance is not known; set PINUP_GITLAB_URL or CI_SERVER_URL")
	}
	platform := wire.Platform(env.URL, env.Token, env.Header)
	ctx := context.Background()

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

	// The other side, per project. A project that cannot be read is left
	// out of the map, which Compare reports as unreadable rather than as
	// having no merge requests.
	open := map[string][]publish.MergeRequest{}
	for _, p := range plans {
		proj, err := platform.Project(ctx, p.Repo.Path)
		if err != nil {
			fmt.Fprintf(errw, "%s: %v\n", p.Repo.Path, err)
			continue
		}
		mrs, err := platform.OpenMergeRequests(ctx, proj, *prefix)
		if err != nil {
			fmt.Fprintf(errw, "%s: %v\n", p.Repo.Path, err)
			continue
		}
		// The requests Renovate had merged in the last day too: a branch
		// one lifecycle further along is a match, not a miss.
		done, err := platform.MergedMergeRequests(ctx, proj, *prefix, time.Now().Add(-24*time.Hour))
		if err != nil {
			fmt.Fprintf(errw, "%s: %v\n", p.Repo.Path, err)
			continue
		}
		open[p.Repo.Path] = append(mrs, done...)
	}

	sup, err := report.LoadSuppressions(*suppressions)
	if err != nil {
		return fmt.Errorf("suppressions: %w", err)
	}
	var controlList []string
	for _, c := range strings.Split(*controls, ",") {
		if c = strings.TrimSpace(c); c != "" {
			controlList = append(controlList, c)
		}
	}
	prev, err := report.LoadState(*statePath)
	if err != nil {
		return fmt.Errorf("shadow: state: %w", err)
	}
	res, next := report.Compare(plans, open, sup, controlList, version, time.Now(), prev)
	if *statePath != "" {
		if err := next.Save(*statePath); err != nil {
			return fmt.Errorf("shadow: state: %w", err)
		}
	}

	fmt.Fprint(out, res.String())
	fmt.Fprintln(out, res.Summary())
	if *reportPath != "" {
		raw, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(*reportPath, append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	if !res.Passed() {
		for _, f := range res.Failures {
			fmt.Fprintln(errw, "shadow:", f)
		}
		return fmt.Errorf("shadow: %d failures", len(res.Failures))
	}
	return nil
}
