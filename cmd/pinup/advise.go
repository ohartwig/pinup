// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ohartwig/pinup/advise"
	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/wire"
)

// cmdAdvise resolves a configuration as a run would and reports what to
// change - performance, security, hygiene, compatibility - each finding
// with its pointer, the layer that wrote the value and, where the file owns
// it, a fix. With --plan it also reads what runs found. With --fix it
// applies the fixes to the file, byte for byte, and writes the result only
// where the caller says and only after the resolution has been checked to
// change exactly where the fixes declared.
func cmdAdvise(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("advise", flag.ContinueOnError)
	fs.SetOutput(errw)
	cfgPath := fs.String("config", "", "configuration file to analyse (required)")
	runner := fs.String("runner", "", "the runner's configuration a local> extends resolves to: a path, or local>project fetched through the platform")
	asJSON := fs.Bool("json", false, "print the findings as JSON")
	strict := fs.Bool("strict", false, "exit 1 when any finding is an error")
	fix := fs.Bool("fix", false, "apply the fixes to the file in memory and verify the result; a dry run unless --out or --write")
	outPath := fs.String("out", "", "with --fix: write the fixed file here")
	write := fs.Bool("write", false, "with --fix: write the fixed file in place")
	var plans multiFlag
	fs.Var(&plans, "plan", "a plan whatif wrote, for the checks that read what runs found (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("advise: --config is required")
	}
	if (*outPath != "" || *write) && !*fix {
		return fmt.Errorf("advise: --out and --write need --fix")
	}
	if *outPath != "" && *write {
		return fmt.Errorf("advise: --out and --write exclude each other")
	}
	sources, cleanup, err := runnerChain(context.Background(), *runner, os.Getenv)
	if err != nil {
		return fmt.Errorf("advise: %w", err)
	}
	defer cleanup()

	layer, err := config.LoadFile(*cfgPath)
	if err != nil {
		return err
	}
	in, err := advise.Load(layer, sources, wire.Versionings())
	if err != nil {
		return fmt.Errorf("advise: %w", err)
	}
	in.Covers = wire.Covers
	in.Datasources = wire.DatasourceNames(in.Decoded.CustomDatasources)
	for _, p := range plans {
		f, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("advise: --plan %s: %w", p, err)
		}
		plan, err := model.ReadPlan(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("advise: --plan %s: %w", p, err)
		}
		in.Plans = append(in.Plans, plan)
	}
	findings, skipped := advise.Run(in, advise.Catalogue())

	report := adviceReport{File: *cfgPath, Plans: plans, Skipped: skipped, Findings: findings, Counts: map[advise.Severity]int{}}
	for _, f := range findings {
		report.Counts[f.Severity]++
	}
	if *fix {
		if err := applyFixes(&report, layer, sources, findings, *outPath, *write); err != nil {
			return fmt.Errorf("advise: %w", err)
		}
	}
	if *asJSON {
		if err := json.MarshalWrite(out, report, jsontext.WithIndent("  ")); err != nil {
			return err
		}
	} else {
		report.print(out)
	}
	if *strict && report.Counts[advise.Error] > 0 {
		return fmt.Errorf("advise: %d errors", report.Counts[advise.Error])
	}
	return nil
}

// adviceReport is the command's whole output, printed as text or JSON.
type adviceReport struct {
	File     string                  `json:"file"`
	Plans    []string                `json:"plans"`
	Skipped  []string                `json:"skipped"`
	Counts   map[advise.Severity]int `json:"counts"`
	Findings []advise.Finding        `json:"findings"`
	// The --fix half; absent without it.
	Fix *fixReport `json:"fix,omitempty"`
}

type fixReport struct {
	Applied []advise.Applied `json:"applied"`
	Skipped []advise.Skipped `json:"skipped"`
	// Wrote is where the result went, "" for a dry run.
	Wrote string `json:"wrote"`
}

// applyFixes rewrites the file in memory with every fix the findings carry,
// runs the gate, and writes the result where asked - after the gate, never
// before. A refusal is an error and nothing is written.
func applyFixes(r *adviceReport, layer config.Layer, sources preset.Source, findings []advise.Finding, outPath string, write bool) error {
	path := strings.TrimPrefix(layer.Source, "file:")
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fixes []advise.Fix
	for _, f := range findings {
		if f.Fix != nil {
			fixes = append(fixes, *f.Fix)
		}
	}
	after, applied, skipped, err := advise.Apply(src, path, fixes)
	if err != nil {
		return err
	}
	r.Fix = &fixReport{Applied: applied, Skipped: skipped}
	if len(applied) == 0 {
		return nil
	}
	var done []advise.Fix
	for _, a := range applied {
		done = append(done, a.Fix)
	}
	if err := advise.Verify(src, after, path, sources, done); err != nil {
		return err
	}
	switch {
	case outPath != "":
		if err := os.WriteFile(outPath, after, 0o644); err != nil {
			return err
		}
		r.Fix.Wrote = outPath
	case write:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, after, info.Mode().Perm()); err != nil {
			return err
		}
		r.Fix.Wrote = path
	}
	return nil
}

// print renders the report: findings grouped by severity, then category,
// each with its message, its fix and the layer that wrote the value.
func (r adviceReport) print(w io.Writer) {
	fmt.Fprintf(w, "%s", r.File)
	if len(r.Plans) > 0 {
		fmt.Fprintf(w, "  (%d plans)", len(r.Plans))
	}
	fmt.Fprintln(w)
	for _, sev := range []advise.Severity{advise.Error, advise.Warn, advise.Info} {
		if r.Counts[sev] == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s (%d)\n", sev, r.Counts[sev])
		var cat advise.Category
		for _, f := range r.Findings {
			if f.Severity != sev {
				continue
			}
			if f.Category != cat {
				cat = f.Category
				fmt.Fprintf(w, "  %s\n", cat)
			}
			fmt.Fprintf(w, "    %s  %s\n      %s\n", f.ID, f.Pointer, f.Msg)
			if f.Fix != nil {
				fmt.Fprintf(w, "      fix:  %s\n", describeFix(*f.Fix))
			}
			fmt.Fprintf(w, "      from: %s\n", describeOrigin(f.Origin))
		}
	}
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "\nnothing to change")
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(w, "\nskipped: %d plan checks (no --plan)\n", len(r.Skipped))
	}
	if r.Fix == nil {
		return
	}
	fmt.Fprintf(w, "\nfixes: %d applied, %d skipped\n", len(r.Fix.Applied), len(r.Fix.Skipped))
	for _, a := range r.Fix.Applied {
		fmt.Fprintf(w, "  applied  %-7s %s  line %d\n", a.Fix.Op, a.Fix.Pointer, a.Line)
	}
	for _, s := range r.Fix.Skipped {
		fmt.Fprintf(w, "  skipped  %-7s %s  %s\n", s.Fix.Op, s.Fix.Pointer, s.Reason)
	}
	switch {
	case len(r.Fix.Applied) == 0:
	case r.Fix.Wrote != "":
		fmt.Fprintf(w, "verified: the resolution changed only where the fixes declared; wrote %s\n", r.Fix.Wrote)
	default:
		fmt.Fprintln(w, "verified: the resolution changed only where the fixes declared")
		fmt.Fprintln(w, "dry run, nothing written (--write or --out <path> writes)")
	}
}

func describeFix(f advise.Fix) string {
	switch f.Op {
	case advise.OpRemove:
		return "remove " + f.Pointer
	case advise.OpAppend:
		return fmt.Sprintf("append %s to %s", jsonText(f.Value), f.Pointer)
	}
	return fmt.Sprintf("set %s = %s", f.Pointer, jsonText(f.Value))
}

func jsonText(v any) string {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func describeOrigin(o model.Origin) string {
	if o.Rule != model.NoRule {
		return fmt.Sprintf("%s (packageRules[%d])", o.Source, o.Rule)
	}
	return o.Source
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
