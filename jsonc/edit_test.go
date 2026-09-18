// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package jsonc

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// The scanner records spans in the bytes the user wrote: comments, trailing
// commas and JSON5 keys included. A pointer lands on the exact value text.
func TestScanLocatesValuesInTheOriginalBytes(t *testing.T) {
	for _, c := range []struct {
		name, src, pointer, want string
		err                      string
	}{
		{"plain", `{"a": 1, "b": [true, "x"]}`, "/b/1", `"x"`, ""},
		{"escaped segment", `{"c/d": {"~": 2}}`, "/c~1d/~0", `2`, ""},
		{"comment marker inside a string", `{"url": "http://x", "a": 1}`, "/a", `1`, ""},
		{"line comment before the value", "{\n  // the value\n  \"a\": 1\n}", "/a", `1`, ""},
		{"block comment before the value", `{"a": /* why */ 1}`, "/a", `1`, ""},
		{"trailing comma in an object", "{\n  \"a\": 1,\n}", "/a", `1`, ""},
		{"trailing comma in an array", `{"a": [1, 2,]}`, "/a/1", `2`, ""},
		{"json5 bare key and single quotes", "{\n  extends: ['config:recommended'],\n}", "/extends/0", `'config:recommended'`, ""},
		{"empty containers", `{"a": {}, "b": []}`, "/b", `[]`, ""},
		{"nested rule", `{"packageRules": [{"automerge": true}, {"enabled": false}]}`, "/packageRules/1/enabled", `false`, ""},
		{"missing intermediate", `{"a": {"b": 1}}`, "/x/y", "", "/x/y: /x is not in the file"},
		{"unterminated string", `{"a": "x`, "/a", "", "unterminated string"},
		{"text after the document", `{"a": 1} {`, "/a", "", "text after the document"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, err := scan([]byte(c.src))
			var got string
			if err == nil {
				var parent *node
				var idx int
				parent, _, idx, err = find([]byte(c.src), root, c.pointer)
				if err == nil {
					if idx < 0 {
						t.Fatalf("%s not found", c.pointer)
					}
					v := parent.elems[idx].val
					got = c.src[v.start:v.end]
				}
			}
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("value at %s = %q, want %q", c.pointer, got, c.want)
			}
		})
	}
}

// Every edit leaves the bytes outside its range untouched and a document
// that still parses; the cases pin the text the edit produces.
func TestEditsAreByteExact(t *testing.T) {
	type op func(src []byte, pointer string, value []byte) ([]byte, Edit, error)
	set := func(src []byte, p string, v []byte) ([]byte, Edit, error) { return Set(src, p, v) }
	remove := func(src []byte, p string, _ []byte) ([]byte, Edit, error) { return Remove(src, p) }
	appendOp := func(src []byte, p string, v []byte) ([]byte, Edit, error) { return Append(src, p, v) }
	for _, c := range []struct {
		name    string
		op      op
		src     string
		pointer string
		value   string
		want    string
		err     string
	}{
		// set
		{"set replaces a scalar", set, "{\n  \"a\": 1,\n  \"b\": 2\n}", "/a", "true", "{\n  \"a\": true,\n  \"b\": 2\n}", ""},
		{"set replaces an array inline", set, `{"a": ["x", "y"]}`, "/a", `["x"]`, `{"a": ["x"]}`, ""},
		{"set adds after the last member with its indent", set, "{\n  \"a\": 1\n}", "/k", `"v"`, "{\n  \"a\": 1,\n  \"k\": \"v\"\n}", ""},
		{"set adds after a trailing comma keeping the style", set, "{\n  \"a\": 1,\n}", "/k", `"v"`, "{\n  \"a\": 1,\n  \"k\": \"v\",\n}", ""},
		{"set adds keeping a trailing comment on its line", set, "{\n  \"a\": 1 // one\n}", "/k", `2`, "{\n  \"a\": 1, // one\n  \"k\": 2\n}", ""},
		{"set adds behind a trailing comma and its comment", set, "{\n  \"a\": 1, // one\n}", "/k", `2`, "{\n  \"a\": 1, // one\n  \"k\": 2,\n}", ""},
		{"set adds to a one-line object", set, `{ "a": 1 }`, "/k", `2`, `{ "a": 1, "k": 2 }`, ""},
		{"set into an empty object in a multi-line document", set, "{\n  \"major\": {}\n}", "/major/automerge", "false", "{\n  \"major\": {\n    \"automerge\": false\n  }\n}", ""},
		{"set into an empty object on one line", set, `{"major": {}}`, "/major/automerge", "false", `{"major": { "automerge": false }}`, ""},
		{"set indents a multi-line value", set, "{\n  \"a\": 1\n}", "/r", "{\n  \"x\": 1\n}", "{\n  \"a\": 1,\n  \"r\": {\n    \"x\": 1\n  }\n}", ""},
		{"set keeps CRLF", set, "{\r\n  \"a\": 1\r\n}", "/k", "2", "{\r\n  \"a\": 1,\r\n  \"k\": 2\r\n}", ""},
		{"set refuses a missing parent", set, `{"a": 1}`, "/major/automerge", "false", "", "/major/automerge: /major is not in the file"},
		{"set on an array item", set, `{"a": [1, 2]}`, "/a/1", "3", `{"a": [1, 3]}`, ""},
		// remove
		{"remove a middle member takes its line", remove, "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}", "/b", "", "{\n  \"a\": 1,\n  \"c\": 3\n}", ""},
		{"remove the first member", remove, "{\n  \"a\": 1,\n  \"b\": 2\n}", "/a", "", "{\n  \"b\": 2\n}", ""},
		{"remove the last member takes the comma before it", remove, "{\n  \"a\": 1,\n  \"b\": 2\n}", "/b", "", "{\n  \"a\": 1\n}", ""},
		{"remove the last member behind a trailing comma", remove, "{\n  \"a\": 1,\n  \"b\": 2,\n}", "/b", "", "{\n  \"a\": 1,\n}", ""},
		{"remove takes the comment closing its line", remove, "{\n  \"a\": 1, // gone\n  \"b\": 2\n}", "/a", "", "{\n  \"b\": 2\n}", ""},
		{"remove keeps the comment on the line above", remove, "{\n  // stays\n  \"a\": 1,\n  \"b\": 2\n}", "/a", "", "{\n  // stays\n  \"b\": 2\n}", ""},
		{"remove the last member keeps a comment between", remove, "{\n  \"a\": 1, // kept\n  \"b\": 2\n}", "/b", "", "{\n  \"a\": 1 // kept\n}", ""},
		{"remove an array element", remove, "{\n  \"extends\": [\n    \"a\",\n    \"b\",\n    \"c\"\n  ]\n}", "/extends/1", "", "{\n  \"extends\": [\n    \"a\",\n    \"c\"\n  ]\n}", ""},
		{"remove the last array element", remove, `{"extends": ["a", "b"]}`, "/extends/1", "", `{"extends": ["a"]}`, ""},
		{"remove an inline member", remove, `{"a": 1, "b": 2, "c": 3}`, "/b", "", `{"a": 1, "c": 3}`, ""},
		{"remove the only member leaves an empty object", remove, "{\n  \"r\": {\n    \"a\": 1\n  }\n}", "/r/a", "", "{\n  \"r\": {}\n}", ""},
		{"remove the only item leaves an empty array", remove, `{"a": [1]}`, "/a/0", "", `{"a": []}`, ""},
		{"remove a whole rule", remove, "{\n  \"packageRules\": [\n    {\n      \"a\": 1\n    },\n    {\n      \"b\": 2\n    }\n  ]\n}", "/packageRules/0", "", "{\n  \"packageRules\": [\n    {\n      \"b\": 2\n    }\n  ]\n}", ""},
		{"remove out of range", remove, `{"a": [1]}`, "/a/3", "", "", "/a/3 is not in the file"},
		{"remove a missing key", remove, `{"a": 1}`, "/b", "", "", "/b is not in the file"},
		// append
		{"append to a multi-line array", appendOp, "{\n  \"extends\": [\n    \"a\"\n  ]\n}", "/extends", `"b"`, "{\n  \"extends\": [\n    \"a\",\n    \"b\"\n  ]\n}", ""},
		{"append to a one-line array", appendOp, `{"extends": ["a"]}`, "/extends", `"b"`, `{"extends": ["a", "b"]}`, ""},
		{"append to an empty array", appendOp, `{"extends": []}`, "/extends", `"b"`, `{"extends": [ "b" ]}`, ""},
		{"append behind a trailing comma", appendOp, "{\n  \"extends\": [\n    \"a\",\n  ]\n}", "/extends", `"b"`, "{\n  \"extends\": [\n    \"a\",\n    \"b\",\n  ]\n}", ""},
		{"append to a non-array", appendOp, `{"a": 1}`, "/a", `2`, "", "/a is not an array"},
		{"append to a missing key", appendOp, `{"a": 1}`, "/b", `2`, "", "/b is not in the file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := []byte(c.src)
			out, e, err := c.op(src, c.pointer, []byte(c.value))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != c.want {
				t.Fatalf("got\n%s\nwant\n%s", out, c.want)
			}
			if string(out[:e.Start]) != c.src[:e.Start] || string(out[e.Start+len(e.New):]) != c.src[e.End:] {
				t.Errorf("bytes outside the edit changed: %+v", e)
			}
			if e.Old != c.src[e.Start:e.End] {
				t.Errorf("Old = %q, want %q", e.Old, c.src[e.Start:e.End])
			}
			var v any
			if err := json.Unmarshal(Strip(out), &v); err != nil {
				t.Errorf("the result does not parse: %v", err)
			}
		})
	}
}

// The unit is read off the file: tabs stay tabs, four spaces stay four.
func TestIndentIsReadOffTheFile(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"{\n  \"a\": 1\n}", "  "},
		{"{\n    \"a\": 1\n}", "    "},
		{"{\n\t\"a\": 1\n}", "\t"},
		{`{"a": 1}`, "  "},
		{"// head\n{\n\t\"a\": 1\n}", "\t"},
	} {
		if got := Indent([]byte(c.src)); got != c.want {
			t.Errorf("Indent(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}
