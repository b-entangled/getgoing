package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// idRe and hrefRe pull anchors and links out of the generated page.
var (
	idRe     = regexp.MustCompile(`\sid="([^"]+)"`)
	hrefRe   = regexp.MustCompile(`\shref="#([^"]+)"`)
	scriptRe = regexp.MustCompile(`(?s)<script>.*?</script>|<style>.*?</style>`)
	fixTime  = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
)

// pageBody strips the inlined CSS and JS payloads so the structural
// checks only look at real page markup.
func pageBody(page string) string {
	return scriptRe.ReplaceAllString(page, "")
}

func TestRenderMarkdownPrefixesHeadingAnchors(t *testing.T) {
	r := renderMarkdown("# Title\n\n## The Thing\n", nil, "doc-x--")
	if want := "doc-x--title"; !strings.Contains(r.HTML, `id="`+want+`"`) {
		t.Errorf("h1 anchor missing %q in:\n%s", want, r.HTML)
	}
	if want := "doc-x--the-thing"; !strings.Contains(r.HTML, `id="`+want+`"`) {
		t.Errorf("h2 anchor missing %q in:\n%s", want, r.HTML)
	}
	if r.Title != "Title" {
		t.Errorf("Title = %q, want %q", r.Title, "Title")
	}
	if len(r.Headings) != 2 {
		t.Fatalf("got %d headings, want 2", len(r.Headings))
	}
	if r.Headings[1].Text != "The Thing" || r.Headings[1].Slug != "the-thing" {
		t.Errorf("heading = %+v, want text The Thing slug the-thing", r.Headings[1])
	}
}

func TestRenderMarkdownEscapesRawHTML(t *testing.T) {
	r := renderMarkdown("<script>alert(1)</script>\n\n& < >\n", nil, "")
	if strings.Contains(r.HTML, "<script>") {
		t.Error("raw script tag survived rendering")
	}
	if !strings.Contains(r.HTML, "&lt;script&gt;") {
		t.Errorf("expected escaped script tag in:\n%s", r.HTML)
	}
}

func TestRenderMarkdownRendersItalicAndBold(t *testing.T) {
	r := renderMarkdown("keep *these* and **those** words\n", nil, "")
	for _, want := range []string{"<em>these</em>", "<strong>those</strong>"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("missing %q in:\n%s", want, r.HTML)
		}
	}
}

func TestCompositionFencesRenderAsInteractiveDiagrams(t *testing.T) {
	src := "```\nplacement: hashring.Ring   key → node\nmembership: gossip.Network   who is alive\n```\n"
	r := renderMarkdown(src, nil, "")
	if !strings.Contains(r.HTML, `class="diagram"`) {
		t.Fatalf("labelled composition fence was not promoted to a diagram:\n%s", r.HTML)
	}
	if got := strings.Count(r.HTML, "diagram-node"); got < 3 {
		t.Errorf("got %d diagram nodes, want at least 3", got)
	}
	if !strings.Contains(r.HTML, "hashring.Ring") || !strings.Contains(r.HTML, "gossip.Network") {
		t.Errorf("components missing from diagram:\n%s", r.HTML)
	}
}

func TestPipelineFencesChainAcrossArrowContinuations(t *testing.T) {
	body := "request → ratelimit.Bucket   (admission)\n        → queue.Queue        (bounded wait)\n"
	rows := parseDiagram(body)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (arrow lines continue the chain)", len(rows))
	}
	if got := len(rows[0].Nodes); got != 3 {
		t.Fatalf("got %d nodes, want 3: %+v", got, rows[0])
	}
	if rows[0].Nodes[1].Main != "ratelimit.Bucket" || rows[0].Nodes[1].Note != "admission" {
		t.Errorf("node = %+v, want main ratelimit.Bucket note admission", rows[0].Nodes[1])
	}
}

func TestPlainFencesStayVerbatimCode(t *testing.T) {
	r := renderMarkdown("```\nadr/            the decision record\n```\n", nil, "")
	if strings.Contains(r.HTML, "diagram") {
		t.Errorf("a plain tree fence became a diagram:\n%s", r.HTML)
	}
	if !strings.Contains(r.HTML, "<pre") || !strings.Contains(r.HTML, "adr/") {
		t.Errorf("plain fence lost its code block:\n%s", r.HTML)
	}
}

func TestFencedGoCodeIsHighlightedAndEscaped(t *testing.T) {
	src := "```go\nfunc f() { x := g() } // <tag>\n```\n"
	r := renderMarkdown(src, nil, "")
	for _, want := range []string{"tok-k", "tok-f", "tok-c", "&lt;tag&gt;"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("missing %q in:\n%s", want, r.HTML)
		}
	}
	if strings.Contains(r.HTML, "<tag>") {
		t.Error("code content was not escaped")
	}
}

// TestEveryGeneratedLinkTargetsAnExistingAnchor builds the real site
// from the repository's markdown and proves no internal link dangles.
func TestEveryGeneratedLinkTargetsAnExistingAnchor(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	page = pageBody(page)
	ids := map[string]bool{}
	for _, m := range idRe.FindAllStringSubmatch(page, -1) {
		ids[m[1]] = true
	}
	checked := 0
	for _, m := range hrefRe.FindAllStringSubmatch(page, -1) {
		checked++
		if !ids[m[1]] {
			t.Errorf("href #%s has no matching anchor", m[1])
		}
	}
	if checked < 50 {
		t.Errorf("only %d internal links checked; the resolver probably failed", checked)
	}
	if n := strings.Count(page, `href="/`); n > 0 {
		t.Errorf("%d root-relative links leaked through the resolver", n)
	}
}

// TestGeneratedPageHasUniqueElementIDs guards the anchors the
// scrollspy, search palette and deep links all depend on.
func TestGeneratedPageHasUniqueElementIDs(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	page = pageBody(page)
	seen := map[string]bool{}
	for _, m := range idRe.FindAllStringSubmatch(page, -1) {
		if seen[m[1]] {
			t.Errorf("duplicate id %q", m[1])
		}
		seen[m[1]] = true
	}
	for _, want := range []string{"search", "sidebar", "toc-body", "chips", "help", "player", "p-play", "listen-toggle", "p-voice", "p-voice-test", "filter-count"} {
		if !seen[want] {
			t.Errorf("page element #%s missing", want)
		}
	}
}

// TestEveryManifestDocumentIsRendered checks all manifest documents made it
// into the page with their category filter attached.
func TestEveryManifestDocumentIsRendered(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	page = pageBody(page)
	for _, spec := range manifest {
		id := docID(spec.Path)
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("document %s (section %s) missing from the page", spec.Path, id)
		}
	}
	if n := strings.Count(page, `class="doc"`); n != len(manifest) {
		t.Errorf("rendered %d document sections, want %d", n, len(manifest))
	}
}

func TestReaderStyleSelectorAndThemesAreRendered(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`id="reader-style"`,
		`<option value="current">Current</option>`,
		`<option value="cold">Cold</option>`,
		`<option value="hacker">Hacker</option>`,
		`<option value="retro">Retro</option>`,
		`<option value="cli">CLI</option>`,
		`<option value="danger">Danger</option>`,
		`<option value="red-hacker">Red Hacker</option>`,
		`html[data-reader-style="cold"][data-theme="dark"]`,
		`html[data-reader-style="hacker"][data-theme="light"]`,
		`html[data-reader-style="cli"][data-theme="dark"]`,
		`html[data-reader-style="danger"][data-theme="light"]`,
		`html[data-reader-style="red-hacker"][data-theme="dark"]`,
		`guide-reader-style`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("generated site is missing reader style feature %q", want)
		}
	}
}

// TestWhyParagraphsRenderAsMotivationCallouts checks the renderer tags
// a pattern's motivation paragraph and leaves ordinary prose alone.
func TestWhyParagraphsRenderAsMotivationCallouts(t *testing.T) {
	why := renderMarkdown("**Why.** It races without one.\n", nil, "")
	if !strings.Contains(why.HTML, `<p class="why">`) {
		t.Errorf("why paragraph not tagged:\n%s", why.HTML)
	}
	plain := renderMarkdown("**Intent.** Exactly one instance.\n", nil, "")
	if strings.Contains(plain.HTML, `class="why"`) {
		t.Errorf("non-why paragraph was tagged:\n%s", plain.HTML)
	}
}

// TestEveryPatternEntryExplainsWhyItIsUsed builds the real site and
// asserts every numbered pattern entry carries a motivation callout.
func TestEveryPatternEntryExplainsWhyItIsUsed(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	const patterns = 28
	if n := strings.Count(pageBody(page), `<p class="why">`); n < patterns {
		t.Errorf("got %d motivation callouts, want at least %d (one per pattern)", n, patterns)
	}
}

// TestPatternSectionsCarryViewSourcePanels asserts every numbered
// pattern entry embeds its real package source, spliced directly
// under the heading.
func TestPatternSectionsCarryViewSourcePanels(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	body := pageBody(page)
	const patterns = 28
	if n := strings.Count(body, `<details class="source-panel">`); n < patterns {
		t.Errorf("got %d view-source panels, want at least %d (one per pattern)", n, patterns)
	}
	if !strings.Contains(body, "</h2>\n<details class=\"source-panel\">") {
		t.Error("no view-source panel directly under its heading")
	}
	if !strings.Contains(body, `<code>creational/singleton/</code>`) {
		t.Error("singleton package panel label missing")
	}
	// The panel embeds real source, so a symbol only the implementation
	// has must appear somewhere in the page.
	if !strings.Contains(body, `language-go`) {
		t.Error("panel source not highlighted")
	}
}

func TestCompanionAndCompilerSectionsCarrySourcePanels(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	body := pageBody(page)
	for _, dir := range []string{
		"concepts/closures/",
		"mistakes/loopvar/",
		"internals/escape/",
		"sdesign/lru/",
		"distributed/raft/",
		"compilers/minilang/",
	} {
		if !strings.Contains(body, `<code>`+dir+`</code>`) {
			t.Errorf("source panel for %s missing from generated page", dir)
		}
	}
	if !strings.Contains(body, `<span class="source-file-name">legacy/legacy.go</span>`) {
		t.Error("nested adapter package source missing from generated page")
	}
}

// TestEveryPatternEntryHasAWalkthrough asserts every numbered pattern
// entry ends with a worked example before its proving tests.
func TestEveryPatternEntryHasAWalkthrough(t *testing.T) {
	page, err := buildSite("../..", fixTime)
	if err != nil {
		t.Fatal(err)
	}
	const patterns = 28
	if n := strings.Count(pageBody(page), `<strong>Walkthrough.</strong>`); n < patterns {
		t.Errorf("got %d walkthroughs, want at least %d (one per pattern)", n, patterns)
	}
}
