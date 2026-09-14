// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
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
		{"shadow", "compare plan reports with the merge requests Renovate has open", cmdShadow},
		{"notify", "keep the estate-wide rolling-major notice in one issue (notify rolling-major)", cmdNotify},
		{"advisories", "ask OSV about every dependency the index carries and report the new advisories, no clone", cmdAdvisories},
		{"migrate", "convert a renovate config and report what is supported", cmdMigrate},
		{"token", "renew the bot's own token before it expires and store it (token rotate)", cmdToken},
		{"version", "print the version", cmdVersion},
		// askpass is git's credential prompt, answered from the environment.
		// Listed so `pinup askpass` is discoverable, though nobody types it.
		{"askpass", "answer git's credential prompt from PINUP_GITLAB_TOKEN (used as GIT_ASKPASS)", cmdAskpass},
	}
}

func run(args []string, out, errw io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}
	// git invokes GIT_ASKPASS with the prompt as the only argument, no
	// subcommand: "Username for 'https://...': ". Recognise that shape so
	// this binary can be the askpass program as it is.
	if strings.HasPrefix(args[0], "Username for") || strings.HasPrefix(args[0], "Password for") {
		return cmdAskpass(args, out, errw)
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

// cmdAskpass is what git calls when it needs a credential and GIT_ASKPASS
// points at this binary. The token never touches a file or a URL: git
// asks, the environment answers. The username is whatever the token kind
// wants - "gitlab-ci-token" for a job token, "oauth2" otherwise - and the
// password is the token.
func cmdAskpass(args []string, out, errw io.Writer) error {
	prompt := strings.Join(args, " ")
	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if env.Token == "" {
		return fmt.Errorf("askpass: no token in the environment")
	}
	// The prompt names the URL git is talking to ("Password for
	// 'https://oauth2@git.example.org': "). The token is for one host;
	// a prompt for any other gets nothing, whatever led git there.
	if !promptNamesHost(prompt, env.Host) {
		return fmt.Errorf("askpass: the prompt is not for %s; no credential is given", env.Host)
	}
	switch {
	case strings.HasPrefix(strings.ToLower(prompt), "username"):
		if env.Header == "JOB-TOKEN" {
			fmt.Fprintln(out, "gitlab-ci-token")
		} else {
			fmt.Fprintln(out, "oauth2")
		}
	default:
		fmt.Fprintln(out, env.Token)
	}
	return nil
}

// promptNamesHost tells whether git's credential prompt is for host: the
// quoted URL in it has that host, with or without a user part.
func promptNamesHost(prompt, host string) bool {
	if host == "" {
		return false
	}
	i := strings.Index(prompt, "'")
	j := strings.LastIndex(prompt, "'")
	if i < 0 || j <= i {
		return false
	}
	u, err := url.Parse(prompt[i+1 : j])
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, host)
}
