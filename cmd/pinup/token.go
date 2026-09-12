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

// cmdToken renews the bot's own token before it expires: `pinup token
// rotate --scope groups/1210 --variables PINUP_GITLAB_TOKEN,GITLAB_TOKEN`.
// It runs in the runner's pipeline before the scans, so a rotated value is
// what the scan jobs start with. The job fails - not warns - when the
// token's expiry cannot be read, when a variable holds another token, when
// the write is refused, and loudest of all when the new value could not be
// stored after the old one was revoked; see platform/gitlab.Rotation.
func cmdToken(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(errw)
	scope := fs.String("scope", "", "owner of the variables, groups/<id> or projects/<id> (required)")
	variables := fs.String("variables", "PINUP_GITLAB_TOKEN,GITLAB_TOKEN", "comma-separated CI/CD variables that carry the token")
	threshold := fs.Int("threshold-days", 7, "rotate when fewer days than this remain")
	lifetime := fs.Int("lifetime-days", 30, "how long the new token lives")
	dryRun := fs.Bool("dry-run", false, "rehearse the write whatever the expiry, rotate nothing")
	if len(args) == 0 || args[0] != "rotate" {
		return fmt.Errorf("token: usage: pinup token rotate --scope groups/<id> [--variables A,B] [--threshold-days 7] [--lifetime-days 30] [--dry-run]")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *scope == "" {
		return fmt.Errorf("token: --scope is required")
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
	var names []string
	for _, v := range strings.Split(*variables, ",") {
		if v = strings.TrimSpace(v); v != "" {
			names = append(names, v)
		}
	}
	rot := gitlab.Rotation{
		Platform:  gitlab.New(env.URL, nil, gitlab.Token{Value: env.Token, Header: env.Header}),
		Scope:     *scope,
		Variables: names,
		Threshold: time.Duration(*threshold) * 24 * time.Hour,
		Lifetime:  time.Duration(*lifetime) * 24 * time.Hour,
		DryRun:    *dryRun,
		Out:       out,
		Sleep:     time.Sleep,
	}
	return rot.Run(context.Background(), time.Now())
}
