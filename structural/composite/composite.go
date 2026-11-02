// Package composite demonstrates the Composite pattern.
//
// Composite lets clients treat individual objects and groups uniformly.
// In Go: an interface (the Component), leaf structs, and a container struct
// that holds a slice of that interface and recurses over it.
//
// Two shapes are shown: a filesystem tree (recursive aggregation) and a
// dashboard of widgets (heterogeneous renderables), so the pattern is not
// mistaken for "only useful for trees of files".
package composite

import (
	"fmt"
	"io"
	"strings"
)

// Entry is the Component interface: every node, leaf or group, exposes the
// same small surface.
type Entry interface {
	Name() string
	Size() int64
	Render(w io.Writer, depth int) error
}

// --- Leaf ---

// File is a Leaf: it has a size and no children.
type File struct {
	name string
	size int64
}

// NewFile constructs a leaf.
func NewFile(name string, size int64) *File { return &File{name: name, size: size} }

// Name returns the file name.
func (f *File) Name() string { return f.name }

// Size returns the byte count.
func (f *File) Size() int64 { return f.size }

// Render writes an indented line.
func (f *File) Render(w io.Writer, depth int) error {
	_, err := fmt.Fprintf(w, "%s%s (%d)\n", indent(depth), f.name, f.size)
	return err
}

// --- Composite ---

// Dir is a Composite: it holds children and aggregates their sizes.
// Pointer receiver throughout, so the whole component set is consistent
// and a *Dir never gets copied by accident.
type Dir struct {
	name     string
	children []Entry
}

// NewDir constructs an empty directory.
func NewDir(name string) *Dir { return &Dir{name: name} }

// Add appends children and returns the receiver for chaining.
func (d *Dir) Add(entries ...Entry) *Dir {
	d.children = append(d.children, entries...)
	return d
}

// Name returns the directory name.
func (d *Dir) Name() string { return d.name }

// Size aggregates over children — the essence of Composite. The client does
// not know (or care) whether it is summing a file or a subtree.
func (d *Dir) Size() int64 {
	var total int64
	for _, c := range d.children {
		total += c.Size()
	}
	return total
}

// Render writes this directory then recurses into children.
func (d *Dir) Render(w io.Writer, depth int) error {
	if _, err := fmt.Fprintf(w, "%s%s/\n", indent(depth), d.name); err != nil {
		return err
	}
	for _, c := range d.children {
		if err := c.Render(w, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// Children returns a copy of the child list.
func (d *Dir) Children() []Entry { return append([]Entry(nil), d.children...) }

// Compile-time proof that the composite satisfies the component interface.
var (
	_ Entry = (*File)(nil)
	_ Entry = (*Dir)(nil)
)

// Find walks the tree and returns every entry whose name matches, in
// depth-first order. It is written once against Entry and works for both
// leaves and subtrees — which is the whole benefit of the pattern.
func Find(root Entry, match string) []Entry {
	var out []Entry
	var walk func(Entry)
	walk = func(e Entry) {
		if strings.Contains(e.Name(), match) {
			out = append(out, e)
		}
		if d, ok := e.(*Dir); ok {
			for _, c := range d.Children() {
				walk(c)
			}
		}
	}
	walk(root)
	return out
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

// =====================================================================
// Second shape: a heterogeneous widget tree
// =====================================================================

// Widget is a second Component interface for a different domain, showing
// Composite is not filesystem-specific.
type Widget interface {
	// ID returns a stable identifier.
	ID() string
	// Measure returns the widget's intrinsic width.
	Measure() int
	// Render draws the widget and its children into the builder.
	Render(b *strings.Builder, indent string)
}

// Label is a leaf widget.
type Label struct {
	id    string
	text  string
	width int
}

// NewLabel builds a label widget sized to its text.
func NewLabel(id, text string) *Label {
	return &Label{id: id, text: text, width: len(text)}
}

// ID returns the widget id.
func (l *Label) ID() string { return l.id }

// Measure returns the text width.
func (l *Label) Measure() int { return l.width }

// Render writes the label text.
func (l *Label) Render(b *strings.Builder, ind string) {
	fmt.Fprintf(b, "%sLabel(%s): %s\n", ind, l.id, l.text)
}

// Panel is a composite widget with a border and a stack of children.
type Panel struct {
	id       string
	children []Widget
}

// NewPanel builds an empty panel.
func NewPanel(id string) *Panel { return &Panel{id: id} }

// Add appends widgets and returns the receiver.
func (p *Panel) Add(ws ...Widget) *Panel {
	p.children = append(p.children, ws...)
	return p
}

// ID returns the widget id.
func (p *Panel) ID() string { return p.id }

// Measure is the widest child plus padding — aggregates like Dir.Size.
func (p *Panel) Measure() int {
	max := 0
	for _, c := range p.children {
		if m := c.Measure(); m > max {
			max = m
		}
	}
	if max == 0 {
		return 0
	}
	return max + 4 // border padding
}

// Render draws the panel then recurses into children.
func (p *Panel) Render(b *strings.Builder, ind string) {
	fmt.Fprintf(b, "%sPanel(%s) w=%d\n", ind, p.id, p.Measure())
	for _, c := range p.children {
		c.Render(b, ind+"  ")
	}
}

// MeasureAll is written against Widget and therefore works for any mix of
// labels and panels — the client-side payoff.
func MeasureAll(w Widget) int { return w.Measure() }

var (
	_ Widget = (*Label)(nil)
	_ Widget = (*Panel)(nil)
)
