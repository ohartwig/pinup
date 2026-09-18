// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package glob

import (
	"encoding/json"
	"github.com/ohartwig/pinup/fake/fixture"
	"os"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern string
		yes     []string
		no      []string
	}{
		// Exact, the majority case.
		{"solr", []string{"solr"}, []string{"solr-extra", "x/solr", "Solr"}},
		{"typo3/cms", []string{"typo3/cms"}, []string{"typo3/cms-core", "typo3"}},

		// A trailing globstar matches the prefix itself and any depth below.
		{"@moselwal/**",
			[]string{"@moselwal/eslint-config", "@moselwal/a/b/c", "@moselwal"},
			[]string{"@moselwalx", "@koh/x", "x/@moselwal/y"}},
		{"devops/ci-cd-components/**",
			[]string{"devops/ci-cd-components", "devops/ci-cd-components/lint-tools"},
			[]string{"devops/ci-mirrors", "development/ci-cd-components/x"}},

		// A leading globstar: any number of leading segments, including none.
		{"**/composer.json",
			[]string{"composer.json", "src/composer.json", "a/b/c/composer.json"},
			[]string{"composer.lock", "composer.json/x"}},

		// A globstar in the middle.
		{"registry.ole-hartwig.eu/**/sources",
			[]string{
				"registry.ole-hartwig.eu/sources",
				"registry.ole-hartwig.eu/devops/sources",
				"registry.ole-hartwig.eu/devops/images/sources",
			},
			[]string{"registry.ole-hartwig.eu/sources/x", "other.host/devops/sources"}},

		// The brace idiom from the config: {/,} makes the separator optional,
		// so the bare prefix and any subpath both match.
		{"git.ole-hartwig.eu/development/dependency_proxy/containers/{/,}**",
			[]string{
				"git.ole-hartwig.eu/development/dependency_proxy/containers",
				"git.ole-hartwig.eu/development/dependency_proxy/containers/library/node",
				"git.ole-hartwig.eu/development/dependency_proxy/containers/node",
			},
			[]string{
				"git.ole-hartwig.eu/development/dependency_proxy/other/node",
				"git.ole-hartwig.eu:443/development/dependency_proxy/containers/node",
			}},

		// A single star stays inside its segment.
		{"typo3/cms-*",
			[]string{"typo3/cms-core", "typo3/cms-backend"},
			[]string{"typo3/cms", "typo3/cms-core/sub", "typo3/other"}},
		{"*",
			[]string{"solr", "anything"},
			[]string{"a/b"}},

		// Character classes and ?.
		{"php-8.?", []string{"php-8.5", "php-8.4"}, []string{"php-8.10", "php-8."}},
		{"go-1.2[567]", []string{"go-1.25", "go-1.27"}, []string{"go-1.24", "go-1.28"}},
		{"img-[!0-9]", []string{"img-a"}, []string{"img-3"}},
	} {
		m := Compile(c.pattern)
		for _, s := range c.yes {
			if !m.Match(s) {
				t.Errorf("%q should match %q", c.pattern, s)
			}
		}
		for _, s := range c.no {
			if m.Match(s) {
				t.Errorf("%q should NOT match %q", c.pattern, s)
			}
		}
	}
}

func TestExpandBraces(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"plain", []string{"plain"}},
		{"a{b,c}d", []string{"abd", "acd"}},
		{"x/{/,}**", []string{"x//**", "x/**"}},
		{"{a,b}{c,d}", []string{"ac", "ad", "bc", "bd"}},
		{"a{b,{c,d}}e", []string{"abe", "ace", "ade"}},
		{"unbalanced{a,b", []string{"unbalanced{a,b"}},
	} {
		got := ExpandBraces(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("ExpandBraces(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The predicate, which is where a subtle error changes which packages a rule
// governs. Rule 36 in the estate config is negation-only.
func TestSetPredicate(t *testing.T) {
	for _, c := range []struct {
		name    string
		entries []string
		yes     []string
		no      []string
	}{
		{
			name:    "negation only selects everything outside the exclusions",
			entries: []string{"!moselwal/**", "!koh/**", "!@moselwal/**", "!@koh/**"},
			yes:     []string{"symfony/console", "typo3/cms-core", "unrelated"},
			no:      []string{"koh/foo", "moselwal/bar", "@koh/x", "@moselwal/y"},
		},
		{
			name:    "a positive minus a nested negative",
			entries: []string{"registry.ole-hartwig.eu/**", "!registry.ole-hartwig.eu/devops/ci-mirrors/**"},
			yes: []string{
				"registry.ole-hartwig.eu/devops/images/golang",
				"registry.ole-hartwig.eu/devops/images/curl",
			},
			no: []string{
				"registry.ole-hartwig.eu/devops/ci-mirrors/renovate",
				"index.docker.io/library/node",
			},
		},
		{
			name:    "an exact name beside a negated glob",
			entries: []string{"typo3/*", "!typo3/cms-*"},
			yes:     []string{"typo3/cms", "typo3/other"},
			no:      []string{"typo3/cms-core", "symfony/console"},
		},
		{
			name:    "exact positives only",
			entries: []string{"solr", "bash"},
			yes:     []string{"solr", "bash"},
			no:      []string{"solrx", "ba"},
		},
	} {
		s := NewSet(c.entries)
		for _, n := range c.yes {
			if !s.Match(n) {
				t.Errorf("%s: %q should match %v", c.name, n, c.entries)
			}
		}
		for _, n := range c.no {
			if s.Match(n) {
				t.Errorf("%s: %q should NOT match %v", c.name, n, c.entries)
			}
		}
	}
}

// Every pattern in the real config must compile and behave. The counts are
// asserted so a walk that finds nothing cannot pass as agreement.
func TestEveryPatternInTheRealConfigCompiles(t *testing.T) {
	configPath := fixture.Config(t)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("the captured acceptance config is missing: %v", err)
	}
	var cfg struct {
		PackageRules []map[string]json.RawMessage `json:"packageRules"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	var exact, globs, negated, regexes int
	for _, rule := range cfg.PackageRules {
		for _, key := range []string{"matchPackageNames", "matchDepNames", "matchFileNames"} {
			raw, ok := rule[key]
			if !ok {
				continue
			}
			var entries []string
			if err := json.Unmarshal(raw, &entries); err != nil {
				t.Fatalf("%s: %v", key, err)
			}
			for _, e := range entries {
				body := strings.TrimPrefix(e, "!")
				if strings.HasPrefix(e, "!") {
					negated++
				}
				switch {
				case strings.HasPrefix(body, "/") && strings.HasSuffix(body, "/"):
					// The regex form is the caller's business, not this
					// package's; count it so the total adds up.
					regexes++
				case HasMeta(body):
					globs++
					m := Compile(body)
					// A compiled glob must at least match something it was
					// obviously written for: itself with the metacharacters
					// stripped to a plausible value.
					if m.Raw() != body {
						t.Errorf("Compile(%q) lost its raw form", body)
					}
				default:
					exact++
					if m := Compile(body); !m.IsExact() || !m.Match(body) {
						t.Errorf("exact pattern %q does not match itself", body)
					}
				}
			}
		}
	}

	total := exact + globs + regexes
	t.Logf("patterns in default.json: %d total (%d exact, %d glob, %d regex; %d negated)",
		total, exact, globs, regexes, negated)

	// Denominators. These are the measured values; a change means the config
	// moved, and the parity snapshots need to move with it.
	if want := fixture.Expect(t).PatternEntries; total != want {
		t.Errorf("found %d pattern entries, expected %d - the config changed", total, want)
	}
	if exact != 54 || globs != 52 || regexes != 1 {
		t.Errorf("classification drifted: %d exact, %d glob, %d regex; expected 54/52/1", exact, globs, regexes)
	}
	if negated != 12 {
		t.Errorf("found %d negated entries, expected 12", negated)
	}
}

// The empty case is the whole reason IgnoreSet exists: Set and IgnoreSet
// answer opposite questions, and each one's empty behaviour is a bug in the
// other's caller.
func TestEmptySetAndEmptyIgnoreSetDisagreeOnPurpose(t *testing.T) {
	if !NewSet(nil).Match("anything") {
		t.Error("an empty Set should match everything: a rule with only exclusions governs the rest")
	}
	if NewIgnoreSet(nil).Ignores("anything") {
		t.Error("an empty IgnoreSet must ignore nothing; configuring no exclusions excludes none")
	}
	if !NewIgnoreSet([]string{"**/prometheus-exporter/**"}).Ignores("a/prometheus-exporter/b.json") {
		t.Error("a configured exclusion did not exclude")
	}
	if NewIgnoreSet([]string{"**/prometheus-exporter/**"}).Ignores("a/other/b.json") {
		t.Error("an exclusion matched something outside it")
	}
	if !NewIgnoreSet(nil).Empty() || NewIgnoreSet([]string{"x"}).Empty() {
		t.Error("Empty() misreports whether patterns were configured")
	}
}
