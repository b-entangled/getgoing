package command

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInsertDoUndo(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("ab")
	c := NewInsertText(doc, 1, "XY")

	if err := c.Do(ctx); err != nil {
		t.Fatal(err)
	}
	if got := doc.String(); got != "aXYb" {
		t.Fatalf("got %q", got)
	}
	if err := c.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	if got := doc.String(); got != "ab" {
		t.Fatalf("undo failed: %q", got)
	}
}

func TestDeleteDoUndoRestoresExactly(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("hello world")
	c := NewDeleteAt(doc, 6, 5)

	if err := c.Do(ctx); err != nil {
		t.Fatal(err)
	}
	if got := doc.String(); got != "hello " {
		t.Fatalf("got %q", got)
	}
	if err := c.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	if got := doc.String(); got != "hello world" {
		t.Fatalf("undo failed: %q", got)
	}
}

func TestUndoBeforeDoErrors(t *testing.T) {
	c := NewDeleteAt(NewDoc("x"), 0, 1)
	if err := c.Undo(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestReplaceDoUndo(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("the quick brown")
	c := NewReplace(doc, 4, "quick", "slow")

	if err := c.Do(ctx); err != nil {
		t.Fatal(err)
	}
	if got := doc.String(); got != "the slow brown" {
		t.Fatalf("got %q", got)
	}
	if err := c.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	if got := doc.String(); got != "the quick brown" {
		t.Fatalf("undo failed: %q", got)
	}
}

func TestCommandDoesNotExecuteOnConstruction(t *testing.T) {
	doc := NewDoc("seed")
	_ = NewInsertText(doc, 0, "MUTATION")
	if got := doc.String(); got != "seed" {
		t.Fatalf("construction must not mutate the receiver: %q", got)
	}
}

// The invoker is generic: it manages commands without knowing what they do.
func TestHistoryUndoRedoCycle(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("")
	h := NewHistory(0)

	steps := []Command{
		NewInsertText(doc, 0, "Hello"),
		NewInsertText(doc, 5, " World"),
		NewReplace(doc, 5, " World", "!"),
	}
	for _, c := range steps {
		if err := h.Run(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if doc.String() != "Hello!" {
		t.Fatalf("got %q", doc.String())
	}
	if h.Undoable() != 3 || h.Redoable() != 0 {
		t.Fatalf("undo=%d redo=%d", h.Undoable(), h.Redoable())
	}

	// Undo everything.
	for h.Undoable() > 0 {
		if err := h.Undo(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if doc.String() != "" {
		t.Fatalf("full undo should empty the doc, got %q", doc.String())
	}
	if h.Redoable() != 3 {
		t.Fatalf("redo=%d", h.Redoable())
	}

	// Redo everything.
	for h.Redoable() > 0 {
		if err := h.Redo(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if doc.String() != "Hello!" {
		t.Fatalf("full redo got %q", doc.String())
	}
}

func TestNewActionClearsRedoStack(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("")
	h := NewHistory(0)

	_ = h.Run(ctx, NewInsertText(doc, 0, "one"))
	_ = h.Undo(ctx)
	if h.Redoable() != 1 {
		t.Fatal("setup")
	}

	// Branching: a new command makes the undone step unreachable.
	_ = h.Run(ctx, NewInsertText(doc, 0, "two"))
	if h.Redoable() != 0 {
		t.Fatalf("redo stack must be cleared, got %d", h.Redoable())
	}
	if doc.String() != "two" {
		t.Fatalf("got %q", doc.String())
	}
}

func TestFailedCommandIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("ab")
	h := NewHistory(0)

	// Index out of range → Do fails → no undo entry.
	err := h.Run(ctx, NewInsertText(doc, 99, "zz"))
	if err == nil {
		t.Fatal("expected error")
	}
	if h.Undoable() != 0 {
		t.Fatal("failed command must not enter the undo stack")
	}
	if doc.String() != "ab" {
		t.Fatalf("document should be unchanged, got %q", doc.String())
	}
}

func TestUndoOnEmptyHistoryIsNoop(t *testing.T) {
	h := NewHistory(0)
	if err := h.Undo(context.Background()); err != nil {
		t.Fatalf("empty undo must be a no-op, got %v", err)
	}
	if err := h.Redo(context.Background()); err != nil {
		t.Fatalf("empty redo must be a no-op, got %v", err)
	}
}

func TestHistoryCapDropsOldestSteps(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("")
	h := NewHistory(2)

	for i, s := range []string{"a", "b", "c", "d"} {
		if err := h.Run(ctx, NewInsertText(doc, i, s)); err != nil {
			t.Fatal(err)
		}
	}
	if h.Undoable() != 2 {
		t.Fatalf("cap not enforced: %d", h.Undoable())
	}
	// Undoing twice reaches "ab"; the earliest two steps are unreachable.
	_ = h.Undo(ctx)
	_ = h.Undo(ctx)
	if doc.String() != "ab" {
		t.Fatalf("got %q", doc.String())
	}
	if h.Undoable() != 0 {
		t.Fatalf("got %d", h.Undoable())
	}
}

func TestCancellationBlocksExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doc := NewDoc("x")
	if err := NewInsertText(doc, 0, "y").Do(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if doc.String() != "x" {
		t.Fatal("cancelled command must not mutate the receiver")
	}
}

func TestAuditLog(t *testing.T) {
	ctx := context.Background()
	doc := NewDoc("")
	h := NewHistory(0)
	_ = h.Run(ctx, NewInsertText(doc, 0, "a"))
	_ = h.Undo(ctx)
	_ = h.Redo(ctx)
	got := h.AuditString()
	if got != "do:insert -> undo:insert -> redo:insert" {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(h.String(doc), "undo=1") {
		t.Fatalf("got %s", h.String(doc))
	}
}

// --- queue without undo ---

func TestQueueRunsFIFO(t *testing.T) {
	var order []string
	q := &Queue{}
	q.Push(func(context.Context) error { order = append(order, "1"); return nil })
	q.Push(func(context.Context) error { order = append(order, "2"); return nil })
	q.Push(func(context.Context) error { order = append(order, "3"); return nil })

	if q.Len() != 3 {
		t.Fatalf("len=%d", q.Len())
	}
	if err := q.RunAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "1,2,3" {
		t.Fatalf("got %v", order)
	}
	if q.Len() != 0 {
		t.Fatal("queue should be drained")
	}
}

func TestQueueStopsOnError(t *testing.T) {
	var order []string
	boom := errors.New("boom")
	q := &Queue{}
	q.Push(func(context.Context) error { order = append(order, "1"); return nil })
	q.Push(func(context.Context) error { return boom })
	q.Push(func(context.Context) error { order = append(order, "3"); return nil })

	err := q.RunAll(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if strings.Join(order, ",") != "1" {
		t.Fatalf("later jobs must not run: %v", order)
	}
}
