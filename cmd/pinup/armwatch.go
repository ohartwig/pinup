// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/platform/gitlab"
	"github.com/ohartwig/pinup/publish"
	"github.com/ohartwig/pinup/runner"
)

// cmdArmWatch arms automerge on the bot's open requests whose description
// asks for it (runner.ArmWatch) - no clone, no plan, a handful of calls per
// request. The runner starts it every ten minutes, beside the release
// watch: a request whose pipeline GitLab created after the run had stopped
// waiting is armed within minutes instead of at the next run that happens
// not to push.
//
// It fails when the listing fails or a request could not be read or armed;
// a request that is not ready is said, not failed - the next pass asks
// again.
func cmdArmWatch(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("arm-watch", flag.ContinueOnError)
	fs.SetOutput(errw)
	label := fs.String("label", "pinup", "the label every merge request of the bot carries")
	filter := fs.String("filter", "", `act only on projects matching these globs, a JSON list with ! negations (the runner's live filter)`)
	author := fs.String("author-email", os.Getenv("PINUP_GIT_EMAIL"), "the bot's commit email; a request with a commit by anyone else is left alone (default $PINUP_GIT_EMAIL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *author == "" {
		return fmt.Errorf("arm-watch: --author-email (or PINUP_GIT_EMAIL) is required: without it no request can be told from one a person took over")
	}
	var only *glob.Set
	if *filter != "" {
		var patterns []string
		if err := json.Unmarshal([]byte(*filter), &patterns); err != nil {
			return fmt.Errorf("--filter: %w", err)
		}
		only = glob.NewSet(patterns)
	}
	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if env.Kind != "gitlab" || env.Host == "" || env.Header != "PRIVATE-TOKEN" {
		return fmt.Errorf("arm-watch: a GitLab instance and the bot's access token are required: set PINUP_GITLAB_URL (or CI_SERVER_URL) and PINUP_GITLAB_TOKEN")
	}
	platform := gitlab.New(env.URL, nil, gitlab.Token{Value: env.Token, Header: env.Header})
	return armWatch(context.Background(), platform, platform.Calls, runner.ArmWatchOptions{Label: *label, Only: only, AuthorEmail: *author}, out)
}

// armWatch is cmdArmWatch without the environment: one pass, one line per
// request, then the counts and what the pass cost in API calls.
func armWatch(ctx context.Context, w publish.ArmWatcher, calls func() int64, o runner.ArmWatchOptions, out io.Writer) error {
	results, err := runner.ArmWatch(ctx, w, o)
	if err != nil {
		return fmt.Errorf("arm-watch: %w", err)
	}
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Action]++
		fmt.Fprintln(out, "  "+r.String())
	}
	fmt.Fprintf(out, "arm-watch: %d open requests labelled %s in scope: %d armed, %d merged, %d not yet, %d refused, %d skipped, %d failed; %d API calls\n",
		len(results), o.Label, counts["armed"], counts["merged"], counts["not-yet"], counts["refused"], counts["skipped"], counts["failed"], calls())
	if counts["failed"] > 0 {
		return fmt.Errorf("arm-watch: %d request(s) could not be read or armed", counts["failed"])
	}
	return nil
}
