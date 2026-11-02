package main

import "strings"

// highlight renders escaped, tokenised source for a fenced block.
// Go gets the full treatment (keywords, types, calls, strings,
// comments); shell gets comments, strings and flags; anything else is
// escaped verbatim.
func highlight(code, lang string) string {
	switch lang {
	case "go":
		return highlightGo(code)
	case "bash", "sh", "shell", "make":
		return highlightShell(code)
	default:
		return escape(code)
	}
}

var goKeywords = words(
	"break", "case", "chan", "const", "continue", "default", "defer",
	"else", "fallthrough", "for", "func", "go", "goto", "if", "import",
	"interface", "map", "package", "range", "return", "select", "struct",
	"switch", "type", "var",
)

var goBuiltins = words(
	"append", "cap", "clear", "close", "complex", "copy", "delete",
	"imag", "len", "make", "max", "min", "new", "panic", "print",
	"println", "real", "recover",
)

var goTypes = words(
	"bool", "byte", "complex64", "complex128", "error", "float32",
	"float64", "int", "int8", "int16", "int32", "int64", "rune",
	"string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
	"any", "comparable",
)

// highlightGo tokenises Go source in one left-to-right pass: comments,
// strings, identifiers, numbers and everything else.
func highlightGo(code string) string {
	var b strings.Builder
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case strings.HasPrefix(code[i:], "//"):
			j := strings.IndexByte(code[i:], '\n')
			if j < 0 {
				j = len(code) - i
			}
			writeToken(&b, "tok-c", code[i:i+j])
			i += j
		case strings.HasPrefix(code[i:], "/*"):
			j := strings.Index(code[i:], "*/")
			if j < 0 {
				j = len(code) - i
			} else {
				j += 2
			}
			writeToken(&b, "tok-c", code[i:i+j])
			i += j
		case c == '"' || c == '`':
			j := scanString(code, i)
			writeToken(&b, "tok-s", code[i:j])
			i = j
		case c == '\'':
			j := scanRune(code, i)
			writeToken(&b, "tok-s", code[i:j])
			i = j
		case isIdentStart(c):
			j := i
			for j < len(code) && isIdentPart(code[j]) {
				j++
			}
			ident := code[i:j]
			rest := code[j:]
			switch {
			case goKeywords[ident]:
				writeToken(&b, "tok-k", ident)
			case goBuiltins[ident]:
				writeToken(&b, "tok-b", ident)
			case goTypes[ident]:
				writeToken(&b, "tok-t", ident)
			case strings.HasPrefix(rest, "("):
				writeToken(&b, "tok-f", ident)
			case isUpper(ident[0]) && j-i > 1:
				writeToken(&b, "tok-t", ident)
			default:
				b.WriteString(escape(ident))
			}
			i = j
		case isDigit(c):
			j := i
			for j < len(code) && (isDigit(code[j]) || code[j] == '.' || code[j] == '_' ||
				code[j] == 'x' || code[j] == 'X' || isHex(code[j])) {
				j++
			}
			writeToken(&b, "tok-n", code[i:j])
			i = j
		default:
			j := i + 1
			for j < len(code) && !isTokenStart(code, j) {
				j++
			}
			b.WriteString(escape(code[i:j]))
			i = j
		}
	}
	return b.String()
}

// isTokenStart reports whether code[j] begins something worth
// tokenising, which bounds the run of plain punctuation.
func isTokenStart(code string, j int) bool {
	c := code[j]
	if c == '/' || c == '"' || c == '`' || c == '\'' || isIdentStart(c) || isDigit(c) {
		return true
	}
	return false
}

var shellKeywords = words(
	"if", "then", "else", "elif", "fi", "for", "while", "until", "do",
	"done", "case", "esac", "function", "return", "exit", "export",
	"local", "echo", "printf",
)

// highlightShell tokenises shell/make source: comments, strings,
// flags and keywords.
func highlightShell(code string) string {
	var b strings.Builder
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == '#':
			j := strings.IndexByte(code[i:], '\n')
			if j < 0 {
				j = len(code) - i
			}
			writeToken(&b, "tok-c", code[i:i+j])
			i += j
		case c == '"' || c == '\'':
			j := scanString(code, i)
			writeToken(&b, "tok-s", code[i:j])
			i = j
		case c == '-' && i+1 < len(code) && (isDigit(code[i+1]) || code[i+1] == '-'):
			j := i + 1
			for j < len(code) && (isIdentPart(code[j]) || code[j] == '-') {
				j++
			}
			writeToken(&b, "tok-fl", code[i:j])
			i = j
		case isIdentStart(c):
			j := i
			for j < len(code) && (isIdentPart(code[j]) || code[j] == '-' || code[j] == '/') {
				j++
			}
			ident := code[i:j]
			if shellKeywords[ident] {
				writeToken(&b, "tok-k", ident)
			} else {
				b.WriteString(escape(ident))
			}
			i = j
		case isDigit(c):
			j := i
			for j < len(code) && (isDigit(code[j]) || code[j] == '.') {
				j++
			}
			writeToken(&b, "tok-n", code[i:j])
			i = j
		default:
			j := i + 1
			for j < len(code) && !isShellTokenStart(code, j) {
				j++
			}
			b.WriteString(escape(code[i:j]))
			i = j
		}
	}
	return b.String()
}

func isShellTokenStart(code string, j int) bool {
	c := code[j]
	if c == '#' || c == '"' || c == '\'' || c == '-' || isIdentStart(c) || isDigit(c) {
		return true
	}
	return false
}

// scanString returns the index just past the string that starts at i.
func scanString(code string, i int) int {
	quote := code[i]
	for j := i + 1; j < len(code); j++ {
		if code[j] == '\\' && quote != '`' {
			j++
			continue
		}
		if code[j] == quote {
			return j + 1
		}
		if quote == '`' && code[j] == '\n' {
			// raw strings may span lines; keep going
		}
	}
	return len(code)
}

// scanRune returns the index just past the rune literal at i,
// tolerating the unterminated case.
func scanRune(code string, i int) int {
	for j := i + 1; j < len(code); j++ {
		if code[j] == '\\' {
			j++
			continue
		}
		if code[j] == '\'' {
			return j + 1
		}
		if code[j] == '\n' {
			return j
		}
	}
	return len(code)
}

func writeToken(b *strings.Builder, class, text string) {
	b.WriteString(`<span class="` + class + `">` + escape(text) + `</span>`)
}

func words(list ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range list {
		for _, f := range strings.Fields(w) {
			m[f] = true
		}
	}
	return m
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
