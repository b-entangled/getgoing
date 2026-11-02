// Package swallowerr documents discarded errors — the quietest way to
// lose a failure.
//
// Shapes you will meet:
//
//	_, _ = w.Write(data)   // explicit discard: at least it is visible
//	defer f.Close()        // implicit discard: the error is gone
//	if err != nil {}       // handled by doing nothing
//	_, err := f(); if err != nil { return nil } // converted to success
//
// The first is honest, the rest are bugs: the caller receives success
// while the work did not happen. Defences:
//   - check every error you are not *deliberately* ignoring, and say why
//     when you do (`//nolint:errcheck // best-effort log flush`);
//   - for `defer f.Close()`, capture it into a named return so the close
//     error joins the result;
//   - run errcheck / staticcheck in CI — this class of bug is invisible
//     in review because nothing looks wrong.
//
// Domain: writing a report to a destination that can fail.
package swallowerr

import (
	"errors"
	"io"
)

// ErrWriteFailed is the sentinel every failing operation here returns.
var ErrWriteFailed = errors.New("swallowerr: write failed")

// ErrCloseFailed is the sentinel for a failing Close.
var ErrCloseFailed = errors.New("swallowerr: close failed")

// failWriter fails every write and close — the fixture for "the work did
// not happen".
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, ErrWriteFailed }
func (failWriter) Close() error              { return ErrCloseFailed }

// NewFailWriter returns a destination where every operation fails.
func NewFailWriter() io.WriteCloser { return failWriter{} }

// failAfterWriter succeeds on the first write and fails afterwards —
// the fixture for "half the work happened".
type failAfterWriter struct{ calls int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > 1 {
		return 0, ErrWriteFailed
	}
	return len(p), nil
}
func (w *failAfterWriter) Close() error { return nil }

// NewFailSecondWriter returns a destination whose *second* write fails.
func NewFailSecondWriter() io.WriteCloser { return &failAfterWriter{} }

// okWriter is a destination that works.
type okWriter struct{ n int }

func (w *okWriter) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }
func (w *okWriter) Close() error                { return nil }

// NewOKWriter returns a destination that succeeds.
func NewOKWriter() io.WriteCloser { return &okWriter{} }

// BadWrite discards the write error and reports success: the caller has
// no idea the report never landed.
func BadWrite(w io.Writer, data []byte) error {
	_, _ = w.Write(data) // discarded — the mistake
	return nil           // ...and the caller is told everything is fine
}

// GoodWrite propagates the error so the caller can react.
func GoodWrite(w io.Writer, data []byte) error {
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}

// BadClose defers Close without capturing its error: whatever Close
// returns is thrown away when the function returns nil.
func BadClose(c io.Closer) error {
	defer c.Close() // error discarded at return time
	return nil
}

// GoodClose folds the deferred Close into a named return, so a failing
// close is reported even though the body succeeded.
func GoodClose(c io.Closer) (err error) {
	defer func() {
		err = errors.Join(err, c.Close())
	}()
	return nil
}

// BadPartial writes two chunks and ignores the second failure: the first
// write succeeding hides the partial result.
func BadPartial(w io.Writer, a, b []byte) error {
	if _, err := w.Write(a); err != nil {
		return err
	}
	_, _ = w.Write(b) // "the first one worked, ship it"
	return nil
}
