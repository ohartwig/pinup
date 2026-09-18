// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package gomod

// This file is the small line scanner gomod.go's semantic reading sits on
// top of. go.mod's grammar is line-oriented - one directive or one
// require-block entry per line, `//` starting a comment that runs to the end
// of the line - so nothing more than a byte-accurate line splitter and a
// whitespace tokenizer is needed. Both report absolute byte offsets into the
// original file, never a normalised copy, so a model.Locus brackets exactly
// the bytes on disk and CRLF line endings shift nothing: the "\r", when
// present, is excluded from a line's text but its byte is still counted in
// every offset that follows it.

// line is one physical line of the file, with its content's absolute byte
// offset and 1-based number. text excludes the line terminator - "\n", and
// the "\r" immediately before it when the file uses CRLF endings.
type line struct {
	text   string
	start  int
	number int
}

// splitLines splits src into its physical lines, preserving absolute byte
// offsets across CRLF and LF endings alike.
func splitLines(src []byte) []line {
	var lines []line
	pos, n := 0, len(src)
	for lineNo := 1; pos <= n; lineNo++ {
		start := pos
		end := start
		for end < n && src[end] != '\n' {
			end++
		}
		textEnd := end
		if textEnd > start && src[textEnd-1] == '\r' {
			textEnd--
		}
		lines = append(lines, line{text: string(src[start:textEnd]), start: start, number: lineNo})
		if end == n {
			break
		}
		pos = end + 1
	}
	return lines
}

// splitComment splits a line's text at its first "//", the only comment form
// go.mod uses. The comment's own leading and trailing blanks are trimmed;
// the code part is returned exactly as written, since fieldsWithOffsets does
// its own whitespace skipping and every offset it reports must stay relative
// to the untouched line text.
func splitComment(text string) (code, comment string) {
	if i := indexSlashSlash(text); i >= 0 {
		return text[:i], trimBlanks(text[i+2:])
	}
	return text, ""
}

// indexSlashSlash finds the first "//" in s, or -1. A manual scan rather than
// strings.Index only to keep this file free of any assumption about what
// follows - go.mod module paths never contain "//", so the first occurrence
// is always a comment.
func indexSlashSlash(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '/' && s[i+1] == '/' {
			return i
		}
	}
	return -1
}

func trimBlanks(s string) string {
	start, end := 0, len(s)
	for start < end && isBlank(s[start]) {
		start++
	}
	for end > start && isBlank(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

// tok is one whitespace-delimited token, with its byte span relative to the
// text it was read from - a line's code part, so ln.start+tok.start is the
// token's absolute offset in the file.
type tok struct {
	text       string
	start, end int
}

// fieldsWithOffsets splits s on ASCII blanks, like strings.Fields, but keeps
// each field's byte span instead of discarding it - the only reason this
// exists rather than strings.Fields plus strings.Index, which could not tell
// two identical tokens on the same line apart.
func fieldsWithOffsets(s string) []tok {
	var toks []tok
	i, n := 0, len(s)
	for i < n {
		for i < n && isBlank(s[i]) {
			i++
		}
		if i >= n {
			break
		}
		j := i
		for j < n && !isBlank(s[j]) {
			j++
		}
		toks = append(toks, tok{text: s[i:j], start: i, end: j})
		i = j
	}
	return toks
}
