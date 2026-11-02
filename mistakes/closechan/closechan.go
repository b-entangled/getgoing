// Package closechan documents who may close a channel — and what happens
// when the rule is broken.
//
// The rule: **the sender closes, the receiver does not.** Whoever will
// never send again is the only side allowed to call close, because:
//
//   - close on an already-closed channel panics ("close of closed
//     channel");
//   - send on a closed channel panics ("send on closed channel");
//   - receive on a closed channel is perfectly safe: it yields the zero
//     value with ok == false, which is exactly how range terminates.
//
// Both panics are ordinary runtime panics — recoverable in tests, fatal
// in production. When several parties might finish sending, don't race
// the close: pick one owner, or use a sync.Once, or signal completion
// with a separate done channel instead of closing the data channel.
//
// Domain: a producer feeding a consumer over one channel.
package closechan

import "sync"

// CloseTwice is the mistake in one line: closing twice panics.
func CloseTwice(c chan int) {
	close(c)
	close(c) // panic: close of closed channel
}

// ReceiverCloses is the ownership mistake: a receiver closes the data
// channel while the real producer may still be sending. The producer's
// next send panics — usually in a different goroutine, which is why this
// bug is so hard to trace back to the closer.
func ReceiverCloses(c chan int) {
	close(c) // the receiver is not the owner
}

// ReceiveFromClosed shows the *safe* side: receiving from a closed
// channel never panics and reports ok == false.
func ReceiveFromClosed(c chan int) (v int, ok bool) {
	close(c)
	v, ok = <-c
	return v, ok
}

// SafeCloser lets many goroutines "finish" without racing: only the
// first call closes, the rest are no-ops. This is the fix when you
// genuinely cannot nominate a single owner.
type SafeCloser struct {
	once sync.Once
	c    chan struct{}
}

// NewSafeCloser builds a closer over an unbuffered channel.
func NewSafeCloser() *SafeCloser {
	return &SafeCloser{c: make(chan struct{})}
}

// Channel returns the channel this closer owns.
func (s *SafeCloser) Channel() <-chan struct{} { return s.c }

// Finish closes the channel exactly once, no matter how many callers.
func (s *SafeCloser) Finish() { s.once.Do(func() { close(s.c) }) }
