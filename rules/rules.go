// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package rules applies packageRules to one dependency.
//
// Layer 2. It takes the resolved configuration - the merged document with
// its packageRules array - and a Subject describing one dependency at one
// moment (before lookup, or after, when the update type is known), and
// returns the configuration that applies to that dependency, plus the list
// of rules that fired in order. That order is the whole answer to "why is
// this dependency configured like this": a later rule overrides an earlier
// one, and Explain names them all.
//
// Semantics are Renovate's, checked against testdata/parity/.../rules:
// every rule is tried in array order; a rule applies when every matcher it
// carries matches; applying merges the rule's non-matcher keys over the
// configuration, arrays and objects replacing, description appending.
// enabled:false sets skipReason "package-rules"; a later enabled:true clears
// it. Nothing here consults the network or the clock.
package rules

import (
	"fmt"
	"sort"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/glob"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// Subject is the dependency as the matchers see it. Empty strings mean
// "not known": a matcher over an unknown field does not match, which is how
// matchUpdateTypes rules stay silent before lookup has produced an update.
type Subject struct {
	DepName        string
	PackageName    string
	Datasource     string
	Manager        string // Renovate's name: "gitlabci", "custom.regex"
	PackageFile    string
	DepType        string
	CurrentValue   string
	CurrentVersion string
	Versioning     string
	UpdateType     string
	SourceURL      string
	// IsLockfileUpdate is the only field the estate's matchJsonata rules read
	// besides SourceURL.
	IsLockfileUpdate bool
}

// SkipPackageRules is the skipReason Renovate writes for enabled:false.
const SkipPackageRules = "package-rules"

// Rule is one compiled packageRules entry.
type Rule struct {
	Index int
	// Apply holds the rule's non-matcher keys, the ones merged on a match.
	Apply map[string]any

	matchers []matcher
	// unsupported names a matcher this engine cannot evaluate; the rule then
	// never fires and Compile reports it, so a silent non-match is not an
	// option.
	unsupported string
}

type matcher func(s Subject, vs versioning.Registry) bool

// Engine is a compiled rule set.
type Engine struct {
	Rules []Rule
	// Warnings lists rules the engine cannot evaluate faithfully. A caller
	// that ignores them is running with rules that never fire.
	Warnings []string

	vs versioning.Registry
}

// Compile prepares packageRules. An unknown match* key is an error: a rule
// carrying a matcher this engine has never heard of would otherwise be
// applied as if the matcher were absent, which is a rule firing for every
// dependency.
func Compile(packageRules []any, vs versioning.Registry) (*Engine, error) {
	e := &Engine{vs: vs}
	for i, raw := range packageRules {
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("packageRules[%d] is not an object", i)
		}
		r := Rule{Index: i, Apply: map[string]any{}}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := obj[k]
			if !strings.HasPrefix(k, "match") && !strings.HasPrefix(k, "exclude") {
				r.Apply[k] = v
				continue
			}
			m, err := compileMatcher(k, v)
			if err != nil {
				var u unsupportedError
				if asUnsupported(err, &u) {
					r.unsupported = string(u)
					e.Warnings = append(e.Warnings, fmt.Sprintf("packageRules[%d]: %s; the rule never fires", i, u))
					continue
				}
				return nil, fmt.Errorf("packageRules[%d].%s: %w", i, k, err)
			}
			r.matchers = append(r.matchers, m)
		}
		e.Rules = append(e.Rules, r)
	}
	return e, nil
}

type unsupportedError string

func (u unsupportedError) Error() string { return string(u) }

func asUnsupported(err error, out *unsupportedError) bool {
	u, ok := err.(unsupportedError)
	if ok {
		*out = u
	}
	return ok
}

func stringList(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("expected strings, found %T", e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected a string or a list of strings, found %T", v)
}

func compileMatcher(key string, v any) (matcher, error) {
	switch key {
	case "matchPackageNames", "matchDepNames", "matchDatasources", "matchManagers",
		"matchDepTypes", "matchUpdateTypes", "matchCurrentValue", "matchSourceUrls",
		"matchFileNames", "matchCategories":
		raw, err := stringList(v)
		if err != nil {
			return nil, err
		}
		l, err := compileList(raw)
		if err != nil {
			return nil, err
		}
		switch key {
		case "matchPackageNames":
			// Measured against packageName; depName is the fallback for a
			// dependency that has no packageName of its own.
			return func(s Subject, _ versioning.Registry) bool {
				if s.PackageName != "" {
					return l.match(s.PackageName)
				}
				return s.DepName != "" && l.match(s.DepName)
			}, nil
		case "matchDepNames":
			return func(s Subject, _ versioning.Registry) bool { return s.DepName != "" && l.match(s.DepName) }, nil
		case "matchDatasources":
			return func(s Subject, _ versioning.Registry) bool { return s.Datasource != "" && l.match(s.Datasource) }, nil
		case "matchManagers":
			// Measured: "custom.regex" in a rule matches a dependency whose
			// manager is "regex" - the prefix names the family, and the
			// matcher compares without it.
			return func(s Subject, _ versioning.Registry) bool {
				if s.Manager == "" {
					return false
				}
				bare := strings.TrimPrefix(s.Manager, "custom.")
				return l.match(bare) || l.match("custom."+bare)
			}, nil
		case "matchDepTypes":
			return func(s Subject, _ versioning.Registry) bool { return s.DepType != "" && l.match(s.DepType) }, nil
		case "matchUpdateTypes":
			return func(s Subject, _ versioning.Registry) bool { return s.UpdateType != "" && l.match(s.UpdateType) }, nil
		case "matchCurrentValue":
			return func(s Subject, _ versioning.Registry) bool { return s.CurrentValue != "" && l.match(s.CurrentValue) }, nil
		case "matchSourceUrls":
			return func(s Subject, _ versioning.Registry) bool {
				// The URL is matched as the datasource reported it; the
				// trailing-slash leniency lives in the pattern, where it
				// is measured to be (a regex sees the slash, a glob
				// forgives it).
				return s.SourceURL != "" && l.match(s.SourceURL)
			}, nil
		case "matchFileNames":
			// File names are paths and case-sensitive; minimatch semantics
			// with `**/` admitting zero directories.
			set := make([]*glob.Matcher, 0, len(raw))
			for _, p := range raw {
				set = append(set, glob.Compile(p))
			}
			return func(s Subject, _ versioning.Registry) bool {
				if s.PackageFile == "" {
					return false
				}
				for _, m := range set {
					if m.Match(s.PackageFile) {
						return true
					}
				}
				return false
			}, nil
		case "matchCategories":
			return nil, unsupportedError("matchCategories: pinup does not compute categories")
		}
	case "matchCurrentVersion":
		raw, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected a string, found %T", v)
		}
		return currentVersionMatcher(raw)
	case "matchJsonata":
		raw, err := stringList(v)
		if err != nil {
			return nil, err
		}
		return jsonataMatcher(raw)
	}
	return nil, fmt.Errorf("unknown matcher")
}

// currentVersionMatcher is Renovate's documented behaviour: a `/regex/`
// tests the current value; anything else is a range in the dependency's
// own versioning, satisfied by the current version.
func currentVersionMatcher(raw string) (matcher, error) {
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "!/") {
		p, err := compilePattern(raw)
		if err != nil {
			return nil, err
		}
		return func(s Subject, _ versioning.Registry) bool {
			if s.CurrentValue == "" {
				return false
			}
			return p.matches(s.CurrentValue) != p.negate
		}, nil
	}
	return func(s Subject, vs versioning.Registry) bool {
		v, err := vs.Get(schemeFor(s))
		if err != nil {
			return false
		}
		cur := s.CurrentVersion
		if !v.IsVersion(cur) {
			cur = s.CurrentValue
		}
		if !v.IsVersion(cur) {
			return false
		}
		return v.Satisfies(cur, raw)
	}, nil
}

func schemeFor(s Subject) string {
	if s.Versioning != "" {
		return s.Versioning
	}
	return "semver"
}

// jsonataMatcher covers the two expression shapes the estate's resolved
// config contains; anything else is reported rather than guessed.
func jsonataMatcher(exprs []string) (matcher, error) {
	var ms []matcher
	for _, expr := range exprs {
		e := strings.TrimSpace(expr)
		switch {
		case e == "isLockfileUpdate = true":
			ms = append(ms, func(s Subject, _ versioning.Registry) bool { return s.IsLockfileUpdate })
		case strings.HasPrefix(e, "$detectPlatform(sourceUrl) = '") && strings.HasSuffix(e, "'"):
			want := strings.TrimSuffix(strings.TrimPrefix(e, "$detectPlatform(sourceUrl) = '"), "'")
			ms = append(ms, func(s Subject, _ versioning.Registry) bool { return detectPlatform(s.SourceURL) == want })
		default:
			return nil, unsupportedError(fmt.Sprintf("matchJsonata %q is outside the supported subset", expr))
		}
	}
	// A list of expressions matches when any of them does.
	return func(s Subject, vs versioning.Registry) bool {
		for _, m := range ms {
			if m(s, vs) {
				return true
			}
		}
		return false
	}, nil
}

// detectPlatform is the subset of Renovate's $detectPlatform the estate's
// rules compare against: the hosted platforms by hostname.
func detectPlatform(sourceURL string) string {
	host := sourceURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return "github"
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com") || strings.HasPrefix(host, "gitlab."):
		return "gitlab"
	case host == "codeberg.org":
		return "forgejo"
	case host == "gitea.com":
		return "gitea"
	}
	return ""
}

// Resolution is the configuration that applies to one subject.
type Resolution struct {
	// Config is the merged result. It shares no memory with the base.
	Config map[string]any
	// Matched lists the indices of the rules that fired, in order.
	Matched []int
	// Wrote maps each key a rule set to the rules that wrote it, in order;
	// the last one won.
	Wrote map[string][]int
	// SkipReason is SkipPackageRules when the subject ends up disabled.
	SkipReason string
}

// Apply resolves the subject against base, which must be the configuration
// the rules were compiled from. base is not modified.
func (e *Engine) Apply(base map[string]any, s Subject) Resolution {
	cfg := make(map[string]any, len(base))
	for k, v := range base {
		if k == "packageRules" {
			continue
		}
		cfg[k] = v
	}
	// The subject's own fields take part in matching only; Renovate carries
	// them in the config object, which is why a rule may set `versioning`
	// and change how the next matchCurrentVersion reads the dependency.
	res := Resolution{Config: cfg, Wrote: map[string][]int{}}
	if enabled, ok := cfg["enabled"].(bool); ok && !enabled {
		res.SkipReason = SkipPackageRules
	}

	for _, r := range e.Rules {
		if r.unsupported != "" || !e.matches(r, s) {
			continue
		}
		res.Matched = append(res.Matched, r.Index)
		for k, v := range r.Apply {
			switch k {
			case "description":
				// Appends, measured: after resolution the key lists every
				// rule that fired. It is also how the harness knows which
				// rules Renovate applied.
				cfg[k] = appendList(cfg[k], v)
			case "enabled":
				on, _ := v.(bool)
				if !on {
					if cur, ok := cfg["enabled"].(bool); !ok || cur {
						res.SkipReason = SkipPackageRules
					}
				} else {
					res.SkipReason = ""
				}
				cfg[k] = v
			case "versioning":
				if name, ok := v.(string); ok {
					s.Versioning = name
				}
				cfg[k] = v
			default:
				// Most objects a rule sets replace the one there, measured:
				// a rule's postUpgradeTasks arrives without the base's
				// empty installTools. prBodyDefinitions is the exception
				// - a rule adding .Package for golang.org/x/ leaves the
				// other twelve columns in place - and it merges key by
				// key into a copy, so the base is never written through.
				if add, ok := v.(map[string]any); ok && mergedObjectKeys[k] {
					if existing, ok := cfg[k].(map[string]any); ok {
						cfg[k] = mergeObjects(existing, add)
						res.Wrote[k] = append(res.Wrote[k], r.Index)
						continue
					}
				}
				cfg[k] = v
			}
			res.Wrote[k] = append(res.Wrote[k], r.Index)
		}
	}
	return res
}

// mergedObjectKeys are the object-valued keys a rule merges into rather
// than replaces. Measured over the rule vectors (3366, rules/parity_test):
// only prBodyDefinitions behaves this way among the keys the estate's
// rules set; a key not exercised there replaces, the behaviour every
// other measured object shows.
var mergedObjectKeys = map[string]bool{"prBodyDefinitions": true}

// mergeObjects returns a new object holding base's keys overlaid with add's,
// nested objects merged the same way, arrays and scalars replaced.
func mergeObjects(base, add map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(add))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range add {
		if addMap, ok := v.(map[string]any); ok {
			if baseMap, ok := out[k].(map[string]any); ok {
				out[k] = mergeObjects(baseMap, addMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func (e *Engine) matches(r Rule, s Subject) bool {
	for _, m := range r.matchers {
		if !m(s, e.vs) {
			return false
		}
	}
	return true
}

func appendList(existing, add any) any {
	var out []any
	switch x := existing.(type) {
	case []any:
		out = append(out, x...)
	case string:
		out = append(out, x)
	}
	switch x := add.(type) {
	case []any:
		out = append(out, x...)
	default:
		out = append(out, x)
	}
	return out
}

// Explain renders which rules wrote a key, winner last. An empty chain says
// so: "set by no rule" is a different fact from "set to the default".
func (r Resolution) Explain(key string) string {
	chain := r.Wrote[key]
	if len(chain) == 0 {
		return fmt.Sprintf("%s: set by no rule", key)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", key)
	for i, idx := range chain {
		mark := "  "
		if i == len(chain)-1 {
			mark = "> "
		}
		fmt.Fprintf(&b, "%spackageRules[%d]\n", mark, idx)
	}
	return b.String()
}

// SubjectOf builds a matching subject from a dependency. Manager names are
// translated to Renovate's: every custom regex definition is "custom.regex".
func SubjectOf(d model.Dependency, updateType string) Subject {
	manager := d.Manager
	if d.CustomManager != model.NoCustomManager {
		manager = "custom.regex"
	}
	return Subject{
		DepName: d.DepName, PackageName: d.PackageName, Datasource: d.Datasource,
		Manager: manager, PackageFile: d.File, DepType: d.DepType,
		CurrentValue: d.CurrentValue, CurrentVersion: d.CurrentValue,
		Versioning: d.Versioning, UpdateType: updateType, SourceURL: d.SourceURL,
	}
}
