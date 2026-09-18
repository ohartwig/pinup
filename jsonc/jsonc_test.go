// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package jsonc

import (
	"encoding/json"
	"github.com/ohartwig/pinup/fake/fixture"
	"reflect"
	"strings"
	"testing"
)

func TestStripProducesTheSameValueAsTheCommentFreeEquivalent(t *testing.T) {
	withComments := `{
  // a line comment
  "a": 1, /* inline block */
  "b": [
    1,
    2,   // trailing comment
  ],
  /* a
     multi-line
     block */
  "c": "a string with // not a comment and /* not one either */",
  "d": {"e": true,},
}`
	plain := `{
  "a": 1,
  "b": [1, 2],
  "c": "a string with // not a comment and /* not one either */",
  "d": {"e": true}
}`

	var got, want any
	if err := json.Unmarshal(Strip([]byte(withComments)), &got); err != nil {
		t.Fatalf("stripped document does not parse: %v\n%s", err, Strip([]byte(withComments)))
	}
	if err := json.Unmarshal([]byte(plain), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values differ\n got %#v\nwant %#v", got, want)
	}
}

// The reason this package exists rather than a regex: an error must point at
// the byte the user wrote.
func TestOffsetsArePreserved(t *testing.T) {
	src := []byte(`{
  // comment that would shift everything after it
  "a": nope
}`)
	stripped := Strip(src)
	if len(stripped) != len(src) {
		t.Fatalf("length changed: %d -> %d", len(src), len(stripped))
	}

	err := json.Unmarshal(stripped, &struct{}{})
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	se, ok := err.(*json.SyntaxError)
	if !ok {
		t.Fatalf("expected a *json.SyntaxError, got %T", err)
	}
	// The offset must land on the bad token in the ORIGINAL text.
	around := string(src[max(0, int(se.Offset)-6):min(len(src), int(se.Offset)+4)])
	if !strings.Contains(around, "nope") {
		t.Errorf("offset %d points at %q in the original, not at the bad token", se.Offset, around)
	}
	// And the line count must be unchanged, so line numbers still match.
	if a, b := strings.Count(string(src), "\n"), strings.Count(string(stripped), "\n"); a != b {
		t.Errorf("line count changed: %d -> %d", a, b)
	}
}

func TestStringsAreNotTouched(t *testing.T) {
	for _, s := range []string{
		`{"a":"// not a comment"}`,
		`{"a":"/* not a comment */"}`,
		`{"a":"trailing comma inside a string: ,]"}`,
		`{"a":"escaped quote \" then // still a string"}`,
		`{"a":"backslash at end \\"}`,
	} {
		if got := string(Strip([]byte(s))); got != s {
			t.Errorf("Strip changed a string literal:\n got %s\nwant %s", got, s)
		}
	}
}

func TestTrailingCommas(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`[1,2,]`, `[1,2 ]`},
		{`{"a":1,}`, `{"a":1 }`},
		{`[1,2,
]`, `[1,2 
]`},
		{`[[1,],]`, `[[1 ] ]`},
		{`[1,2]`, `[1,2]`}, // nothing to do
	} {
		if got := string(Strip([]byte(c.in))); got != c.want {
			t.Errorf("Strip(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHasComments(t *testing.T) {
	if HasComments([]byte(`{"a":1}`)) {
		t.Error("plain JSON reported as having comments")
	}
	if !HasComments([]byte(`{"a":1} // yes`)) {
		t.Error("a comment was not detected")
	}
}

// The acceptance surface is plain JSON, and it must survive untouched.
func TestTheRealConfigIsUnchanged(t *testing.T) {
	src := mustRead(t, fixture.Config(t))
	if HasComments(src) {
		t.Error("default.json contains comments; it is meant to be plain JSON")
	}
	var a, b any
	if err := json.Unmarshal(src, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(Strip(src), &b); err != nil {
		t.Fatalf("stripping the real config broke it: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("stripping changed the value of the real config")
	}
}
