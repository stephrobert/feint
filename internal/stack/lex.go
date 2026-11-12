// Package stack reads what a Terraform or OpenTofu configuration declares.
//
// It reads block headers, not expressions, and that bound is what makes it
// honest. The labels of a block are literal by the language's own rules —
// `resource "${var.t}" "x"` is refused by Terraform with "Template sequences
// are not allowed in this string" — so the set of declared resource and data
// TYPES is exact, with no evaluation at all. Everything that needs evaluation
// (which instances will exist, where an endpoint points, what a module whose
// source is an expression contains) is reported as unknown rather than guessed.
//
// WHY THIS IS NOT A GREP, AND NOT hashicorp/hcl EITHER
//
// The mechanism this replaces concatenated the files, removed comments with a
// three-state scanner, and searched the result for substrings
// (internal/cli/stack.go). That scanner has no heredoc state, which is a
// measured defect and not a theoretical one: a heredoc body holding one
// unbalanced quote — a shell script in `user_data` is enough — flips the
// scanner's string state, and after that a commented-out resource survives the
// strip and the warning fires on dead text. The opposite of what the guard
// promises, on an input its test does not cover.
//
// `hashicorp/hcl/v2` would give the syntax and would cost the zero-dependency
// rule (eleven direct requirements, and a go.sum, whose absence a pre-commit
// hook treats as the check itself). It would also not give the part that
// matters most here: which files the engine loads, which is Terraform's rule
// and OpenTofu's, not HCL's. The dependency buys the lexer and leaves the
// semantics to write anyway.
//
// # WHAT IS LEXED, AND THE RULES THAT DECIDE IT
//
// From hclsyntax/spec.md of hcl v2.24.0, the three that bite:
//
//   - "Comments and whitespace cannot begin within other comments, or within
//     template literals except inside an interpolation sequence or template
//     directive". So `#` inside a quoted string is data, and `#` inside a
//     heredoc body is data too.
//   - A heredoc opens with `<<` or `<<-` followed by an identifier and "ends
//     when the given identifier subsequently appears again on a line of its
//     own". Its body is a template, not code.
//   - Block labels "can either be quoted literal strings or naked
//     identifiers", and Terraform 1.16.1 accepts `resource null_resource x {}`
//     (measured), so both forms are lexed.
//
// A file whose string, heredoc or brace does not close is reported unreadable
// and counts as unknown. It is never reported as parsed: a tokenizer that
// guesses past its own confusion is the instrument this package exists to
// replace.
package stack

import (
	"fmt"
	"strings"
)

// kind is what a token is, at the coarseness this reader needs. Expressions are
// not parsed, so every operator, number and bracket that is not structural
// arrives as kindPunct and is skipped by the reader.
type kind int

const (
	kindIdent kind = iota
	// kindString is a quoted template. literal is true when it held no
	// interpolation, which is what lets a label be read as an exact value.
	kindString
	kindPunct
	// kindHeredoc stands for a whole heredoc: its body is consumed and never
	// reaches the reader as code. text is the opening identifier.
	kindHeredoc
	kindNewline
)

type token struct {
	kind    kind
	text    string
	line    int
	literal bool
}

// scanned is one file's tokens, plus the text with comments removed.
//
// Stripped keeps heredoc bodies verbatim, deliberately. A body can carry a real
// cloud hostname in a startup script, and internal/cli's hazard scan looks for
// exactly that; deleting the body to make the lexer's life easier would silence
// a true finding to fix a false one. What the heredoc state buys is that the
// body no longer corrupts the scanner's idea of where strings are.
type scanned struct {
	tokens   []token
	stripped string
}

// unterminated is what a file that does not close reports. It carries the line
// the construct opened on, because "this file is unreadable" without a line is
// a verdict nobody can act on.
type unterminated struct {
	what string
	line int
}

func (u unterminated) Error() string {
	return fmt.Sprintf("unterminated %s opened on line %d", u.what, u.line)
}

// isIdent reports whether c can appear in an HCL identifier. The dash is in
// there: identifiers "may contain letters, digits, underscores, and hyphens"
// (hclsyntax spec), which is why a provider local name like `null-resource`
// lexes as one token rather than three.
func isIdent(c byte) bool {
	return c == '_' || c == '-' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// scan tokenizes one file.
//
// The loop is a hand-written scanner rather than a regexp pass for the reason
// the package comment gives: the states are what the correctness lives in, and
// a regexp cannot hold "inside a heredoc" or "inside an interpolation".
func scan(src string) (scanned, error) {
	var out scanned
	var b strings.Builder
	b.Grow(len(src))

	line := 1
	for i := 0; i < len(src); {
		c := src[i]

		switch {
		case c == '\n':
			line++
			out.tokens = append(out.tokens, token{kind: kindNewline, line: line - 1})
			b.WriteByte(c)
			i++

		case c == ' ' || c == '\t' || c == '\r':
			b.WriteByte(c)
			i++

		// Comments. Removed from the stripped text, and never emitted as
		// tokens: a reader that had to skip them would be a reader that can
		// forget to.
		case c == '#' || (c == '/' && i+1 < len(src) && src[i+1] == '/'):
			for i < len(src) && src[i] != '\n' {
				i++
			}

		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			start := line
			i += 2
			closed := false
			for i < len(src) {
				if src[i] == '\n' {
					line++
					b.WriteByte('\n')
				}
				if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return out, unterminated{what: "block comment", line: start}
			}

		case c == '"':
			start := line
			tok, consumed, raw, err := scanQuoted(src[i:], line)
			if err != nil {
				return out, unterminated{what: "quoted string", line: start}
			}
			tok.line = start
			out.tokens = append(out.tokens, tok)
			b.WriteString(raw)
			line += strings.Count(raw, "\n")
			i += consumed

		case c == '<' && i+1 < len(src) && src[i+1] == '<':
			start := line
			tok, consumed, raw, err := scanHeredoc(src[i:])
			if err != nil {
				return out, unterminated{what: "heredoc", line: start}
			}
			tok.line = start
			out.tokens = append(out.tokens, tok)
			b.WriteString(raw)
			line += strings.Count(raw, "\n")
			i += consumed

		case isIdent(c):
			j := i
			for j < len(src) && isIdent(src[j]) {
				j++
			}
			out.tokens = append(out.tokens, token{kind: kindIdent, text: src[i:j], line: line})
			b.WriteString(src[i:j])
			i = j

		default:
			out.tokens = append(out.tokens, token{kind: kindPunct, text: string(c), line: line})
			b.WriteByte(c)
			i++
		}
	}

	out.stripped = b.String()
	return out, nil
}

// scanQuoted reads one quoted template.
//
// It returns the token, how many bytes it consumed, and the raw text so the
// stripped output keeps the file byte for byte where it is not a comment.
//
// The interpolation rule is the one that matters: inside `${ … }` the quote
// character opens a nested string rather than closing this one, so
// `"${file("x")}"` is one template and not two. Depth is tracked rather than
// matched, because interpolations nest.
func scanQuoted(src string, line int) (token, int, string, error) {
	if len(src) == 0 || src[0] != '"' {
		return token{}, 0, "", fmt.Errorf("not a quoted string")
	}
	var text strings.Builder
	interpolated := false
	depth := 0
	i := 1
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			// An escape, whatever it escapes. Both bytes belong to the
			// template, and the second one cannot close it.
			text.WriteByte(c)
			text.WriteByte(src[i+1])
			i += 2
			continue

		case depth == 0 && c == '"':
			return token{kind: kindString, text: text.String(), literal: !interpolated},
				i + 1, src[:i+1], nil

		case c == '$' && i+1 < len(src) && src[i+1] == '{',
			c == '%' && i+1 < len(src) && src[i+1] == '{':
			interpolated = true
			depth++
			text.WriteString(src[i : i+2])
			i += 2
			continue

		case depth > 0 && c == '{':
			depth++

		case depth > 0 && c == '}':
			depth--

		case depth > 0 && c == '"':
			// A nested string inside an interpolation. Consumed whole, so its
			// own closing quote cannot be mistaken for this template's.
			_, consumed, raw, err := scanQuoted(src[i:], line)
			if err != nil {
				return token{}, 0, "", err
			}
			text.WriteString(raw)
			i += consumed
			continue
		}

		text.WriteByte(c)
		i++
	}
	return token{}, 0, "", fmt.Errorf("unterminated quoted string")
}

// scanHeredoc reads `<<IDENT` or `<<-IDENT` and its whole body.
//
// The end is "the given identifier … on a line of its own" (hclsyntax spec), so
// the terminator is matched on the line's trimmed content and not with a
// substring search: a body line mentioning the identifier in prose does not
// close the document.
//
// The body is returned in raw, so the stripped text keeps it, and emitted as
// one token, so the reader never sees its contents as code. That is the whole
// fix: a quote or a `#` in a startup script stops moving the lexer's state.
func scanHeredoc(src string) (token, int, string, error) {
	i := 2 // past "<<"
	if i < len(src) && src[i] == '-' {
		i++
	}
	// The spec allows whitespace between the marker and the identifier in
	// neither direction, but Terraform's own parser is forgiving here and so is
	// this: a space costs nothing to accept and refusing it would report a
	// readable file as unreadable.
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	start := i
	for i < len(src) && isIdent(src[i]) {
		i++
	}
	ident := src[start:i]
	if ident == "" {
		return token{}, 0, "", fmt.Errorf("heredoc with no identifier")
	}
	// Past the rest of the opening line.
	for i < len(src) && src[i] != '\n' {
		i++
	}
	if i < len(src) {
		i++ // the newline itself
	}

	for i <= len(src) {
		end := strings.IndexByte(src[i:], '\n')
		var lineText string
		if end < 0 {
			lineText = src[i:]
		} else {
			lineText = src[i : i+end]
		}
		if strings.TrimSpace(lineText) == ident {
			stop := i + len(lineText)
			return token{kind: kindHeredoc, text: ident}, stop, src[:stop], nil
		}
		if end < 0 {
			break
		}
		i += end + 1
	}
	return token{}, 0, "", fmt.Errorf("unterminated heredoc %q", ident)
}
