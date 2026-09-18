// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package advise

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ohartwig/pinup/config"
	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/rules"
	"github.com/ohartwig/pinup/versioning"
)

// Category is what a finding is about.
type Category string

const (
	Performance Category = "performance"
	Security    Category = "security"
	Hygiene     Category = "hygiene"
	Compat      Category = "compat"
)

// Severity is how much a finding matters. An error is something the run
// will refuse or get wrong; a warning changes behaviour the author probably
// did not intend; info is a tidy-up.
type Severity string

const (
	Error Severity = "error"
	Warn  Severity = "warn"
	Info  Severity = "info"
)

// Op is what a fix does at its pointer.
type Op string

const (
	OpSet    Op = "set"
	OpRemove Op = "remove"
	OpAppend Op = "append"
)

// Fix is one edit to the user's file - the document as written, never a
// preset. Pointer is RFC 6901 into that document. Parents are never
// created: a check that wants /major/automerge on a file without /major
// sets /major to {"automerge": false}, and an append to a missing key
// becomes a set of a one-element array.
//
// Changes lists the file pointers whose resolved value may differ once the
// fix is applied. nil means the fix preserves semantics: the resolution
// must come out byte-identical, and the gate refuses it otherwise.
type Fix struct {
	Pointer string   `json:"pointer"`
	Op      Op       `json:"op"`
	Value   any      `json:"value,omitempty"`
	Changes []string `json:"changes,omitempty"`
}

// Frame names the document a finding's pointer indexes.
const (
	FrameFile     = "file"
	FrameResolved = "resolved"
)

// Finding is one thing to change, and why.
type Finding struct {
	// ID is "<prefix>/<slug>": sec/automerge-major, perf/ignore-paths-replaced.
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Severity Severity `json:"severity"`
	// Pointer is where the problem is; Frame says in which document.
	Pointer string `json:"pointer"`
	Frame   string `json:"frame"`
	// Origin is the layer that wrote the value: the file, a preset, the
	// builtin defaults.
	Origin model.Origin `json:"origin"`
	Msg    string       `json:"msg"`
	// Fix is nil when the file does not own the value.
	Fix *Fix `json:"fix,omitempty"`
}

// Check is one rule of the catalogue.
type Check struct {
	ID  string
	Run func(*Input) []Finding
	// Plan marks a check that reads plans; without any it is skipped, not
	// failed, and the report says so.
	Plan bool
}

// Input is everything a check may read. It is built once by Load; the
// registries a check needs from wire are filled in by the caller.
type Input struct {
	// Layer is the file as written, before migration.
	Layer config.Layer
	// Resolved is defaults, presets and file, migrated: what a run reads.
	Resolved *config.Resolved
	Decoded  config.Decoded
	// Inherited is what the file's extends resolve to on their own - the
	// document the file's own keys are laid over. A key the file repeats
	// from here changes nothing.
	Inherited map[string]any
	// Visited lists every preset the full resolution merged, in order.
	Visited []string
	// Reached maps each of the file's own extends entries to every preset
	// resolving that entry alone merges - what one entry brings in by itself.
	Reached        map[string][]string
	PresetWarnings []string
	// Engine is nil when the rules do not compile; RuleError says why.
	Engine    *rules.Engine
	RuleError string
	// Plans are optional; a Plan check runs only with at least one.
	Plans []*model.Plan
	// Covers reports whether a manager name is implemented; nil disables
	// the check that asks.
	Covers func(string) bool
	// Datasources is the set of datasource names a configuration may use;
	// nil disables the check that asks.
	Datasources map[string]bool
}

// Load resolves a configuration the way a run does and gathers what the
// checks read. A configuration that does not resolve is an error - there is
// nothing to advise on - but rules that do not compile are a finding.
func Load(layer config.Layer, src preset.Source, vs versioning.Registry) (*Input, error) {
	r, warnings, err := config.ResolveLayer(layer, src)
	if err != nil {
		return nil, err
	}
	full, err := preset.Resolve(layer.Raw, src)
	if err != nil {
		return nil, err
	}
	in := &Input{Layer: layer, Resolved: r, Inherited: map[string]any{}, Visited: full.Visited, Reached: map[string][]string{}, PresetWarnings: warnings}
	if ext, ok := layer.Raw["extends"]; ok {
		inherited, err := preset.Resolve(map[string]any{"extends": ext}, src)
		if err != nil {
			return nil, err
		}
		in.Inherited = inherited.Config
		for _, name := range stringsOf(ext) {
			one, err := preset.Resolve(map[string]any{"extends": []any{name}}, src)
			if err != nil {
				return nil, err
			}
			in.Reached[name] = one.Visited
		}
	}
	if in.Decoded, err = config.Decode(r.Raw); err != nil {
		return nil, fmt.Errorf("%s: %w", layer.Source, err)
	}
	if rulesRaw, ok := r.Raw["packageRules"].([]any); ok {
		eng, err := rules.Compile(rulesRaw, vs)
		if err != nil {
			in.RuleError = err.Error()
		} else {
			in.Engine = eng
		}
	}
	return in, nil
}

// Run executes the checks and returns their findings in report order -
// severity, category, ID, pointer - and the IDs of the checks skipped for
// want of a plan.
func Run(in *Input, checks []Check) ([]Finding, []string) {
	var out []Finding
	var skipped []string
	for _, c := range checks {
		if c.Plan && len(in.Plans) == 0 {
			skipped = append(skipped, c.ID)
			continue
		}
		out = append(out, c.Run(in)...)
	}
	slices.SortStableFunc(out, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(rank(a.Severity), rank(b.Severity)),
			cmp.Compare(a.Category, b.Category),
			cmp.Compare(a.ID, b.ID),
			comparePointers(a.Pointer, b.Pointer),
			cmp.Compare(a.Msg, b.Msg),
		)
	})
	return out, skipped
}

// comparePointers orders pointers segment by segment, indices as numbers:
// /packageRules/2 before /packageRules/11.
func comparePointers(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := range min(len(as), len(bs)) {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			return cmp.Compare(an, bn)
		}
		return cmp.Compare(as[i], bs[i])
	}
	return cmp.Compare(len(as), len(bs))
}

func rank(s Severity) int {
	switch s {
	case Error:
		return 0
	case Warn:
		return 1
	}
	return 2
}

// Catalogue is every check, in the order the files declare them.
func Catalogue() []Check {
	var all []Check
	all = append(all, compatChecks...)
	all = append(all, hygieneChecks...)
	all = append(all, perfChecks...)
	all = append(all, secChecks...)
	all = append(all, planChecks...)
	return all
}
