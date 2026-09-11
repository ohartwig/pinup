// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"io"
)

// exit codes, per docs/tech-spec.md §15. They are part of the CLI contract:
// a caller distinguishes "nothing to do" from "a plan was produced" without
// parsing output.
const (
	codeNothingToDo = 0
	codePlanned     = 0
	codeFailure     = 1
)

// command is one subcommand. Every subcommand takes its own flag set so
// `pinup whatif -h` lists only what whatif accepts.
type command struct {
	name    string
	summary string
	run     func(args []string, out, errw io.Writer) error
}

func commands() []command {
	return []command{
		{"whatif", "resolve and plan without writing anything", cmdWhatif},
		{"run", "plan, then apply and publish", cmdRun},
		{"print-config", "print the resolved config, optionally explained", cmdPrintConfig},
		{"migrate", "convert a renovate config and report what is supported", cmdMigrate},
		{"version", "print the version", cmdVersion},
	}
}

func run(args []string, out, errw io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}
	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(args[1:], out, errw)
		}
	}
	usage(errw)
	return fmt.Errorf("unknown command %q", args[0])
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "pinup keeps dependency references current.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "usage: pinup <command> [flags]")
	fmt.Fprintln(w)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-14s %s\n", c.name, c.summary)
	}
}

func cmdVersion(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(errw)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprintln(out, version)
	return nil
}

// The remaining subcommands are wired in later phases; they exist now so the
// CLI surface is fixed and `usage` cannot drift from what is implemented.

func cmdRun(args []string, out, errw io.Writer) error     { return errNotYet("run") }
func cmdMigrate(args []string, out, errw io.Writer) error { return errNotYet("migrate") }

func errNotYet(name string) error {
	return fmt.Errorf("%s is not implemented yet", name)
}
