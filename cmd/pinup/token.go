// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/platform/gitlab"
)

// cmdToken renews the bot's tokens before they expire: `pinup token
// rotate --scope groups/1210 --variables PINUP_GITLAB_TOKEN,GITLAB_TOKEN`.
// A variable may carry its own scope (`projects/827:PINUP_GITLAB_TOKEN`)
// when the token lives in several places, and `--also
// PINUP_READ_TOKEN=projects/827:PINUP_READ_TOKEN` names a further token
// of the same account - one the dry-run partitions carry with read_api -
// read from that environment variable, rotated by id with this run's
// token and stored in its own targets. One token for everything, or one
// per role: both shapes are one command line.
//
// It runs in the runner's pipeline before the scans, so a rotated value is
// what the scan jobs start with. The job fails - not warns - when a
// token's expiry cannot be read, when a variable holds another token, when
// the write is refused, and loudest of all when a new value could not be
// stored after the old one was revoked; see platform/gitlab.Rotation.
func cmdToken(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(errw)
	scope := fs.String("scope", "", "owner of the variables without a scope of their own, groups/<id> or projects/<id>")
	variables := fs.String("variables", "PINUP_GITLAB_TOKEN,GITLAB_TOKEN", "comma-separated CI/CD variables that carry the token, each optionally as <scope>:<KEY>")
	var also alsoFlags
	fs.Var(&also, "also", "a further token: <ENV>=<scope>:<KEY>[,<scope>:<KEY>] (repeatable)")
	threshold := fs.Int("threshold-days", 7, "rotate when fewer days than this remain")
	lifetime := fs.Int("lifetime-days", 30, "how long the new token lives")
	dryRun := fs.Bool("dry-run", false, "rehearse the write whatever the expiry, rotate nothing")
	if len(args) == 0 || args[0] != "rotate" {
		return fmt.Errorf("token: usage: pinup token rotate [--scope groups/<id>] [--variables A,<scope>:B] [--also ENV=<scope>:KEY] [--threshold-days 7] [--lifetime-days 30] [--dry-run]")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *lifetime <= *threshold {
		return fmt.Errorf("token: --lifetime-days %d must exceed --threshold-days %d, or every run rotates", *lifetime, *threshold)
	}
	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if env.Host == "" || env.Token == "" || env.Header != "PRIVATE-TOKEN" {
		return fmt.Errorf("token: a personal access token and its instance are required: set PINUP_GITLAB_URL (or CI_SERVER_URL) and PINUP_GITLAB_TOKEN; a job token cannot rotate itself")
	}
	targets, err := parseTargets(*variables, *scope)
	if err != nil {
		return fmt.Errorf("token: --variables: %w", err)
	}
	var others []gitlab.Other
	for _, a := range also {
		name, spec, ok := strings.Cut(a, "=")
		if !ok || name == "" {
			return fmt.Errorf("token: --also %q: want <ENV>=<scope>:<KEY>", a)
		}
		ts, err := parseTargets(spec, "")
		if err != nil {
			return fmt.Errorf("token: --also %s: %w", name, err)
		}
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("token: --also %s: the variable is not set in this environment", name)
		}
		others = append(others, gitlab.Other{Name: name, Value: value, Targets: ts})
	}
	rot := gitlab.Rotation{
		Platform:  gitlab.New(env.URL, nil, gitlab.Token{Value: env.Token, Header: env.Header}),
		Targets:   targets,
		Others:    others,
		Threshold: time.Duration(*threshold) * 24 * time.Hour,
		Lifetime:  time.Duration(*lifetime) * 24 * time.Hour,
		DryRun:    *dryRun,
		Out:       out,
		Sleep:     time.Sleep,
	}
	return rot.Run(context.Background(), time.Now())
}

// parseTargets reads "A,B,projects/827:C": a bare key lives under the
// default scope, which must then be given.
func parseTargets(spec, defaultScope string) ([]gitlab.Target, error) {
	var out []gitlab.Target
	for _, v := range strings.Split(spec, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		scope, key, ok := strings.Cut(v, ":")
		if !ok {
			scope, key = defaultScope, v
		}
		if scope == "" {
			return nil, fmt.Errorf("%q has no scope and --scope is not set", v)
		}
		if !strings.HasPrefix(scope, "groups/") && !strings.HasPrefix(scope, "projects/") {
			return nil, fmt.Errorf("%q: a scope is groups/<id> or projects/<id>", v)
		}
		out = append(out, gitlab.Target{Scope: scope, Key: key})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no variables named")
	}
	return out, nil
}

// alsoFlags collects a repeatable --also.
type alsoFlags []string

func (a *alsoFlags) String() string     { return strings.Join(*a, " ") }
func (a *alsoFlags) Set(v string) error { *a = append(*a, v); return nil }
