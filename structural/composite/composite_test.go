package composite

import (
	"bytes"
	"strings"
	"testing"
)

func tree() *Dir {
	return NewDir("root").
		Add(
			NewDir("src").
				Add(NewFile("main.go", 120), NewFile("util.go", 80)),
			NewDir("docs").
				Add(NewFile("readme.md", 40)),
			NewFile("go.mod", 20),
		)
}

func TestLeafSize(t *testing.T) {
	f := NewFile("a", 10)
	if f.Size() != 10 {
		t.Fatalf("got %d", f.Size())
	}
}

func TestCompositeAggregates(t *testing.T) {
	root := tree()
	// 120+80 + 40 + 20 = 260
	if got := root.Size(); got != 260 {
		t.Fatalf("want 260, got %d", got)
	}
}

func TestNestedSubtreeSize(t *testing.T) {
	src := NewDir("src").Add(NewFile("a", 1), NewFile("b", 2))
	if src.Size() != 3 {
		t.Fatalf("want 3, got %d", src.Size())
	}
	root := NewDir("r").Add(src, NewFile("c", 4))
	if root.Size() != 7 {
		t.Fatalf("want 7, got %d", root.Size())
	}
}

// The client treats leaves and composites identically.
func TestClientIsUniform(t *testing.T) {
	var entries = []Entry{NewFile("leaf", 5), NewDir("sub").Add(NewFile("x", 5))}
	var total int64
	for _, e := range entries {
		total += e.Size() // no type switch needed
	}
	if total != 10 {
		t.Fatalf("got %d", total)
	}
}

func TestRenderTree(t *testing.T) {
	var buf bytes.Buffer
	if err := tree().Render(&buf, 0); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := `root/
  src/
    main.go (120)
    util.go (80)
  docs/
    readme.md (40)
  go.mod (20)
`
	if got != want {
		t.Fatalf("\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestFindWalksBothLeavesAndSubtrees(t *testing.T) {
	root := tree()
	got := Find(root, "main")
	if len(got) != 1 || got[0].Name() != "main.go" {
		t.Fatalf("got %v", got)
	}
	if got := Find(root, "src"); len(got) != 1 {
		t.Fatalf("dir itself should match, got %v", got)
	}
	if got := Find(root, ".go"); len(got) != 2 {
		t.Fatalf("want 2 go files, got %v", got)
	}
}

func TestChildrenReturnsCopy(t *testing.T) {
	d := NewDir("x").Add(NewFile("a", 1))
	kids := d.Children()
	if len(kids) != 1 {
		t.Fatal("setup")
	}
	kids[0] = NewFile("tampered", 9)
	if d.Children()[0].Name() != "a" {
		t.Fatal("Children leaked internal slice")
	}
}

func TestEmptyDirSizeZero(t *testing.T) {
	if got := NewDir("empty").Size(); got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestMixedDepthAggregation(t *testing.T) {
	deep := NewDir("a").
		Add(NewDir("b").
			Add(NewDir("c").
				Add(NewFile("deep", 42))))
	if deep.Size() != 42 {
		t.Fatalf("want 42, got %d", deep.Size())
	}
}

// --- widget shape ---

func TestPanelAggregatesWidth(t *testing.T) {
	p := NewPanel("root").
		Add(NewLabel("l1", "hello"), NewLabel("l2", "hi"))
	// widest child is "hello" (5) + 4 padding = 9
	if got := p.Measure(); got != 9 {
		t.Fatalf("want 9, got %d", got)
	}
}

func TestNestedPanelMeasure(t *testing.T) {
	inner := NewPanel("inner").Add(NewLabel("l", "abcdefgh")) // 8+4 = 12
	outer := NewPanel("outer").Add(inner)                     // 12+4 = 16
	if got := MeasureAll(outer); got != 16 {
		t.Fatalf("want 16, got %d", got)
	}
}

func TestWidgetRenderIsRecursive(t *testing.T) {
	var b strings.Builder
	p := NewPanel("root").Add(NewLabel("l", "hi"))
	p.Render(&b, "")
	out := b.String()
	if !strings.Contains(out, "Panel(root)") || !strings.Contains(out, "Label(l): hi") {
		t.Fatalf("bad render:\n%s", out)
	}
	if !strings.Contains(out, "  Label") {
		t.Fatalf("child should be indented:\n%s", out)
	}
}

func TestZeroMeasurePanel(t *testing.T) {
	if got := NewPanel("empty").Measure(); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}
