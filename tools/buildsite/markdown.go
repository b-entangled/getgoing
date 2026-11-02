// Package main renders the repository's learning material into one
// self-contained HTML page. markdown.go implements the Markdown
// subset the repository actually uses: ATX headings, fenced code
// blocks (syntax-highlighted, with composition fences promoted to
// interactive diagrams), pipe tables, bullet and ordered lists,
// horizontal rules, paragraphs, and inline code, bold, italic and
// links.
package main

import (
	"strings"
)

// Heading is one ATX heading found while rendering a document. ID is
// the final anchor (document prefix included); Slug is the bare
// GitHub-style slug of the plain text, used to resolve fragment links.
type Heading struct {
	Level int
	Text  string // plain text with markup stripped: what the navigation shows
	Slug  string
	ID    string
}

// Rendered is one document's HTML plus the structure extracted from it.
type Rendered struct {
	HTML     string
	Title    string // plain text of the first h1; "" when there is none
	Headings []Heading
}

// renderMarkdown converts src to HTML. Every link target is rewritten
// through resolve so the generated site navigates internally. Heading
// ids are prefixed with prefix (the owning document's anchor) so they
// stay unique across the single-page site.
func renderMarkdown(src string, resolve func(url string) string, prefix string) Rendered {
	var r Rendered
	seen := map[string]int{}
	lines := strings.Split(src, "\n")
	var out strings.Builder
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "```"):
			i = writeFence(&out, lines, i)
		case isHeading(trimmed):
			text := headingText(trimmed)
			level := headingLevel(trimmed)
			plain := plainText(text)
			s := slug(plain)
			seen[s]++
			if n := seen[s]; n > 1 {
				s += "-" + itoa(n)
			}
			id := prefix + s
			r.Headings = append(r.Headings, Heading{Level: level, Text: plain, Slug: s, ID: id})
			if level == 1 && r.Title == "" {
				r.Title = plain
			}
			out.WriteString(renderHeading(level, text, id, resolve))
			i++
		case trimmed == "---":
			out.WriteString("<hr>\n")
			i++
		case isTableStart(lines, i):
			i = writeTable(&out, lines, i, resolve)
		case isOrderedItem(trimmed) || isBulletItem(trimmed):
			i = writeList(&out, lines, i, resolve)
		case strings.HasPrefix(trimmed, "> "):
			i = writeQuote(&out, lines, i, resolve)
		case trimmed == "":
			i++
		default:
			i = writeParagraph(&out, lines, i, resolve)
		}
	}
	r.HTML = out.String()
	return r
}

// writeFence renders one fenced block. Tagged blocks are syntax
// highlighted. Untagged blocks that look like a composition flow are
// promoted to an interactive diagram; the rest stay plain <pre>.
func writeFence(out *strings.Builder, lines []string, i int) int {
	lang := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "```"))
	i++
	var body strings.Builder
	for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
		body.WriteString(lines[i] + "\n")
		i++
	}
	if i < len(lines) {
		i++ // closing fence
	}
	src := body.String()

	if lang == "" && looksLikeDiagram(src) {
		out.WriteString(renderDiagram(src))
		return i
	}
	if lang == "" && looksLikeShell(src) {
		lang = "bash"
	}
	out.WriteString(`<pre class="code"><code class="language-` + escape(lang) + `">` +
		highlight(src, lang) + "</code></pre>\n")
	return i
}

func isHeading(t string) bool {
	if !strings.HasPrefix(t, "#") {
		return false
	}
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	return n <= 6 && n < len(t) && t[n] == ' '
}

// headingLevel and headingText decompose an ATX heading line.
func headingLevel(t string) int {
	level := 0
	for level < len(t) && level < 6 && t[level] == '#' {
		level++
	}
	return level
}

func headingText(t string) string {
	level := 0
	for level < len(t) && t[level] == '#' {
		level++
	}
	return strings.TrimSpace(t[level:])
}

func renderHeading(level int, text, id string, resolve func(string) string) string {
	if level > 6 {
		level = 6
	}
	return "<h" + itoa(level) + ` id="` + escape(id) + `">` + inline(text, resolve) +
		`<a class="heading-anchor" href="#` + escape(id) + `" aria-label="Permalink" tabindex="-1">#</a>` +
		"</h" + itoa(level) + ">\n"
}

// isTableStart reports whether lines[i] opens a pipe table: the line
// begins with a cell and the next line is the |---| separator.
func isTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
		return false
	}
	sep := strings.TrimSpace(lines[i+1])
	if !strings.HasPrefix(sep, "|") || !strings.Contains(sep, "-") {
		return false
	}
	for _, r := range sep {
		if r != '|' && r != '-' && r != ':' && r != ' ' {
			return false
		}
	}
	return true
}

func writeTable(out *strings.Builder, lines []string, i int, resolve func(string) string) int {
	header := splitRow(lines[i])
	i += 2 // skip separator
	out.WriteString("<table>\n<thead><tr>")
	for _, c := range header {
		out.WriteString("<th>" + inline(strings.TrimSpace(c), resolve) + "</th>")
	}
	out.WriteString("</tr></thead>\n<tbody>\n")
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
		cells := splitRow(lines[i])
		out.WriteString("<tr>")
		for j := 0; j < len(cells) || j < len(header); j++ {
			cell := ""
			if j < len(cells) {
				cell = cells[j]
			}
			out.WriteString("<td>" + inline(strings.TrimSpace(cell), resolve) + "</td>")
		}
		out.WriteString("</tr>\n")
		i++
	}
	out.WriteString("</tbody>\n</table>\n")
	return i
}

// splitRow splits a pipe row into cells, ignoring pipes inside code
// spans (tables in this repository occasionally contain `a|b`).
func splitRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '`':
			inCode = !inCode
			cur.WriteByte('`')
		case '|':
			if inCode {
				cur.WriteByte('|')
			} else {
				cells = append(cells, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(t[i])
		}
	}
	cells = append(cells, cur.String())
	return cells
}

func isBulletItem(t string) bool {
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")
}

func isOrderedItem(t string) bool {
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(t) && t[i] == '.' && t[i+1] == ' '
}

func itemBody(t string) (ordered bool, body string, ok bool) {
	switch {
	case isOrderedItem(t):
		dot := strings.Index(t, ".")
		return true, strings.TrimSpace(t[dot+1:]), true
	case isBulletItem(t):
		return false, strings.TrimSpace(t[2:]), true
	}
	return false, "", false
}

// writeList renders one list block. A blank line continues the list
// when the next item is another item of the same kind (loose list);
// anything else ends it. Indented continuation lines join their item.
func writeList(out *strings.Builder, lines []string, i int, resolve func(string) string) int {
	_, first, _ := itemBody(strings.TrimSpace(lines[i]))
	ordered := isOrderedItem(strings.TrimSpace(lines[i]))
	_ = first
	if ordered {
		out.WriteString("<ol>\n")
	} else {
		out.WriteString("<ul>\n")
	}
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			// Skip blanks that separate items of the same list.
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j < len(lines) {
				nt := strings.TrimSpace(lines[j])
				o, _, ok := itemBody(nt)
				if ok && o == ordered {
					i = j
					continue
				}
			}
			break
		}
		o, body, ok := itemBody(t)
		if !ok || o != ordered {
			break
		}
		out.WriteString("<li>" + inline(body, resolve))
		i++
		// Lazy continuation lines: plain, non-item, non-blank.
		for i < len(lines) {
			c := strings.TrimSpace(lines[i])
			if c == "" {
				break
			}
			if _, _, isItem := itemBody(c); isItem {
				break
			}
			if isHeading(c) || strings.HasPrefix(c, "```") || strings.HasPrefix(c, "|") || c == "---" {
				break
			}
			out.WriteString(" " + inline(c, resolve))
			i++
		}
		out.WriteString("</li>\n")
	}
	if ordered {
		out.WriteString("</ol>\n")
	} else {
		out.WriteString("</ul>\n")
	}
	return i
}

func writeQuote(out *strings.Builder, lines []string, i int, resolve func(string) string) int {
	out.WriteString("<blockquote><p>")
	var parts []string
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, ">") {
			break
		}
		parts = append(parts, strings.TrimSpace(strings.TrimPrefix(t, ">")))
		i++
	}
	out.WriteString(inline(strings.Join(parts, " "), resolve))
	out.WriteString("</p></blockquote>\n")
	return i
}

func writeParagraph(out *strings.Builder, lines []string, i int, resolve func(string) string) int {
	var parts []string
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if t == "" || isHeading(t) || strings.HasPrefix(t, "```") ||
			strings.HasPrefix(t, "|") || t == "---" ||
			isBulletItem(t) || isOrderedItem(t) || strings.HasPrefix(t, "> ") {
			break
		}
		parts = append(parts, t)
		i++
	}
	if len(parts) > 0 {
		body := inline(strings.Join(parts, " "), resolve)
		// A "**Why.**" paragraph is a pattern's motivation: give it a
		// class so the page can present it as a callout.
		class := ""
		if strings.HasPrefix(body, "<strong>Why.</strong>") {
			class = ` class="why"`
		}
		out.WriteString("<p" + class + ">" + body + "</p>\n")
	}
	return i
}

// inline renders code spans, bold, links and escapes everything else.
// Links with an http(s) target open externally; all others go through
// resolve (internal navigation).
func inline(s string, resolve func(string) string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			j := strings.IndexByte(s[i+1:], '`')
			if j < 0 {
				b.WriteString(escape(s[i:]))
				return b.String()
			}
			b.WriteString("<code>" + escape(s[i+1:i+1+j]) + "</code>")
			i += j + 2
		case strings.HasPrefix(s[i:], "**"):
			j := strings.Index(s[i+2:], "**")
			if j < 0 {
				b.WriteString(escape(s[i:]))
				return b.String()
			}
			b.WriteString("<strong>" + inline(s[i+2:i+2+j], resolve) + "</strong>")
			i += j + 4
		case s[i] == '*':
			j := strings.IndexByte(s[i+1:], '*')
			if j < 0 || j == 0 {
				b.WriteString(escape(s[i:]))
				return b.String()
			}
			b.WriteString("<em>" + inline(s[i+1:i+1+j], resolve) + "</em>")
			i += j + 2
		case s[i] == '[':
			text, url, n, ok := parseLink(s[i:])
			if !ok {
				b.WriteString(escape(s[i : i+1]))
				i++
				continue
			}
			href := url
			external := strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
			if !external && resolve != nil {
				href = resolve(url)
			}
			b.WriteString(`<a href="` + escape(href) + `"`)
			if external {
				b.WriteString(` target="_blank" rel="noopener"`)
			}
			b.WriteString(">" + inline(text, resolve) + "</a>")
			i += n
		default:
			j := strings.IndexAny(s[i:], "`*[")
			if j < 0 {
				b.WriteString(escape(s[i:]))
				return b.String()
			}
			b.WriteString(escape(s[i : i+j]))
			i += j
		}
	}
	return b.String()
}

// parseLink parses [text](url) at the start of s, returning the
// consumed length.
func parseLink(s string) (text, url string, n int, ok bool) {
	if len(s) < 4 || s[0] != '[' {
		return "", "", 0, false
	}
	closeBracket := -1
	for i := 1; i < len(s)-1; i++ {
		if s[i] == ']' && s[i+1] == '(' {
			closeBracket = i
			break
		}
	}
	if closeBracket < 0 {
		return "", "", 0, false
	}
	end := strings.IndexByte(s[closeBracket+2:], ')')
	if end < 0 {
		return "", "", 0, false
	}
	text = s[1:closeBracket]
	url = s[closeBracket+2 : closeBracket+2+end]
	return text, url, closeBracket + 2 + end + 1, true
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// plainText strips the inline markup the repository uses (code spans,
// bold, italic, links) so navigation labels and slugs read like the
// rendered text rather than the source.
func plainText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			j := strings.IndexByte(s[i+1:], '`')
			if j < 0 {
				b.WriteString(s[i:])
				i = len(s)
			} else {
				b.WriteString(s[i+1 : i+1+j])
				i += j + 2
			}
		case strings.HasPrefix(s[i:], "**"):
			j := strings.Index(s[i+2:], "**")
			if j < 0 {
				b.WriteString(s[i:])
				i = len(s)
			} else {
				b.WriteString(s[i+2 : i+2+j])
				i += j + 4
			}
		case s[i] == '*':
			j := strings.IndexByte(s[i+1:], '*')
			if j < 0 {
				b.WriteString(s[i:])
				i = len(s)
			} else {
				b.WriteString(s[i+1 : i+1+j])
				i += j + 2
			}
		case s[i] == '[':
			text, _, n, ok := parseLink(s[i:])
			if !ok {
				b.WriteByte(s[i])
				i++
				continue
			}
			b.WriteString(plainText(text))
			i += n
		default:
			j := strings.IndexAny(s[i:], "`*[")
			if j < 0 {
				b.WriteString(s[i:])
				i = len(s)
				continue
			}
			b.WriteString(s[i : i+j])
			i += j
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// slug turns heading text into a GitHub-style anchor.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
