// source.go generates the collapsible "View source" panel that sits
// under every numbered pattern heading: the real package source,
// read from disk at build time so it can never drift from the code.
package main

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// sourceDocs are the package indexes whose linked entries get a source
// panel. Keeping this allowlist makes the published source set deliberate.
var sourceDocs = map[string]bool{
	"creational/README.md":  true,
	"structural/README.md":  true,
	"behavioral/README.md":  true,
	"concurrency/README.md": true,
	"idioms/README.md":      true,
	"concepts/README.md":    true,
	"mistakes/README.md":    true,
	"internals/README.md":   true,
	"sdesign/README.md":     true,
	"distributed/README.md": true,
	"compilers/README.md":   true,
}

// sourceFile is one package source file ready to be rendered.
type sourceFile struct {
	Name string
	Src  string
}

// attachSourcePanels inserts a generated panel after every numbered
// pattern heading of d. The heading text carries the package link
// ("1. Singleton — singleton/"), which is resolved relative to the
// document's own directory.
func attachSourcePanels(root string, d *doc) {
	if !sourceDocs[d.Spec.Path] {
		return
	}
	for _, h := range d.Headings {
		if h.Level != 2 {
			continue
		}
		pkg := headingPackage(h.Text)
		if pkg == "" {
			continue
		}
		files := packageSources(root, path.Join(path.Dir(d.Spec.Path), pkg))
		if len(files) == 0 {
			continue
		}
		d.HTML = insertAfterHeading(d.HTML, h.ID,
			sourcePanel(path.Join(path.Dir(d.Spec.Path), pkg), files))
	}
}

// headingPackage extracts the package directory from a heading like
// "24. Worker Pool — workerpool". It returns "" when the heading
// does not link a package.
func headingPackage(text string) string {
	k := strings.LastIndex(text, " — ")
	if k < 0 {
		return ""
	}
	p := strings.TrimSpace(text[k+4:])
	p = strings.Trim(p, "`")
	return strings.TrimSuffix(p, "/")
}

// packageSources reads every non-test .go file below dir (files named
// after the package first), or nil when the directory contains no Go
// source. Relative file paths keep nested packages distinguishable.
func packageSources(root, dir string) []sourceFile {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	base := path.Base(dir)
	var files []sourceFile
	_ = filepath.WalkDir(abs, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if file != abs && (entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(abs, file)
		if err != nil {
			return nil
		}
		files = append(files, sourceFile{Name: filepath.ToSlash(rel), Src: string(b)})
		return nil
	})
	if len(files) == 0 {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		pi := strings.TrimSuffix(files[i].Name, ".go") == base
		pj := strings.TrimSuffix(files[j].Name, ".go") == base
		if pi != pj {
			return pi
		}
		return files[i].Name < files[j].Name
	})
	return files
}

// sourcePanel renders the files as a collapsed <details> block whose
// code is syntax-highlighted exactly like a fenced markdown block.
func sourcePanel(dir string, files []sourceFile) string {
	total := 0
	for _, f := range files {
		total += lineCount(f.Src)
	}
	flabel := "file"
	if len(files) != 1 {
		flabel = "files"
	}
	var b strings.Builder
	b.WriteString("\n<details class=\"source-panel\">\n")
	b.WriteString(`<summary><span class="source-label">View source</span> <code>` +
		escape(dir) + `/</code> <span class="source-meta">` +
		itoa(len(files)) + " " + flabel + " · " + itoa(total) + " lines</span></summary>\n")
	for _, f := range files {
		b.WriteString("<div class=\"source-file\">\n")
		b.WriteString(`<div class="source-file-head"><span class="source-file-name">` +
			escape(f.Name) + `</span><span class="source-lines">` +
			itoa(lineCount(f.Src)) + " lines</span></div>\n")
		b.WriteString(`<pre class="code"><code class="language-go">` +
			highlight(f.Src, "go") + "</code></pre>\n")
		b.WriteString("</div>\n")
	}
	b.WriteString("</details>")
	return b.String()
}

// insertAfterHeading splices html into h just after the closing tag of
// the heading whose id it carries.
func insertAfterHeading(html, id, insert string) string {
	marker := `id="` + escape(id) + `"`
	k := strings.Index(html, marker)
	if k < 0 {
		return html
	}
	rest := html[k:]
	end := strings.Index(rest, "</h")
	if end < 0 {
		return html
	}
	gt := strings.IndexByte(rest[end:], '>')
	if gt < 0 {
		return html
	}
	j := k + end + gt + 1
	return html[:j] + insert + html[j:]
}

// lineCount counts lines the way a reader would see them.
func lineCount(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
