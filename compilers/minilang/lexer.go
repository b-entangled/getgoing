package minilang

import (
	"fmt"
	"unicode"
)

type tokenKind uint8

const (
	tokEOF tokenKind = iota
	tokIdentifier
	tokInteger
	tokLet
	tokPrint
	tokWhile
	tokAssign
	tokEqual
	tokPlus
	tokMinus
	tokStar
	tokSlash
	tokGreater
	tokLeftParen
	tokRightParen
	tokLeftBrace
	tokRightBrace
	tokSemicolon
)

type token struct {
	kind      tokenKind
	text      string
	line, col int
}

type lexer struct {
	source    []rune
	position  int
	line, col int
}

func lex(source string) ([]token, error) {
	l := lexer{source: []rune(source), line: 1, col: 1}
	tokens := make([]token, 0, len(l.source)/2+1)
	for !l.atEnd() {
		r := l.peek()
		if unicode.IsSpace(r) {
			l.advance()
			continue
		}
		if r == '/' && l.peekNext() == '/' {
			for !l.atEnd() && l.peek() != '\n' {
				l.advance()
			}
			continue
		}

		start := token{line: l.line, col: l.col}
		switch {
		case isIdentifierStart(r):
			start.text = l.readIdentifier()
			start.kind = tokIdentifier
			switch start.text {
			case "let":
				start.kind = tokLet
			case "print":
				start.kind = tokPrint
			case "while":
				start.kind = tokWhile
			}
		case isDecimalDigit(r):
			start.kind = tokInteger
			start.text = l.readDigits()
		default:
			l.advance()
			switch r {
			case '=':
				start.kind, start.text = tokAssign, "="
				if l.peek() == '=' {
					l.advance()
					start.kind, start.text = tokEqual, "=="
				}
			case '+':
				start.kind, start.text = tokPlus, "+"
			case '-':
				start.kind, start.text = tokMinus, "-"
			case '*':
				start.kind, start.text = tokStar, "*"
			case '/':
				start.kind, start.text = tokSlash, "/"
			case '>':
				start.kind, start.text = tokGreater, ">"
			case '(':
				start.kind, start.text = tokLeftParen, "("
			case ')':
				start.kind, start.text = tokRightParen, ")"
			case '{':
				start.kind, start.text = tokLeftBrace, "{"
			case '}':
				start.kind, start.text = tokRightBrace, "}"
			case ';':
				start.kind, start.text = tokSemicolon, ";"
			default:
				return nil, fmt.Errorf("%d:%d: unexpected character %q", start.line, start.col, r)
			}
		}
		tokens = append(tokens, start)
	}
	tokens = append(tokens, token{kind: tokEOF, line: l.line, col: l.col})
	return tokens, nil
}

func (l *lexer) atEnd() bool { return l.position >= len(l.source) }

func (l *lexer) peek() rune {
	if l.atEnd() {
		return 0
	}
	return l.source[l.position]
}

func (l *lexer) peekNext() rune {
	if l.position+1 >= len(l.source) {
		return 0
	}
	return l.source[l.position+1]
}

func (l *lexer) advance() rune {
	r := l.source[l.position]
	l.position++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func (l *lexer) readIdentifier() string {
	start := l.position
	for !l.atEnd() && isIdentifierContinue(l.peek()) {
		l.advance()
	}
	return string(l.source[start:l.position])
}

func (l *lexer) readDigits() string {
	start := l.position
	for !l.atEnd() && isDecimalDigit(l.peek()) {
		l.advance()
	}
	return string(l.source[start:l.position])
}

func isIdentifierStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }

func isIdentifierContinue(r rune) bool { return isIdentifierStart(r) || unicode.IsDigit(r) }

func isDecimalDigit(r rune) bool { return r >= '0' && r <= '9' }
