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

	"github.com/ohartwig/pinup/config"
)

// cmdPrintConfig prints the resolved configuration - the file with its
// extends expanded - as flattened path lines, as JSON, explained per key, or
// diffed against a captured snapshot. The flattened form is the one the
// parity gate compares; printing it here is what lets a human run the same
// comparison by eye.
func cmdPrintConfig(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("print-config", flag.ContinueOnError)
	fs.SetOutput(errw)
	cfgPath := fs.String("config", "", "configuration file to resolve (required)")
	asJSON := fs.Bool("json", false, "print the resolved document as JSON instead of flattened lines")
	explain := fs.String("explain", "", "print every source that set this path, winner last (print-config notation, e.g. packageRules[25].automerge)")
	diffPath := fs.String("diff", "", "compare against a resolved snapshot (JSON) and print the differing lines")
	runner := fs.String("runner", "", "the runner's configuration a local> extends resolves to: a path, or local>project fetched through the platform")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("print-config: --config is required")
	}
	sources, cleanup, err := runnerChain(context.Background(), *runner, os.Getenv)
	if err != nil {
		return fmt.Errorf("print-config: %w", err)
	}
	defer cleanup()

	r, warnings, err := config.ResolveFile(*cfgPath, sources)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(errw, "warning:", w)
	}
	for _, n := range r.Migrations {
		fmt.Fprintln(errw, "migrated:", n)
	}

	switch {
	case *explain != "":
		ptr := config.PointerOf(*explain)
		fmt.Fprint(out, r.Explain(ptr))
		if _, ok := r.Nearest(ptr); ok {
			return nil
		}
		// A missing key is a different answer from an unset one, and the
		// exit code says which.
		return fmt.Errorf("print-config: %s is set by no source", *explain)

	case *diffPath != "":
		raw, err := os.ReadFile(*diffPath)
		if err != nil {
			return err
		}
		var want map[string]any
		if err := json.Unmarshal(raw, &want); err != nil {
			return fmt.Errorf("%s: %w", *diffPath, err)
		}
		d := config.Diff(config.Flatten(r.Raw), config.Flatten(want))
		for _, l := range d {
			fmt.Fprintln(out, l)
		}
		if len(d) != 0 {
			return fmt.Errorf("print-config: %d lines differ from %s", len(d), *diffPath)
		}
		fmt.Fprintf(errw, "identical: %d lines\n", len(config.Flatten(want)))
		return nil

	case *asJSON:
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(r.Raw)

	default:
		for _, l := range config.Flatten(r.Raw) {
			fmt.Fprintln(out, l)
		}
		return nil
	}
}
