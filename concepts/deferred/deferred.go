// Package deferred demonstrates how `defer` behaves: ordering, argument
// evaluation, interaction with panics and named return values.
//
// Five rules, each with a test:
//
//  1. Defers run **LIFO** — last deferred, first executed.
//  2. A deferred call's **arguments are evaluated when `defer` executes**,
//     not when the call runs. The *call* is postponed; the *evaluation*
//     is not.
//  3. Defers run when the surrounding function returns — **including via
//     panic** — before the caller sees the result.
//  4. `recover()` inside a deferred call turns a panic into a normal
//     return: it only works while the stack is unwinding.
//  5. With a **named return value**, a deferred function can read and even
//     *replace* the value the caller receives.
//
// Domain: a tiny recorder that logs events, so order and timing are
// observable without real I/O.
package deferred

// Recorder collects events in the order they happen.
type Recorder struct {
	events []string
}

// Record appends an event.
func (r *Recorder) Record(s string) { r.events = append(r.events, s) }

// Events returns a copy of the recorded events.
func (r *Recorder) Events() []string { return append([]string(nil), r.events...) }

// RunDeferredLIFO defers three records; they execute in reverse order.
func RunDeferredLIFO(r *Recorder) {
	defer r.Record("third")
	defer r.Record("second")
	defer r.Record("first")
	r.Record("body")
}

// RunDeferredWithArgs evaluates arguments at defer time: the recorded
// value is the one x had when the defer statement ran.
func RunDeferredWithArgs(r *Recorder, x int) {
	defer r.Record("value")
	defer r.Record(itoa(x)) // evaluated NOW, with x == 1
	x = 2
}

// RunDeferredWithClosure postpones evaluation too: the closure body runs
// later and reads x's final value.
func RunDeferredWithClosure(r *Recorder, x int) {
	defer func() { r.Record(itoa(x)) }()
	x = 2
}

// RunDefersOnPanic shows defers running while a panic unwinds the stack
// and recover converting the panic into a normal return.
func RunDefersOnPanic(r *Recorder) (panicked bool) {
	defer func() {
		r.Record("after-recover")
		if recover() != nil {
			panicked = true
		}
	}()
	defer r.Record("defer-before-recover") // runs first: LIFO
	r.Record("body")
	panic("boom")
}

// ErrBoom is the sentinel error.
type ErrBoom struct{ Msg string }

// Error implements error.
func (e *ErrBoom) Error() string { return "boom: " + e.Msg }

var errBoom = &ErrBoom{Msg: "converted panic"}

// BoomError returns the sentinel FailWithNamedReturn substitutes.
func BoomError() error { return errBoom }

// FailWithNamedReturn lets the deferred call replace the return value:
// the caller sees the error the defer installed.
func FailWithNamedReturn(r *Recorder) (err error) {
	defer func() {
		if recover() != nil {
			r.Record("converted")
			err = errBoom
		}
	}()
	panic("boom")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for n := i; n > 0; n /= 10 {
		b = append([]byte{'0' + byte(n%10)}, b...)
	}
	return string(b)
}
