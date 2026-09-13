// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package runner

import (
	"strings"
	"unicode"
)

// Sanitize makes text written by somebody else safe to place in a merge
// request description that pinup writes as the bot.
//
// Two things in a description are not just text. A line starting with "/"
// is a quick action GitLab executes for whoever wrote the description -
// verified on the estate's instance 2026-09-13: an issue created through
// the API with a "/label" line had the line consumed. A release body from
// upstream saying "/merge" would merge the request as the bot. And "#123",
// "!45" and "@name" are references: GitLab writes "mentioned in" onto the
// target and mails the person, so a release body full of upstream PR
// numbers spams the estate's issues.
//
// The first "/" of such a line becomes "&#47;", which renders the same and
// is not a command. A reference gets a zero-width space after its sigil,
// which renders the same and is not a link. Fenced code blocks and URLs
// are left alone: a "#fragment" in a link must stay a link.
func Sanitize(text string) string {
	lines := strings.Split(text, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		line = breakReferences(line)
		if strings.HasPrefix(trimmed, "/") {
			indent := line[:len(line)-len(trimmed)]
			line = indent + "&#47;" + line[len(indent)+1:]
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// breakReferences inserts a zero-width space after every "#", "!" or "@"
// that would start a reference, outside URLs and inline code.
func breakReferences(line string) string {
	var b strings.Builder
	b.Grow(len(line) + 16)
	runes := []rune(line)
	inCode := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '`' {
			inCode = !inCode
		}
		// A URL runs to the next whitespace; copy it whole.
		if !inCode && (r == 'h' || r == 'H') && hasPrefixFold(runes[i:], "http://", "https://") {
			for i < len(runes) && !unicode.IsSpace(runes[i]) && runes[i] != ')' && runes[i] != '>' {
				b.WriteRune(runes[i])
				i++
			}
			i--
			continue
		}
		// A numeric entity ("&#47;", "&#8203;") is copied whole: its
		// "#" is not a reference.
		if r == '&' && i+2 < len(runes) && runes[i+1] == '#' {
			for i < len(runes) && runes[i] != ';' {
				b.WriteRune(runes[i])
				i++
			}
			if i < len(runes) {
				b.WriteRune(';')
			}
			continue
		}
		b.WriteRune(r)
		if inCode {
			continue
		}
		if (r == '#' || r == '!' || r == '@') && i+1 < len(runes) && startsReference(r, runes[i+1]) {
			b.WriteString("&#8203;")
		}
	}
	return b.String()
}

// startsReference tells whether the rune after a sigil makes it a
// reference: a digit after "#" or "!", a name character after "@".
func startsReference(sigil, next rune) bool {
	switch sigil {
	case '@':
		return unicode.IsLetter(next) || unicode.IsDigit(next) || next == '_'
	default:
		return unicode.IsDigit(next)
	}
}

func hasPrefixFold(rs []rune, prefixes ...string) bool {
	s := strings.ToLower(string(rs[:min(len(rs), 8)]))
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
