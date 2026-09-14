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
	"slices"
	"sort"
	"time"

	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/wire"
)

// advisoriesReport is what `pinup advisories` writes: every advisory it
// had not seen before, with the repositories and dependencies it affects,
// so the watch job can start one targeted run per repository.
type advisoriesReport struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Queried     int              `json:"queried"`
	Findings    int              `json:"findings"`
	New         []advisoryHit    `json:"new"`
	Warnings    []string         `json:"warnings,omitempty"`
	Control     *advisoryControl `json:"control,omitempty"`
}

// advisoryHit is one advisory against one dependency, with its consumers.
type advisoryHit struct {
	Advisory     string    `json:"advisory"`
	Aliases      []string  `json:"aliases,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Severity     string    `json:"severity,omitempty"`
	Published    time.Time `json:"published"`
	Datasource   string    `json:"datasource"`
	PackageName  string    `json:"packageName"`
	Version      string    `json:"version"`
	Fixed        string    `json:"fixed,omitempty"`
	Repositories []string  `json:"repositories"`
}

// advisoryControl is the watch's own check: the control repository's
// known-vulnerable dependencies must yield advisories, or the watch is not
// watching.
type advisoryControl struct {
	Repository string `json:"repository"`
	Queried    int    `json:"queried"`
	Findings   int    `json:"findings"`
}

// advisoriesState is the file between runs: every advisory seen, keyed
// "id|datasource|package", so a run reports each once.
type advisoriesState struct {
	Seen map[string]time.Time `json:"seen"`
}

// cmdAdvisories asks OSV about every dependency the consumer index carries
// - no clone, one batch of queries per thousand - and reports the
// advisories it has not reported before. The watch job runs it every few
// minutes and starts a targeted run for each repository named. It exits
// with an error when the control repository yields nothing: an empty
// answer is a broken watch, not a safe estate (plan.md §6.5).
func cmdAdvisories(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("advisories", flag.ContinueOnError)
	fs.SetOutput(errw)
	indexPath := fs.String("index", ".pinup/consumers.json", "the consumer index a full run wrote")
	statePath := fs.String("state", ".pinup/advisories.json", "advisories already reported; written back after the run")
	reportPath := fs.String("report", "", "write the report as JSON to this path")
	control := fs.String("control", "", "a repository with known-vulnerable dependencies that must yield findings, e.g. pinup/shadow-fixture")
	filter := fs.String("filter", "", `report only repositories matching these globs, a JSON list with ! negations`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var only *glob.Set
	if *filter != "" {
		var patterns []string
		if err := json.Unmarshal([]byte(*filter), &patterns); err != nil {
			return fmt.Errorf("--filter: %w", err)
		}
		only = glob.NewSet(patterns)
	}
	idx, err := report.LoadIndex(*indexPath)
	if err != nil {
		return err
	}
	if len(idx.Dependencies) == 0 {
		return fmt.Errorf("advisories: %s carries no dependency versions; a full run since pinup 0.17 writes them", *indexPath)
	}
	state := advisoriesState{Seen: map[string]time.Time{}}
	if raw, err := os.ReadFile(*statePath); err == nil {
		if err := json.Unmarshal(raw, &state); err != nil {
			return fmt.Errorf("advisories: %s: %w", *statePath, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	now := time.Now().UTC()

	rep, err := watchAdvisories(context.Background(), &osv.Client{}, idx, only, *control, &state, now)
	if err != nil {
		return err
	}
	if *reportPath != "" {
		raw, _ := json.MarshalIndent(rep, "", " ")
		if err := os.WriteFile(*reportPath, append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(*statePath, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(errw, "warning: %s\n", w)
	}
	fmt.Fprintf(out, "advisories: %d dependencies queried, %d with findings, %d new\n", rep.Queried, rep.Findings, len(rep.New))
	for _, h := range rep.New {
		fmt.Fprintf(out, "  %s %s %s@%s fixed %q: %v\n", h.Advisory, h.Datasource, h.PackageName, h.Version, h.Fixed, h.Repositories)
	}
	if rep.Control != nil && rep.Control.Findings == 0 {
		return fmt.Errorf("advisories: the control %s yielded no finding over %d dependencies; the watch is not watching", rep.Control.Repository, rep.Control.Queried)
	}
	return nil
}

// watchAdvisories is cmdAdvisories without the files: the index in, the
// report out, the state updated.
func watchAdvisories(ctx context.Context, client *osv.Client, idx *report.Index, only *glob.Set, control string, state *advisoriesState, now time.Time) (*advisoriesReport, error) {
	// One query per distinct dependency; the repositories that carry it
	// are remembered for the report.
	type key struct{ datasource, pkg, version, versioning string }
	consumers := map[key][]string{}
	var order []key
	repos := make([]string, 0, len(idx.Dependencies))
	for r := range idx.Dependencies {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, r := range repos {
		if only != nil && !only.Match(r) && r != control {
			continue
		}
		for _, d := range idx.Dependencies[r] {
			if d.Version == "" {
				continue
			}
			k := key{d.Datasource, d.PackageName, d.Version, d.Versioning}
			if _, seen := consumers[k]; !seen {
				order = append(order, k)
			}
			consumers[k] = append(consumers[k], r)
		}
	}
	queries := make([]osv.Query, 0, len(order))
	for _, k := range order {
		queries = append(queries, osv.Query{Datasource: k.datasource, PackageName: k.pkg, Version: k.version, Versioning: k.versioning})
	}
	findings, err := client.Check(ctx, wire.Versionings(), queries)
	if err != nil {
		return nil, fmt.Errorf("advisories: %w", err)
	}
	// Queried counts what OSV was asked: a dependency of a datasource
	// OSV has no ecosystem for, or whose value is a range, is not.
	rep := &advisoriesReport{GeneratedAt: now}
	var ctl *advisoryControl
	if control != "" {
		ctl = &advisoryControl{Repository: control}
		rep.Control = ctl
	}
	for i, f := range findings {
		k := order[i]
		rs := consumers[k]
		isControl := ctl != nil && slices.Contains(rs, control)
		if isControl && f.Ecosystem != "" && len(f.Warnings) == 0 {
			ctl.Queried++
		}
		rep.Warnings = append(rep.Warnings, f.Warnings...)
		if f.Ecosystem == "" || len(f.Warnings) > 0 {
			continue
		}
		rep.Queried++
		if len(f.Advisories) == 0 {
			continue
		}
		rep.Findings++
		if isControl {
			ctl.Findings++
		}
		for _, a := range f.Advisories {
			id := a.ID + "|" + k.datasource + "|" + k.pkg
			if _, seen := state.Seen[id]; seen {
				continue
			}
			state.Seen[id] = now
			affected := make([]string, 0, len(rs))
			for _, r := range rs {
				if only == nil || only.Match(r) {
					affected = append(affected, r)
				}
			}
			if len(affected) == 0 {
				continue
			}
			rep.New = append(rep.New, advisoryHit{
				Advisory: a.ID, Aliases: a.Aliases, Summary: a.Summary, Severity: a.Severity, Published: a.Published,
				Datasource: k.datasource, PackageName: k.pkg, Version: k.version, Fixed: a.Fixed, Repositories: affected,
			})
		}
	}
	sort.Slice(rep.New, func(i, j int) bool {
		if rep.New[i].PackageName != rep.New[j].PackageName {
			return rep.New[i].PackageName < rep.New[j].PackageName
		}
		return rep.New[i].Advisory < rep.New[j].Advisory
	})
	return rep, nil
}
