// Package clock abstracts time so that anything with timeouts, TTLs or
// refill rates can be tested deterministically instead of by sleeping.
//
// Design note: system-design code is full of time — cache expiry, rate
// limits, backoff, leases. Tests that call time.Sleep are slow and
// flaky; tests driven by a manual clock are instant and exact. The
// dependency runs one way: production code depends on the Clock
// interface, never on time directly.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock is the time source used by the components in this directory.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

// Real is the production clock, backed by the runtime.
type Real struct{}

// Now implements Clock.
func (Real) Now() time.Time { return time.Now() }

// After implements Clock.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Manual is a clock the test drives: nothing happens until Advance is
// called, so time-based behaviour becomes a deterministic sequence.
type Manual struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	when time.Time
	ch   chan time.Time
}

// NewManual returns a manual clock standing at the given instant
// (the zero time is fine — only deltas matter).
func NewManual(at time.Time) *Manual { return &Manual{now: at} }

// Now implements Clock.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// After implements Clock: the returned channel fires during the first
// Advance that passes d.
func (m *Manual) After(d time.Duration) <-chan time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan time.Time, 1)
	m.waiters = append(m.waiters, waiter{when: m.now.Add(d), ch: ch})
	return ch
}

// Advance moves time forward by d and fires every waiter that is now
// due, in due order. Waiters with the same deadline fire in
// registration order.
func (m *Manual) Advance(d time.Duration) {
	if d < 0 {
		panic("clock: negative Advance")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)

	due := []waiter{}
	rest := []waiter{}
	for _, w := range m.waiters {
		if !w.when.After(m.now) {
			due = append(due, w)
		} else {
			rest = append(rest, w)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].when.Before(due[j].when) })
	m.waiters = rest
	for _, w := range due {
		w.ch <- m.now
	}
}
