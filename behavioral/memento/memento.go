// Package memento demonstrates the Memento pattern.
//
// Memento captures an object's state so it can be restored later, without
// exposing that state to the caretaker. Go gets encapsulation for free here:
// the Snapshot type has *unexported* fields, so a package that stores
// snapshots (the caretaker) genuinely cannot read or mutate them — the
// compiler enforces what GoF had to police with convention.
//
// Domain: a vector drawing editor with an undo/redo stack.
package memento

import (
	"errors"
	"fmt"
	"sort"
)

// ShapeKind enumerates the drawable primitives.
type ShapeKind string

const (
	Rect   ShapeKind = "rect"
	Line   ShapeKind = "line"
	Circle ShapeKind = "circle"
)

// Shape is a drawable primitive. All fields are values (no pointers or
// slices) so a shallow copy is already a deep copy — this is what makes
// snapshots cheap.
type Shape struct {
	Kind   ShapeKind
	X, Y   int
	W, H   int
	Label  string
	Filled bool
}

// Drawing is the originator: the object whose state is snapshotted.
// Its fields are unexported, so the only way to observe the state is
// through its methods and the only way to restore it is Restore.
type Drawing struct {
	shapes []Shape
	name   string
	rev    int
}

// NewDrawing creates an empty, named drawing.
func NewDrawing(name string) *Drawing { return &Drawing{name: name} }

// Add appends a shape and bumps the revision.
func (d *Drawing) Add(s Shape) {
	d.shapes = append(d.shapes, s)
	d.rev++
}

// Remove deletes the shape at index i.
func (d *Drawing) Remove(i int) error {
	if i < 0 || i >= len(d.shapes) {
		return fmt.Errorf("memento: index %d out of range", i)
	}
	d.shapes = append(d.shapes[:i], d.shapes[i+1:]...)
	d.rev++
	return nil
}

// Move translates a shape.
func (d *Drawing) Move(i, dx, dy int) error {
	if i < 0 || i >= len(d.shapes) {
		return fmt.Errorf("memento: index %d out of range", i)
	}
	d.shapes[i].X += dx
	d.shapes[i].Y += dy
	d.rev++
	return nil
}

// Shapes returns a copy of the shape list.
func (d *Drawing) Shapes() []Shape { return append([]Shape(nil), d.shapes...) }

// Name returns the drawing's name.
func (d *Drawing) Name() string { return d.name }

// Rev returns the revision counter — increments on every mutation, so a
// test can prove a restore actually rewound it.
func (d *Drawing) Rev() int { return d.rev }

// Describe renders the drawing for display.
func (d *Drawing) Describe() string {
	sorted := d.Shapes()
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Label != sorted[j].Label {
			return sorted[i].Label < sorted[j].Label
		}
		return sorted[i].X < sorted[j].X
	})
	out := d.name + ":"
	for _, s := range sorted {
		out += fmt.Sprintf(" %s(%s@%d,%d)", s.Kind, s.Label, s.X, s.Y)
	}
	return out
}

// --- the memento ---

// Snapshot is the memento. Every field is unexported: outside this package
// you may *hold* a Snapshot (the caretaker stores thousands of them) but you
// may not inspect, construct, or modify one. Encapsulation by compiler.
type Snapshot struct {
	shapes []Shape
	name   string
	rev    int
}

// Snapshot captures the current state as an independent copy. Later
// mutations of the drawing cannot be observed through the snapshot.
func (d *Drawing) Snapshot() Snapshot {
	cp := make([]Shape, len(d.shapes))
	copy(cp, d.shapes)
	return Snapshot{shapes: cp, name: d.name, rev: d.rev}
}

// Restore replaces the drawing's entire state with a snapshot's, again by
// copy, so the snapshot remains reusable (redo needs to restore twice).
func (d *Drawing) Restore(s Snapshot) {
	cp := make([]Shape, len(s.shapes))
	copy(cp, s.shapes)
	d.shapes = cp
	d.name = s.name
	d.rev = s.rev
}

// Rev exposes the snapshot's revision for diagnostics.
func (s Snapshot) Rev() int { return s.rev }

// Size reports how many shapes the snapshot holds.
func (s Snapshot) Size() int { return len(s.shapes) }

// --- the caretaker ---

// ErrEmptyStack is returned when there is nothing to undo or redo.
var ErrEmptyStack = errors.New("memento: nothing to restore")

// Editor is the caretaker: it keeps history without knowing what a shape
// is, what Field one has, or how state is represented. It only ever calls
// Snapshot and Restore.
type Editor struct {
	drawing *Drawing
	undo    []Snapshot
	redo    []Snapshot
	limit   int
	events  []string
}

// NewEditor wires a drawing to a history with a maximum depth.
// A limit of 0 means unlimited.
func NewEditor(d *Drawing, limit int) *Editor {
	return &Editor{drawing: d, limit: limit}
}

// Drawing exposes the originator (read-oriented operations only).
func (e *Editor) Drawing() *Drawing { return e.drawing }

// apply is the single funnel for every mutation: take a snapshot *before*
// changing anything, record the action, clear redo. Getting this order
// wrong is the classic undo bug.
func (e *Editor) apply(action string, mutate func() error) error {
	before := e.drawing.Snapshot()
	if err := mutate(); err != nil {
		return err // failed mutation: discard the pre-snapshot
	}
	e.undo = append(e.undo, before)
	e.redo = e.redo[:0]
	e.events = append(e.events, action)
	e.trim()
	return nil
}

// Add records a new shape.
func (e *Editor) Add(s Shape) error {
	return e.apply("add "+s.Label, func() error {
		e.drawing.Add(s)
		return nil
	})
}

// Erase removes a shape by index.
func (e *Editor) Erase(i int) error {
	return e.apply("erase", func() error { return e.drawing.Remove(i) })
}

// Move translates a shape.
func (e *Editor) Move(i, dx, dy int) error {
	return e.apply("move", func() error { return e.drawing.Move(i, dx, dy) })
}

// Undo rewinds one step.
func (e *Editor) Undo() error {
	if len(e.undo) == 0 {
		return ErrEmptyStack
	}
	// Capture the *current* state so redo can come back to it.
	current := e.drawing.Snapshot()
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.redo = append(e.redo, current)
	e.drawing.Restore(s)
	e.events = append(e.events, "undo")
	return nil
}

// Redo re-applies the most recently undone step.
func (e *Editor) Redo() error {
	if len(e.redo) == 0 {
		return ErrEmptyStack
	}
	current := e.drawing.Snapshot()
	s := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.undo = append(e.undo, current)
	e.drawing.Restore(s)
	e.events = append(e.events, "redo")
	return nil
}

// CanUndo reports whether an undo step exists.
func (e *Editor) CanUndo() bool { return len(e.undo) > 0 }

// CanRedo reports whether a redo step exists.
func (e *Editor) CanRedo() bool { return len(e.redo) > 0 }

// Events returns the action log.
func (e *Editor) Events() []string { return append([]string(nil), e.events...) }

// trim enforces the history depth by dropping the oldest snapshots.
func (e *Editor) trim() {
	if e.limit <= 0 || len(e.undo) <= e.limit {
		return
	}
	drop := len(e.undo) - e.limit
	e.undo = append([]Snapshot(nil), e.undo[drop:]...)
}
