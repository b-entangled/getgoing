// Package escape shows escape analysis — the decision that turns a
// pointer into a stack slot or a heap allocation.
//
// The compiler answers one question for every local: *does this value
// outlive the function?*
//
//   - No → the value stays on the stack. Costs nothing, is freed the
//     instant the frame returns, and never reaches the garbage collector.
//   - Yes (stored in a package variable, sent on a channel, returned
//     when the caller keeps it, boxed into an interface that escapes) →
//     the value moves to the heap.
//
// The escape set is computed from real flows: `sink = &local` makes
// local escape; `fmt.Sprintf` (whose format arguments can be kept by any
// implementation) historically makes its arguments escape; a value passed
// only to functions that do not leak it stays put.
//
// Why it matters: allocations are the unit of GC work. A function that
// allocates 10 times per call forces 10 garbage objects per call, at
// whatever rate the caller invokes it. The fix for a hot path is usually
// to *stop values from escaping* (return by value, take a caller-owned
// buffer) — not to micro-optimise arithmetic.
//
// Measurement: testing.AllocsPerRun with exact totals over a loop of 100
// operations, so a zero is a real zero.
package escape

// payload is bigger than a word and contains no pointers: it can never be
// stored directly in an interface word, so boxing it must allocate when
// it escapes.
type payload struct {
	A, B, C int
}

// sink forces values to escape — a package variable can be read by
// anything at any time, so whatever flows into it outlives the call.
var sink any
var ptrSink *payload

// Pure does all its work on the stack: the local payload is never
// captured by anything that outlives the call.
func Pure(n int) int {
	p := payload{1, 2, 3}
	return p.A + p.B + p.C + n
}

// Leaky stores the local payload in a package variable: the payload — and
// the interface box holding it — must live on the heap.
func Leaky(n int) int {
	p := payload{n, n + 1, n + 2}
	sink = p
	return p.A
}

// TakeByPointer passes a local by pointer to a callee that does not keep
// it: still stack — escape analysis follows the call (and inlines it).
func TakeByPointer(n int) int {
	p := payload{1, 2, 3}
	return sum(&p) + n
}

func sum(p *payload) int { return p.A + p.B + p.C }

// ReturnedByValue hands back a copy, so nothing about the local escapes.
func ReturnedByValue(n int) payload {
	return payload{n, n, n}
}

// ReturnedByPointer hands back the address: the caller may keep it, so
// the value must be heap-allocated.
func ReturnedByPointer(n int) *payload {
	return &payload{n, n, n}
}

// ResetSink drops the escaped value so measurements start clean.
func ResetSink() { sink = nil; ptrSink = nil }
