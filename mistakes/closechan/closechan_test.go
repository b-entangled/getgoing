package closechan

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// recoverPanic runs f and returns the panic message, or "" if none.
func recoverPanic(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return msg
}

func TestClosingTwicePanics(t *testing.T) {
	msg := recoverPanic(func() { CloseTwice(make(chan int)) })
	if msg == "" {
		t.Fatal("expected a panic on double close")
	}
	if want := "close of closed channel"; !strings.Contains(msg, want) {
		t.Fatalf("panic = %q, want it to mention %q", msg, want)
	}
}

// The ownership bug detonates in the *producer's* goroutine, far from the
// code that closed the channel.
func TestReceiverClosingMakesTheProducerPanic(t *testing.T) {
	c := make(chan int, 1)
	ReceiverCloses(c) // the receiver closed it

	caught := make(chan string, 1)
	go func() {
		caught <- recoverPanic(func() { c <- 1 }) // producer sends
	}()

	select {
	case msg := <-caught:
		if want := "send on closed channel"; !strings.Contains(msg, want) {
			t.Fatalf("panic = %q, want %q", msg, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("producer neither sent nor panicked")
	}
}

func TestReceivingFromClosedChannelIsSafe(t *testing.T) {
	// Close an empty buffered channel: receive yields the zero value with
	// ok == false, and never panics.
	c := make(chan int, 1)
	v, ok := ReceiveFromClosed(c)
	if ok || v != 0 {
		t.Fatalf("got (%d, %v), want (0, false)", v, ok)
	}

	// Buffered values are still delivered before the stream ends.
	d := make(chan int, 1)
	d <- 7
	close(d)
	if v, ok := <-d; !ok || v != 7 {
		t.Fatalf("first receive = (%d, %v), want (7, true)", v, ok)
	}
	if v, ok := <-d; ok || v != 0 {
		t.Fatalf("second receive = (%d, %v), want (0, false)", v, ok)
	}
}

// The fix: one owner, many callers, exactly one close.
func TestSafeCloserClosesExactlyOnceFromManyGoroutines(t *testing.T) {
	s := NewSafeCloser()

	const n = 32
	finished := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			s.Finish() // every caller races; sync.Once decides
			finished <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-finished
	}

	// The channel is closed: a receive returns immediately.
	select {
	case _, ok := <-s.Channel():
		if ok {
			t.Fatal("channel should be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel never closed")
	}
	// Calling Finish again is still harmless.
	s.Finish()
}
