// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ohartwig/pinup/plugin"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ohartwig/pinup/cache"
	"github.com/ohartwig/pinup/changelog"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/git"
	"github.com/ohartwig/pinup/glob"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/osv"
	"github.com/ohartwig/pinup/publish"
	"github.com/ohartwig/pinup/report"
	"github.com/ohartwig/pinup/runner"
	"github.com/ohartwig/pinup/wire"
)

// cmdRun plans, then executes: the branches the plan carries are pushed
// and their merge requests opened or updated. --dry-run stops after the
// plan, which makes it whatif with the platform's view of existing merge
// requests folded in.
//
// The repository is either a checkout (--repo <dir>) or a project path
// (--project group/name), which is cloned into a temporary directory with
// the environment's token answering git's credential prompt through this
// binary's own askpass - the token never lands in a file or a URL.
func cmdRun(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(errw)
	repoDir := fs.String("repo", "", "path to an existing checkout with an origin remote")
	// --dir is the spelling yasrt uses for the same thing; one common word.
	fs.StringVar(repoDir, "dir", "", "alias for --repo")
	project := fs.String("project", "", "project path to clone and run against, e.g. devops/images/ci-tools")
	autodiscover := fs.String("autodiscover", "", `run against every project the token can see that matches these patterns, a JSON list of globs with ! negations, e.g. '["devops/images/**", "!devops/images/pinup"]'`)
	released := fs.String("released", "", "the fast lane: a project path that was just released, optionally @version; runs only its consumers from the index, only for that dependency, with fresh lookups")
	indexPath := fs.String("index", "", "path of the consumer index (default: consumers.json beside --cache)")
	cfgPath := fs.String("config", "", "configuration file to resolve (required)")
	reportPath := fs.String("report", "", "write the plan as JSON to this path; with several projects a %s in it becomes the project path")
	cachePath := fs.String("cache", os.Getenv("PINUP_CACHE"), "path of the lookup cache file (bbolt)")
	cacheTTL := fs.Duration("cache-ttl", time.Hour, "how long a cached lookup counts as fresh")
	dryRun := fs.Bool("dry-run", false, "plan only; push nothing, open nothing")
	baseBranch := fs.String("base", "", "plan and branch from this branch instead of the project's default branch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("run: --config is required")
	}
	chosen := 0
	for _, v := range []string{*repoDir, *project, *autodiscover, *released} {
		if v != "" {
			chosen++
		}
	}
	// --released takes --autodiscover as a restriction on its consumers:
	// the fast lane of a live partition acts on the projects that are
	// live and leaves the others to the dry run. Elsewhere the four are
	// exclusive.
	if *released != "" && *autodiscover != "" {
		chosen--
	}
	if chosen != 1 {
		return fmt.Errorf("run: exactly one of --repo, --project, --autodiscover and --released is required (--released may add --autodiscover as a filter)")
	}
	if *indexPath == "" && *cachePath != "" {
		*indexPath = filepath.Join(filepath.Dir(*cachePath), "consumers.json")
	}
	env, err := platformFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if env.Host == "" {
		return fmt.Errorf("run: the GitLab instance is not known; set PINUP_GITLAB_URL or CI_SERVER_URL")
	}
	// A dry run commits nothing, so it needs no author and no key; the
	// shadow phase runs that way with none provisioned.
	identity, signing, err := gitIdentityFromEnv(os.Getenv)
	if err != nil && !*dryRun {
		return err
	}
	ctx := context.Background()
	now := time.Now()

	platform := wire.Platform(env.URL, env.Token, env.Header)

	// --config may name the runner's file on the platform rather than a
	// path: "local>devops/renovate-runner". The job that runs pinup against
	// its own repository has no checkout of the runner project, and a copy
	// of the file would be a second truth.
	runnerDefault := ""
	runnerProj := runnerProject(os.Getenv, *cfgPath)
	if strings.HasPrefix(*cfgPath, "local>") {
		local, err := fetchConfig(ctx, platform, *cfgPath)
		if err != nil {
			return err
		}
		defer os.Remove(local)
		// The alias the repositories extend is the project's default.json,
		// whichever file of that project this run is configured with:
		// release-fast.json extends it too.
		project, path, _, _ := preset.ParseLocal(strings.TrimPrefix(*cfgPath, "local>"))
		if path != "default.json" {
			def, err := fetchConfig(ctx, platform, "local>"+project)
			if err != nil {
				return err
			}
			defer os.Remove(def)
			runnerDefault = def
		}
		*cfgPath = local
	} else if def := filepath.Join(filepath.Dir(*cfgPath), "default.json"); filepath.Base(*cfgPath) != "default.json" {
		if _, err := os.Stat(def); err == nil {
			runnerDefault = def
		}
	}

	var store lookup.Cache
	if *cachePath != "" {
		s, err := cache.Open(*cachePath)
		if err != nil {
			return fmt.Errorf("cache: %w", err)
		}
		defer s.Close()
		store = s
	}
	one := &runOptions{
		cfgPath: *cfgPath, runnerDefault: runnerDefault, runnerProject: runnerProj, cache: store, cacheTTL: *cacheTTL, dryRun: *dryRun, env: env,
		client: httpClient(env), identity: identity, signing: signing, platform: platform, now: now, base: *baseBranch,
		dashboardTitle: dashboardTitle(os.Getenv),
	}
	dsOpts, err := datasourceOptions(env, os.Getenv)
	if err != nil {
		return err
	}
	one.datasources = wire.Datasources(one.client, dsOpts)
	one.dsOptions = dsOpts
	if *indexPath != "" {
		idx, err := report.LoadIndex(*indexPath)
		if err != nil {
			return fmt.Errorf("index: %w", err)
		}
		one.index, one.indexPath = idx, *indexPath
	}

	if *released != "" {
		var only *glob.Set
		if *autodiscover != "" {
			var patterns []string
			if err := json.Unmarshal([]byte(*autodiscover), &patterns); err != nil {
				return fmt.Errorf("--autodiscover: %w", err)
			}
			only = glob.NewSet(patterns)
		}
		return runReleased(ctx, one, *released, only, *reportPath, out, errw)
	}

	if *autodiscover != "" {
		var patterns []string
		if err := json.Unmarshal([]byte(*autodiscover), &patterns); err != nil {
			return fmt.Errorf("--autodiscover: %w", err)
		}
		all, err := platform.ListProjects(ctx)
		if err != nil {
			return fmt.Errorf("run: listing projects: %w", err)
		}
		filter := glob.NewSet(patterns)
		var selected []string
		for _, p := range all {
			if filter.Match(p) {
				selected = append(selected, p)
			}
		}
		fmt.Fprintf(errw, "autodiscover: %d of %d projects match\n", len(selected), len(all))
		if len(selected) == 0 {
			return fmt.Errorf("run: --autodiscover matched no project; a partition that scans nothing is a broken partition")
		}
		failed := runEach(ctx, one, selected, repositoryConcurrency(os.Getenv), *reportPath, out, errw)
		if failed > 0 {
			return fmt.Errorf("run: %d of %d projects failed", failed, len(selected))
		}
		return nil
	}
	return runProject(ctx, one, *project, *repoDir, *reportPath, out, errw)
}

// runEach runs the projects, up to parallel at a time, and returns how many
// failed. Every project has its own checkout and report; the lookup cache
// and the consumer index are shared, the cache through bbolt's own
// locking, the index through runOptions.indexMu. Measured before this: a
// partition of 67 repositories took 746 s one after the other, 393 s of it
// waiting on clones - time in which the runner's other cores sat idle.
func runEach(ctx context.Context, o *runOptions, projects []string, parallel int, reportPath string, out, errw io.Writer) int {
	return forEach(projects, parallel, func(p string, pout, perr io.Writer) error {
		return runProject(ctx, o, p, "", reportPath, pout, perr)
	}, out, errw)
}

// forEach calls run for every project, up to parallel at a time, reports
// each failure on errw and returns how many there were. Every project
// writes to buffers of its own, flushed together when it is done: eight
// repositories' warnings and summaries interleaved line by line is a log
// nobody can read.
func forEach(projects []string, parallel int, run func(string, io.Writer, io.Writer) error, out, errw io.Writer) int {
	if parallel < 1 {
		parallel = 1
	}
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed int
	)
	work := make(chan string)
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				var pout, perr bytes.Buffer
				err := run(p, &pout, &perr)
				mu.Lock()
				_, _ = io.Copy(out, &pout)
				_, _ = io.Copy(errw, &perr)
				if err != nil {
					failed++
					fmt.Fprintf(errw, "%s: %v\n", p, err)
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range projects {
		work <- p
	}
	close(work)
	wg.Wait()
	return failed
}

// rebaseSet is the branches the dashboard asked to push again.
func rebaseSet(c report.Checks) map[string]bool {
	out := map[string]bool{}
	for b := range c.Rebase {
		out[b] = true
	}
	for b := range c.Retry {
		out[b] = true
	}
	if c.RebaseAll {
		out["*"] = true
	}
	return out
}

// publishDashboard renders the dashboard for the plan and what the run did
// and writes it: to the issue in a live run, beside the report in a dry
// run, where the shadow phase can read what the issue would say.
func publishDashboard(ctx context.Context, o *runOptions, platform publish.Platform, proj publish.Project, plan *model.Plan, outcomes []runner.Outcome, reportPath, project string, out, errw io.Writer) error {
	states := map[string]report.BranchState{}
	for _, oc := range outcomes {
		states[oc.Branch] = report.BranchState{Action: oc.Action, MRIID: oc.MRIID, Message: oc.Message}
	}
	open, err := platform.OpenMergeRequests(ctx, proj, "renovate/")
	if err != nil {
		return err
	}
	body := report.Dashboard(plan, states, open, o.now)
	if o.dryRun {
		if reportPath == "" {
			return nil
		}
		path := reportPath
		if project != "" && strings.Contains(reportPath, "%s") {
			path = fmt.Sprintf(reportPath, strings.ReplaceAll(project, "/", "-"))
		}
		return os.WriteFile(strings.TrimSuffix(path, ".json")+".dashboard.md", []byte(body), 0o644)
	}
	issue, changed, err := platform.UpsertIssue(ctx, proj, o.dashboardTitle, body, []string{"pinup"})
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(out, "%s: dashboard #%d updated\n", proj.Path, issue.IID)
	}
	return nil
}

// runOptions is what every project in one invocation shares.
type runOptions struct {
	cfgPath       string
	runnerDefault string
	runnerProject string
	cache         lookup.Cache
	cacheTTL      time.Duration
	dryRun        bool
	env           platformEnv
	// client is the one HTTP client every project's lookups share: its
	// per-host concurrency limits and retry state are the job's, not a
	// repository's.
	client *httpx.Client
	// datasources, when set, replaces the wired registry: a test hands in
	// a canned one and proves the live path without a network.
	datasources lookup.Registry
	dsOptions   wire.DatasourceOptions
	identity    git.Identity
	signing     git.Signing
	platform    publish.Platform
	now         time.Time
	// dashboardTitle names the dashboard issue. "pinup Dashboard" until
	// the cutover, so it lives beside Renovate's; PINUP_DASHBOARD_TITLE
	// overrides, and the configuration's dependencyDashboardTitle takes
	// over with D.16.
	dashboardTitle string
	// index is the consumer index, updated after every plan; nil means
	// none is kept. indexMu serialises the projects that feed it.
	index     *report.Index
	indexMu   sync.Mutex
	indexPath string
	// released narrows a run to one dependency; see whatifOptions.
	released string
	// base, when set, replaces the project's default branch as the
	// branch the run reads and branches from.
	base string
}

// runReleased is the fast lane. The consumers come from the index the
// scheduled runs maintain - no search, no clone of the estate - and a
// package nobody is known to consume is a no-op, not a full scan: the run
// that is too broad is what queued the Renovate runner's triggers for a
// day. A package@version seen within the last hour is not run again; 36
// triggers for 16 packages are 16 runs.
func runReleased(ctx context.Context, o *runOptions, spec string, only *glob.Set, reportPath string, out, errw io.Writer) error {
	path, version, _ := strings.Cut(spec, "@")
	if o.index == nil {
		return fmt.Errorf("run --released needs the consumer index: pass --cache or --index")
	}
	seenPath := filepath.Join(filepath.Dir(o.indexPath), "released.json")
	if seenRecently(seenPath, spec, o.now) {
		fmt.Fprintf(out, "%s: already run within the hour; skipped\n", spec)
		return nil
	}
	consumers := o.index.ConsumersOf(path)
	if len(consumers) == 0 {
		fmt.Fprintf(out, "%s: no known consumer in the index (%d repositories indexed); nothing to do\n", path, len(o.index.Repositories))
		return nil
	}
	if only != nil {
		all := consumers
		consumers = consumers[:0:0]
		for _, c := range all {
			if only.Match(c) {
				consumers = append(consumers, c)
			}
		}
		fmt.Fprintf(errw, "fast lane: %s%s has %d consumers, %d within the filter\n", path, atVersion(version), len(all), len(consumers))
		if len(consumers) == 0 {
			fmt.Fprintf(out, "%s: no consumer within the filter; nothing to do\n", path)
			return nil
		}
	} else {
		fmt.Fprintf(errw, "fast lane: %s%s has %d consumers\n", path, atVersion(version), len(consumers))
	}
	o.released = path
	failed := 0
	failed = runEach(ctx, o, consumers, repositoryConcurrency(os.Getenv), reportPath, out, errw)
	if failed > 0 {
		return fmt.Errorf("run: %d of %d consumers failed", failed, len(consumers))
	}
	// Only a run that went through counts as done; a failed one may be
	// retried by the next trigger.
	if !o.dryRun {
		if err := markSeen(seenPath, spec, o.now); err != nil {
			// The run is done; only the debounce is lost, and the next
			// trigger for the same release would run again. Say so.
			fmt.Fprintf(errw, "warning: debounce record: %v\n", err)
		}
	}
	return nil
}

func atVersion(v string) string {
	if v == "" {
		return ""
	}
	return "@" + v
}

// seenRecently and markSeen keep the debounce record: spec -> last run.
func seenRecently(path, spec string, now time.Time) bool {
	seen := map[string]time.Time{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &seen)
	}
	t, ok := seen[spec]
	return ok && now.Sub(t) < time.Hour
}

func markSeen(path, spec string, now time.Time) error {
	seen := map[string]time.Time{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &seen)
	}
	for k, t := range seen {
		if now.Sub(t) > 24*time.Hour {
			delete(seen, k)
		}
	}
	seen[spec] = now.UTC()
	raw, err := json.MarshalIndent(seen, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// runProject plans and executes one project, given as a path to clone or
// as an existing checkout.
func runProject(ctx context.Context, o *runOptions, project, repoDir, reportPath string, out, errw io.Writer) error {
	// Phase timings on the summary line: where a slow run spends its
	// minutes is the first thing anyone reading a job log wants to know.
	phase := time.Now()
	var took []string
	lap := func(name string) {
		took = append(took, fmt.Sprintf("%s %s", name, time.Since(phase).Round(100*time.Millisecond)))
		phase = time.Now()
	}

	repo, repoName, cleanup, err := checkout(ctx, o, project, repoDir)
	if err != nil {
		return err
	}
	defer cleanup()
	lap("clone")
	proj, err := o.platform.Project(ctx, repoName)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if o.base != "" {
		proj.DefaultBranch = o.base
	}
	lap("project")

	opts, err := planOptions(ctx, o, repo, proj, errw)
	if err != nil {
		return err
	}
	plan, err := whatif(ctx, opts)
	if err != nil {
		return err
	}
	lap("plan")
	recordIndex(o, proj, plan, errw)

	var outcomes []runner.Outcome
	if !o.dryRun {
		outcomes, err = runner.Execute(ctx, plan, runner.Options{
			Repo: repo, Remote: "origin", Base: proj.DefaultBranch, Identity: o.identity, Signing: o.signing,
			Platform: o.platform, Project: proj, Labels: []string{"renovate"},
			Footer:          "This merge request was generated by pinup.",
			HourlyLimit:     plan.Limits.PRHourlyLimit,
			ConcurrentLimit: plan.Limits.PRConcurrentLimit,
			Prefix:          "renovate/", Now: o.now,
			Tasks:  plugin.TaskRunner{Runner: taskRunner(os.Getenv)},
			Sleep:  time.Sleep,
			Rebase: rebaseSet(opts.Checks),
		})
		if err != nil {
			return err
		}
	}
	if plan.Dashboard.Enabled {
		if err := publishDashboard(ctx, o, o.platform, proj, plan, outcomes, reportPath, project, out, errw); err != nil {
			fmt.Fprintf(errw, "warning: %s: dashboard: %v\n", proj.Path, err)
		}
	}
	if err := writeReport(reportPath, project, plan); err != nil {
		return err
	}
	lap("publish")
	summarise(out, errw, proj.Path, plan, outcomes, took)
	return nil
}

// checkout is the repository the run works on: a fresh blobless clone of
// project through the binary's own askpass, or the checkout at repoDir.
// cleanup removes what was cloned.
func checkout(ctx context.Context, o *runOptions, project, repoDir string) (repo *git.Repo, name string, cleanup func(), err error) {
	cleanup = func() {}
	if project == "" {
		repo, err = git.Open(repoDir)
		if err != nil {
			return nil, "", cleanup, err
		}
		return repo, strings.TrimSuffix(filepath.Base(repoDir), ".git"), cleanup, nil
	}
	dir, err := os.MkdirTemp("", "pinup-run-")
	if err != nil {
		return nil, "", cleanup, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	self, err := os.Executable()
	if err != nil {
		return nil, "", cleanup, err
	}
	gitEnv := []string{"GIT_ASKPASS=" + self, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_PARAMETERS='credential.helper='"}
	repo, err = git.Clone(ctx, o.env.URL+"/"+project+".git", filepath.Join(dir, "repo"), git.CloneOptions{Blobless: true}, gitEnv)
	if err != nil {
		return nil, "", cleanup, err
	}
	repo.Env = gitEnv
	if o.base != "" {
		if err := repo.Fetch(ctx, "origin", o.base); err != nil {
			return nil, "", cleanup, err
		}
		if err := repo.Checkout(ctx, o.base, "origin/"+o.base); err != nil {
			return nil, "", cleanup, err
		}
	}
	return repo, project, cleanup, nil
}

// planOptions is everything whatif needs for one project: the shared
// registry, cache and client, the dashboard's ticked boxes read before
// planning, the advisory checker, the changelog fetcher, the allowlist.
func planOptions(ctx context.Context, o *runOptions, repo *git.Repo, proj publish.Project, errw io.Writer) (whatifOptions, error) {
	opts := whatifOptions{
		Root: repo.Dir, ConfigPath: o.cfgPath, RepoName: proj.Path, Now: o.now,
		Datasources:   o.datasources,
		Cache:         o.cache,
		CacheTTL:      o.cacheTTL,
		Presets:       preset.Remote{Reader: o.platform, Ctx: ctx},
		Released:      o.released,
		RunnerDefault: o.runnerDefault,
		RunnerProject: o.runnerProject,
	}
	opts.CustomDatasources = customDatasourcesHook(o.client, o.dsOptions)
	// The dashboard's ticked boxes, read before planning: an approval, a
	// window or a release age lifted by a person, a rebase asked for.
	if !o.dryRun {
		if _, body, ok, err := o.platform.ReadIssue(ctx, proj, o.dashboardTitle); err != nil {
			fmt.Fprintf(errw, "warning: %s: dashboard: %v\n", proj.Path, err)
		} else if ok {
			opts.Checks = report.ParseChecks(body)
		}
	}
	advisories := &osv.Client{}
	if o.cache != nil {
		advisories.Store = advisoryStore{cache: o.cache, now: o.now, warn: func(m string) { fmt.Fprintf(errw, "warning: %s: %s\n", proj.Path, m) }}
	}
	opts.Advisories = advisories
	notes := &changelog.Fetcher{Client: o.client, GitLabURL: o.env.URL, TTL: changelogTTL, Now: o.now, MaxBody: noteBodyLimit}
	if o.cache != nil {
		notes.Cache = o.cache
	}
	opts.Changelog = notes
	opts.LookPath = exec.LookPath
	var err error
	if opts.AllowedCommands, err = allowedCommands(os.Getenv); err != nil {
		return opts, err
	}
	return opts, nil
}

// recordIndex feeds the consumer index. Every full plan feeds it; a
// fast-lane plan sees one dependency and must not overwrite what the
// repository has.
func recordIndex(o *runOptions, proj publish.Project, plan *model.Plan, errw io.Writer) {
	if o.index == nil || o.released != "" {
		return
	}
	o.indexMu.Lock()
	o.index.Record(proj.Path, plan, o.now)
	err := o.index.Save(o.indexPath)
	o.indexMu.Unlock()
	if err != nil {
		fmt.Fprintf(errw, "warning: index: %v\n", err)
	}
}

// writeReport writes the plan and its markdown beside it. Several projects
// write several reports: the path gains the project's path with slashes
// folded when it is not one project.
func writeReport(reportPath, project string, plan *model.Plan) error {
	if reportPath == "" {
		return nil
	}
	path := reportPath
	if project != "" && strings.Contains(reportPath, "%s") {
		path = fmt.Sprintf(reportPath, strings.ReplaceAll(project, "/", "-"))
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeMarkdown(path, plan); err != nil {
		return err
	}
	return model.WritePlan(f, plan)
}

// summarise writes the project's line, one line per outcome, and the
// warnings a person running the job should see.
func summarise(out, errw io.Writer, path string, plan *model.Plan, outcomes []runner.Outcome, took []string) {
	fmt.Fprintf(out, "%s: %d dependencies, %d updates (%d held), %d branches [%s]\n",
		path, plan.Stats.DepsExtracted, plan.Stats.UpdatesFound, plan.Stats.UpdatesBlocked, plan.Stats.BranchesPlanned, strings.Join(took, ", "))
	for _, o := range outcomes {
		line := fmt.Sprintf("  %-9s %s", o.Action, o.Branch)
		if o.MRIID != 0 {
			line += fmt.Sprintf(" !%d", o.MRIID)
		}
		if o.Message != "" && o.Message != "[]" {
			line += "  " + o.Message
		}
		fmt.Fprintln(out, line)
	}
	for _, w := range plan.Warnings {
		if w.Stage == "publish" || w.Stage == "apply" || w.Stage == "cache" {
			fmt.Fprintf(errw, "warning: %s: %s\n", w.Stage, w.Msg)
		}
	}
}

// gitIdentityFromEnv reads who commits and how commits are signed:
// PINUP_GIT_NAME / PINUP_GIT_EMAIL, PINUP_SIGNING_FORMAT (ssh or openpgp),
// PINUP_SIGNING_KEY, PINUP_ALLOWED_SIGNERS. Unsigned is allowed only when
// PINUP_SIGNING_FORMAT is the explicit word "none": the estate's merge
// requests show Verified today, and a run that silently stopped signing
// would be a regression nobody asked for.
func gitIdentityFromEnv(getenv func(string) string) (git.Identity, git.Signing, error) {
	id := git.Identity{Name: getenv("PINUP_GIT_NAME"), Email: getenv("PINUP_GIT_EMAIL")}
	if id.Name == "" || id.Email == "" {
		return id, git.Signing{}, fmt.Errorf("PINUP_GIT_NAME and PINUP_GIT_EMAIL must be set: commits need an author")
	}
	format := getenv("PINUP_SIGNING_FORMAT")
	switch format {
	case "none":
		return id, git.Signing{}, nil
	case "ssh", "openpgp":
		s := git.Signing{Format: format, Key: getenv("PINUP_SIGNING_KEY"), AllowedSigners: getenv("PINUP_ALLOWED_SIGNERS")}
		if s.Key == "" {
			return id, s, fmt.Errorf("PINUP_SIGNING_KEY must be set for %s signing", format)
		}
		if format == "ssh" && s.AllowedSigners == "" {
			return id, s, fmt.Errorf("PINUP_ALLOWED_SIGNERS must be set for ssh signing, so the author can be checked against the key")
		}
		return id, s, nil
	}
	return id, git.Signing{}, fmt.Errorf("PINUP_SIGNING_FORMAT must be ssh, openpgp, or the explicit word none")
}

// fetchConfig fetches a local> configuration through the platform into a
// temporary file, named so provenance still reads as the preset it is.
func fetchConfig(ctx context.Context, platform interface {
	ReadFile(ctx context.Context, project, path, ref string) ([]byte, error)
}, name string) (string, error) {
	project, path, ref, err := preset.ParseLocal(strings.TrimPrefix(name, "local>"))
	if err != nil {
		return "", err
	}
	raw, err := platform.ReadFile(ctx, project, path, ref)
	if err != nil {
		return "", fmt.Errorf("run: %s: %w", name, err)
	}
	f, err := os.CreateTemp("", "pinup-config-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}
