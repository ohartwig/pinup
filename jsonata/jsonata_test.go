// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package jsonata

import (
	"encoding/json"
	"github.com/ohartwig/pinup/fake/fixture"
	"reflect"
	"strings"
	"testing"
)

func doc(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The two expressions the estate actually uses, against the payload shapes the
// real endpoints serve.
func TestTheTwoRealTransforms(t *testing.T) {
	// wolfi and koh-apk: the sidecar serves {"versions":[{"version":"..."}]}
	got, err := Transform(`{ "releases": versions }`,
		doc(t, `{"versions":[{"version":"1.27.0-r1"},{"version":"1.27.1-r0"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"releases": []any{
		map[string]any{"version": "1.27.0-r1"},
		map[string]any{"version": "1.27.1-r0"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wolfi transform:\n got %#v\nwant %#v", got, want)
	}

	// protonpass: a path mapped over an array, rebuilt into release objects.
	got, err = Transform(`{ "releases": [ { "version": passCliVersions.version } ] }`,
		doc(t, `{"passCliVersions":[{"version":"1.2.3","os":"linux"},{"version":"1.3.0","os":"linux"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"releases": []any{
		map[string]any{"version": "1.2.3"},
		map[string]any{"version": "1.3.0"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("protonpass transform:\n got %#v\nwant %#v", got, want)
	}
}

func TestPathsMapOverSequences(t *testing.T) {
	got, err := Transform(`a.b`, doc(t, `{"a":[{"b":1},{"b":2},{"c":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []any{1.0, 2.0}) {
		t.Errorf("got %#v, want [1 2] - a path over an array maps and drops misses", got)
	}
	// A missing field yields nothing rather than an error: a datasource whose
	// payload lacks a field should produce no releases, not a failed run.
	got, err = Transform(`nosuch.field`, doc(t, `{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("got %#v, want nil", got)
	}
}

// An unsupported construct must be a located error. A transform that silently
// produced nothing would look exactly like a package with no releases.
func TestUnsupportedConstructsAreLocatedErrors(t *testing.T) {
	for _, c := range []struct{ expr, want string }{
		{`$map(versions, function($v) { $v })`, "unsupported construct"},
		{`versions[0]`, "predicates and index expressions"},
		{`{ "releases": versions`, "unclosed object"},
		{`[ versions`, "unclosed array"},
		{`{ versions }`, "expected a quoted string"},
		{`{ "a" versions }`, "expected ':'"},
		{`"unterminated`, "unterminated string"},
		{`1 + 1`, "unsupported construct"},
		{``, "ended early"},
	} {
		_, err := Transform(c.expr, doc(t, `{}`))
		if err == nil {
			t.Errorf("%q was accepted; an unreadable transform must be refused", c.expr)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %q does not mention %q", c.expr, err, c.want)
		}
		if _, ok := err.(*Error); !ok {
			t.Errorf("%q: error carries no location", c.expr)
		}
	}
}

// Every transform in the real config must parse, so a new one appearing
// upstream turns this red before it turns a run wrong.
func TestEveryTransformInTheRealConfigParses(t *testing.T) {
	raw := doc(t, mustRead(t, fixture.Config(t)))
	cfg, _ := raw.(map[string]any)
	ds, _ := cfg["customDatasources"].(map[string]any)
	if len(ds) == 0 {
		t.Fatal("no customDatasources found; the walk did not reach the config")
	}
	var checked int
	for name, v := range ds {
		m, _ := v.(map[string]any)
		tpls, _ := m["transformTemplates"].([]any)
		for _, tpl := range tpls {
			s, _ := tpl.(string)
			checked++
			if _, err := Parse(s); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	t.Logf("parsed %d transform templates from %d custom datasources", checked, len(ds))
	if checked != 3 {
		t.Errorf("found %d transforms, expected 3", checked)
	}
}
