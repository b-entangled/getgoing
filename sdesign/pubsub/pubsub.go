// Package pubsub implements a fan-out broker with an explicit policy
// for slow subscribers — the design decision every notification system
// has to make and most code hides.
//
// Design decisions made explicit here:
//
//   - Publishers never block. Publish performs a non-blocking send per
//     subscriber: one stuck consumer must not stall every other
//     consumer or the publisher itself (head-of-line blocking is how
//     inboxes become outages).
//   - The cost lands on the slow subscriber, as a *policy* chosen at
//     subscribe time: DropOldest keeps the freshest window (live
//     feeds, telemetry — stale news is no news), DropNewest keeps the
//     earliest arrivals (event streams where the first commands
//     matter). Either way the loss is explicit and countable, not a
//     hidden OOM.
//   - Buffering belongs to the subscriber: each subscription owns its
//     own channel and its own capacity, so one consumer's appetite
//     cannot affect another's delivery.
//   - Unsubscribe/Close close the subscriber's channel, so a `range`
//     over it terminates — the standard Go end-of-stream signal.
package pubsub

import (
	"sync"
)

// Policy decides what a Publish does when a subscriber's buffer is
// full.
type Policy int

const (
	// DropOldest discards the oldest buffered item to make room for
	// the new one: the subscriber sees the most recent window.
	DropOldest Policy = iota
	// DropNewest discards the item being published: the subscriber
	// keeps what it already has.
	DropNewest
)

// Broker fans values out to subscribers of type T.
type Broker[T any] struct {
	mu     sync.Mutex
	subs   map[uint64]*subscription[T]
	nextID uint64
	closed bool
}

type subscription[T any] struct {
	ch     chan T
	policy Policy
}

// New returns an empty broker.
func New[T any]() *Broker[T] {
	return &Broker[T]{subs: make(map[uint64]*subscription[T])}
}

// Subscribe registers a subscriber with the given buffer capacity and
// overflow policy. The returned channel receives published values; the
// returned cancel func detaches the subscriber (idempotently) and
// closes the channel so a `range` ends.
func (b *Broker[T]) Subscribe(buffer int, policy Policy) (<-chan T, func()) {
	if buffer < 0 {
		panic("pubsub: buffer must be >= 0")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	id := b.nextID
	sub := &subscription[T]{ch: make(chan T, buffer), policy: policy}
	if b.closed {
		close(sub.ch)
	} else {
		b.subs[id] = sub
	}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if s, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(s.ch)
			}
		})
	}
	return sub.ch, cancel
}

// Publish delivers v to every subscriber without blocking, and
// returns how many subscribers accepted it (drops are not counted).
// After Close it delivers nothing.
func (b *Broker[T]) Publish(v T) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return 0
	}
	delivered := 0
	for _, s := range b.subs {
		if b.trySend(s, v) {
			delivered++
		}
	}
	return delivered
}

// Count reports the current subscriber count.
func (b *Broker[T]) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Close detaches every subscriber (closing their channels) and fails
// all further publishes. Close is idempotent.
func (b *Broker[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, s := range b.subs {
		close(s.ch)
		delete(b.subs, id)
	}
}

// trySend performs the non-blocking delivery for one subscriber,
// applying its policy when the buffer is full. Callers hold b.mu.
func (b *Broker[T]) trySend(s *subscription[T], v T) bool {
	select {
	case s.ch <- v:
		return true
	default:
	}

	switch s.policy {
	case DropOldest:
		select { // make room by evicting the oldest
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- v:
			return true
		default:
			return false // unbuffered and no receiver ready: dropped
		}
	case DropNewest:
		return false
	}
	return false
}
