// Package slicecap documents the slice aliasing mistake: `append` may
// reuse the backing array you already hold a view into.
//
// A slice is a (pointer, length, capacity) header over a backing array.
// `append(a, x)`:
//
//  1. if len(a) < cap(a): writes x into the *existing* array at a[len],
//     then returns a header with length+1 — **the same array**;
//  2. if len(a) == cap(a): allocates a new array, copies, and only then
//     the two slices are independent.
//
// So this silently shares memory:
//
//	a := make([]int, 2, 4)
//	b := append(a, 3)   // b shares a's array
//	b[0] = 99           // a[0] is 99 now too
//
// Fixes: copy explicitly (`append(a[:0:0], a...)` — a zero-cap slice
// forces allocation), use a full slice expression `a[:2:2]` to pin the
// capacity, or `copy` into a fresh slice.
//
// Domain: batching events — the natural place two views of one buffer
// appear.
package slicecap

// BadSharedAppend appends to a slice with spare capacity and mutates the
// result. The caller's original slice changes too: both share the array.
// Returns (original view, appended view) for the test to compare.
func BadSharedAppend() (orig, shared []int) {
	orig = make([]int, 2, 4) // [0 0], cap 4 — room to grow in place
	orig[0], orig[1] = 1, 2
	shared = append(orig, 3) // reuses orig's array
	shared[0] = 99           // writes through the shared header
	return orig, shared
}

// GoodCopy appends the same way but into a fresh backing array: mutations
// never reach the original.
func GoodCopy() (orig, independent []int) {
	orig = make([]int, 2, 4)
	orig[0], orig[1] = 1, 2
	independent = append(orig[:0:0], orig...) // zero capacity forces a new array
	independent = append(independent, 3)
	independent[0] = 99
	return orig, independent
}

// GoodFullSliceExpression pins the capacity of the view, so a later
// append must allocate instead of writing into the shared array.
func GoodFullSliceExpression() (orig, view []int) {
	orig = make([]int, 2, 4)
	orig[0], orig[1] = 1, 2
	view = orig[:2:2] // len 2, cap 2: no spare room
	view = append(view, 3)
	view[0] = 99
	return orig, view
}

// Shows the other half of the rule: when capacity is already exhausted,
// append allocates and the slices diverge on their own.
func AppendAtCapacityAllocates() (orig, grown []int) {
	orig = []int{1, 2} // len 2 == cap 2
	grown = append(orig, 3)
	grown[0] = 99
	return orig, grown
}

// SafeAppendToNil shows the common case that has no aliasing at all:
// appending to a nil slice always allocates.
func SafeAppendToNil(values ...int) []int {
	var out []int
	return append(out, values...)
}
