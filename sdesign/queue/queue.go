// Package queue implements a bounded, blocking, generic FIFO — the
// backpressure primitive at the heart of every pipeline design: a
// producer that outruns its consumer must *wait* (or be told no), not
// silently buffer without limit.
//
// Design decisions made explicit here:
//
//   - The bound is fixed at construction: beyond it, Put blocks until
//     space frees or the caller's context expires. Blocking is the
//     backpressure signal — memory stays bounded no matter the
//     producer/consumer imbalance.
//   - Get blocks on an empty queue instead of failing: consumers are
//     allowed to be faster than producers; the cost is that callers
//     must pass a context to be able to give up.
//   - Close drains: Get keeps returning buffered items after Close and
//     only then reports ErrClosed, so shutdown loses nothing. Blocked
//     Puts wake with ErrClosed.
//   - Put racing Close is decided by the race itself: a Put that wins
//     is queued (and drainable), a Put that loses returns ErrClosed.
//     Never a panic — unlike a bare channel send on a closed channel.
//
// The implementation uses two broadcast channels (one per condition)
// so any number of waiters wake on each state change, with no
// polling and no condition-variable/context impedance mismatch.
package queue

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is reported by Put and Get after Close.
var ErrClosed = errors.New("queue: closed")

// Queue is a bounded FIFO of T.
type Queue[T any] struct {
	mu       sync.Mutex
	buf      []T
	capacity int
	closed   bool
	notFull  chan struct{} // closed & replaced whenever space frees
	notEmpty chan struct{} // closed & replaced whenever an item arrives or on Close
}

// New returns a queue holding at most capacity items.
func New[T any](capacity int) *Queue[T] {
	if capacity < 1 {
		panic("queue: capacity must be >= 1")
	}
	return &Queue[T]{
		capacity: capacity,
		notFull:  make(chan struct{}),
		notEmpty: make(chan struct{}),
	}
}

// Put appends v, blocking while the queue is full. It returns
// ctx.Err() if the caller gives up, or ErrClosed if the queue closes
// while it waits.
func (q *Queue[T]) Put(ctx context.Context, v T) error {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return ErrClosed
		}
		if len(q.buf) < q.capacity {
			q.buf = append(q.buf, v)
			q.wakeLocked(&q.notEmpty)
			q.mu.Unlock()
			return nil
		}
		space := q.notFull
		q.mu.Unlock()

		select {
		case <-space:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Get removes the oldest item, blocking while the queue is empty. It
// drains items buffered before Close and only returns ErrClosed once
// they are gone; ctx.Err() reports the caller giving up.
func (q *Queue[T]) Get(ctx context.Context) (T, error) {
	for {
		q.mu.Lock()
		if len(q.buf) > 0 {
			v := q.buf[0]
			if len(q.buf) == 1 {
				q.buf = q.buf[:0]
			} else {
				q.buf = q.buf[1:]
			}
			q.wakeLocked(&q.notFull)
			q.mu.Unlock()
			return v, nil
		}
		if q.closed {
			q.mu.Unlock()
			var zero T
			return zero, ErrClosed
		}
		item := q.notEmpty
		q.mu.Unlock()

		select {
		case <-item:
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}

// Close wakes every blocked caller: Puts with ErrClosed, Gets with
// whatever is left to drain and then ErrClosed. Close is idempotent.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.notEmpty)
	close(q.notFull)
}

// Len reports the number of buffered items.
func (q *Queue[T]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.buf)
}

// Cap reports the fixed capacity.
func (q *Queue[T]) Cap() int { return q.capacity }

// wakeLocked closes *ch (waking every waiter on it) and replaces it
// with a fresh channel for the next generation of waiters. Callers
// must hold q.mu. After Close, both channels are already closed and
// every waiter already awake, so there is nothing left to signal —
// and closing again would panic.
func (q *Queue[T]) wakeLocked(ch *chan struct{}) {
	if q.closed {
		return
	}
	close(*ch)
	*ch = make(chan struct{})
}
