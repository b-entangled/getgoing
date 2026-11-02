package memento

import (
	"errors"
	"strings"
	"testing"
)

func newEditor(limit int) *Editor {
	return NewEditor(NewDrawing("logo"), limit)
}

func TestSnapshotIsIndependentOfLaterEdits(t *testing.T) {
	d := NewDrawing("d")
	d.Add(Shape{Kind: Rect, X: 1, Y: 1, Label: "r"})

	s := d.Snapshot()
	if s.Size() != 1 || s.Rev() != 1 {
		t.Fatalf("size=%d rev=%d", s.Size(), s.Rev())
	}

	d.Add(Shape{Kind: Circle, X: 9, Y: 9, Label: "c"})
	d.Move(0, 100, 100) // mutate the original shape too

	if s.Size() != 1 {
		t.Fatalf("snapshot grew with the drawing: %d", s.Size())
	}
	if d.Rev() != 3 {
		t.Fatalf("revision should have advanced, got %d", d.Rev())
	}
	if s.Rev() != 1 {
		t.Fatalf("snapshot revision changed: %d", s.Rev())
	}
}

func TestRestoreIsRepeatable(t *testing.T) {
	d := NewDrawing("d")
	d.Add(Shape{Label: "a"})
	s := d.Snapshot()

	d.Add(Shape{Label: "b"})
	d.Add(Shape{Label: "c"})

	d.Restore(s)
	if len(d.Shapes()) != 1 {
		t.Fatalf("restore once failed: %d", len(d.Shapes()))
	}
	// Restoring the same snapshot twice must give the same result (redo
	// restores the same memento repeatedly).
	d.Add(Shape{Label: "x"})
	d.Restore(s)
	if len(d.Shapes()) != 1 || d.Shapes()[0].Label != "a" {
		t.Fatalf("second restore differs: %+v", d.Shapes())
	}
}

func TestSnapshotCopiesTheShapeSlice(t *testing.T) {
	d := NewDrawing("d")
	d.Add(Shape{Label: "orig"})
	s := d.Snapshot()

	d.Remove(0)
	d.Add(Shape{Label: "replacement"})

	if s.Size() != 1 {
		t.Fatalf("snapshot corrupted: %d", s.Size())
	}
	restored := NewDrawing("r")
	restored.Restore(s)
	if got := restored.Shapes()[0].Label; got != "orig" {
		t.Fatalf("got %q", got)
	}
}

// The compiler enforces encapsulation: this test lives in-package, so it
// can reach the fields; the comment documents what an external package
// would be unable to do.
func TestCaretakerCannotForgeSnapshots(t *testing.T) {
	// From another package, `memento.Snapshot{}` would fail to compile:
	// "unknown field shapes" / "cannot refer to unexported field".
	// The caretaker's only source of snapshots is Snapshot().
	e := newEditor(10)
	if err := e.Add(Shape{Label: "a"}); err != nil {
		t.Fatal(err)
	}
	if !e.CanUndo() {
		t.Fatal("add must create an undo step")
	}
}

func TestUndoRestoresPreviousState(t *testing.T) {
	e := newEditor(10)
	ctx := e.Drawing()

	_ = e.Add(Shape{Kind: Rect, X: 0, Y: 0, Label: "a"})
	_ = e.Add(Shape{Kind: Line, X: 5, Y: 5, Label: "b"})
	before := ctx.Describe()

	_ = e.Move(0, 50, 50)
	after := ctx.Describe()
	if before == after {
		t.Fatal("move did not change the drawing")
	}

	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := ctx.Describe(); got != before {
		t.Fatalf("undo did not restore:\nwant %s\ngot  %s", before, got)
	}
}

func TestRedoReapplies(t *testing.T) {
	e := newEditor(10)
	_ = e.Add(Shape{Label: "a"})
	stateAfterAdd := e.Drawing().Describe()

	_ = e.Add(Shape{Label: "b"})
	_ = e.Undo()
	if e.Drawing().Describe() != stateAfterAdd {
		t.Fatal("undo failed")
	}
	if err := e.Redo(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Drawing().Describe(), "b") {
		t.Fatalf("redo did not reapply: %s", e.Drawing().Describe())
	}
}

func TestUndoRedoRoundTripIsLossless(t *testing.T) {
	e := newEditor(0)
	_ = e.Add(Shape{Kind: Rect, X: 0, Y: 0, Label: "a"})
	_ = e.Add(Shape{Kind: Circle, X: 1, Y: 1, Label: "b"})
	_ = e.Move(1, 3, 4)
	full := e.Drawing().Describe()

	_ = e.Undo()
	_ = e.Undo()
	_ = e.Undo()
	if e.Drawing().Describe() != "logo:" {
		t.Fatalf("expected empty drawing, got %s", e.Drawing().Describe())
	}

	_ = e.Redo()
	_ = e.Redo()
	_ = e.Redo()
	if e.Drawing().Describe() != full {
		t.Fatalf("round trip lost state:\nwant %s\ngot  %s", full, e.Drawing().Describe())
	}
}

func TestUndoOnEmptyStack(t *testing.T) {
	e := newEditor(0)
	if err := e.Undo(); !errors.Is(err, ErrEmptyStack) {
		t.Fatalf("want ErrEmptyStack, got %v", err)
	}
	if err := e.Redo(); !errors.Is(err, ErrEmptyStack) {
		t.Fatalf("want ErrEmptyStack, got %v", err)
	}
}

func TestNewMutationClearsRedo(t *testing.T) {
	e := newEditor(0)
	_ = e.Add(Shape{Label: "a"})
	_ = e.Undo()
	if !e.CanRedo() {
		t.Fatal("setup")
	}
	_ = e.Add(Shape{Label: "branch"})
	if e.CanRedo() {
		t.Fatal("redo stack must be cleared by a new action")
	}
}

func TestFailedMutationDoesNotPolluteHistory(t *testing.T) {
	e := newEditor(0)
	_ = e.Add(Shape{Label: "a"})
	steps := len(e.Events())

	// Index out of range → mutate fails → no snapshot recorded.
	if err := e.Erase(42); err == nil {
		t.Fatal("expected error")
	}
	if len(e.Events()) != steps {
		t.Fatalf("failed action was recorded: %v", e.Events())
	}
	if len(e.Drawing().Shapes()) != 1 {
		t.Fatal("drawing changed on a failed action")
	}
}

func TestHistoryLimitDropsOldestSnapshots(t *testing.T) {
	e := newEditor(2)
	_ = e.Add(Shape{Label: "a"})
	_ = e.Add(Shape{Label: "b"})
	_ = e.Add(Shape{Label: "c"})
	_ = e.Add(Shape{Label: "d"})

	// Only 2 undos are available; the first two actions are unreachable.
	undone := 0
	for e.CanUndo() {
		if err := e.Undo(); err != nil {
			t.Fatal(err)
		}
		undone++
	}
	if undone != 2 {
		t.Fatalf("want 2 undoable steps, got %d", undone)
	}
	if !strings.Contains(e.Drawing().Describe(), "a") {
		t.Fatalf("expected the first shape to survive the trim: %s", e.Drawing().Describe())
	}
}

func TestEraseThenUndoRestoresShapeInPlace(t *testing.T) {
	e := newEditor(0)
	_ = e.Add(Shape{Kind: Rect, Label: "keep"})
	_ = e.Add(Shape{Kind: Line, Label: "gone"})
	_ = e.Add(Shape{Kind: Circle, Label: "also"})

	// Erase the middle one.
	if err := e.Erase(1); err != nil {
		t.Fatal(err)
	}
	if len(e.Drawing().Shapes()) != 2 {
		t.Fatalf("erase failed: %+v", e.Drawing().Shapes())
	}
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	got := e.Drawing().Shapes()
	if len(got) != 3 || got[1].Label != "gone" {
		t.Fatalf("undo did not restore in position: %+v", got)
	}
}

func TestEventLogRecordsActions(t *testing.T) {
	e := newEditor(0)
	_ = e.Add(Shape{Label: "a"})
	_ = e.Undo()
	_ = e.Redo()
	want := "add a -> undo -> redo"
	if strings.Join(e.Events(), " -> ") != want {
		t.Fatalf("got %s", strings.Join(e.Events(), " -> "))
	}
}
