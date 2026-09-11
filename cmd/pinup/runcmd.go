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

	"git.ole-hartwig.eu/pinup/pinup/cache"
	"git.ole-hartwig.eu/pinup/pinup/config/preset"
	"git.ole-hartwig.eu/pinup/pinup/git"
	"git.ole-hartwig.eu/pinup/pinup/glob"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/publish"
	"git.ole-hartwig.eu/pinup/pinup/report"
	"git.ole-hartwig.eu/pinup/pinup/runner"
	"git.ole-hartwig.eu/pinup/pinup/wire"
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
	project := fs.String("project", "", "project path to clone and run against, e.g. devops/images/ci-tools")
	autodiscover := fs.String("autodiscover", "", `run against every project the token can see that matches these patterns, a JSON list of globs with ! negations, e.g. '["devops/images/**", "!devops/images/pinup"]'`)
	released := fs.String("released", "", "the fast lane: a project path that was just released, optionally @version; runs only its consumers from the index, only for that dependency, with fresh lookups")
	indexPath := fs.String("index", "", "path of the consumer index (default: consumers.json beside --cache)")
	cfgPath := fs.String("config", "", "configuration file to resolve (required)")
	reportPath := fs.String("report", "", "write the plan as JSON to this path; with several projects a %s in it becomes the project path")
	cachePath := fs.String("cache", os.Getenv("PINUP_CACHE"), "path of the lookup cache file (bbolt)")
	cacheTTL := fs.Duration("cache-ttl", time.Hour, "how long a cached lookup counts as fresh")
	dryRun := fs.Bool("dry-run", false, "plan only; push nothing, open nothing")
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
	if chosen != 1 {
		return fmt.Errorf("run: exactly one of --repo, --project, --autodiscover and --released is required")
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
	one := runOptions{
		cfgPath: *cfgPath, runnerDefault: runnerDefault, cache: store, cacheTTL: *cacheTTL, dryRun: *dryRun, env: env,
		identity: identity, signing: signing, platform: platform, now: now,
	}
	if *indexPath != "" {
		idx, err := report.LoadIndex(*indexPath)
		if err != nil {
			return fmt.Errorf("index: %w", err)
		}
		one.index, one.indexPath = idx, *indexPath
	}

	if *released != "" {
		return runReleased(ctx, one, *released, *reportPath, out, errw)
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
		failed := 0
		for _, p := range selected {
			if err := runProject(ctx, one, p, "", *reportPath, out, errw); err != nil {
				failed++
				fmt.Fprintf(errw, "%s: %v\n", p, err)
			}
		}
		if failed > 0 {
			return fmt.Errorf("run: %d of %d projects failed", failed, len(selected))
		}
		return nil
	}
	return runProject(ctx, one, *project, *repoDir, *reportPath, out, errw)
}

// runOptions is what every project in one invocation shares.
type runOptions struct {
	cfgPath       string
	runnerDefault string
	cache         lookup.Cache
	cacheTTL      time.Duration
	dryRun        bool
	env           platformEnv
	identity      git.Identity
	signing       git.Signing
	platform      publish.Platform
	now           time.Time
	// index is the consumer index, updated after every plan; nil means
	// none is kept.
	index     *report.Index
	indexPath string
	// released narrows a run to one dependency; see whatifOptions.
	released string
}

// runReleased is the fast lane. The consumers come from the index the
// scheduled runs maintain - no search, no clone of the estate - and a
// package nobody is known to consume is a no-op, not a full scan: the run
// that is too broad is what queued the Renovate runner's triggers for a
// day. A package@version seen within the last hour is not run again; 36
// triggers for 16 packages are 16 runs.
func runReleased(ctx context.Context, o runOptions, spec, reportPath string, out, errw io.Writer) error {
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
	fmt.Fprintf(errw, "fast lane: %s%s has %d consumers\n", path, atVersion(version), len(consumers))
	o.released = path
	failed := 0
	for _, p := range consumers {
		if err := runProject(ctx, o, p, "", reportPath, out, errw); err != nil {
			failed++
			fmt.Fprintf(errw, "%s: %v\n", p, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("run: %d of %d consumers failed", failed, len(consumers))
	}
	// Only a run that went through counts as done; a failed one may be
	// retried by the next trigger.
	if !o.dryRun {
		markSeen(seenPath, spec, o.now)
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

func markSeen(path, spec string, now time.Time) {
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
	if raw, err := json.MarshalIndent(seen, "", " "); err == nil {
		_ = os.WriteFile(path, raw, 0o644)
	}
}

// runProject plans and executes one project, given as a path to clone or
// as an existing checkout.
func runProject(ctx context.Context, o runOptions, project, repoDir, report string, out, errw io.Writer) error {
	env, platform := o.env, o.platform

	// The checkout.
	var repo *git.Repo
	var repoName string
	var err error
	if project != "" {
		repoName = project
		dir, err := os.MkdirTemp("", "pinup-run-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		self, err := os.Executable()
		if err != nil {
			return err
		}
		repo, err = git.Clone(ctx, env.URL+"/"+project+".git", filepath.Join(dir, "repo"), 0,
			[]string{"GIT_ASKPASS=" + self, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_PARAMETERS='credential.helper='"})
		if err != nil {
			return err
		}
		repo.Env = []string{"GIT_ASKPASS=" + self, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_PARAMETERS='credential.helper='"}
	} else {
		repo, err = git.Open(repoDir)
		if err != nil {
			return err
		}
		repoName = strings.TrimSuffix(filepath.Base(repoDir), ".git")
	}
	proj, err := platform.Project(ctx, repoName)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	opts := whatifOptions{
		Root: repo.Dir, ConfigPath: o.cfgPath, RepoName: proj.Path, Now: o.now,
		Datasources:   wire.Datasources(httpClient(env), datasourceOptions(env)),
		Cache:         o.cache,
		CacheTTL:      o.cacheTTL,
		Presets:       preset.Remote{Reader: platform, Ctx: ctx},
		Released:      o.released,
		RunnerDefault: o.runnerDefault,
	}
	opts.CustomDatasources = customDatasourcesHook(env)
	plan, err := whatif(ctx, opts)
	if err != nil {
		return err
	}
	// The index is fed by every full plan; a fast-lane plan sees one
	// dependency and must not overwrite what the repository has.
	if o.index != nil && o.released == "" {
		o.index.Record(proj.Path, plan, o.now)
		if err := o.index.Save(o.indexPath); err != nil {
			fmt.Fprintf(errw, "warning: index: %v\n", err)
		}
	}

	var outcomes []runner.Outcome
	if !o.dryRun {
		outcomes, err = runner.Execute(ctx, plan, runner.Options{
			Repo: repo, Remote: "origin", Base: proj.DefaultBranch, Identity: o.identity, Signing: o.signing,
			Platform: platform, Project: proj, Labels: []string{"renovate"},
			Footer:          "This merge request was generated by pinup.",
			HourlyLimit:     plan.Limits.PRHourlyLimit,
			ConcurrentLimit: plan.Limits.PRConcurrentLimit,
			Prefix:          "renovate/", Now: o.now,
		})
		if err != nil {
			return err
		}
	}

	if report != "" {
		// Several projects write several reports: the path gains the
		// project's path with slashes folded when it is not one project.
		path := report
		if project != "" && strings.Contains(report, "%s") {
			path = fmt.Sprintf(report, strings.ReplaceAll(project, "/", "-"))
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := model.WritePlan(f, plan); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%s: %d dependencies, %d updates (%d held), %d branches\n",
		proj.Path, plan.Stats.DepsExtracted, plan.Stats.UpdatesFound, plan.Stats.UpdatesBlocked, plan.Stats.BranchesPlanned)
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
	return nil
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
