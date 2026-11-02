// Package contextdemo documents the Go-native Context/Cancellation pattern.
//
// context.Context is not a "pattern" in the GoF sense — it is a language
// library — but the *way* it is used is a repeatable design decision with
// several distinct shapes, and getting it wrong causes either a leak or a
// hang. This package implements each shape as a small, testable function.
//
// Shapes covered:
//  1. cancel on completion      — free resources as soon as work is done;
//  2. WithTimeout budget        — a hard deadline for a whole operation;
//  3. first-error-wins          — one failure aborts the siblings;
//  4. fan-out with per-item ctx — each item gets its own child context;
//  5. graceful drain            — stop accepting, finish what is in flight;
//  6. propagation rules         — values, deadlines, and what must never
//     be stored in a context.
//
// Two rules the whole package is built on:
//   - every blocking operation must select on ctx.Done(), otherwise
//     cancellation is merely a suggestion;
//   - whoever creates a WithCancel/WithTimeout must defer cancel(), or the
//     parent context keeps the child alive until the parent itself ends —
//     the classic context leak.
package contextdemo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Work is a unit of simulated work with a name and a duration.
type Work struct {
	Name     string
	Duration time.Duration
	Err      error
}

// Simulate performs a unit of work, honouring cancellation throughout. It
// is the primitive every other function in this package builds on: the
// select on ctx.Done() is what makes cancellation real rather than advisory.
func Simulate(ctx context.Context, w Work) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.Err != nil && w.Duration <= 0 {
		return w.Err
	}

	timer := time.NewTimer(w.Duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		// Distinguish "our deadline fired" from "someone cancelled us" so
		// callers can decide whether this is a failure.
		return ctx.Err()
	case <-timer.C:
		return w.Err
	}
}

// =====================================================================
// Shape 1: cancel on completion
// =====================================================================

// WithDerived derives a child context and guarantees cancellation. It is a
// helper that makes the "defer cancel()" rule impossible to forget —
// prefer it over hand-rolled WithCancel at call sites.
//
// Returns the child context and a release func that is idempotent, so it
// can safely be deferred and also called explicitly.
func WithDerived(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

// DoOnce runs work and releases the derived context before returning.
// Compare with the buggy version: a caller who derives a context and
// forgets cancel() leaks a goroutine per call until the parent ends.
func DoOnce(parent context.Context, w Work) error {
	ctx, cancel := WithDerived(parent)
	defer cancel() // released on every path: success, failure, panic
	return Simulate(ctx, w)
}

// =====================================================================
// Shape 2: a time budget over a whole operation
// =====================================================================

// Budget runs several units of work under a single deadline. The remaining
// budget is recomputed per item, so one slow early item cannot silently
// consume the time available to the rest — a common bug when each item
// gets the *original* timeout instead of what is left.
func Budget(ctx context.Context, budget time.Duration, work []Work) ([]error, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	errs := make([]error, len(work))
	for i, w := range work {
		if err := ctx.Err(); err != nil {
			// Everything after the deadline is reported as the deadline,
			// not attempted — no point starting work we know we cannot
			// finish.
			for j := i; j < len(work); j++ {
				errs[j] = err
			}
			return errs, ctx.Err()
		}
		errs[i] = Simulate(ctx, w)
	}
	return errs, nil
}

// =====================================================================
// Shape 3: first-error-wins
// =====================================================================

// FirstError runs work concurrently and returns the first error to occur,
// cancelling everything else immediately. This is the pattern's core
// payoff: a failure 200ms in must not wait for a 10s sibling to finish.
//
// Implementation detail: the error slot is guarded by sync.Once so two
// simultaneous failures cannot race, and the derived context is cancelled
// inside that once — exactly once, no matter how many tasks fail.
func FirstError(ctx context.Context, work []Work) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // always release, even on the success path

	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)

	for _, w := range work {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Simulate(ctx, w); err != nil {
				once.Do(func() {
					first = err
					cancel()
				})
			}
		}()
	}
	wg.Wait()
	return first
}

// =====================================================================
// Shape 4: fan-out with a child context per item
// =====================================================================

// EachItem runs every unit of work with its *own* child context, derived
// from the parent. Giving each item a child means one item can be
// individually abandoned (say, by a per-item timeout) without touching its
// siblings — which a single shared context cannot express.
//
// Returns per-item errors in input order.
func EachItem(ctx context.Context, perItem time.Duration, work []Work) []error {
	errs := make([]error, len(work))
	var wg sync.WaitGroup

	for i, w := range work {
		i, w := i, w
		itemCtx, cancel := context.WithTimeout(ctx, perItem)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel() // the rule: creator of the child cancels it
			errs[i] = Simulate(itemCtx, w)
		}()
	}
	wg.Wait()
	return errs
}

// =====================================================================
// Shape 5: graceful drain
// =====================================================================

// Server accepts work until Shutdown is called, then finishes everything
// already accepted and rejects everything new. The pattern is two contexts:
// an "accept" context that Shutdown cancels, and the parent that governs
// the in-flight tasks.
type Server struct {
	acceptCtx    context.Context
	acceptCancel context.CancelFunc

	mu       sync.Mutex
	inFlight int
	rejected int
	drained  chan struct{}
}

// NewServer builds a server in the accepting state.
func NewServer(parent context.Context) *Server {
	acceptCtx, cancel := context.WithCancel(parent)
	return &Server{
		acceptCtx:    acceptCtx,
		acceptCancel: cancel,
		drained:      make(chan struct{}),
	}
}

// Accept reports whether a new request may be admitted. Shutdown flips
// this to false without disturbing requests already running.
func (s *Server) Accept() bool {
	select {
	case <-s.acceptCtx.Done():
		return false
	default:
		return true
	}
}

// Handle runs one request. It returns an error if the server is shutting
// down (the request is rejected *before* starting) or if the request's own
// context is cancelled while it runs.
func (s *Server) Handle(ctx context.Context, w Work) error {
	if !s.Accept() {
		s.mu.Lock()
		s.rejected++
		s.mu.Unlock()
		return errors.New("contextdemo: server is shutting down")
	}

	s.mu.Lock()
	s.inFlight++
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()

	return Simulate(ctx, w)
}

// Shutdown stops accepting new work, waits for in-flight requests to
// finish (up to grace), then reports what was still running.
//
// Two contexts again: acceptCancel stops admission immediately; a separate
// wait loop polls inFlight rather than racing it with a mutex.
func (s *Server) Shutdown(grace time.Duration) (drained bool) {
	s.acceptCancel()

	deadline := time.Now().Add(grace)
	for {
		s.mu.Lock()
		n := s.inFlight
		s.mu.Unlock()
		if n == 0 {
			close(s.drained)
			return true
		}
		if time.Now().After(deadline) {
			return false // caller decides what to do about stragglers
		}
		time.Sleep(time.Millisecond)
	}
}

// InFlight reports how many requests are currently running.
func (s *Server) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight
}

// Rejected reports how many requests were refused after Shutdown.
func (s *Server) Rejected() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejected
}

// Drained returns a channel closed once a successful Shutdown completed.
func (s *Server) Drained() <-chan struct{} { return s.drained }

// Close releases the accept context's resources. Always call it.
func (s *Server) Close() { s.acceptCancel() }

// =====================================================================
// Shape 6: propagation helpers
// =====================================================================

// key is an unexported type: the recommended way to keep context keys
// private so no other package can collide with or overwrite them.
type key int

const requestIDKey key = 1

// WithRequestID attaches a request ID. Values in a context are for
// request-scoped data (trace IDs, auth principals) — *never* for optional
// function parameters, and never for a dependency like a database handle.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts the ID, reporting whether it was present. The comma-ok
// form is required: a missing value must not be confused with "".
func RequestID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(requestIDKey).(string)
	return v, ok
}

// =====================================================================
// Utilities
// =====================================================================

// ErrTimeout is a domain error used to show wrapping that preserves
// errors.Is against context.DeadlineExceeded.
var ErrTimeout = errors.New("contextdemo: operation timed out")

// RunAll runs work sequentially and returns a joined error. Kept simple on
// purpose: when order matters, do not reach for goroutines.
func RunAll(ctx context.Context, work []Work) error {
	var errs []error
	for i, w := range work {
		if err := Simulate(ctx, w); err != nil {
			errs = append(errs, fmt.Errorf("work %d (%s): %w", i, w.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Names sorts work names for deterministic assertions.
func Names(work []Work) []string {
	out := make([]string, len(work))
	for i, w := range work {
		out[i] = w.Name
	}
	sort.Strings(out)
	return out
}
