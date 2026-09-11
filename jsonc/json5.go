// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package jsonc

import (
	"bytes"
	"strings"
)

// FromJSON5 rewrites the JSON5 the estate actually writes into JSON that
// encoding/json accepts: unquoted identifier keys are quoted, single-quoted
// strings become double-quoted, and comments and trailing commas go through
// Strip. That is the whole of what the one renovate.json5 in the estate uses
// (deploy/moselwal-websites-deploy: `$schema:`, `extends:`, comments,
// trailing commas). Hexadecimal numbers, Infinity, NaN, leading-dot
// numerals and line continuations are not JSON5 anyone here wrote and are
// left to fail in the decoder with their own message.
//
// Offsets are NOT preserved - quoting a key adds two bytes - so a decoder
// error on a .json5 file points at the rewritten text. Strip keeps offsets
// for .jsonc, where the promise matters more and the rewrite is a no-op.
func FromJSON5(src []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(src) + 64)
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"':
			// A double-quoted string copies through, escapes honoured.
			j := i + 1
			for j < len(src) {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '"' {
					j++
					break
				}
				j++
			}
			out.Write(src[i:min(j, len(src))])
			i = j

		case c == '\'':
			// A single-quoted string: re-quote, escaping any inner double
			// quote and unescaping an escaped single quote.
			out.WriteByte('"')
			j := i + 1
			for j < len(src) && src[j] != '\'' {
				if src[j] == '\\' && j+1 < len(src) {
					if src[j+1] == '\'' {
						out.WriteByte('\'')
					} else {
						out.Write(src[j : j+2])
					}
					j += 2
					continue
				}
				if src[j] == '"' {
					out.WriteString(`\"`)
				} else {
					out.WriteByte(src[j])
				}
				j++
			}
			out.WriteByte('"')
			i = j + 1

		case c == '/' && i+1 < len(src) && (src[i+1] == '/' || src[i+1] == '*'):
			// Comments are copied as they are; Strip removes them after.
			j := i + 2
			if src[i+1] == '/' {
				for j < len(src) && src[j] != '\n' {
					j++
				}
			} else {
				for j+1 < len(src) && !(src[j] == '*' && src[j+1] == '/') {
					j++
				}
				j = min(j+2, len(src))
			}
			out.Write(src[i:j])
			i = j

		case isIdentStart(c):
			// An identifier is a key when the next significant byte is a
			// colon; true, false and null are values and stay bare.
			j := i + 1
			for j < len(src) && isIdentPart(src[j]) {
				j++
			}
			word := string(src[i:j])
			k := j
			for k < len(src) && (src[k] == ' ' || src[k] == '\t' || src[k] == '\r' || src[k] == '\n') {
				k++
			}
			if k < len(src) && src[k] == ':' {
				out.WriteByte('"')
				out.WriteString(word)
				out.WriteByte('"')
			} else {
				out.WriteString(word)
			}
			i = j

		default:
			out.WriteByte(c)
			i++
		}
	}
	return Strip(out.Bytes())
}

func isIdentStart(c byte) bool {
	return c == '$' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// IsJSON5Name reports whether a file name asks for the JSON5 rewrite.
func IsJSON5Name(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".json5")
}
