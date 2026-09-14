// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package hbs

import (
	"encoding/json"
	"github.com/ohartwig/pinup/fake/fixture"
	"os"
	"strings"
	"testing"
)

// manager10 is the packageNameTemplate of custom manager #10 in the estate
// config, verbatim. It is the deepest construct in use - three nested #if with
// the equals helper - and it decides which GitLab project a first-party
// composer package resolves against.
const manager10 = `{{#if (equals depName 'moselwal/fa4t3')}}development/moselwal/typo3-fathom-analytics{{else}}{{#if (equals depName 'moselwal/acme-news')}}development/acme/extensions/acme-news{{else}}{{#if (equals depName 'moselwal/acme-sitepackage')}}development/acme/extensions/sitepackage{{else}}development/{{depName}}{{/if}}{{/if}}{{/if}}:{{depName}}`

func env(pairs ...string) MapEnv {
	m := MapEnv{Values: map[string]string{}, Absent: map[string]bool{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		m.Values[pairs[i]] = pairs[i+1]
	}
	return m
}

func TestManager10Template(t *testing.T) {
	for _, c := range []struct{ depName, want string }{
		{"moselwal/fa4t3", "development/moselwal/typo3-fathom-analytics:moselwal/fa4t3"},
		{"moselwal/acme-news", "development/acme/extensions/acme-news:moselwal/acme-news"},
		{"moselwal/acme-sitepackage", "development/acme/extensions/sitepackage:moselwal/acme-sitepackage"},
		{"moselwal/anything-else", "development/moselwal/anything-else:moselwal/anything-else"},
	} {
		got, set, err := RenderString(manager10, env("depName", c.depName))
		if err != nil {
			t.Fatalf("%s: %v", c.depName, err)
		}
		if !set {
			t.Errorf("%s: field reported unset", c.depName)
		}
		if got != c.want {
			t.Errorf("depName=%q:\n got %q\nwant %q", c.depName, got, c.want)
		}
	}
}

// The tri-state environment. This is the behaviour that lets one manager
// replace a pair, so it is tested as a first-class case rather than an edge.
func TestAbsentNameUnsetsTheField(t *testing.T) {
	absent := MapEnv{Values: map[string]string{}, Absent: map[string]bool{"registryUrl": true}}

	for _, c := range []struct {
		name    string
		tmpl    string
		env     Env
		wantVal string
		wantSet bool
	}{
		{
			name:    "a whole-template interpolation of an absent group is unset",
			tmpl:    "{{{registryUrl}}}",
			env:     absent,
			wantVal: "", wantSet: false,
		},
		{
			name:    "so is one with literal text around it",
			tmpl:    "https://{{{registryUrl}}}",
			env:     absent,
			wantVal: "https://", wantSet: false,
		},
		{
			name:    "a present-but-empty group is set, and empty",
			tmpl:    "{{{registryUrl}}}",
			env:     env("registryUrl", ""),
			wantVal: "", wantSet: true,
		},
		{
			name:    "a present group is set",
			tmpl:    "https://{{{registryUrl}}}",
			env:     env("registryUrl", "git.ole-hartwig.eu"),
			wantVal: "https://git.ole-hartwig.eu", wantSet: true,
		},
		{
			// Absence consumed by a condition must NOT unset the field: the
			// else branch is a real answer.
			name:    "absence inside an if condition falls to else and stays set",
			tmpl:    "{{#if versioning}}{{{versioning}}}{{else}}semver{{/if}}",
			env:     MapEnv{Values: map[string]string{}, Absent: map[string]bool{"versioning": true}},
			wantVal: "semver", wantSet: true,
		},
		{
			name:    "and when it is present the branch is taken",
			tmpl:    "{{#if versioning}}{{{versioning}}}{{else}}semver{{/if}}",
			env:     env("versioning", "docker"),
			wantVal: "docker", wantSet: true,
		},
		{
			name:    "an empty value is falsy, as in Handlebars",
			tmpl:    "{{#if versioning}}{{{versioning}}}{{else}}semver{{/if}}",
			env:     env("versioning", ""),
			wantVal: "semver", wantSet: true,
		},
	} {
		got, set, err := RenderString(c.tmpl, c.env)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.wantVal || set != c.wantSet {
			t.Errorf("%s:\n got (%q, set=%v)\nwant (%q, set=%v)", c.name, got, set, c.wantVal, c.wantSet)
		}
	}
}

func TestEscaping(t *testing.T) {
	e := env("v", "a<b&c")
	raw, _, err := RenderString("{{{v}}}", e)
	if err != nil {
		t.Fatal(err)
	}
	if raw != "a<b&c" {
		t.Errorf("triple-stash escaped its value: %q", raw)
	}
	esc, _, err := RenderString("{{v}}", e)
	if err != nil {
		t.Fatal(err)
	}
	if esc == raw {
		t.Errorf("double-stash did not escape: %q", esc)
	}
	if !strings.Contains(esc, "&lt;") {
		t.Errorf("double-stash escaping looks wrong: %q", esc)
	}
}

// An unsupported construct must be a located error naming the token. A subset
// that renders the wrong thing quietly is worse than one that refuses.
func TestUnsupportedConstructsAreLocatedErrors(t *testing.T) {
	for _, c := range []struct{ tmpl, want string }{
		{"{{#each xs}}{{/each}}", "unsupported block helper"},
		{"{{#if (lookup a b)}}x{{/if}}", "unsupported helper"},
		{"{{a.b}}", "unsupported name"},
		{"{{@index}}", "unsupported name"},
		{"{{}}", "empty interpolation"},
		{"{{#if x}}unclosed", "unclosed"},
		{"{{unclosed", "unclosed {{"},
		{"{{{unclosed}}", "unclosed {{{"},
		{"{{/if}}", "unexpected closing tag"},
		{"{{else}}", "outside an"},
		{"{{#if (equals x)}}a{{/if}}", "two arguments"},
		{"{{#if (equals x y)}}a{{/if}}", "quoted literal"},
	} {
		_, _, err := RenderString(c.tmpl, env())
		if err == nil {
			t.Errorf("%q rendered without error; it must be refused", c.tmpl)
			continue
		}
		var e *Error
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %q does not mention %q", c.tmpl, err, c.want)
		}
		if _, ok := err.(*Error); !ok {
			_ = e
			t.Errorf("%q: error is not a *hbs.Error, so it carries no location", c.tmpl)
		}
	}
}

func TestNames(t *testing.T) {
	tmpl, err := Parse(manager10)
	if err != nil {
		t.Fatal(err)
	}
	got := tmpl.Names()
	if len(got) != 1 || got[0] != "depName" {
		t.Errorf("Names() = %v, want [depName]", got)
	}
}

func TestPlainTextIsAlwaysSet(t *testing.T) {
	got, set, err := RenderString("gitlab-releases", env())
	if err != nil {
		t.Fatal(err)
	}
	if got != "gitlab-releases" || !set {
		t.Errorf("got (%q, %v), want (\"gitlab-releases\", true)", got, set)
	}
}

// The template surface is closed: every construct in the real config must
// parse with this package and use only implemented helpers. A new helper
// appearing upstream then turns this red before it turns a run wrong.
func TestEveryTemplateInTheRealConfigParses(t *testing.T) {
	raw, err := os.ReadFile(fixture.Config(t))
	if err != nil {
		t.Fatalf("the captured acceptance config is missing: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	var checked int
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			if !strings.Contains(v, "{{") {
				return
			}
			checked++
			if _, err := Parse(v); err != nil {
				t.Errorf("a template in the config does not parse: %v", err)
			}
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(cfg)

	t.Logf("parsed %d templates from default.json", checked)
	// Denominator: a walk that found nothing would pass silently.
	if checked < 25 {
		t.Errorf("only %d templates found; the walk did not reach the config", checked)
	}
}
