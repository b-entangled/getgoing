// Package taskgroup demonstrates the Go-native Task Group pattern — the
// in-process equivalent of golang.org/x/sync/errgroup.
//
// Run several tasks concurrently, wait for all of them, and return the
// first error while cancelling the rest. This replaces the GoF "observer
// waits for N subjects" shape: instead of counting completions with a
// mutex, the group owns a derived context and a single error slot.
//
// Implementation notes that matter:
//   - exactly one error is stored, guarded by sync.Once, so concurrent
//     failures do not race on the slot;
//   - the derived context is cancelled on the first error, so siblings
//     stop; it is also cancelled when Wait returns, releasing resources;
//   - Wait must not return before every task goroutine has finished, or a
//     caller reads a half-written result — hence the WaitGroup;
//   - Go() after Wait() is a programming error and panics rather than
//     silently leaking a goroutine that nobody will wait for.
//
// Domain: fetching several URLs (simulated) in parallel and failing fast.
package taskgroup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ErrNoTasks is returned by Wait when nothing was submitted. Callers can
// distinguish "nothing to do" from a real failure with errors.Is.
var ErrNoTasks = errors.New("taskgroup: no tasks were submitted")

// ErrConcurrentUse is the panic message for Go() after Wait().
var ErrConcurrentUse = errors.New("taskgroup: Go called after Wait")

// Group runs tasks concurrently and reports the first error.
type Group struct {
	ctx    context.Context
	cancel context.CancelFunc

	wg   sync.WaitGroup
	mu   sync.Mutex
	err  error
	once sync.Once

	// limit bounds concurrency; 0 means unlimited.
	limit chan struct{}

	started int
	waiting bool
}

// New creates a group whose tasks receive the derived context. The caller
// must call Wait — every started goroutine is waited for, so no goroutine
// outlives the group.
func New(parent context.Context) *Group {
	ctx, cancel := context.WithCancel(parent)
	return &Group{ctx: ctx, cancel: cancel}
}

// WithLimit creates a group that runs at most `n` tasks at a time. A limit
// of 0 or less means unlimited. Bounded concurrency is why a group is
// preferable to a bare `go func()` loop over a large slice.
func WithLimit(parent context.Context, n int) *Group {
	g := New(parent)
	if n > 0 {
		g.limit = make(chan struct{}, n)
	}
	return g
}

// Context returns the group's derived context, already cancelled if a task
// has failed. Pass it to tasks so they can abort mid-flight.
func (g *Group) Context() context.Context { return g.ctx }

// Go starts fn in a new goroutine, subject to the concurrency limit.
// It blocks only when the limit is reached — which is the point: the
// caller cannot accidentally start 100k goroutines.
func (g *Group) Go(fn func(ctx context.Context) error) {
	g.mu.Lock()
	if g.waiting {
		g.mu.Unlock()
		panic(ErrConcurrentUse)
	}
	g.started++
	g.mu.Unlock()

	if g.limit != nil {
		select {
		case g.limit <- struct{}{}:
		case <-g.ctx.Done():
			return // a sibling already failed; do not start this task
		}
	}

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if g.limit != nil {
			defer func() { <-g.limit }()
		}

		// Do not start work if the group has already failed.
		if g.ctx.Err() != nil {
			g.setError(g.ctx.Err())
			return
		}

		if err := fn(g.ctx); err != nil {
			g.setError(err)
		}
	}()
}

// setError records the first error only. sync.Once guarantees exactly one
// winner even when several tasks fail simultaneously.
func (g *Group) setError(err error) {
	g.once.Do(func() {
		g.mu.Lock()
		g.err = err
		g.mu.Unlock()
		g.cancel() // stop the siblings
	})
}

// Wait blocks until every task finishes and returns the first error, if
// any. The derived context is cancelled before returning so its resources
// (timers, AfterFunc registrations) are released even on the success path.
func (g *Group) Wait() error {
	g.mu.Lock()
	g.waiting = true
	g.mu.Unlock()

	g.wg.Wait()
	g.cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

// Started reports how many tasks were actually submitted. Useful in tests
// that assert the concurrency limit did not admit everything at once.
func (g *Group) Started() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started
}

// =====================================================================
// Convenience builders for common shapes
// =====================================================================

// Task is a named unit of work, so failures can say which one broke.
type Task struct {
	Name string
	Fn   func(ctx context.Context) error
}

// RunAll starts every task and returns the first error. It is the common
// case: no limit, no per-task result, just "do these in parallel and tell
// me what went wrong".
func RunAll(ctx context.Context, tasks ...Task) error {
	if len(tasks) == 0 {
		return ErrNoTasks
	}
	g := New(ctx)
	for _, t := range tasks {
		t := t
		g.Go(func(ctx context.Context) error {
			if err := t.Fn(ctx); err != nil {
				return fmt.Errorf("task %s: %w", t.Name, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// TryAll runs every task to completion even if some fail, then joins all
// errors. Use it when partial success is still useful — the opposite of
// RunAll's fail-fast policy, and a one-line difference.
func TryAll(ctx context.Context, tasks ...Task) error {
	if len(tasks) == 0 {
		return ErrNoTasks
	}
	// A group that ignores individual errors: run each, collect separately.
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	derived, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, t := range tasks {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := t.Fn(derived); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("task %s: %w", t.Name, err))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// =====================================================================
// A worked example: a parallel fetch with a timeout budget
// =====================================================================

// Fetcher resolves a logical URL to a body. Tests substitute a fake.
type Fetcher func(ctx context.Context, url string) (string, error)

// FetchAll fetches every URL concurrently with a bounded group and a total
// time budget. It returns bodies keyed by URL for the URLs that succeeded,
// plus an error describing the first failure.
//
// The shape — derive a deadline, bound concurrency, fail fast, wait — is
// the standard composition this pattern exists to make repeatable.
func FetchAll(ctx context.Context, fetch Fetcher, urls []string, concurrency int, budget time.Duration) (map[string]string, error) {
	if len(urls) == 0 {
		return map[string]string{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	g := WithLimit(ctx, concurrency)
	var (
		mu     sync.Mutex
		bodies = make(map[string]string, len(urls))
	)

	for _, u := range urls {
		u := u
		g.Go(func(ctx context.Context) error {
			body, err := fetch(ctx, u)
			if err != nil {
				return err
			}
			mu.Lock()
			bodies[u] = body
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return bodies, err
	}
	return bodies, nil
}

// URLs returns the sorted keys of a body map, for deterministic output.
func Bodies(bodies map[string]string) []string {
	out := make([]string, 0, len(bodies))
	for k := range bodies {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
