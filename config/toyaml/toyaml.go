// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package toyaml rewrites a JSON, JSONC or JSON5 configuration as YAML that
// loads to the same document: keys in the order the file wrote them, every
// "description" turned into the comment it wanted to be, every scalar that
// YAML would read as something else quoted.
//
// Why a converter at all: the runner configuration carries its reasoning in
// "description" arrays because JSON has no comments - forty-nine rules with
// paragraphs of prose as string lists. YAML puts the prose above the rule.
// Why hand-rolled: the emitter writes the JSON subset only (mappings,
// sequences, strings, numbers, booleans, null), and yaml.v3 stays confined to
// yamlx, the one place the tree may depend on it.
//
// The two things YAML gets wrong about a configuration are guarded: a value
// that looks like a number or a boolean ("3.10", "1", "no", "on", "yes") is
// quoted, and a string with anything outside the plain-scalar alphabet is
// written as a double-quoted JSON string, which YAML reads the same way.
package toyaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"git.ole-hartwig.eu/pinup/pinup/jsonc"
)

// Options shape the output.
type Options struct {
	// KeepDescriptions leaves "description" keys in the document as well as
	// writing them as comments. Off by default: the comment is the point.
	KeepDescriptions bool
	// Width is the column comments wrap at; 0 means 78.
	Width int
}

// Convert rewrites src, named so the parser can be chosen (.json, .jsonc,
// .json5), as a YAML document.
func Convert(src []byte, name string, o Options) ([]byte, error) {
	doc, err := parseOrdered(src, name)
	if err != nil {
		return nil, err
	}
	if o.Width <= 0 {
		o.Width = 78
	}
	var b bytes.Buffer
	e := &emitter{w: &b, o: o}
	obj, ok := doc.(*object)
	if !ok {
		return nil, fmt.Errorf("toyaml: %s: the document is not an object", name)
	}
	e.writeDescription(obj, 0)
	if len(obj.keys) == 0 || (len(obj.keys) == 1 && obj.keys[0] == "description" && !o.KeepDescriptions) {
		b.WriteString("{}\n")
		return b.Bytes(), nil
	}
	e.mapping(obj, 0)
	return b.Bytes(), nil
}

// object is a JSON object with its keys in file order.
type object struct {
	keys   []string
	values map[string]any
}

// parseOrdered decodes src into nested *object / []any / scalars, keys in
// the order the file wrote them.
func parseOrdered(src []byte, name string) (any, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json5":
		src = jsonc.Strip(jsonc.FromJSON5(src))
	case ".yaml", ".yml":
		return nil, fmt.Errorf("toyaml: %s is YAML already", name)
	default:
		src = jsonc.Strip(src)
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, fmt.Errorf("toyaml: %s: %w", name, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("toyaml: %s: trailing content after the document", name)
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &object{values: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key %v is not a string", kt)
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := obj.values[key]; !dup {
					obj.keys = append(obj.keys, key)
				}
				obj.values[key] = val
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			return obj, nil
		case '[':
			var arr []any
			for dec.More() {
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			if arr == nil {
				arr = []any{}
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return tok, nil
	}
}

type emitter struct {
	w *bytes.Buffer
	o Options
}

func (e *emitter) indent(n int) { e.w.WriteString(strings.Repeat("  ", n)) }

// writeDescription writes an object's "description" as comment lines at
// the given indentation - a string, or a list of strings, each wrapped.
func (e *emitter) writeDescription(obj *object, level int) {
	d, ok := obj.values["description"]
	if !ok {
		return
	}
	var lines []string
	switch x := d.(type) {
	case string:
		lines = []string{x}
	case []any:
		for _, s := range x {
			if str, ok := s.(string); ok {
				lines = append(lines, str)
			}
		}
	}
	width := e.o.Width - 2*level - 2
	if width < 20 {
		width = 20
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			e.indent(level)
			e.w.WriteString("#\n")
			continue
		}
		for _, wrapped := range wrap(line, width) {
			e.indent(level)
			e.w.WriteString("# ")
			e.w.WriteString(wrapped)
			e.w.WriteByte('\n')
		}
	}
}

// wrap breaks a line at spaces so no piece exceeds width, a word longer
// than width standing alone.
func wrap(s string, width int) []string {
	words := strings.Fields(s)
	var out []string
	cur := ""
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			out = append(out, cur)
			cur = w
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func (e *emitter) mapping(obj *object, level int) {
	for _, k := range obj.keys {
		if k == "description" && !e.o.KeepDescriptions {
			continue
		}
		v := obj.values[k]
		e.indent(level)
		e.w.WriteString(scalar(k))
		e.w.WriteByte(':')
		e.value(v, level, false)
	}
}

// value writes v after a "key:" or "- " already on the line. inSeq says
// the value is a sequence item, whose mapping continues on the same line.
func (e *emitter) value(v any, level int, inSeq bool) {
	switch x := v.(type) {
	case *object:
		if len(x.keys) == 0 || (len(x.keys) == 1 && x.keys[0] == "description" && !e.o.KeepDescriptions) {
			e.w.WriteString(" {}\n")
			return
		}
		if inSeq {
			// "- key: value" - the first key shares the dash's line, the
			// description precedes the dash (written by the caller).
			first := true
			for _, k := range x.keys {
				if k == "description" && !e.o.KeepDescriptions {
					continue
				}
				if first {
					e.w.WriteByte(' ')
					first = false
				} else {
					e.indent(level + 1)
				}
				e.w.WriteString(scalar(k))
				e.w.WriteByte(':')
				e.value(x.values[k], level+1, false)
			}
			return
		}
		e.w.WriteByte('\n')
		e.writeDescription(x, level+1)
		e.mapping(x, level+1)
	case []any:
		if len(x) == 0 {
			e.w.WriteString(" []\n")
			return
		}
		e.w.WriteByte('\n')
		for _, item := range x {
			if obj, ok := item.(*object); ok {
				e.writeDescription(obj, level+1)
			}
			e.indent(level + 1)
			e.w.WriteByte('-')
			e.value(item, level+1, true)
		}
	default:
		e.w.WriteByte(' ')
		e.w.WriteString(scalar(v))
		e.w.WriteByte('\n')
	}
}

// plainRE is the alphabet a string may use unquoted: it starts with a
// letter or underscore and continues with letters, digits and the
// punctuation the estate's names carry (paths, scopes, preset names with a
// colon, versions with a prefix, hosts), never ending in a colon. Anything
// else is written as a JSON string.
var plainRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./@+:-]*[A-Za-z0-9_./@+-]$|^[A-Za-z_]$`)

// yamlWords are plain scalars YAML reads as something other than a string.
var yamlWords = map[string]bool{
	"true": true, "false": true, "null": true, "yes": true, "no": true, "on": true, "off": true,
	"y": true, "n": true, "~": true, "True": true, "False": true, "Yes": true, "No": true,
	"On": true, "Off": true, "TRUE": true, "FALSE": true, "NULL": true, "YES": true, "NO": true,
	"ON": true, "OFF": true, "Null": true,
}

// scalar writes one value: strings plain where safe, quoted as JSON
// otherwise; numbers as written; booleans and null by name.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		if plainRE.MatchString(x) && !yamlWords[x] && !looksNumeric(x) {
			return x
		}
		b, _ := json.Marshal(x)
		return string(b)
	case json.Number:
		return x.String()
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// looksNumeric is whether YAML would read the plain scalar as a number:
// digits with optional sign, dot, exponent, or a hex/octal form. A version
// written "3.10" would otherwise become 3.1.
func looksNumeric(s string) bool {
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseInt(s, 0, 64); err == nil {
		return true
	}
	return false
}
