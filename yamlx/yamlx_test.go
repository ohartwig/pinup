// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package yamlx

import (
	"reflect"
	"testing"
)

func TestUnmarshalNormalisesToTheJSONShape(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want map[string]any
	}{
		{
			name: "plain mapping",
			in:   "a: 1\nb: two\n",
			want: map[string]any{"a": 1.0, "b": "two"},
		},
		{
			// A GitLab pipeline really does have a key like this, and yaml.v3
			// hands back map[any]any the moment a key is not a string.
			name: "a boolean-looking key",
			in:   "on:\n  push: true\n",
			want: map[string]any{"on": map[string]any{"push": true}},
		},
		{
			name: "a numeric key",
			in:   "platform:\n  8.5: php\n",
			want: map[string]any{"platform": map[string]any{"8.5": "php"}},
		},
		{
			name: "nested sequences",
			in:   "labels:\n  - a\n  - b\n",
			want: map[string]any{"labels": []any{"a", "b"}},
		},
		{
			name: "an empty document is an empty mapping, not nil",
			in:   "",
			want: map[string]any{},
		},
	} {
		var got map[string]any
		if err := Unmarshal([]byte(c.in), &got); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, got, c.want)
		}
	}
}

// Numbers must land as float64, or a YAML layer merging over a JSON one would
// compare 1 (int) against 1 (float64) and find them different.
func TestIntegersBecomeFloats(t *testing.T) {
	var got map[string]any
	if err := Unmarshal([]byte("prHourlyLimit: 20\n"), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["prHourlyLimit"].(float64); !ok {
		t.Errorf("prHourlyLimit is %T, want float64 - it would not compare equal to the JSON form", got["prHourlyLimit"])
	}
}

func TestTopLevelMustBeAMapping(t *testing.T) {
	var got map[string]any
	if err := Unmarshal([]byte("- a\n- b\n"), &got); err == nil {
		t.Error("a top-level sequence was accepted; configuration is a mapping")
	}
}

func TestOffset(t *testing.T) {
	src := []byte("image: alpine\ntag: \"3.21\"\nother: x\n")
	for _, c := range []struct {
		line, col, want int
	}{
		{1, 1, 0},
		{1, 8, 7},  // "alpine"
		{2, 1, 14}, // start of line 2
		{3, 1, 26}, // start of line 3
		{99, 1, -1},
		{0, 1, -1},
		{1, 0, -1},
	} {
		if got := Offset(src, c.line, c.col); got != c.want {
			t.Errorf("Offset(line=%d,col=%d) = %d, want %d", c.line, c.col, got, c.want)
		}
	}
	// The offset must actually point at the text it claims to.
	off := Offset(src, 2, 6)
	if string(src[off:off+6]) != `"3.21"` {
		t.Errorf("offset %d points at %q, not at the tag", off, src[off:off+6])
	}
}

func TestNodePreservesPositions(t *testing.T) {
	src := []byte("image: alpine:3.21\n")
	n, err := ParseTree(src)
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind == 0 || len(n.Content) == 0 {
		t.Fatal("no document node")
	}
	mapping := n.Content[0]
	if len(mapping.Content) < 2 {
		t.Fatal("no key/value pair")
	}
	val := mapping.Content[1]
	off := Offset(src, val.Line, val.Column)
	if got := string(src[off : off+len(val.Value)]); got != val.Value {
		t.Errorf("node position points at %q, but the node value is %q", got, val.Value)
	}
}
