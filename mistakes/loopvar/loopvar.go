// Package loopvar documents the loop-variable mistake.
//
// **What changed in Go 1.22:** the loop variable is now created fresh for
// *every iteration*. Closures and goroutines started inside the loop
// capture their own copy, so the classic bug — every closure returning the
// final value — is gone for the loop variable itself:
//
//	for i := 0; i < 3; i++ { go func() { use(i) }() }   // safe today
//
// **What is still a mistake:** any *other* variable you mutate inside the
// loop and capture is shared exactly as it always was. The fix is the same
// as it ever was: give each iteration its own variable, or capture the
// value, not the shared slot:
//
//	for _, v := range xs {
//		v := v // or pass v as an argument
//		fs = append(fs, func() int { return v })
//	}
//
// Domain: collecting closures and goroutine results from a loop, which is
// where the mistake shows up in real code (event handlers, workers,
// callbacks).
package loopvar

import "sync"

// PerIterationClosures is correct: each closure captures the range
// variable of *its own* iteration (Go 1.22+).
func PerIterationClosures(n int) []func() int {
	var fs []func() int
	for i := 0; i < n; i++ {
		fs = append(fs, func() int { return i })
	}
	return fs
}

// SharedOuterClosure is the remaining mistake: `last` is a *single*
// variable declared outside the loop, mutated each iteration. Every
// closure captures that one variable, so all of them return its final
// value. Go 1.22 did not and could not fix this — it is not the loop
// variable.
func SharedOuterClosure(values []int) []func() int {
	var last int
	var fs []func() int
	for _, v := range values {
		last = v
		fs = append(fs, func() int { return last })
	}
	return fs
}

// FixedOuterClosure copies the value into a per-iteration variable before
// capturing it. Equivalent: pass it as an argument instead of capturing.
func FixedOuterClosure(values []int) []func() int {
	var fs []func() int
	for _, v := range values {
		v := v // per-iteration copy
		last := v
		fs = append(fs, func() int { return last })
	}
	return fs
}

// PerIterationGoroutines runs one goroutine per index and returns what
// each saw. Safe only because `i` is per-iteration: the goroutines write
// to distinct elements and each captures a distinct variable. Under the
// pre-1.22 semantics this was both wrong *and* a data race.
func PerIterationGoroutines(n int) []int {
	out := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			out[i] = i
		}()
	}
	wg.Wait()
	return out
}
