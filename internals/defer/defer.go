// Package defer shows the two implementations of `defer` and the rule
// that decides between them.
//
// The compiler emits **open-coded defers** for a defer statement that
// sits in the function body proper: the deferred call is recorded in the
// stack frame itself and invoked by the return sequence. That costs
// **zero allocations** — modern Go defer is essentially free.
//
// A defer inside a **loop** cannot be open-coded: the number of pending
// calls is dynamic, so each one needs a heap-allocated record linked
// into a list. Ten loop iterations → ten allocations. Defers in a loop
// are both slow *and* (see the deftime mistake) semantically wrong,
// because nothing runs until the function returns.
//
// Measurement uses testing.AllocsPerRun, which counts real heap
// allocations — the difference between the two shapes is visible without
// a profiler.
package defer_

// OpenCoded has one defer in the function body: the compiler open-codes
// it, so the whole call allocates nothing.
func OpenCoded() (out int) {
	defer func() { out++ }()
	return 1
}

// OpenCodedMany has several top-level defers; all of them are open-coded
// (the limit is on per-defer state in the frame, not on the count being
// small — these all qualify).
func OpenCodedMany() (out int) {
	defer func() { out += 1 }()
	defer func() { out += 2 }()
	defer func() { out += 4 }()
	return 0
}

// InLoop defers inside a loop: the dynamic count forces heap-allocated
// defer records, one per iteration.
func InLoop(n int) {
	for i := 0; i < n; i++ {
		i := i
		defer func() { _ = i }()
	}
}

// Mixed has a top-level defer *and* loop defers: only the loop ones pay
// for heap records.
func Mixed(n int) {
	defer func() {}()
	for i := 0; i < n; i++ {
		i := i
		defer func() { _ = i }()
	}
}
