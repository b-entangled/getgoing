// Command buildsite renders this repository's markdown — every design
// pattern, concept, mistake, runtime internal and composed design —
// into one self-contained interactive HTML page: sidebar navigation,
// scrollspy table of contents, instant search, category filters,
// highlighted code with copy buttons, interactive composition diagrams
// and keyboard shortcuts.
//
// Usage:
//
//	go run ./tools/buildsite -out site/index.html
package main

import (
	"embed"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

//go:embed site.html assets/styles.css assets/app.js
var assets embed.FS

// docSpec is one markdown file in the reading order of the site.
type docSpec struct {
	Path     string // repo-relative, slash-separated
	Category string
}

// manifest is the site's table of contents. Order is navigation order.
var manifest = []docSpec{
	{"README.md", "Overview"},
	{"LEARNING_PATH.md", "Overview"},
	{"concepts/BASICS.md", "Go basics"},
	{"concepts/README.md", "Concepts"},
	{"rust/README.md", "Rust"},
	{"idioms/README.md", "Idioms"},
	{"mistakes/README.md", "Mistakes"},
	{"creational/README.md", "Patterns"},
	{"structural/README.md", "Patterns"},
	{"behavioral/README.md", "Patterns"},
	{"concurrency/README.md", "Concurrency"},
	{"internals/README.md", "Internals"},
	{"internals/MEMORY.md", "Internals"},
	{"internals/RUNTIME.md", "Internals"},
	{"compilers/README.md", "Compilers"},
	{"compilers/BUILDING_A_LANGUAGE.md", "Compilers"},
	{"execution/README.md", "Execution"},
	{"linux/README.md", "Linux"},
	{"linux/CONTAINERS.md", "Containers"},
	{"linux/KUBERNETES.md", "Containers"},
	{"linux/CONTAINER_NETWORKING.md", "Containers"},
	{"linux/CONTAINER_STORAGE.md", "Containers"},
	{"linux/NETWORKING.md", "Linux internals"},
	{"linux/DMA.md", "Linux internals"},
	{"linux/DISK.md", "Linux internals"},
	{"networking/README.md", "Networking"},
	{"webservices/README.md", "Web services"},
	{"database/README.md", "Databases"},
	{"gpu/README.md", "GPU"},
	{"sdesign/README.md", "System design"},
	{"sdesign/DESIGNS.md", "System design"},
	{"adr/0001-golang-design-patterns.md", "Decision record"},
	{"networking/DESIGNS.md", "Designs"},
	{"linux/DESIGNS.md", "Designs"},
	{"database/DESIGNS.md", "Designs"},
	{"distributed/README.md", "Distributed"},
	{"distributed/DESIGNS.md", "Distributed"},
	{"rl/BASICS.md", "RL"},
	{"rl/MUJOCO.md", "RL"},
	{"rl/README.md", "RL"},
	{"rl/ROBOTICS.md", "RL"},
	{"llm/README.md", "LLM"},
	{"llm/vllm.md", "LLM"},
	{"agentictools/README.md", "Agentic tools"},
	{"android/README.md", "Android app"},
}

// doc is one rendered document.
type doc struct {
	Spec     docSpec
	ID       string // anchor of the section itself
	Title    string // first h1, markup stripped
	Category string
	Headings []Heading
	HTML     string
}

type site struct {
	docs   []*doc
	byPath map[string]*doc // "creational/README.md"
	byDir  map[string]*doc // "creational" → its README (or first doc)
}

func main() {
	out := flag.String("out", "site/index.html", "path of the HTML file to write")
	root := flag.String("root", ".", "repository root to read markdown from")
	flag.Parse()

	html, err := buildSite(*root, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildsite:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "buildsite:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(html), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "buildsite:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d documents)\n", *out, len(manifest))
}

// buildSite renders every document in the manifest and assembles the
// self-contained page.
func buildSite(root string, generated time.Time) (string, error) {
	s := &site{
		byPath: map[string]*doc{},
		byDir:  map[string]*doc{},
	}

	// Pass one: read the sources and extract titles and headings, so
	// pass two can resolve every link against the finished structure.
	// The prefix is applied here too: link resolution during pass two
	// reads other documents' heading lists, which must already carry
	// their final anchors.
	for _, spec := range manifest {
		src, err := readFile(root, spec.Path)
		if err != nil {
			return "", err
		}
		id := docID(spec.Path)
		r := renderMarkdown(src, nil, id+"--")
		d := &doc{
			Spec:     spec,
			ID:       id,
			Title:    r.Title,
			Category: spec.Category,
			Headings: r.Headings,
		}
		s.docs = append(s.docs, d)
		s.byPath[spec.Path] = d
		dir := path.Dir(spec.Path)
		// A directory's own README wins over sibling documents, so
		// links like "../sdesign/" land on its index page.
		if _, ok := s.byDir[dir]; !ok || path.Base(spec.Path) == "README.md" {
			s.byDir[dir] = d
		}
	}

	// Pass two: render with link resolution and per-document anchors.
	for _, d := range s.docs {
		src, err := readFile(root, d.Spec.Path)
		if err != nil {
			return "", err
		}
		r := renderMarkdown(src, s.resolver(d.Spec.Path), d.ID+"--")
		d.HTML = r.HTML
		d.Headings = r.Headings
		attachSourcePanels(root, d)
	}

	return assemble(s, generated), nil
}

func readFile(root, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", rel, err)
	}
	return string(b), nil
}

// docID is the stable anchor for a document path.
func docID(mdPath string) string {
	return "doc-" + slug(strings.TrimSuffix(mdPath, ".md"))
}

// resolver rewrites every link in fromPath to an in-page anchor,
// climbing to the nearest document that exists in the site.
func (s *site) resolver(fromPath string) func(string) string {
	return func(url string) string {
		target, frag := fromPath, ""
		if k := strings.Index(url, "#"); k >= 0 {
			target, frag = url[:k], url[k+1:]
			if target == "" {
				target = fromPath
			} else {
				target = path.Clean(path.Join(path.Dir(fromPath), target))
			}
		} else if url != "" {
			target = path.Clean(path.Join(path.Dir(fromPath), url))
		}

		base := path.Base(target)
		for t := target; ; t = path.Dir(t) {
			d := s.byPath[t]
			if d == nil {
				d = s.byDir[t]
			}
			if d != nil {
				return "#" + s.anchor(d, base, target, frag)
			}
			if t == "." || t == "/" {
				break
			}
		}
		return "#doc-readme"
	}
}

// anchor picks the best heading inside d for a fragment or for a link
// whose final path element names a package.
func (s *site) anchor(d *doc, base, target, frag string) string {
	if frag != "" {
		if id := headingBySlug(d, frag); id != "" {
			return id
		}
		return d.ID
	}
	// Links that point at a markdown file aim at that document
	// itself; links that name a package (a directory or a .go file)
	// aim at the section that documents it.
	name := strings.TrimSuffix(base, ".md")
	isFileLink := name != base // had a .md extension
	name = strings.TrimSuffix(name, ".go")
	if isFileLink {
		return d.ID
	}
	if name != "" && name != path.Base(d.Spec.Path) && name != "README" {
		if id := headingLike(d, name); id != "" {
			return id
		}
	}
	return d.ID
}

// headingBySlug resolves an exact (or embedded) fragment within a document.
func headingBySlug(d *doc, frag string) string {
	for _, h := range d.Headings {
		if h.Slug == frag {
			return h.ID
		}
	}
	for _, h := range d.Headings {
		if strings.Contains(h.Slug, frag) {
			return h.ID
		}
	}
	return ""
}

// headingLike finds the section whose slug names a package, e.g.
// "singleton" → "## 1. Singleton — singleton/".
func headingLike(d *doc, name string) string {
	name = strings.ToLower(name)
	for _, h := range d.Headings {
		if h.Level < 2 {
			continue
		}
		s := h.Slug
		if s == name || strings.Contains(s, "-"+name) || strings.Contains(s, name+"-") {
			return h.ID
		}
	}
	for _, h := range d.Headings {
		if h.Level >= 2 && strings.Contains(h.Slug, name) {
			return h.ID
		}
	}
	return ""
}

// assemble fills the page shell with the navigation, filter chips and
// document sections.
func assemble(s *site, generated time.Time) string {
	sections := &strings.Builder{}
	sectionsUsed := 0
	for _, d := range s.docs {
		for _, h := range d.Headings {
			if h.Level >= 2 {
				sectionsUsed++
			}
		}
		sections.WriteString(`<section class="doc" id="` + escape(d.ID) +
			`" data-category="` + escape(d.Category) +
			`" data-title="` + escape(d.Title) +
			`" data-path="` + escape(d.Spec.Path) + `">` + "\n")
		sections.WriteString(`<div class="doc-head"><span class="doc-cat">` + escape(d.Category) +
			`</span><span class="doc-file">` + escape(d.Spec.Path) +
			`</span><a class="doc-permalink" href="#` + escape(d.ID) +
			`" aria-label="Permalink to this document">§</a></div>` + "\n")
		sections.WriteString(d.HTML)
		sections.WriteString("</section>\n\n")
	}

	stats := fmt.Sprintf("%d documents · %d sections", len(s.docs), sectionsUsed)

	page, err := assets.ReadFile("site.html")
	if err != nil {
		panic(err)
	}
	css, err := assets.ReadFile("assets/styles.css")
	if err != nil {
		panic(err)
	}
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		panic(err)
	}

	repl := strings.NewReplacer(
		"{{Title}}", "Learnings — Interactive Systems Field Guide",
		"{{Description}}", "Explore Go, compilers, program execution, Linux, networking, web security, GPUs, distributed systems and LLMs in one searchable, interactive field guide.",
		"{{CSS}}", string(css),
		"{{JS}}", string(js),
		"{{Nav}}", buildNav(s),
		"{{Chips}}", buildChips(s),
		"{{Content}}", sections.String(),
		"{{Stats}}", stats,
		"{{DocCount}}", fmt.Sprint(len(s.docs)),
		"{{SectionCount}}", fmt.Sprint(sectionsUsed),
		"{{Generated}}", generated.Format("2 Jan 2006"),
	)
	return repl.Replace(string(page))
}

// buildNav renders the sidebar: categories containing documents, each
// document expandable to its h2 sections.
func buildNav(s *site) string {
	counts := map[string]int{}
	for _, d := range s.docs {
		counts[d.Category]++
	}
	var b strings.Builder
	var lastCat string
	for _, d := range s.docs {
		if d.Category != lastCat {
			if lastCat != "" {
				b.WriteString("</div></div>\n")
			}
			lastCat = d.Category
			leaf := ""
			if counts[d.Category] == 1 {
				leaf = " is-leaf"
			}
			b.WriteString(`<div class="nav-group` + leaf + `" data-category="` + escape(d.Category) + `">` +
				`<button type="button" class="nav-group-head" aria-expanded="true">` +
				`<span class="nav-group-name">` + escape(d.Category) + `</span>` +
				`<span class="nav-group-meta"><span class="nav-group-n">` +
				fmt.Sprint(counts[d.Category]) + `</span>` +
				`<span class="nav-group-chevron" aria-hidden="true">▾</span></span></button>` +
				`<div class="nav-group-body">` + "\n")
		}
		b.WriteString(`<div class="nav-doc" data-doc="` + escape(d.ID) + `">` +
			`<a class="nav-doc-title" href="#` + escape(d.ID) + `">` +
			`<span class="nav-doc-name">` + escape(d.Title) + `</span>` +
			`</a>` + "\n")
		b.WriteString("</div>\n")
	}
	if lastCat != "" {
		b.WriteString("</div></div>\n")
	}
	return b.String()
}

// buildChips renders the category filter row (an "All" chip first).
func buildChips(s *site) string {
	counts := map[string]int{}
	var order []string
	for _, d := range s.docs {
		if counts[d.Category] == 0 {
			order = append(order, d.Category)
		}
		counts[d.Category]++
	}
	var b strings.Builder
	b.WriteString(`<button type="button" class="chip is-active" data-cat="*">All <span class="chip-n">` +
		fmt.Sprint(len(s.docs)) + `</span></button>` + "\n")
	for _, c := range order {
		b.WriteString(`<button type="button" class="chip" data-cat="` + escape(c) + `">` +
			escape(c) + ` <span class="chip-n">` + fmt.Sprint(counts[c]) + `</span></button>` + "\n")
	}
	return b.String()
}
