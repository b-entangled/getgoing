// Package command demonstrates the Command pattern.
//
// A Command object bundles an operation with everything needed to run and
// reverse it, so an invoker (an undo stack, a job queue, an audit log) can
// manage operations without understanding them.
//
// Go idiom notes:
//   - the interface has Do and Undo, both taking context so long-running
//     commands are cancellable;
//   - each concrete command stores its own reversal data — the invoker
//     stays generic;
//   - for "just queue this work" with no undo, a plain
//     func(context.Context) error is enough and no interface is needed
//     (see Runnable below).
package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Command is the interface the invoker manages.
type Command interface {
	// Do executes the command.
	Do(ctx context.Context) error
	// Undo reverses the effect of the last successful Do.
	Undo(ctx context.Context) error
	// Name identifies the command in logs and audit trails.
	Name() string
}

// Runnable is the "no undo needed" simplification.
type Runnable func(ctx context.Context) error

// --- the receiver ---

// Doc is the receiver: the thing commands actually manipulate.
// It is deliberately small and has no knowledge of commands.
type Doc struct {
	mu   sync.Mutex
	text []rune
}

// NewDoc builds a document from initial text.
func NewDoc(s string) *Doc { return &Doc{text: []rune(s)} }

// String returns the current content.
func (d *Doc) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.text)
}

// Len returns the rune count.
func (d *Doc) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.text)
}

// Insert splices runes in at an index.
func (d *Doc) Insert(at int, s string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at < 0 || at > len(d.text) {
		return fmt.Errorf("command: insert index %d out of range [0,%d]", at, len(d.text))
	}
	rs := []rune(s)
	d.text = append(d.text[:at], append(rs, d.text[at:]...)...)
	return nil
}

// Delete removes n runes at an index and returns what was removed, so a
// command can reverse the operation exactly.
func (d *Doc) Delete(at, n int) ([]rune, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at < 0 || at+n > len(d.text) {
		return nil, fmt.Errorf("command: delete range [%d,%d) out of range [0,%d)", at, at+n, len(d.text))
	}
	removed := append([]rune(nil), d.text[at:at+n]...)
	d.text = append(d.text[:at], d.text[at+n:]...)
	return removed, nil
}

// --- concrete commands ---

// InsertText inserts a string and remembers where, so Undo can delete it.
type InsertText struct {
	doc  *Doc
	at   int
	text string
}

// NewInsertText builds the command. It does not touch the document yet —
// construction and execution are separate, which is the point.
func NewInsertText(d *Doc, at int, s string) *InsertText {
	return &InsertText{doc: d, at: at, text: s}
}

// Name identifies the command.
func (c *InsertText) Name() string { return "insert" }

// Do splices the text in.
func (c *InsertText) Do(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.doc.Insert(c.at, c.text)
}

// Undo deletes exactly what was inserted.
func (c *InsertText) Undo(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.doc.Delete(c.at, len([]rune(c.text)))
	return err
}

// DeleteAt removes n runes and caches them for undo.
type DeleteAt struct {
	doc     *Doc
	at      int
	n       int
	removed []rune // populated by Do, consumed by Undo
}

// NewDeleteAt builds the command.
func NewDeleteAt(d *Doc, at, n int) *DeleteAt {
	return &DeleteAt{doc: d, at: at, n: n}
}

// Name identifies the command.
func (c *DeleteAt) Name() string { return "delete" }

// Do removes the runes and stores them.
func (c *DeleteAt) Do(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	removed, err := c.doc.Delete(c.at, c.n)
	if err != nil {
		return err
	}
	c.removed = removed
	return nil
}

// Undo re-inserts the cached runes.
func (c *DeleteAt) Undo(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.removed == nil {
		return errors.New("command: undo before do")
	}
	return c.doc.Insert(c.at, string(c.removed))
}

// Replace swaps a range for new text and caches both sides.
type Replace struct {
	doc     *Doc
	at      int
	old     string
	neu     string
	applied bool
}

// NewReplace builds the command.
func NewReplace(d *Doc, at int, oldText, newText string) *Replace {
	return &Replace{doc: d, at: at, old: oldText, neu: newText}
}

// Name identifies the command.
func (c *Replace) Name() string { return "replace" }

// Do verifies the expected text is present, then swaps it.
func (c *Replace) Do(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := c.doc.Delete(c.at, len([]rune(c.old))); err != nil {
		return err
	}
	if err := c.doc.Insert(c.at, c.neu); err != nil {
		// best-effort rollback so a failed command leaves no partial state
		_ = c.doc.Insert(c.at, c.old)
		return err
	}
	c.applied = true
	return nil
}

// Undo restores the original text.
func (c *Replace) Undo(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.applied {
		return errors.New("command: undo before do")
	}
	if _, err := c.doc.Delete(c.at, len([]rune(c.neu))); err != nil {
		return err
	}
	return c.doc.Insert(c.at, c.old)
}

// --- the invoker ---

// History is the invoker: it runs commands, maintains the undo/redo stacks,
// and knows nothing about documents, text, or indices.
type History struct {
	undo  []Command
	redo  []Command
	audit []string
	max   int
}

// NewHistory builds an invoker that keeps at most `max` undo steps
// (0 or negative means unlimited).
func NewHistory(max int) *History { return &History{max: max} }

// Run executes a command and, on success, records it for undo. Any new
// action invalidates the redo stack — redoing a branch that was abandoned
// would corrupt the document's history.
func (h *History) Run(ctx context.Context, c Command) error {
	if err := c.Do(ctx); err != nil {
		return fmt.Errorf("command %s: %w", c.Name(), err)
	}
	h.undo = append(h.undo, c)
	h.redo = h.redo[:0]
	h.audit = append(h.audit, "do:"+c.Name())
	h.trim()
	return nil
}

// Undo reverses the most recent command.
func (h *History) Undo(ctx context.Context) error {
	if len(h.undo) == 0 {
		return nil // no-op, matching a real editor's greyed-out Undo
	}
	c := h.undo[len(h.undo)-1]
	if err := c.Undo(ctx); err != nil {
		return fmt.Errorf("undo %s: %w", c.Name(), err)
	}
	h.undo = h.undo[:len(h.undo)-1]
	h.redo = append(h.redo, c)
	h.audit = append(h.audit, "undo:"+c.Name())
	return nil
}

// Redo re-applies the most recently undone command.
func (h *History) Redo(ctx context.Context) error {
	if len(h.redo) == 0 {
		return nil
	}
	c := h.redo[len(h.redo)-1]
	if err := c.Do(ctx); err != nil {
		return fmt.Errorf("redo %s: %w", c.Name(), err)
	}
	h.redo = h.redo[:len(h.redo)-1]
	h.undo = append(h.undo, c)
	h.audit = append(h.audit, "redo:"+c.Name())
	return nil
}

// Undoable reports how many steps can be undone.
func (h *History) Undoable() int { return len(h.undo) }

// Redoable reports how many steps can be redone.
func (h *History) Redoable() int { return len(h.redo) }

// Audit returns the recorded operation log.
func (h *History) Audit() []string { return append([]string(nil), h.audit...) }

// trim enforces the history cap by dropping the oldest commands, which
// makes those steps permanently un-undoable (documented trade-off).
func (h *History) trim() {
	if h.max <= 0 || len(h.undo) <= h.max {
		return
	}
	drop := len(h.undo) - h.max
	h.undo = append([]Command(nil), h.undo[drop:]...)
}

// String renders the document and history for a test or a debug endpoint.
func (h *History) String(doc *Doc) string {
	return fmt.Sprintf("doc=%q undo=%d redo=%d", doc.String(), h.Undoable(), h.Redoable())
}

// --- queue-style usage without undo ---

// Queue runs Runnables in order; it is the pattern reduced to its minimum
// when no reversal is required.
type Queue struct {
	mu   sync.Mutex
	jobs []Runnable
}

// Push appends work.
func (q *Queue) Push(r Runnable) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, r)
}

// RunAll drains the queue in FIFO order and stops on the first error.
func (q *Queue) RunAll(ctx context.Context) error {
	q.mu.Lock()
	jobs := q.jobs
	q.jobs = nil
	q.mu.Unlock()

	for i, j := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := j(ctx); err != nil {
			return fmt.Errorf("job %d: %w", i, err)
		}
	}
	return nil
}

// Len reports pending jobs.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// AuditString joins the audit log for display.
func (h *History) AuditString() string { return strings.Join(h.audit, " -> ") }
