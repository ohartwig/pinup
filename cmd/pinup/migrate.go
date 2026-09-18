// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/ohartwig/pinup/advise"
	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/config/toyaml"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/wire"
)

// cmdMigrate resolves a configuration as a run would and reports, key by
// key, what pinup supports. It changes no file: the twenty renovate.json
// files leave the critical path through the runner alias, and a
// conversion nobody asked for would be a second config to keep true.
func cmdMigrate(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(errw)
	cfgPath := fs.String("config", "", "configuration file to classify (required)")
	asJSON := fs.Bool("json", false, "print the classification as JSON")
	to := fs.String("to", "", "rewrite the file instead of classifying it: yaml (descriptions become comments, scalars YAML would misread are quoted)")
	outPath := fs.String("out", "", "with --to: write here instead of stdout")
	keep := fs.Bool("keep-descriptions", false, "with --to yaml: keep description keys as well as the comments")
	runner := fs.String("runner", "", "the runner's configuration a local> extends resolves to: a path, or local>project fetched through the platform")
	var renames renameFlag
	fs.Var(&renames, "extends", "with --to: rename one extends entry, old=new (repeatable; the resolution must not change)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("migrate: --config is required")
	}
	sources, cleanup, err := runnerChain(context.Background(), *runner, os.Getenv)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer cleanup()
	if *to != "" {
		if *to != "yaml" {
			return fmt.Errorf("migrate: --to %q: only yaml is written", *to)
		}
		src, err := os.ReadFile(*cfgPath)
		if err != nil {
			return err
		}
		// What the rewrite changes on purpose: the extends entries it was
		// told to rename, and the $schema line, which names Renovate's
		// schema and would be a lie on a pinup file. Nothing else.
		rewritten, err := renames.apply(src)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		rewritten = dropSchema(rewritten)
		converted, err := toyaml.Convert(rewritten, *cfgPath, toyaml.Options{KeepDescriptions: *keep})
		if err != nil {
			return err
		}
		// The converted document must load to what the original loads
		// to - checked here, every time, not only in the test. The
		// renamed extends are aliases of one document, so the resolution
		// is the same or the rename was wrong.
		if err := sameResolution(src, *cfgPath, converted, sources); err != nil {
			return fmt.Errorf("migrate: the conversion does not load to the same document: %w", err)
		}
		if *outPath == "" {
			_, err = out.Write(converted)
			return err
		}
		return os.WriteFile(*outPath, converted, 0o644)
	}
	if len(renames) > 0 {
		return fmt.Errorf("migrate: --extends renames a file; it needs --to")
	}
	layer, err := config.LoadFile(*cfgPath)
	if err != nil {
		return err
	}
	r, warnings, err := config.ResolveLayer(layer, sources)
	if err != nil {
		return err
	}

	classes := map[advise.Support][]string{}
	seen := map[string]bool{}
	classify := func(where, key string) {
		id := key
		if where != "" {
			id = where + "." + key
		}
		if seen[id] {
			return
		}
		seen[id] = true
		cls, ok := advise.KeySupport[key]
		if !ok {
			cls = advise.Unsupported
		}
		classes[cls] = append(classes[cls], id)
	}
	for k := range layer.Raw {
		classify("", k)
	}
	if rulesRaw, ok := layer.Raw["packageRules"].([]any); ok {
		for _, rule := range rulesRaw {
			if obj, ok := rule.(map[string]any); ok {
				for k := range obj {
					classify("packageRules[]", k)
				}
			}
		}
	}

	// Managers and datasources the resolved configuration names.
	decoded, err := config.Decode(r.Raw)
	if err != nil {
		return err
	}
	var missingManagers []string
	for _, m := range decoded.EnabledManagers {
		if !wire.Covers(m) && m != "custom.regex" {
			missingManagers = append(missingManagers, m)
		}
	}
	sort.Strings(missingManagers)

	// Rules the engine cannot evaluate.
	var ruleWarnings []string
	if rulesRaw, ok := r.Raw["packageRules"].([]any); ok {
		if eng, err := rules.Compile(rulesRaw, wire.Versionings()); err != nil {
			ruleWarnings = append(ruleWarnings, err.Error())
		} else {
			ruleWarnings = eng.Warnings
		}
	}

	for _, cls := range []advise.Support{advise.Supported, advise.Partial, advise.Unsupported} {
		sort.Strings(classes[cls])
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"file": *cfgPath, "supported": classes[advise.Supported], "partial": classes[advise.Partial],
			"x-unsupported": classes[advise.Unsupported], "managersNotImplemented": missingManagers,
			"rulesNotEvaluable": ruleWarnings, "presetWarnings": warnings, "migrations": r.Migrations,
		})
	}
	fmt.Fprintf(out, "%s\n", *cfgPath)
	for _, cls := range []advise.Support{advise.Supported, advise.Partial, advise.Unsupported} {
		fmt.Fprintf(out, "\n%s (%d)\n", cls, len(classes[cls]))
		for _, k := range classes[cls] {
			fmt.Fprintf(out, "  %s\n", k)
		}
	}
	if len(missingManagers) > 0 {
		fmt.Fprintf(out, "\nmanagers enabled but not implemented (%d)\n  %s\n", len(missingManagers), strings.Join(missingManagers, "\n  "))
	}
	if len(ruleWarnings) > 0 {
		fmt.Fprintf(out, "\nrules the engine does not evaluate (%d)\n  %s\n", len(ruleWarnings), strings.Join(ruleWarnings, "\n  "))
	}
	if len(warnings) > 0 {
		fmt.Fprintf(out, "\npresets (%d)\n  %s\n", len(warnings), strings.Join(warnings, "\n  "))
	}
	if len(r.Migrations) > 0 {
		fmt.Fprintf(out, "\nmigrated (%d)\n  %s\n", len(r.Migrations), strings.Join(r.Migrations, "\n  "))
	}
	return nil
}

// sameResolution parses both documents and compares their resolutions line
// by line, the descriptions aside: those are comments in the conversion.
func sameResolution(src []byte, name string, converted []byte, sources preset.Source) error {
	flat := func(b []byte, n string) (map[string]string, error) {
		layer, err := config.Parse(b, n)
		if err != nil {
			return nil, err
		}
		r, _, err := config.ResolveLayer(layer, sources)
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for _, l := range config.Flatten(r.Raw) {
			if l.Path == "description" || strings.Contains(l.Path, ".description") || strings.HasPrefix(l.Path, "description[") {
				continue
			}
			// The extends entries and the schema are what the rewrite
			// changes by design; what they resolve to is compared.
			if l.Path == "$schema" || l.Path == "extends" || strings.HasPrefix(l.Path, "extends[") {
				continue
			}
			out[l.Path] = l.Value
		}
		return out, nil
	}
	before, err := flat(src, name)
	if err != nil {
		return err
	}
	after, err := flat(converted, "converted.yaml")
	if err != nil {
		return err
	}
	for k, v := range before {
		if after[k] != v {
			return fmt.Errorf("%s: %s became %s", k, v, after[k])
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			return fmt.Errorf("%s appeared", k)
		}
	}
	return nil
}

// renameFlag collects --extends old=new pairs.
type renameFlag [][2]string

func (r *renameFlag) String() string { return fmt.Sprint([][2]string(*r)) }

func (r *renameFlag) Set(v string) error {
	old, new, ok := strings.Cut(v, "=")
	if !ok || old == "" || new == "" {
		return fmt.Errorf("--extends wants old=new, got %q", v)
	}
	*r = append(*r, [2]string{old, new})
	return nil
}

// apply renames the extends entries in the source text: the quoted
// string, as JSON writes it, replaced byte for byte so comments and order
// survive. A name that does not occur is an error - a rename that renames
// nothing is a typo.
func (r renameFlag) apply(src []byte) ([]byte, error) {
	for _, pair := range r {
		from, to := []byte(`"`+pair[0]+`"`), []byte(`"`+pair[1]+`"`)
		if !bytes.Contains(src, from) {
			return nil, fmt.Errorf("--extends %s: %q is not in the file", pair[0]+"="+pair[1], pair[0])
		}
		src = bytes.ReplaceAll(src, from, to)
	}
	return src, nil
}

// dropSchema removes the "$schema" member from a JSON(C) source, with
// its comma, wherever it stands in the top-level object.
func dropSchema(src []byte) []byte {
	return schemaLine.ReplaceAll(src, nil)
}

var schemaLine = regexp.MustCompile(`(?m)^[ \t]*"\$schema"[ \t]*:[ \t]*"[^"]*"[ \t]*,?[ \t]*\r?\n`)
