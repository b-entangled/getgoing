package main

import (
	"regexp"
	"strings"
)

// diagramRow is one line of a composition fence: an optional role
// label plus the chain of stages it owns.
type diagramRow struct {
	Label string
	Nodes []diagramNode
}

// diagramNode is one stage. Main is the component or action; Note is
// the parenthetical (or column-aligned) commentary next to it.
type diagramNode struct {
	Main string
	Note string
}

// rowLabelRe matches a leading role label such as "placement: " or
// "detect & spread: ".
var rowLabelRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 &_/-]{0,30}):\s+(\S.*)$`)

// looksLikeDiagram reports whether an untagged fence is a composition
// flow: either it contains flow arrows, or every line is a labelled
// role row.
func looksLikeDiagram(body string) bool {
	if strings.Contains(body, "→") || strings.Contains(body, "⇒") {
		return true
	}
	lines := nonEmptyLines(body)
	if len(lines) < 2 {
		return false
	}
	for _, l := range lines {
		if !rowLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// looksLikeShell guesses the language of an untagged fence whose first
// line is a command the repository actually runs.
func looksLikeShell(body string) bool {
	for _, l := range nonEmptyLines(body) {
		for _, p := range []string{"make ", "go ", "gofmt ", "$ ", "golangci-lint "} {
			if strings.HasPrefix(l, p) {
				return true
			}
		}
		return false
	}
	return false
}

// parseDiagram splits a composition fence into labelled rows of nodes.
// Lines starting with an arrow continue the previous row's chain.
func parseDiagram(body string) []diagramRow {
	var rows []diagramRow
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "→") || strings.HasPrefix(line, "⇒") {
			if len(rows) == 0 {
				rows = append(rows, diagramRow{})
			}
			tail := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "→"), "⇒"))
			rows[len(rows)-1].Nodes = append(rows[len(rows)-1].Nodes, splitChain(tail)...)
			continue
		}
		row := diagramRow{}
		rest := line
		if m := rowLabelRe.FindStringSubmatch(line); m != nil {
			row.Label = m[1]
			rest = m[2]
		}
		row.Nodes = splitChain(rest)
		if len(row.Nodes) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// splitChain splits "a → b → c" into three nodes.
func splitChain(s string) []diagramNode {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '→' || r == '⇒' })
	nodes := make([]diagramNode, 0, len(parts))
	for _, p := range parts {
		if n, ok := splitNode(p); ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// splitNode separates a chain segment into its component and its
// commentary: either a trailing parenthetical or a column-aligned tail
// (two or more spaces).
func splitNode(seg string) (diagramNode, bool) {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return diagramNode{}, false
	}
	if strings.HasSuffix(seg, ")") {
		if k := strings.LastIndex(seg, "("); k > 0 {
			main := strings.TrimSpace(seg[:k])
			note := strings.TrimSuffix(strings.TrimSpace(seg[k+1:]), ")")
			if main != "" {
				return diagramNode{Main: main, Note: note}, true
			}
		}
	}
	if k := indexDoubleSpace(seg); k >= 0 {
		main := strings.TrimSpace(seg[:k])
		note := strings.TrimSpace(seg[k:])
		if main != "" && note != "" {
			return diagramNode{Main: main, Note: note}, true
		}
	}
	return diagramNode{Main: seg}, true
}

// indexDoubleSpace finds the first run of two or more spaces, which is
// how the composition fences column-align "component  description".
func indexDoubleSpace(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == ' ' && s[i+1] == ' ' {
			return i
		}
	}
	return -1
}

func nonEmptyLines(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// renderDiagram turns a composition fence into an interactive figure:
// labelled rows of hoverable, focusable stages with a trace control
// and the original ASCII behind a details toggle.
func renderDiagram(body string) string {
	rows := parseDiagram(body)
	if len(rows) == 0 {
		return `<pre class="code"><code class="language-">` + escape(body) + "</code></pre>\n"
	}
	var b strings.Builder
	b.WriteString(`<figure class="diagram" data-diagram>` + "\n")
	b.WriteString(`<div class="diagram-bar">` +
		`<button type="button" class="diagram-trace" data-trace>▶ trace flow</button>` +
		`<span class="diagram-status" data-diagram-status>click a stage to pin it</span>` +
		`<details class="diagram-source"><summary>ASCII</summary><pre>` + escape(body) + `</pre></details>` +
		`</div>` + "\n")
	b.WriteString(`<div class="diagram-body">` + "\n")
	for _, row := range rows {
		b.WriteString(`<div class="diagram-row">`)
		if row.Label != "" {
			b.WriteString(`<span class="diagram-label">` + escape(row.Label) + `</span>`)
		}
		b.WriteString(`<div class="diagram-chain">`)
		for i, n := range row.Nodes {
			if i > 0 {
				b.WriteString(`<span class="diagram-arrow" aria-hidden="true">→</span>`)
			}
			b.WriteString(`<button type="button" class="diagram-node">`)
			b.WriteString(`<span class="node-main">` + escape(n.Main) + `</span>`)
			if n.Note != "" {
				b.WriteString(`<span class="node-note">` + escape(n.Note) + `</span>`)
			}
			b.WriteString(`</button>`)
		}
		b.WriteString(`</div></div>` + "\n")
	}
	b.WriteString(`</div>` + "\n</figure>\n")
	return b.String()
}
