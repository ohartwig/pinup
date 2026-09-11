// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package terraform

// This file is the small HCL-subset scanner terraform.go's semantic reading
// sits on top of. It exists instead of an HCL library because none exists in
// this estate and none may be added (see the package comment in
// terraform.go). It reports byte spans, not values, into the file exactly as
// read - the same reason manager/composerman's jsonParser does not use
// encoding/json - so a version string's model.Locus brackets exactly the
// bytes on disk and nothing is ever re-serialized.
//
// It parses two shapes only: a BLOCK ("ident [\"label\"]* { body }") and an
// ATTRIBUTE ("ident = value"), where value is a quoted string, a nested
// object ("{ body }", itself a sequence of attributes), or anything else -
// a list, a function call, a reference, a heredoc - which is scanned only far
// enough to find where it ends and is never interpreted. That covers every
// shape this manager's semantic layer looks for (terraform, required_providers,
// provider, module) and lets it walk straight past everything Terraform's
// full expression grammar allows that this manager has no use for: resource
// bodies, locals, variables, dynamic blocks, ternaries and the rest.
//
// A file this scanner cannot make sense of at some point never fails outright:
// on anything unrecognized it resynchronizes - consumes one byte and keeps
// going - rather than stopping, the same "warn, don't stop" contract every
// text manager in this estate follows for a single bad file.

// node is one item of a parsed body: either a block or an attribute. Which it
// is, is read off isBlock, attrIsString and attrIsObject; the zero value of
// node - none of those set - describes nothing recognized and is never
// appended to a body.
type node struct {
	// Block fields, valid when isBlock is true.
	isBlock   bool
	blockType string
	labels    []strSpan
	body      []node // the block's own body, parsed the same way

	// Attribute fields, valid when isBlock is false.
	attrName     string
	attrIsString bool
	attrIsObject bool // body holds the nested object's own attributes
	str          strSpan

	// line is the 1-based source line the block's or attribute's name token
	// starts on, for a human-readable model.Locus.Line when there is no
	// string value to read one from.
	line int
}

// strSpan is a quoted string's content span, excluding the surrounding
// quotes, plus the line its opening quote sits on.
type strSpan struct {
	start, end int
	line       int
}

// raw returns the string's content exactly as written, escapes and all - the
// same choice manager/composerman's jsonNode.raw makes, since none of the
// values this package reads (versions, sources, provider labels) ever carry
// an escape sequence in practice, and unescaping would break the invariant
// that a model.Locus brackets exactly model.Dependency.CurrentValue.
func (s strSpan) raw(src []byte) string { return string(src[s.start:s.end]) }

// parseBody scans src as a top-level sequence of blocks and attributes.
func parseBody(src []byte) []node {
	p := &parser{src: src, line: 1}
	return p.parseBody(false)
}

// parser is a hand-rolled recursive-descent scanner over the HCL subset
// described above. It carries no lookahead buffer: every decision is made by
// peeking at most a couple of bytes ahead of pos.
type parser struct {
	src  []byte
	pos  int
	line int // 1-based line of src[pos]
}

func (p *parser) peek() byte {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) peekAt(offset int) byte {
	if p.pos+offset >= len(p.src) {
		return 0
	}
	return p.src[p.pos+offset]
}

// advance consumes exactly one byte, keeping the line counter in step.
func (p *parser) advance() {
	if p.pos >= len(p.src) {
		return
	}
	if p.src[p.pos] == '\n' {
		p.line++
	}
	p.pos++
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c == '-' || (c >= '0' && c <= '9')
}

// readIdent consumes a leading identifier at pos, or reports ok=false and
// consumes nothing when there is none to read.
func (p *parser) readIdent() (string, bool) {
	if !isIdentStart(p.peek()) {
		return "", false
	}
	start := p.pos
	p.advance()
	for isIdentPart(p.peek()) {
		p.advance()
	}
	return string(p.src[start:p.pos]), true
}

// readString consumes a double-quoted string starting at the opening quote
// and returns its content span, excluding both quotes. An unterminated
// string - a malformed file - reads to EOF rather than looping forever.
func (p *parser) readString() strSpan {
	line := p.line
	p.advance() // the opening quote
	contentStart := p.pos
	for {
		switch p.peek() {
		case 0:
			return strSpan{start: contentStart, end: p.pos, line: line}
		case '\\':
			p.advance()
			if p.peek() != 0 {
				p.advance()
			}
		case '"':
			contentEnd := p.pos
			p.advance() // the closing quote
			return strSpan{start: contentStart, end: contentEnd, line: line}
		default:
			p.advance()
		}
	}
}

// skipLineComment consumes up to, but not including, the next newline or EOF -
// covers both "#" and "//" comments, whose only difference is how they start.
func (p *parser) skipLineComment() {
	for p.peek() != '\n' && p.peek() != 0 {
		p.advance()
	}
}

func (p *parser) skipBlockComment() {
	p.advance() // '/'
	p.advance() // '*'
	for {
		switch {
		case p.peek() == 0:
			return
		case p.peek() == '*' && p.peekAt(1) == '/':
			p.advance()
			p.advance()
			return
		default:
			p.advance()
		}
	}
}

// skipHeredoc consumes a "<<EOT ... EOT" or "<<-EOT ... EOT" body, starting
// at the leading "<<". Its content is opaque text, never HCL, so nothing in
// it is at risk of being misread as a brace or a comment.
func (p *parser) skipHeredoc() {
	p.advance() // '<'
	p.advance() // '<'
	if p.peek() == '-' {
		p.advance()
	}
	start := p.pos
	for isIdentPart(p.peek()) {
		p.advance()
	}
	delim := string(p.src[start:p.pos])
	if delim == "" {
		return // not actually a heredoc marker; nothing more to do
	}
	p.skipLineComment() // the rest of the "<<EOT" line
	if p.peek() == '\n' {
		p.advance()
	}
	for {
		lineStart := p.pos
		for p.peek() != '\n' && p.peek() != 0 {
			p.advance()
		}
		line := trimSpace(p.src[lineStart:p.pos])
		atEOF := p.peek() == 0
		if p.peek() == '\n' {
			p.advance()
		}
		if line == delim || atEOF {
			return
		}
	}
}

// trimSpace strips leading and trailing ASCII blanks - all a heredoc closing
// delimiter's indentation ever is.
func trimSpace(b []byte) string {
	start, end := 0, len(b)
	for start < end && isBlank(b[start]) {
		start++
	}
	for end > start && isBlank(b[end-1]) {
		end--
	}
	return string(b[start:end])
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

// skipSpaceAndComments advances past whitespace and comments between two
// tokens at the body level, where a newline carries no meaning of its own.
func (p *parser) skipSpaceAndComments() {
	for {
		switch p.peek() {
		case ' ', '\t', '\r', '\n':
			p.advance()
		case '#':
			p.skipLineComment()
		case '/':
			switch p.peekAt(1) {
			case '/':
				p.skipLineComment()
			case '*':
				p.skipBlockComment()
			default:
				return
			}
		default:
			return
		}
	}
}

// skipValue consumes one attribute's value when it is neither a plain string
// nor a nested object - a list, a function call, a reference, a ternary, a
// heredoc, a number, a boolean. It is never interpreted, only skipped: HCL
// terminates an attribute at the first newline that is not nested inside a
// bracket, brace or paren, and that is the only fact this needs.
func (p *parser) skipValue() {
	depth := 0
	for {
		switch c := p.peek(); {
		case c == 0:
			return
		case c == '\n':
			p.advance()
			if depth == 0 {
				return
			}
		case c == ' ' || c == '\t' || c == '\r':
			p.advance()
		case c == '#':
			p.skipLineComment()
		case c == '/' && p.peekAt(1) == '/':
			p.skipLineComment()
		case c == '/' && p.peekAt(1) == '*':
			p.skipBlockComment()
		case c == '"':
			p.readString()
		case c == '<' && p.peekAt(1) == '<':
			p.skipHeredoc()
		case c == '{' || c == '[' || c == '(':
			depth++
			p.advance()
		case c == '}' || c == ']' || c == ')':
			if depth == 0 {
				return // the enclosing block's own closer; leave it unconsumed
			}
			depth--
			p.advance()
		default:
			p.advance()
		}
	}
}

// parseBody scans a body - the whole file when nested is false, or the
// interior of a "{ ... }" when it is true - into its blocks and attributes.
// Anything it does not recognize is skipped defensively rather than treated
// as an error: one construct this scanner has no model for is not a reason to
// stop reading the rest of the body.
func (p *parser) parseBody(nested bool) []node {
	var items []node
	for {
		p.skipSpaceAndComments()
		switch {
		case p.peek() == 0:
			return items
		case nested && p.peek() == '}':
			p.advance()
			return items
		}

		identLine := p.line
		ident, ok := p.readIdent()
		if !ok {
			p.advance() // resynchronize: not a token this scanner starts a body item with
			continue
		}
		p.skipSpaceAndComments()

		switch p.peek() {
		case '"':
			var labels []strSpan
			for p.peek() == '"' {
				labels = append(labels, p.readString())
				p.skipSpaceAndComments()
			}
			if p.peek() == '{' {
				p.advance()
				items = append(items, node{
					isBlock: true, blockType: ident, labels: labels,
					body: p.parseBody(true), line: identLine,
				})
			}
			// Labels with no body follow is not a block this scanner
			// understands; the labels are simply dropped.

		case '{':
			p.advance()
			items = append(items, node{
				isBlock: true, blockType: ident,
				body: p.parseBody(true), line: identLine,
			})

		case '=':
			p.advance()
			p.skipSpaceAndComments()
			switch p.peek() {
			case '"':
				items = append(items, node{
					attrName: ident, attrIsString: true,
					str: p.readString(), line: identLine,
				})
			case '{':
				p.advance()
				items = append(items, node{
					attrName: ident, attrIsObject: true,
					body: p.parseBody(true), line: identLine,
				})
			default:
				p.skipValue() // a value this manager's semantics never read
			}

		default:
			// A bare identifier with nothing recognizable after it - not a
			// shape this scanner models. Move on.
		}
	}
}
