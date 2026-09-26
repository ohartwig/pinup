// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ohartwig/pinup/advise"
	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/wire"
)

// A repository that has never had pinup has no configuration to advise
// on, and writing the first one is where a new user gives up: the
// Renovate documentation lists three hundred options and none of them says
// which of them this repository needs. `advise --init` answers from the
// repository itself. It extracts offline - no token, no lookup - and
// proposes the smallest configuration that is right for what it found:
//
//   - the recommended presets, a release age and OSV alerts, the three
//     settings the security checks raise on a configuration without them;
//   - enabledManagers limited to the managers that found something, so no
//     run walks the tree for ecosystems the repository does not have;
//   - where the coverage scan finds pins no manager reads, one annotation
//     manager for exactly those files, and the annotation each pin needs.
//
// The proposal has to pass advise itself: it is resolved, planned and run
// through the catalogue, and a warning or an error there fails the
// command. Proposing what the next command criticises would be worse than
// proposing nothing.

// adviseInit writes the proposal for root to outPath, or to out.
func adviseInit(root, outPath string, out io.Writer) error {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("advise --init: %s is not a directory", root)
	}
	now := time.Now()
	p, err := proposeConfig(context.Background(), root, now)
	if err != nil {
		return fmt.Errorf("advise --init: %w", err)
	}
	body, err := writeProposal(p, now)
	if err != nil {
		return err
	}
	if outPath == "" {
		_, err = out.Write(body)
		return err
	}
	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("advise --init: %s exists; --init writes a first configuration, not over one", outPath)
	}
	if err := os.WriteFile(outPath, body, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s\n", outPath)
	return nil
}

// initBase is what every proposal starts from.
func initBase() map[string]any {
	return map[string]any{
		"extends":                []any{"config:recommended"},
		"minimumReleaseAge":      "3 days",
		"osvVulnerabilityAlerts": true,
	}
}

// annotationMatch reads a `# renovate:` annotation and the version on the
// line below it, whatever precedes the version there: `image: a/b:1.2.3`,
// `HELM_VERSION: "3.20.1"`, `ARG X=1.2.3`. The lazy prefix stops at the
// last separator before a version, so an image's name is skipped.
const annotationMatch = `# renovate: datasource=(?<datasource>\S+) depName=(?<depName>\S+)(?: versioning=(?<versioning>\S+))?\s+[^\n]*?[:=]\s*["']?(?<currentValue>v?\d+\.\d+\.\d+[^\s"'@,]*)`

// initProposal is what --init prints: the configuration and why.
type initProposal struct {
	Config    map[string]any
	Managers  map[string]int
	Unmanaged []model.Unmanaged
	// Annotations maps "file:line" to the annotation that pin needs.
	Annotations map[string]string
	// Annotated are files with annotations no manager reads yet.
	Annotated []string
}

// annotationComment is a `# renovate:` annotation as the proposal's
// manager reads it.
var annotationComment = regexp.MustCompile(`#\s*renovate:\s*datasource=\S+\s+depName=\S+`)

// annotatedUnread lists the files carrying annotations from which no
// dependency of the plan came: their annotations name nothing yet.
func annotatedUnread(fsys fs.FS, deps []model.Dependency) []string {
	read := map[string]bool{}
	for _, d := range deps {
		read[d.File] = true
	}
	var out []string
	_ = fs.WalkDir(fsys, ".", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if name := e.Name(); path != "." && (strings.HasPrefix(name, ".") && name != ".gitlab" && name != ".github" || name == "node_modules" || name == "vendor") {
				return fs.SkipDir
			}
			return nil
		}
		if read[path] {
			return nil
		}
		if info, err := e.Info(); err != nil || info.Size() > 400<<10 {
			return nil
		}
		body, err := fs.ReadFile(fsys, path)
		if err == nil && annotationComment.Match(body) {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// proposeConfig plans root offline under a growing configuration and
// returns the proposal, verified.
func proposeConfig(ctx context.Context, root string, now time.Time) (*initProposal, error) {
	cfg := initBase()
	plan, err := offlinePlan(ctx, root, cfg, now)
	if err != nil {
		return nil, err
	}
	p := &initProposal{Config: cfg, Managers: map[string]int{}, Annotations: map[string]string{}}
	for _, d := range plan.Deps {
		if d.CustomManager == model.NoCustomManager {
			p.Managers[d.Manager]++
		}
	}
	managers := slices.Sorted(maps.Keys(p.Managers))
	p.Unmanaged = plan.Unmanaged
	files := map[string]bool{}
	for _, u := range p.Unmanaged {
		files[u.File] = true
		p.Annotations[fmt.Sprintf("%s:%d", u.File, u.Line)] = suggestAnnotation(root, u)
	}
	// Annotations already written where no manager of the proposal reads:
	// the coverage scan passes over an annotated line, so without this
	// they would stay orphaned under the new configuration, unnoticed.
	p.Annotated = annotatedUnread(os.DirFS(root), plan.Deps)
	for _, f := range p.Annotated {
		files[f] = true
	}
	if len(files) > 0 {
		var patterns []any
		for _, f := range slices.Sorted(maps.Keys(files)) {
			patterns = append(patterns, "/^"+regexp.QuoteMeta(f)+"$/")
		}
		cfg["customManagers"] = []any{map[string]any{
			"customType":          "regex",
			"description":         "Versions pinned where no manager reads them, updated through a `# renovate: datasource=… depName=…` annotation on the line above.",
			"managerFilePatterns": patterns,
			"matchStrings":        []any{annotationMatch},
		}}
		managers = append(managers, "custom.regex")
	}
	if len(managers) > 0 {
		cfg["enabledManagers"] = toAny(managers)
	}
	if err := verifyProposal(ctx, root, cfg, now); err != nil {
		return nil, err
	}
	return p, nil
}

// offlinePlan runs whatif on root under cfg with no datasource: every
// dependency is extracted, none looked up, and the coverage scan runs.
func offlinePlan(ctx context.Context, root string, cfg map[string]any, now time.Time) (*model.Plan, error) {
	dir, err := os.MkdirTemp("", "pinup-init-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "proposal.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	return whatif(ctx, whatifOptions{Root: root, ConfigPath: path, RepoName: "init", Now: now, Datasources: lookup.Registry{}, Coverage: true})
}

// verifyProposal is the gate: the proposal resolves, its rules compile,
// and advise finds nothing worse than info on it - coverage aside, which
// reports the pins the proposal is about until they are annotated.
func verifyProposal(ctx context.Context, root string, cfg map[string]any, now time.Time) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	layer, err := config.Parse(raw, ".pinup.jsonc")
	if err != nil {
		return fmt.Errorf("the proposal does not parse: %w", err)
	}
	in, err := advise.Load(layer, runnerSources(nil, "", nil), wire.Versionings())
	if err != nil {
		return fmt.Errorf("the proposal does not resolve: %w", err)
	}
	plan, err := offlinePlan(ctx, root, cfg, now)
	if err != nil {
		return err
	}
	in.Plans = []*model.Plan{plan}
	in.Repo = os.DirFS(root)
	in.Covers = wire.Covers
	in.Datasources = wire.DatasourceNames(in.Decoded.CustomDatasources)
	findings, _ := advise.Run(in, advise.Catalogue())
	var bad []string
	for _, f := range findings {
		if f.Category == advise.Coverage || f.Severity == advise.Info {
			continue
		}
		bad = append(bad, fmt.Sprintf("%s %s: %s", f.ID, f.Pointer, f.Msg))
	}
	if len(bad) > 0 {
		return fmt.Errorf("the proposal fails its own advice - this is a bug in --init:\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}

// imageName is the reference before the pinned tag on an image line.
var imageName = regexp.MustCompile(`([\w.-]+(?:\.[a-z]{2,}(?::\d+)?)?/[\w./-]+):v?\d`)

// suggestAnnotation is the annotation a pin needs: for an image, complete;
// for a variable, with the depName left for the owner to fill, because
// nothing on the line says where the tool is released.
//
// An image's annotation names its versioning. The proposal's own manager
// reads it either way, but a configuration that extends a shared one may
// require it: the estate runner's compose manager does, and an annotation
// without it matched nothing there (devops/compose!166, 2026-09-26).
func suggestAnnotation(root string, u model.Unmanaged) string {
	line := lineOf(os.DirFS(root), u.File, u.Line)
	if m := imageName.FindStringSubmatch(line); m != nil && strings.Contains(line, m[1]+":"+u.Value) {
		return "# renovate: datasource=docker depName=" + m[1] + " versioning=docker"
	}
	return "# renovate: datasource=github-releases depName=<owner>/<repo>"
}

func lineOf(fsys fs.FS, file string, n int) string {
	body, err := fs.ReadFile(fsys, file)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(body), "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// writeProposal renders the proposal as the .pinup.jsonc a user saves:
// the reasoning in comments above the object, the object itself plain.
func writeProposal(p *initProposal, now time.Time) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "// .pinup.jsonc proposed by `pinup advise --init` on %s.\n", now.UTC().Format("2006-01-02"))
	b.WriteString("// A starting point from what the repository contains, checked by advise itself.\n")
	if len(p.Managers) == 0 {
		b.WriteString("//\n// No manager found a dependency; the recommended presets still apply.\n")
	} else {
		b.WriteString("//\n// Managers that found dependencies (enabledManagers lists only these):\n")
		for _, m := range slices.Sorted(maps.Keys(p.Managers)) {
			fmt.Fprintf(&b, "//   %-20s %d\n", m, p.Managers[m])
		}
	}
	if len(p.Unmanaged) > 0 {
		b.WriteString("//\n// Pinned versions no manager reads. The annotation manager below updates each\n")
		b.WriteString("// once its annotation stands on the line directly above; mark a pin that stays\n")
		b.WriteString("// on purpose `pinup: coverage-ignore` instead.\n")
		for _, u := range p.Unmanaged {
			fmt.Fprintf(&b, "//   %s:%d  %s\n//     %s\n", u.File, u.Line, u.Value, p.Annotations[fmt.Sprintf("%s:%d", u.File, u.Line)])
		}
	}
	if len(p.Annotated) > 0 {
		b.WriteString("//\n// Files whose `# renovate:` annotations no manager reads until this one does:\n")
		for _, f := range p.Annotated {
			fmt.Fprintf(&b, "//   %s\n", f)
		}
	}
	// The keys in the order a reader wants them: what it builds on first,
	// what it adds last.
	b.WriteString("{\n")
	keys := []string{"extends", "minimumReleaseAge", "osvVulnerabilityAlerts", "enabledManagers", "customManagers"}
	var present []string
	for _, k := range keys {
		if _, ok := p.Config[k]; ok {
			present = append(present, k)
		}
	}
	if len(present) != len(p.Config) {
		return nil, fmt.Errorf("the proposal carries a key the renderer does not order: %v", slices.Sorted(maps.Keys(p.Config)))
	}
	for i, k := range present {
		raw, err := json.Marshal(p.Config[k], json.Deterministic(true))
		if err != nil {
			return nil, err
		}
		v := jsontext.Value(raw)
		if err := v.Indent(jsontext.WithIndentPrefix("  "), jsontext.WithIndent("  ")); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  %q: %s", k, v)
		if i < len(present)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	b.WriteString("\n")
	return []byte(b.String()), nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
