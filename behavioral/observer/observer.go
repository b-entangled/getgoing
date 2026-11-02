// Package observer demonstrates the Observer / publish-subscribe pattern.
//
// Observers register interest in events and are notified when they occur.
// The GoF form — an Observer interface with an update() method and subjects
// holding a slice of them — works in Go, but the channel form is more
// idiomatic: subscribers receive on their own channel, which gives them
// buffering, backpressure choice, and a natural way to unsubscribe (close).
//
// Design decisions made explicit here, because each is a correctness issue:
//   - sends are non-blocking, so one slow subscriber cannot stall the
//     publisher; drops are *counted*, so loss is observable, not silent;
//   - unsubscribe closes the channel, so `range ch` terminates;
//   - Publish reads under RWMutex.RLock while remove writes under Lock, so
//     a consumer unsubscribing from inside its own receive loop cannot
//     deadlock against a concurrent publish;
//   - the subject's synchronous form recovers panics per listener, so one
//     bad subscriber never takes down the rest.
//
// Two shapes: a typed multi-topic Bus, and a minimal synchronous Subject
// for in-process notification where delivery must complete before the
// publisher continues.
package observer

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Event is what publishers send.
type Event struct {
	Topic   string
	Payload string
	At      time.Time
}

// --- async bus ---

// Bus is a topic-based publish/subscribe hub.
type Bus struct {
	mu     sync.RWMutex
	subs   map[string][]*subscription
	closed bool
	nextID uint64 // written only under the write lock

	published atomic.Int64
	delivered atomic.Int64
	dropped   atomic.Int64
}

type subscription struct {
	id     uint64
	ch     chan Event
	cancel func()
	once   sync.Once
}

// Subscription is a subscriber's handle. The zero value is not usable;
// obtain one from Bus.Subscribe.
type Subscription struct {
	sub *subscription
}

// Chan returns the receive-only channel. It is closed on Unsubscribe or
// when the bus is closed, so consumers may `range` over it safely.
func (s Subscription) Chan() <-chan Event {
	if s.sub == nil {
		c := make(chan Event)
		close(c)
		return c
	}
	return s.sub.ch
}

// Unsubscribe detaches the subscriber and closes its channel.
// Idempotent and safe from any goroutine, including the consumer's own
// receive loop.
func (s Subscription) Unsubscribe() {
	if s.sub != nil {
		s.sub.cancel()
	}
}

// NewBus builds an empty bus.
func NewBus() *Bus { return &Bus{subs: make(map[string][]*subscription)} }

// Subscribe registers a listener for topic with a buffered channel.
// buffer 0 means unbuffered: the publisher will drop unless the consumer is
// already waiting on receive — choose deliberately.
func (b *Bus) Subscribe(topic string, buffer int) Subscription {
	if buffer < 0 {
		buffer = 0
	}
	sub := &subscription{id: 0, ch: make(chan Event, buffer)}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(sub.ch)
		return Subscription{}
	}
	b.nextID++
	sub.id = b.nextID
	b.subs[topic] = append(b.subs[topic], sub)
	b.mu.Unlock()

	sub.cancel = func() { b.remove(topic, sub) }
	return Subscription{sub: sub}
}

// Publish delivers an event to every subscriber of its topic and returns
// how many received it. Never blocks: a subscriber whose buffer is full has
// that event dropped and counted.
func (b *Bus) Publish(topic, payload string) int {
	ev := Event{Topic: topic, Payload: payload, At: time.Now()}
	b.published.Add(1)

	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return 0
	}

	delivered := 0
	for _, s := range b.subs[topic] {
		select {
		case s.ch <- ev:
			delivered++
			b.delivered.Add(1)
		default:
			b.dropped.Add(1)
		}
	}
	return delivered
}

// remove detaches a subscription and closes its channel.
func (b *Bus) remove(topic string, sub *subscription) {
	sub.once.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		list := b.subs[topic]
		for i, s := range list {
			if s == sub {
				b.subs[topic] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(b.subs[topic]) == 0 {
			delete(b.subs, topic)
		}
		close(sub.ch)
	})
}

// Close shuts the bus down and closes every subscriber channel, so all
// consumer `range` loops exit. Publish calls after Close deliver nothing.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, list := range b.subs {
		for _, s := range list {
			s := s
			s.once.Do(func() { close(s.ch) })
		}
	}
	b.subs = nil
}

// Stats reports published / delivered / dropped counts. Dropped > 0 means
// subscribers could not keep up — always surface this metric.
func (b *Bus) Stats() (published, delivered, dropped int) {
	return int(b.published.Load()), int(b.delivered.Load()), int(b.dropped.Load())
}

// Topics lists live topics, sorted.
func (b *Bus) Topics() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.subs))
	for t := range b.subs {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Subscribers reports how many listeners a topic currently has.
func (b *Bus) Subscribers(topic string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[topic])
}

// --- synchronous subject (classic GoF shape) ---

// Listener is called synchronously by Notify. It must not block: a slow
// listener stalls the subject. For anything asynchronous use Bus instead.
type Listener func(e Event)

// reg pairs a listener with its identity so it can be detached. Function
// values are not comparable in Go, so identity comes from a monotonic ID —
// this is the workaround the GoF pattern needs in a language without
// object identity.
type reg struct {
	id uint64
	fn Listener
}

// Subject is the classic subject: a list of listeners notified in
// attachment order. Appropriate when notification must complete before the
// publisher continues (cache invalidation, audit logging in-request).
type Subject struct {
	mu     sync.RWMutex
	regs   []reg
	nextID uint64
}

// NewSubject builds an empty subject.
func NewSubject() *Subject { return &Subject{} }

// Attach registers a listener and returns a detach func. The returned func
// is idempotent — calling it twice does not remove another listener.
func (s *Subject) Attach(l Listener) (detach func()) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.regs = append(s.regs, reg{id: id, fn: l})
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, r := range s.regs {
				if r.id == id {
					s.regs = append(s.regs[:i], s.regs[i+1:]...)
					return
				}
			}
		})
	}
}

// Notify calls every listener in attachment order. A panicking listener is
// recovered and reported, and notification continues with the rest.
func (s *Subject) Notify(e Event) []error {
	s.mu.RLock()
	snapshot := append([]reg(nil), s.regs...)
	s.mu.RUnlock()

	var panics []error
	for _, r := range snapshot {
		if err := callRecover(r.fn, e); err != nil {
			panics = append(panics, err)
		}
	}
	return panics
}

// Count reports the number of attached listeners.
func (s *Subject) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.regs)
}

// callRecover invokes fn, converting a panic into an error.
func callRecover(fn Listener, e Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("observer: listener panicked: %v", r)
		}
	}()
	fn(e)
	return nil
}

// --- shared helpers ---

// WaitFor polls pred until true or ctx expires. It lets tests observe
// asynchronous delivery without arbitrary sleeps.
func WaitFor(ctx context.Context, pred func() bool) error {
	t := time.NewTicker(time.Millisecond)
	defer t.Stop()
	for {
		if pred() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
