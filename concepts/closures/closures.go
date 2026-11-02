// Package closures demonstrates closure semantics: captured variables,
// factories that carry state, and what Go 1.22 changed about loop
// variables.
//
// A closure is a func literal that *references variables from the
// enclosing function*. It captures the **variable**, not its value at
// creation time — so later assignments are visible to every closure that
// captured the same variable.
//
// Domain: small stateful functions (accumulators, predicates, adders) so
// the capture behaviour is visible without any I/O.
//
// What Go 1.22 changed: with per-iteration loop variable semantics, each
// iteration of a range/3-clause loop has its **own** copy of the loop
// variable, so a closure created inside the loop captures a distinct
// variable per iteration. The historical "every goroutine saw the last
// index" bug is gone — but capturing any *other* shared variable is still
// a mistake (see the loopvar mistake package).
package closures

// Adder returns a function that adds n to its argument. The captured n
// lives in the closure: each Adder call creates its own copy.
func Adder(n int) func(int) int {
	return func(x int) int { return x + n }
}

// Counter returns a closure that increments and returns an internal
// counter — state stored in the captured variable, one closure per call.
func Counter(start int) func() int {
	n := start
	return func() int {
		n++
		return n
	}
}

// NewAccumulator returns a closure collecting values and a snapshot
// function: two closures sharing the *same* captured variable.
func NewAccumulator() (add func(int), sum func() int) {
	var total int
	return func(v int) { total += v }, func() int { return total }
}

// SumAll applies each function to v and adds the results.
func SumAll(v int, fs ...func(int) int) int {
	total := 0
	for _, f := range fs {
		total += f(v)
	}
	return total
}

// SharedCaptures builds one closure per element that all reference the
// same captured variable (not the loop variable). Every closure sees the
// final assignment — the capture-by-variable rule in one function.
func SharedCaptures(xs []int) []func() int {
	var last int
	var fs []func() int
	for _, x := range xs {
		last = x
		fs = append(fs, func() int { return last })
	}
	return fs
}

// PerIterationCaptures builds one closure per element capturing the loop
// variable itself. Since Go 1.22 each iteration has its own copy, so the
// closures are independent.
func PerIterationCaptures(xs []int) []func() int {
	var fs []func() int
	for _, x := range xs {
		fs = append(fs, func() int { return x })
	}
	return fs
}
