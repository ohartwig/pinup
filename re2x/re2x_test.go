// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package re2x

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// estateConfigPath is the real config this package's whole design is
// justified by: three pairs of customManagers definitions in it exist
// only because Renovate cannot distinguish "group absent" from "group
// matched empty". Reading it (never writing) keeps the tests honest
// against the patterns actually in production rather than idealized
// ones invented for the test file.
const estateConfigPath = "../testdata/parity/config/default.json"

// estateMatchStrings loads every customManagers[].matchStrings entry
// from the real estate config.
func estateMatchStrings(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(estateConfigPath)
	if err != nil {
		t.Fatalf("reading estate config: %v", err)
	}
	var doc struct {
		CustomManagers []struct {
			MatchStrings []string `json:"matchStrings"`
		} `json:"customManagers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing estate config: %v", err)
	}
	var patterns []string
	for _, cm := range doc.CustomManagers {
		patterns = append(patterns, cm.MatchStrings...)
	}
	if len(patterns) == 0 {
		t.Fatal("estate config yielded no matchStrings; test fixture path may be wrong")
	}
	return patterns
}

// wantGroup is one named-group assertion within a table case: either the
// group participated with a specific value, or it did not participate at
// all (present=false). The two must never be conflated - that conflation
// is exactly the upstream bug this package exists to avoid.
type wantGroup struct {
	name    string
	present bool
	value   string
}

func TestParticipationVersusEmptyVersusAbsent(t *testing.T) {
	for _, c := range []struct {
		name    string
		pattern string
		src     string
		want    []wantGroup
	}{
		{
			// Real shape from the estate config (customManagers[0]):
			// an optional trailing digest group that either does not
			// appear in the source at all, or appears in full.
			name:    "optional digest group absent when source has none",
			pattern: `(?<depName>registry\.ole-hartwig\.eu/devops/ci-mirrors/[a-z0-9._/-]+):(?<currentValue>[a-zA-Z0-9][a-zA-Z0-9._-]*)(?:@(?<currentDigest>sha256:[a-f0-9]{64}))?`,
			src:     "registry.ole-hartwig.eu/devops/ci-mirrors/library/alpine:3.19",
			want: []wantGroup{
				{name: "depName", present: true, value: "registry.ole-hartwig.eu/devops/ci-mirrors/library/alpine"},
				{name: "currentValue", present: true, value: "3.19"},
				{name: "currentDigest", present: false},
			},
		},
		{
			// Same pattern, source now carries the digest: the group
			// must flip to present with the exact captured text.
			name:    "optional digest group present when source has one",
			pattern: `(?<depName>registry\.ole-hartwig\.eu/devops/ci-mirrors/[a-z0-9._/-]+):(?<currentValue>[a-zA-Z0-9][a-zA-Z0-9._-]*)(?:@(?<currentDigest>sha256:[a-f0-9]{64}))?`,
			src:     "registry.ole-hartwig.eu/devops/ci-mirrors/library/alpine:3.19@sha256:" + strings.Repeat("ab", 32),
			want: []wantGroup{
				{name: "currentValue", present: true, value: "3.19"},
				{name: "currentDigest", present: true, value: "sha256:" + strings.Repeat("ab", 32)},
			},
		},
		{
			// A group that participates but captures zero bytes. This
			// must read as present=true, value="" - the one case that
			// FindStringSubmatch cannot tell apart from "absent", and
			// the one this package must never get wrong.
			name:    "group that matches is present even when the match is empty",
			pattern: `^(?<lead>a*)b(?<tail>c*)$`,
			src:     "b",
			want: []wantGroup{
				{name: "lead", present: true, value: ""},
				{name: "tail", present: true, value: ""},
			},
		},
		{
			// Nested named group, verbatim from customManagers[20]: both
			// the outer currentValue and the inner phpSeries must be
			// independently retrievable.
			name:    "nested named group",
			pattern: `"platform"\s*:\s*\{[^}]*?"php"\s*:\s*"(?<currentValue>(?<phpSeries>\d+\.\d+)(?:\.\d+)?)"`,
			src:     `"platform": {"php": "8.2.10"}`,
			want: []wantGroup{
				{name: "currentValue", present: true, value: "8.2.10"},
				{name: "phpSeries", present: true, value: "8.2"},
			},
		},
		{
			// Nested group without the optional patch component: proves
			// phpSeries isn't just echoing currentValue by accident.
			name:    "nested named group without patch component",
			pattern: `"platform"\s*:\s*\{[^}]*?"php"\s*:\s*"(?<currentValue>(?<phpSeries>\d+\.\d+)(?:\.\d+)?)"`,
			src:     `"platform": {"php": "8.2"}`,
			want: []wantGroup{
				{name: "currentValue", present: true, value: "8.2"},
				{name: "phpSeries", present: true, value: "8.2"},
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			re, err := Compile(c.pattern)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			m, ok := re.Find(c.src)
			if !ok {
				t.Fatalf("Find(%q): no match", c.src)
			}
			for _, w := range c.want {
				value, present := m.Get(w.name)
				if present != w.present {
					t.Errorf("Get(%q) present = %v, want %v", w.name, present, w.present)
				}
				if present && value != w.value {
					t.Errorf("Get(%q) value = %q, want %q", w.name, value, w.value)
				}
				if absent := m.Absent(w.name); absent != !w.present {
					t.Errorf("Absent(%q) = %v, want %v", w.name, absent, !w.present)
				}
			}
		})
	}
}

func TestSpanPointsAtOriginalBytes(t *testing.T) {
	for _, c := range []struct {
		name    string
		pattern string
		src     string
		group   string
	}{
		{
			name:    "currentValue span in a digest-bearing tag",
			pattern: `(?<depName>registry\.ole-hartwig\.eu/devops/ci-mirrors/[a-z0-9._/-]+):(?<currentValue>[a-zA-Z0-9][a-zA-Z0-9._-]*)(?:@(?<currentDigest>sha256:[a-f0-9]{64}))?`,
			src:     "registry.ole-hartwig.eu/devops/ci-mirrors/library/alpine:3.19@sha256:" + strings.Repeat("cd", 32),
			group:   "currentDigest",
		},
		{
			name:    "nested phpSeries span sits inside currentValue's span",
			pattern: `"platform"\s*:\s*\{[^}]*?"php"\s*:\s*"(?<currentValue>(?<phpSeries>\d+\.\d+)(?:\.\d+)?)"`,
			src:     `"platform": {"php": "8.2.10"}`,
			group:   "phpSeries",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			re, err := Compile(c.pattern)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			m, ok := re.Find(c.src)
			if !ok {
				t.Fatalf("Find(%q): no match", c.src)
			}
			wantValue, present := m.Get(c.group)
			if !present {
				t.Fatalf("Get(%q): group did not participate", c.group)
			}
			start, end, ok := m.Span(c.group)
			if !ok {
				t.Fatalf("Span(%q): reported absent, but Get found it", c.group)
			}
			if got := c.src[start:end]; got != wantValue {
				t.Errorf("src[%d:%d] = %q, want %q (from Get)", start, end, got, wantValue)
			}
		})
	}
}

func TestSpanReportsNotOKForAbsentGroup(t *testing.T) {
	// A byte span of [0,0) for an absent group would be indistinguishable
	// from a real zero-length match at the start of the string, so Span
	// must refuse with ok=false rather than hand back a bogus range.
	re, err := Compile(`(?<depName>[a-z]+):(?<currentValue>[a-z0-9.]+)(?:@(?<currentDigest>sha256:[a-f0-9]{64}))?`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	m, ok := re.Find("alpine:3.19")
	if !ok {
		t.Fatal("Find: no match")
	}
	if start, end, ok := m.Span("currentDigest"); ok {
		t.Errorf("Span(%q) = (%d, %d, true), want ok=false for an absent group", "currentDigest", start, end)
	}
}

func TestNamesOrdering(t *testing.T) {
	re, err := Compile(`(?<currentValue>(?<phpSeries>\d+\.\d+)(?:\.\d+)?)`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := []string{"currentValue", "phpSeries"}
	if got := re.Names(); !equalStrings(got, want) {
		t.Errorf("Regexp.Names() = %v, want %v", got, want)
	}

	m, ok := re.Find("8.2.10")
	if !ok {
		t.Fatal("Find: no match")
	}
	if got := m.Names(); !equalStrings(got, want) {
		t.Errorf("Match.Names() = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFindAllReturnsEveryMatch(t *testing.T) {
	re, err := Compile(`(?<depName>moselwal/[a-z0-9_-]+)"\s*:\s*"(?<currentValue>[^"]+)"`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	src := `{"moselwal/fa4t3": "1.0.0", "moselwal/acme-news": "2.3.1"}`
	matches := re.FindAll(src)
	if len(matches) != 2 {
		t.Fatalf("FindAll: got %d matches, want 2", len(matches))
	}
	for _, c := range []struct {
		idx  int
		want string
	}{
		{0, "1.0.0"},
		{1, "2.3.1"},
	} {
		got, present := matches[c.idx].Get("currentValue")
		if !present {
			t.Errorf("match %d: currentValue absent", c.idx)
		}
		if got != c.want {
			t.Errorf("match %d: currentValue = %q, want %q", c.idx, got, c.want)
		}
	}
}

func TestCheckRE2AcceptsEveryEstatePattern(t *testing.T) {
	patterns := estateMatchStrings(t)
	for i, p := range patterns {
		if err := CheckRE2(p); err != nil {
			t.Errorf("pattern %d (%q): CheckRE2 rejected a real estate pattern: %v", i, p, err)
		}
	}
	t.Logf("CheckRE2 ran over %d matchStrings patterns from %s", len(patterns), estateConfigPath)
}

func TestCompileAcceptsEveryEstatePattern(t *testing.T) {
	// Compile calls CheckRE2 itself, but this also proves every estate
	// pattern is valid RE2 syntax once past the lint - Go's own compile
	// step doesn't just take the lint's word for it.
	patterns := estateMatchStrings(t)
	for i, p := range patterns {
		if _, err := Compile(p); err != nil {
			t.Errorf("pattern %d (%q): Compile failed: %v", i, p, err)
		}
	}
}

func TestCheckRE2RejectsBackreferencesAndLookaroundButAcceptsNamedGroups(t *testing.T) {
	for _, c := range []struct {
		name    string
		pattern string
		wantErr bool
	}{
		// The one bug this function must not have: a named group must
		// never be mistaken for lookbehind, since (?<name>...) and
		// (?<=...) share their first three characters.
		{name: "named group is not lookbehind", pattern: `(?<name>foo)`, wantErr: false},
		{name: "named group among several, mirrors estate patterns", pattern: `(?<depName>foo)-(?<currentValue>\d+)`, wantErr: false},

		{name: "backreference", pattern: `(\w)\1`, wantErr: true},
		{name: "lookahead", pattern: `(?=foo)`, wantErr: true},
		{name: "negative lookahead", pattern: `(?!foo)`, wantErr: true},
		{name: "lookbehind", pattern: `(?<=foo)`, wantErr: true},
		{name: "negative lookbehind", pattern: `(?<!foo)`, wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := CheckRE2(c.pattern)
			if (err != nil) != c.wantErr {
				t.Errorf("CheckRE2(%q) error = %v, wantErr %v", c.pattern, err, c.wantErr)
			}
		})
	}
}

// Whole is what the recursive matchStrings strategy needs: the outer pattern
// narrows a region and the inner one searches only inside it, so the inner
// search must be confined to exactly these bytes.
func TestWholeSpansTheEntireMatch(t *testing.T) {
	src := "prefix image: alpine:3.21 suffix"
	re, err := Compile(`image: (?<name>[a-z]+):(?<tag>[0-9.]+)`)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := re.Find(src)
	if !ok {
		t.Fatal("no match")
	}
	s, e, ok := m.Whole()
	if !ok {
		t.Fatal("Whole reported no span")
	}
	if got := src[s:e]; got != "image: alpine:3.21" {
		t.Errorf("Whole spans %q, want %q", got, "image: alpine:3.21")
	}
	// The whole match must contain every named group's span.
	for _, name := range m.Names() {
		gs, ge, ok := m.Span(name)
		if !ok {
			continue
		}
		if gs < s || ge > e {
			t.Errorf("group %q spans [%d:%d], outside the whole match [%d:%d]", name, gs, ge, s, e)
		}
	}
}
