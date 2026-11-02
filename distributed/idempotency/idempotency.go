// Package idempotency deduplicates work by key — the mechanism that
// turns "at-least-once delivery" (what queues give you) into
// "effectively-once execution" (what APIs promise).
//
// The shape it models: a client retries a request after a timeout
// because it cannot tell "failed" from "response lost". Without a
// key, the retry charges the card twice. With one, the retry lands on
// the *same execution*: it waits for (or reuses) the first attempt's
// outcome instead of starting a second.
//
// Design decisions made explicit here:
//
//   - Concurrent callers for one key share a single execution: the
//     first in runs, everyone else blocks on the entry's completion
//     signal and receives the same result. One attempt, N results.
//   - Successes are cached until TTL; failures are not. A failed
//     attempt did not produce the effect, so the next caller must be
//     allowed to try again — caching errors would turn one transient
//     failure into a permanent one. (Workflows that *want*
//     failure-caching record the outcome elsewhere as data.)
//   - Expiry is lazy, driven by a clock.Clock: an expired entry is
//     simply re-executed on next use, no sweeper goroutine.
//   - The executing caller's context runs the function. A waiter that
//     gives up leaves the execution untouched — cancellation is
//     per-caller, not per-key.
package idempotency

import (
	"context"
	"sync"
	"time"

	"github.com/b-entangled/projects/learnings/sdesign/clock"
)

type entry[T any] struct {
	done  chan struct{} // closed when the attempt finishes
	value T
	err   error
	at    time.Time // when the attempt finished (TTL base)
}

// Deduper runs each key's work at most once per TTL window.
type Deduper[T any] struct {
	mu      sync.Mutex
	entries map[string]*entry[T]
	ttl     time.Duration
	clk     clock.Clock
}

// New returns a deduper whose successful results are reused for ttl.
// clk may be nil for the real clock; ttl must be positive.
func New[T any](ttl time.Duration, clk clock.Clock) *Deduper[T] {
	if ttl <= 0 {
		panic("idempotency: ttl must be positive")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Deduper[T]{
		entries: make(map[string]*entry[T]),
		ttl:     ttl,
		clk:     clk,
	}
}

// Do runs fn under key unless a successful attempt for that key is
// still within its TTL, in which case the cached result is returned.
// While an attempt is running, every other caller for the same key
// waits for its outcome instead of starting a second attempt.
func (d *Deduper[T]) Do(ctx context.Context, key string, fn func(ctx context.Context) (T, error)) (T, error) {
	d.mu.Lock()
	if e, ok := d.entries[key]; ok {
		select {
		case <-e.done:
			// Finished: reuse while within TTL, re-run once expired.
			if d.clk.Now().Sub(e.at) < d.ttl {
				value, err := e.value, e.err
				d.mu.Unlock()
				return value, err
			}
			delete(d.entries, key)
		default:
			// Still running: TTL does not apply to in-flight work —
			// wait for this attempt instead of starting another.
			d.mu.Unlock()
			select {
			case <-e.done:
				return e.value, e.err
			case <-ctx.Done():
				var zero T
				return zero, ctx.Err()
			}
		}
	}
	e := &entry[T]{done: make(chan struct{})}
	d.entries[key] = e
	d.mu.Unlock()

	// The first caller executes; its context is the execution's context.
	value, err := fn(ctx)

	d.mu.Lock()
	if err == nil {
		e.value = value
		e.at = d.clk.Now()
		// keep the entry: the success is the cacheable effect
	} else {
		delete(d.entries, key) // failures are not cached — retryable
	}
	e.err = err
	d.mu.Unlock()
	close(e.done)

	return value, err
}

// InFlight reports how many attempts are currently running or cached —
// mostly a hook for metrics and tests.
func (d *Deduper[T]) InFlight() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}
