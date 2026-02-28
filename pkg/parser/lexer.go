// Package parser implements a zero-allocation-friendly Dockerfile lexer
// and parser that produces a structured AST of build instructions.
package parser

import (
	"strings"
	"unicode"
)

// TokenType categorises a lexer token.
type TokenType int

const (
	TokenInstruction TokenType = iota // e.g. "FROM", "RUN"
	TokenArgument                     // positional argument
	TokenFlag                         // --flag=value
	TokenComment                      // # comment line
	TokenNewline                      // logical line boundary
	TokenEOF                          // end of input
)

// Token is a single unit produced by the lexer.
type Token struct {
	Type  TokenType
	Value string
	Line  int // 1-based source line
}

// Lexer tokenises Dockerfile source text.
// Designed for single-pass, byte-level scanning with no regex.
type Lexer struct {
	input  string
	pos    int
	line   int
	tokens []Token // pre-allocated, reused across calls
}

// NewLexer creates a lexer for the given Dockerfile source.
// It pre-allocates token storage to avoid repeated slice growth.
func NewLexer(input string) *Lexer {
	// Estimate: ~2 tokens per line on average
	estimatedTokens := strings.Count(input, "\n") * 2
	if estimatedTokens < 32 {
		estimatedTokens = 32
	}
	return &Lexer{
		input:  input,
		pos:    0,
		line:   1,
		tokens: make([]Token, 0, estimatedTokens),
	}
}

// Tokenize scans the entire input and returns all tokens.
// The returned slice is owned by the Lexer and must not be modified.
func (l *Lexer) Tokenize() []Token {
	l.tokens = l.tokens[:0] // reset but keep capacity

	for l.pos < len(l.input) {
		l.skipSpaces()

		if l.pos >= len(l.input) {
			break
		}

		ch := l.input[l.pos]

		switch {
		case ch == '#':
			// Comment — skip to end of line
			start := l.pos
			for l.pos < len(l.input) && l.input[l.pos] != '\n' {
				l.pos++
			}
			l.tokens = append(l.tokens, Token{
				Type:  TokenComment,
				Value: l.input[start:l.pos],
				Line:  l.line,
			})

		case ch == '\n':
			l.tokens = append(l.tokens, Token{
				Type:  TokenNewline,
				Value: "\n",
				Line:  l.line,
			})
			l.pos++
			l.line++

		case ch == '\r':
			// Handle \r\n (Windows line endings)
			l.pos++
			if l.pos < len(l.input) && l.input[l.pos] == '\n' {
				l.pos++
			}
			l.tokens = append(l.tokens, Token{
				Type:  TokenNewline,
				Value: "\n",
				Line:  l.line,
			})
			l.line++

		case ch == '-' && l.pos+1 < len(l.input) && l.input[l.pos+1] == '-':
			// Flag: --key=value or --key value
			l.tokens = append(l.tokens, l.scanFlag())

		case ch == '[':
			// JSON array: ["cmd", "arg1", "arg2"]
			l.tokens = append(l.tokens, l.scanJSONArray())

		case ch == '"':
			// Quoted string
			l.tokens = append(l.tokens, l.scanQuotedString())

		default:
			// Word (instruction keyword or argument)
			l.tokens = append(l.tokens, l.scanWord())
		}
	}

	l.tokens = append(l.tokens, Token{Type: TokenEOF, Line: l.line})
	return l.tokens
}

// skipSpaces advances past spaces and tabs (NOT newlines).
func (l *Lexer) skipSpaces() {
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == ' ' || ch == '\t' {
			l.pos++
		} else if ch == '\\' && l.pos+1 < len(l.input) {
			// Line continuation: backslash followed by newline
			next := l.input[l.pos+1]
			if next == '\n' {
				l.pos += 2
				l.line++
			} else if next == '\r' && l.pos+2 < len(l.input) && l.input[l.pos+2] == '\n' {
				l.pos += 3
				l.line++
			} else {
				return
			}
		} else {
			return
		}
	}
}

// scanWord reads a contiguous non-whitespace word.
func (l *Lexer) scanWord() Token {
	start := l.pos
	startLine := l.line
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			break
		}
		// Check for line continuation within a word
		if ch == '\\' && l.pos+1 < len(l.input) {
			next := l.input[l.pos+1]
			if next == '\n' || next == '\r' {
				break
			}
		}
		l.pos++
	}

	word := l.input[start:l.pos]

	// Check if this word is an instruction keyword
	upper := strings.ToUpper(word)
	if _, ok := LookupInstruction(upper); ok {
		return Token{Type: TokenInstruction, Value: upper, Line: startLine}
	}
	return Token{Type: TokenArgument, Value: word, Line: startLine}
}

// scanFlag reads --key=value or --key.
func (l *Lexer) scanFlag() Token {
	start := l.pos
	startLine := l.line
	l.pos += 2 // skip "--"

	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			break
		}
		l.pos++
	}
	return Token{Type: TokenFlag, Value: l.input[start:l.pos], Line: startLine}
}

// scanQuotedString reads a double-quoted string, handling escapes.
func (l *Lexer) scanQuotedString() Token {
	startLine := l.line
	l.pos++ // skip opening quote
	var sb strings.Builder

	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == '\\' && l.pos+1 < len(l.input) {
			l.pos++
			sb.WriteByte(l.input[l.pos])
			l.pos++
			continue
		}
		if ch == '"' {
			l.pos++ // skip closing quote
			break
		}
		if ch == '\n' {
			l.line++
		}
		sb.WriteByte(ch)
		l.pos++
	}
	return Token{Type: TokenArgument, Value: sb.String(), Line: startLine}
}

// scanJSONArray reads ["arg1", "arg2", ...] as a single token.
func (l *Lexer) scanJSONArray() Token {
	start := l.pos
	startLine := l.line
	depth := 0

	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == '[' {
			depth++
		} else if ch == ']' {
			depth--
			if depth == 0 {
				l.pos++
				break
			}
		} else if ch == '\n' {
			l.line++
		}
		l.pos++
	}
	return Token{Type: TokenArgument, Value: l.input[start:l.pos], Line: startLine}
}

// ParseJSONArray parses a JSON-style array like ["cmd","arg1"] into a string
// slice. Minimal parser — avoids pulling in encoding/json for this hot path.
func ParseJSONArray(s string) []string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil
	}
	inner := s[1 : len(s)-1]
	if strings.TrimSpace(inner) == "" {
		return []string{}
	}

	var result []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		// Strip quotes
		if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
			part = part[1 : len(part)-1]
		}
		result = append(result, part)
	}
	return result
}

// IsUpperAlpha returns true if every rune in s is an uppercase ASCII letter.
func IsUpperAlpha(s string) bool {
	for _, r := range s {
		if !unicode.IsUpper(r) && !unicode.IsLetter(r) {
			return false
		}
	}
	return len(s) > 0
}
