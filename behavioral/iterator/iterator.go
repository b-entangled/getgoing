// Package iterator demonstrates the Iterator pattern in modern Go.
//
// The GoF shape (HasNext/Next with a cursor object) is rejected: it forces
// the iterator to own cursor state and to signal exhaustion with a sentinel.
// Go's answer is range-over-function (iter.Seq, Go 1.23+): a function that
// calls a yield callback. Termination is an ordinary `break`, there is no
// Done flag, no Err() to check, and no state machine to get wrong.
//
// A pre-generics closure-pull idiom (Each + bool return) is included for
// code that must build with an older toolchain.
package iterator

import (
	"iter"
	"slices"
)

// =====================================================================
// Shape 1: range-over-function (preferred)
// =====================================================================

// Node is a binary tree node used to show recursive sequences.
type Node struct {
	Value string
	Left  *Node
	Right *Node
}

// Preorder yields nodes root-left-right. Lazy: nothing is walked until the
// consumer asks for the next value, and a `break` stops the walk entirely.
func (n *Node) Preorder() iter.Seq[*Node] {
	return func(yield func(*Node) bool) { n.preorder(yield) }
}

// preorder returns false when the consumer stopped (break/return).
func (n *Node) preorder(yield func(*Node) bool) bool {
	if n == nil {
		return true
	}
	if !yield(n) {
		return false
	}
	if !n.Left.preorder(yield) {
		return false
	}
	return n.Right.preorder(yield)
}

// Inorder yields nodes left-root-right — for a BST this produces sorted
// output, which is the classic reason to want inorder at all.
func (n *Node) Inorder() iter.Seq[*Node] {
	return func(yield func(*Node) bool) { n.inorder(yield) }
}

func (n *Node) inorder(yield func(*Node) bool) bool {
	if n == nil {
		return true
	}
	if !n.Left.inorder(yield) {
		return false
	}
	if !yield(n) {
		return false
	}
	return n.Right.inorder(yield)
}

// Words yields each whitespace-delimited token without allocating a slice
// of all tokens first — the memory win over strings.Fields.
func Words(s string) iter.Seq[string] {
	return func(yield func(string) bool) {
		start := -1
		for i, r := range s {
			if r == ' ' || r == '\t' || r == '\n' {
				if start >= 0 {
					if !yield(s[start:i]) {
						return
					}
					start = -1
				}
				continue
			}
			if start < 0 {
				start = i
			}
		}
		if start >= 0 {
			yield(s[start:])
		}
	}
}

// Count yields 0..n-1. The simplest possible sequence, useful as a
// replacement for `for i := 0; i < n; i++` in nested compositions.
func Count(n int) iter.Seq[int] {
	return func(yield func(int) bool) {
		for i := 0; i < n; i++ {
			if !yield(i) {
				return
			}
		}
	}
}

// Take yields at most the first n elements of a sequence — a generic
// adaptor written against a sequence, exactly like itertools/itertools.chain.
func Take[T any](n int, seq iter.Seq[T]) iter.Seq[T] {
	return func(yield func(T) bool) {
		if n <= 0 {
			return
		}
		i := 0
		for v := range seq {
			if !yield(v) {
				return
			}
			i++
			if i >= n {
				return
			}
		}
	}
}

// Filter yields only the elements matching pred.
func Filter[T any](seq iter.Seq[T], pred func(T) bool) iter.Seq[T] {
	return func(yield func(T) bool) {
		for v := range seq {
			if pred(v) && !yield(v) {
				return
			}
		}
	}
}

// Collect drains a sequence into a slice. This is the boundary where lazy
// becomes eager — and the only place a slice allocation is unavoidable.
func Collect[T any](seq iter.Seq[T]) []T {
	var out []T
	for v := range seq {
		out = append(out, v)
	}
	return out
}

// Any reports whether any element satisfies pred, stopping at the first
// match — a short-circuit that a slice-returning API could not express.
func Any[T any](seq iter.Seq[T], pred func(T) bool) bool {
	for v := range seq {
		if pred(v) {
			return true
		}
	}
	return false
}

// =====================================================================
// Shape 2: explicit iterator object (when state must be shared)
// =====================================================================

// SliceIter is the rare case where a stateful iterator is justified: the
// cursor lives in one struct so a caller can hand it around, pause, and
// resume. Note it still exposes no HasNext/Next pair — Next returns
// (value, ok) in the idiom of map lookup and channel receive.
type SliceIter[T any] struct {
	items []T
	pos   int
}

// NewSliceIter wraps a slice. The slice is not copied: mutation by the
// owner is visible to the iterator.
func NewSliceIter[T any](items []T) *SliceIter[T] {
	return &SliceIter[T]{items: items}
}

// Next returns the next value and whether one existed.
func (s *SliceIter[T]) Next() (T, bool) {
	var zero T
	if s.pos >= len(s.items) {
		return zero, false
	}
	v := s.items[s.pos]
	s.pos++
	return v, true
}

// Reset rewinds the cursor to the start.
func (s *SliceIter[T]) Reset() { s.pos = 0 }

// Remaining reports how many values are left.
func (s *SliceIter[T]) Remaining() int {
	if s.pos >= len(s.items) {
		return 0
	}
	return len(s.items) - s.pos
}

// All exposes the same iterator as a sequence so it composes with the
// adapters above, without duplicating the logic.
func (s *SliceIter[T]) All() iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, v := range s.items[s.pos:] {
			if !yield(v) {
				return
			}
		}
	}
}

// =====================================================================
// Shape 3: pre-generics closure pull (legacy compatibility)
// =====================================================================

// EachLine calls fn for every line of s until fn returns false.
// The bool return is the "stop" signal — no io.EOF, no sentinel value.
// Returns the number of lines delivered.
func EachLine(s string, fn func(line string) bool) int {
	n := 0
	// deliver counts a line as delivered as soon as fn is invoked, even if
	// fn asks to stop — the caller did receive it.
	deliver := func(line string) bool {
		n++
		return fn(line)
	}

	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		if !deliver(s[start:i]) {
			return n
		}
		start = i + 1
	}
	if start < len(s) {
		deliver(s[start:])
	}
	return n
}

// ReverseLines reads the lines back in reverse order using EachLine.
func ReverseLines(s string) []string {
	var lines []string
	EachLine(s, func(line string) bool {
		lines = append(lines, line)
		return true
	})
	slices.Reverse(lines)
	return lines
}
