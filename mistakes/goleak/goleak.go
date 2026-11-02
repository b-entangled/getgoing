// Package goleak documents goroutine leaks: workers that outlive the work
// because nothing can ever make them exit.
//
// A goroutine leaks when it blocks forever on something no one will ever
// satisfy — an unreceived channel send, a receive from a channel nobody
// closes, a lock nobody unlocks, an HTTP response nobody reads. Each one
// keeps its stack (2–8 KB minimum) and its scheduler slot alive for the
// life of the process, so a leak in a per-request path grows until the
// process dies.
//
// The fix is always the same shape: give every blocking call an escape
// hatch tied to something that actually ends.
//
//   - pass a context and `select { case <-ctx.Done(): ... }`
//   - close the channel the receiver is waiting on
//   - use a WaitGroup / done channel so the caller can *wait* for exit
//
// The mistake version here returns a release handle *only so the test can
// clean up after itself* — production callers of BadSpawn get no such
// handle, which is exactly the bug.
//
// Domain: a background worker started per job.
package goleak

import "context"

// BadSpawn starts a worker that blocks forever: it waits for a signal
// that nothing in the program will ever send, and there is no context,
// timeout or WaitGroup to stop it.
//
// The returned release exists purely so tests can undo the leak; the
// production caller has no way to reach it.
func BadSpawn() (exited <-chan struct{}, release func()) {
	exitedC := make(chan struct{})
	unblock := make(chan struct{})
	go func() {
		<-unblock // stuck here forever: no cancel, no close, no receiver
		close(exitedC)
	}()
	var once bool
	return exitedC, func() {
		if !once {
			once = true
			close(unblock)
		}
	}
}

// GoodSpawn starts the same kind of worker, but the goroutine exits as
// soon as the context is cancelled — and the caller holds the cancel
// function, so shutdown is under its control.
func GoodSpawn(ctx context.Context) (exited <-chan struct{}) {
	exitedC := make(chan struct{})
	go func() {
		defer close(exitedC)
		<-ctx.Done() // ends when the caller cancels or the deadline passes
	}()
	return exitedC
}

// GoodSpawnAndWait is the "run a bounded amount of work" shape: the
// worker signals completion, and the caller waits for it before moving
// on. Nothing can outlive the call.
func GoodSpawnAndWait(ctx context.Context, work func(ctx context.Context) error) error {
	exited := make(chan error, 1)
	go func() { exited <- work(ctx) }()

	select {
	case err := <-exited:
		return err
	case <-ctx.Done():
		// The work overran the budget; the caller still gets an answer
		// and the goroutine (which is the caller's own work) is the only
		// one left — bounded by the work function honouring ctx.
		return ctx.Err()
	}
}
