// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package jsonc strips comments and trailing commas from JSON with comments,
// leaving a document encoding/json accepts.
//
// Offsets are preserved: every byte removed is replaced by a space, so a
// decoder error at offset N in the stripped text points at offset N in the
// file the user actually wrote. Without that, every parse error would be
// reported at the wrong place in any file containing a comment - which is
// every file this package exists for.
//
// JSON5 is deliberately not supported. There is not one .json5 file in the
// estate, and unquoted keys and single-quoted strings would be a second
// grammar to keep true.
package jsonc

import "bytes"

// Strip blanks out comments and trailing commas in place. The result has the
// same length as the input.
func Strip(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	// Positions of commas that might turn out to be trailing. A comma is
	// trailing if the next significant byte is ] or }.
	var pendingComma = -1

	i := 0
	for i < len(out) {
		c := out[i]
		switch {
		case c == '"':
			// A string: copy through, honouring escapes. Comment markers and
			// commas inside a string are data.
			i++
			for i < len(out) {
				if out[i] == '\\' {
					i += 2
					continue
				}
				if out[i] == '"' {
					i++
					break
				}
				i++
			}
			pendingComma = -1

		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}

		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			// Newlines inside a block comment are kept, so line numbers in a
			// decoder error still match the file.
			for i < len(out) {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					out[i], out[i+1] = ' ', ' '
					i += 2
					break
				}
				if out[i] != '\n' {
					out[i] = ' '
				}
				i++
			}

		case c == ',':
			pendingComma = i
			i++

		case c == ']' || c == '}':
			if pendingComma >= 0 {
				out[pendingComma] = ' '
			}
			pendingComma = -1
			i++

		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++

		default:
			pendingComma = -1
			i++
		}
	}
	return out
}

// HasComments reports whether a document contains anything Strip would remove.
// Useful for telling a user their file is JSONC when they thought it was JSON.
func HasComments(src []byte) bool {
	return !bytes.Equal(src, Strip(src))
}
