// Package deftime documents two mistakes around `defer`.
//
//  1. **Arguments are evaluated when `defer` runs, not when the call
//     runs.** The *call* is postponed; the *evaluation* is not:
//
//     x := 1
//     defer log(x)   // logs 1, even if x changes before return
//     x = 2
//
//     The fix is a closure — then the whole body runs later:
//
//     defer func() { log(x) }()   // logs 2
//
//  2. **Defer inside a loop holds every resource until the function
//     returns.** A loop of `defer f.Close()` opens n files and closes none
//     of them until the end — the classic file-descriptor leak in a
//     long-running function. Close inside the loop (or don't defer).
//
// Domain: a batch processor that records events, so timing and order are
// observable.
package deftime

import "fmt"

// BadStaleArgument records x *now*, at the defer statement: the later
// mutation is invisible to the deferred call.
func BadStaleArgument(x int, record func(int)) {
	defer record(x) // evaluated with the current x
	x = 99          // too late: the value is already captured
}

// GoodClosureArgument postpones the whole call, so record reads x's final
// value when it actually runs.
func GoodClosureArgument(x int, record func(int)) {
	defer func() { record(x) }()
	x = 99
}

// BadDeferInLoop defers one release per iteration. All n releases run
// when the function returns — after "body-done" — so n resources stay
// open for the whole loop.
func BadDeferInLoop(n int, record func(string)) {
	for i := 0; i < n; i++ {
		defer record(fmt.Sprintf("release-%d", i))
	}
	record("body-done")
	// every release fires here, in reverse order
}

// GoodReleaseInLoop releases inside the loop: resources are freed as soon
// as each iteration is finished with them.
func GoodReleaseInLoop(n int, record func(string)) {
	for i := 0; i < n; i++ {
		record(fmt.Sprintf("release-%d", i))
	}
	record("body-done")
}
