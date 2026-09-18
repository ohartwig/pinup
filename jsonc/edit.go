// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package jsonc

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
)

// Edit is one byte-range replacement: the bytes [Start, End) of the source
// held Old and hold New afterwards. It is the shape model.Edit carries;
// jsonc sits beside model at L0 and cannot import it, so the caller converts.
type Edit struct {
	Start, End int
	Old, New   string
}

// Set writes value - JSON text, already encoded - at the RFC 6901 pointer:
// it replaces the value of a member or item that exists, and adds the member
// to its object when the last segment does not. Every parent must exist; a
// fix that needs a container the file lacks sets the container itself.
// Comments, key order and the surrounding bytes are untouched; a value that
// spans lines is indented to the member it joins.
func Set(src []byte, pointer string, value []byte) ([]byte, Edit, error) {
	root, err := scan(src)
	if err != nil {
		return nil, Edit{}, err
	}
	parent, last, idx, err := find(src, root, pointer)
	if err != nil {
		return nil, Edit{}, err
	}
	if idx >= 0 {
		v := parent.elems[idx].val
		return replace(src, v.start, v.end, indented(value, lineIndent(src, v.start)))
	}
	if parent.kind != kObject {
		return nil, Edit{}, fmt.Errorf("%s: %s is not an object", pointer, parentOf(pointer))
	}
	key := strconv.Quote(last)
	return insert(src, parent, key+": ", value, "{", "}")
}

// Remove deletes the member or item at the pointer, with its comma, and the
// whole line when the element stands on one - a comment closing that line
// goes with it, one on the line above stays. Removing the last element of a
// container leaves {} or [].
func Remove(src []byte, pointer string) ([]byte, Edit, error) {
	root, err := scan(src)
	if err != nil {
		return nil, Edit{}, err
	}
	parent, _, idx, err := find(src, root, pointer)
	if err != nil {
		return nil, Edit{}, err
	}
	if idx < 0 {
		return nil, Edit{}, fmt.Errorf("%s is not in the file", pointer)
	}
	if len(parent.elems) == 1 {
		empty := "{}"
		if parent.kind == kArray {
			empty = "[]"
		}
		return replace(src, parent.start, parent.end, []byte(empty))
	}
	e := parent.elems[idx]
	start, end := e.start, e.val.end
	if e.comma >= 0 {
		end = e.comma + 1
	}
	ls, le, own := ownLine(src, start, end)
	if own {
		start, end = ls, le
	}
	if e.comma < 0 && idx > 0 {
		// The last element without a trailing comma: the one before it
		// loses its comma, or the document stops parsing. What stood
		// between that comma and the removed line - a newline, a comment -
		// stays; inline, the blank goes with the comma.
		prev := parent.elems[idx-1]
		if prev.comma < 0 {
			return nil, Edit{}, fmt.Errorf("%s: no comma before the element", pointer)
		}
		var joined []byte
		if own {
			joined = append(joined, src[prev.comma+1:start]...)
		}
		return replace(src, prev.comma, end, joined)
	}
	if e.comma >= 0 && !isLineStart(src, start) {
		// Inline: take one blank after the comma along, so "a": 1, "b": 2
		// does not keep two spaces.
		for end < len(src) && src[end] == ' ' {
			end++
		}
	}
	return replace(src, start, end, nil)
}

// Append adds value - JSON text, already encoded - to the array at the
// pointer, after its last item, in the array's own style: on a line of its
// own when the items are, inline when they are.
func Append(src []byte, pointer string, value []byte) ([]byte, Edit, error) {
	root, err := scan(src)
	if err != nil {
		return nil, Edit{}, err
	}
	parent, _, idx, err := find(src, root, pointer)
	if err != nil {
		return nil, Edit{}, err
	}
	if idx < 0 {
		return nil, Edit{}, fmt.Errorf("%s is not in the file", pointer)
	}
	arr := parent.elems[idx].val
	if arr.kind != kArray {
		return nil, Edit{}, fmt.Errorf("%s is not an array", pointer)
	}
	return insert(src, &arr, "", value, "[", "]")
}

// Indent reports the indentation unit the document uses: the leading
// blanks of the first indented line, two spaces when nothing is indented.
func Indent(src []byte) string {
	for line := range bytes.SplitSeq(src, []byte("\n")) {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) < len(line) && len(bytes.TrimSpace(trimmed)) > 0 {
			return string(line[:len(line)-len(trimmed)])
		}
	}
	return "  "
}

// insert adds one element - prefix (a quoted key and colon, or nothing) and
// value - after the last element of a container, or into an empty one.
func insert(src []byte, c *node, prefix string, value []byte, open, close string) ([]byte, Edit, error) {
	nl := newline(src)
	if len(c.elems) == 0 {
		indent := lineIndent(src, c.start)
		if multiline(src) {
			inner := indent + Indent(src)
			text := open + nl + inner + prefix + string(indented(value, inner)) + nl + indent + close
			return replace(src, c.start, c.end, []byte(text))
		}
		return replace(src, c.start, c.end, []byte(open+" "+prefix+string(value)+" "+close))
	}
	last := c.elems[len(c.elems)-1]
	at := last.val.end
	if last.comma >= 0 {
		at = last.comma + 1
	}
	if bytes.IndexByte(src[c.start:c.end], '\n') < 0 {
		text := ", " + prefix + string(value)
		if last.comma >= 0 {
			text = " " + prefix + string(value) + ","
		}
		return replace(src, at, at, []byte(text))
	}
	indent := lineIndent(src, last.start)
	body := prefix + string(indented(value, indent))
	// A comment closing the last element's line stays on that line: the
	// new element goes after it, and a comma the element lacked goes right
	// after its value.
	le := lineEnd(src, at)
	if le > 0 && src[le-1] == '\r' {
		le--
	}
	if !trailing(src[at:le]) {
		le = at
	}
	between := string(src[at:le])
	if last.comma >= 0 {
		return replace(src, at, le, []byte(between+nl+indent+body+","))
	}
	return replace(src, at, le, []byte(","+between+nl+indent+body))
}

func replace(src []byte, start, end int, with []byte) ([]byte, Edit, error) {
	out := make([]byte, 0, len(src)-(end-start)+len(with))
	out = append(out, src[:start]...)
	out = append(out, with...)
	out = append(out, src[end:]...)
	return out, Edit{Start: start, End: end, Old: string(src[start:end]), New: string(with)}, nil
}

// indented prefixes every continuation line of a value with indent, so a
// value encoded against the left margin lines up with the member it joins.
func indented(value []byte, indent string) []byte {
	if indent == "" || bytes.IndexByte(value, '\n') < 0 {
		return value
	}
	return bytes.ReplaceAll(value, []byte("\n"), []byte("\n"+indent))
}

// ownLine reports the line span to delete when the element at [start, end)
// is alone on its line(s): only blanks before it, only blanks or a comment
// after it. The span runs from the line's first byte through its newline.
func ownLine(src []byte, start, end int) (int, int, bool) {
	ls := lineStart(src, start)
	if len(bytes.Trim(src[ls:start], " \t")) != 0 {
		return 0, 0, false
	}
	le := lineEnd(src, end)
	if !trailing(src[end:le]) {
		return 0, 0, false
	}
	if le < len(src) {
		le++ // the newline itself
	}
	return ls, le, true
}

// trailing reports whether the rest of a line is nothing, or a comment.
func trailing(rest []byte) bool {
	rest = bytes.Trim(rest, " \t\r")
	if len(rest) == 0 || bytes.HasPrefix(rest, []byte("//")) {
		return true
	}
	return bytes.HasPrefix(rest, []byte("/*")) && bytes.HasSuffix(rest, []byte("*/"))
}

func isLineStart(src []byte, pos int) bool {
	return len(bytes.Trim(src[lineStart(src, pos):pos], " \t")) == 0
}

func lineStart(src []byte, pos int) int {
	return bytes.LastIndexByte(src[:pos], '\n') + 1
}

// lineEnd is the index of the newline ending the line at pos, or len(src).
func lineEnd(src []byte, pos int) int {
	i := bytes.IndexByte(src[pos:], '\n')
	if i < 0 {
		return len(src)
	}
	return pos + i
}

// lineIndent is the leading blanks of the line pos is on.
func lineIndent(src []byte, pos int) string {
	ls := lineStart(src, pos)
	le := lineEnd(src, ls)
	line := src[ls:le]
	return string(line[:len(line)-len(bytes.TrimLeft(line, " \t"))])
}

func multiline(src []byte) bool { return bytes.IndexByte(src, '\n') >= 0 }

func newline(src []byte) string {
	if i := bytes.IndexByte(src, '\n'); i > 0 && src[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

func parentOf(pointer string) string {
	if p := pointer[:strings.LastIndexByte(pointer, '/')]; p != "" {
		return p
	}
	return "/"
}

// find walks the pointer to the container that holds its last segment. It
// returns that container, the decoded last segment, and the element's index
// in it, -1 when the container has no such member. A missing intermediate
// is an error naming the first pointer that is not in the file.
func find(src []byte, root *node, pointer string) (*node, string, int, error) {
	if pointer == "" || pointer[0] != '/' {
		return nil, "", 0, fmt.Errorf("%q is not a pointer", pointer)
	}
	segs := strings.Split(pointer[1:], "/")
	for i := range segs {
		segs[i] = strings.ReplaceAll(strings.ReplaceAll(segs[i], "~1", "/"), "~0", "~")
	}
	cur := root
	for i, seg := range segs[:len(segs)-1] {
		idx := locate(cur, seg)
		if idx < 0 {
			return nil, "", 0, fmt.Errorf("%s: /%s is not in the file", pointer, strings.Join(segs[:i+1], "/"))
		}
		cur = &cur.elems[idx].val
	}
	last := segs[len(segs)-1]
	return cur, last, locate(cur, last), nil
}

// locate finds seg in a container: a key in an object, an index in an array.
func locate(c *node, seg string) int {
	switch c.kind {
	case kObject:
		for i, e := range c.elems {
			if e.key == seg {
				return i
			}
		}
	case kArray:
		if i, err := strconv.Atoi(seg); err == nil && i >= 0 && i < len(c.elems) {
			return i
		}
	}
	return -1
}

type kind uint8

const (
	kString kind = iota
	kObject
	kArray
	kOther // a number, true, false or null - spanned, never read
)

// node is one JSON value with its byte span in the source.
type node struct {
	kind       kind
	start, end int // the whole value, quotes and brackets included
	elems      []elem
}

// elem is one member of an object or item of an array, in file order.
type elem struct {
	key   string // decoded; unused for an array item
	start int    // the key's first byte for a member, the value's for an item
	val   node
	comma int // the comma after the element, -1 when there is none
}

// scan parses the document tolerantly - comments, trailing commas, bare
// identifier keys and single-quoted strings - recording where every value
// begins and ends in the original bytes. It reads spans, not values.
func scan(src []byte) (*node, error) {
	s := &scanner{src: src}
	root, err := s.value()
	if err != nil {
		return nil, err
	}
	s.space()
	if s.pos < len(src) {
		return nil, fmt.Errorf("byte %d: text after the document", s.pos)
	}
	return &root, nil
}

type scanner struct {
	src []byte
	pos int
}

// space skips blanks and comments.
func (s *scanner) space() {
	for s.pos < len(s.src) {
		switch c := s.src[s.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.pos++
		case c == '/' && s.pos+1 < len(s.src) && s.src[s.pos+1] == '/':
			s.pos = lineEnd(s.src, s.pos)
		case c == '/' && s.pos+1 < len(s.src) && s.src[s.pos+1] == '*':
			i := bytes.Index(s.src[s.pos+2:], []byte("*/"))
			if i < 0 {
				s.pos = len(s.src)
				return
			}
			s.pos += 2 + i + 2
		default:
			return
		}
	}
}

func (s *scanner) value() (node, error) {
	s.space()
	if s.pos >= len(s.src) {
		return node{}, fmt.Errorf("byte %d: unexpected end of input", s.pos)
	}
	switch s.src[s.pos] {
	case '"', '\'':
		start, _, err := s.str()
		return node{kind: kString, start: start, end: s.pos}, err
	case '{':
		return s.container(kObject, '}')
	case '[':
		return s.container(kArray, ']')
	}
	start := s.pos
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case ',', ']', '}', ' ', '\t', '\n', '\r', '/':
			if s.pos == start {
				return node{}, fmt.Errorf("byte %d: unexpected %q", s.pos, s.src[s.pos])
			}
			return node{kind: kOther, start: start, end: s.pos}, nil
		}
		s.pos++
	}
	return node{kind: kOther, start: start, end: s.pos}, nil
}

// str scans a double- or single-quoted string and returns its span and its
// decoded text. Escapes in a single-quoted string are taken literally but
// for \', which is the one the estate's json5 could contain.
func (s *scanner) str() (int, string, error) {
	start := s.pos
	quote := s.src[s.pos]
	s.pos++
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case '\\':
			s.pos += 2
		case quote:
			s.pos++
			raw := s.src[start:s.pos]
			if quote == '\'' {
				return start, strings.ReplaceAll(string(raw[1:len(raw)-1]), `\'`, `'`), nil
			}
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				return start, "", fmt.Errorf("byte %d: %w", start, err)
			}
			return start, text, nil
		default:
			s.pos++
		}
	}
	return start, "", fmt.Errorf("byte %d: unterminated string", start)
}

// key scans an object key: a string, or a bare identifier as JSON5 writes it.
func (s *scanner) key() (int, string, error) {
	if c := s.src[s.pos]; c == '"' || c == '\'' {
		return s.str()
	}
	start := s.pos
	for s.pos < len(s.src) && isIdentPart(s.src[s.pos]) {
		s.pos++
	}
	if s.pos == start {
		return start, "", fmt.Errorf("byte %d: expected a key", start)
	}
	return start, string(s.src[start:s.pos]), nil
}

func (s *scanner) container(k kind, close byte) (node, error) {
	n := node{kind: k, start: s.pos}
	s.pos++
	for {
		s.space()
		if s.pos >= len(s.src) {
			return node{}, fmt.Errorf("byte %d: unterminated %q", n.start, s.src[n.start])
		}
		if s.src[s.pos] == close {
			s.pos++
			n.end = s.pos
			return n, nil
		}
		e := elem{comma: -1}
		if k == kObject {
			start, key, err := s.key()
			if err != nil {
				return node{}, err
			}
			e.start, e.key = start, key
			s.space()
			if s.pos >= len(s.src) || s.src[s.pos] != ':' {
				return node{}, fmt.Errorf("byte %d: expected ':'", s.pos)
			}
			s.pos++
		}
		val, err := s.value()
		if err != nil {
			return node{}, err
		}
		if k == kArray {
			e.start = val.start
		}
		e.val = val
		s.space()
		if s.pos < len(s.src) && s.src[s.pos] == ',' {
			e.comma = s.pos
			s.pos++
		} else if s.pos < len(s.src) && s.src[s.pos] != close {
			return node{}, fmt.Errorf("byte %d: expected ',' or %q", s.pos, close)
		}
		n.elems = append(n.elems, e)
	}
}
