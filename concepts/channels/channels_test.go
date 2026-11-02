package channels

import (
	"reflect"
	"testing"
	"time"
)

// Unbuffered = rendezvous: the sender cannot finish until we receive.
func TestUnbufferedSendBlocksUntilReceive(t *testing.T) {
	ch := make(chan int)
	sent := make(chan struct{})
	go func() {
		ch <- 42
		close(sent)
	}()

	// The send cannot have completed: nothing has received yet.
	select {
	case <-sent:
		t.Fatal("unbuffered send completed before any receive")
	default:
	}

	if v := <-ch; v != 42 {
		t.Fatalf("received %d, want 42", v)
	}
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not proceed after the receive")
	}
}

// Buffered sends succeed until the buffer is full.
func TestBufferedChannelAcceptsUpToCapacity(t *testing.T) {
	ch := make(chan int, 3)
	ch <- 1
	ch <- 2
	ch <- 3
	if len(ch) != 3 {
		t.Fatalf("len = %d, want 3", len(ch))
	}

	// A fourth send must block: the buffer is full.
	full := make(chan struct{})
	go func() {
		ch <- 4
		close(full)
	}()
	select {
	case <-full:
		t.Fatal("send on full buffer completed without a receiver")
	default:
	}

	if v := <-ch; v != 1 {
		t.Fatalf("FIFO order broken: got %d", v)
	}
	select {
	case <-full:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked send did not proceed after a receive")
	}
}

// The sender owns close: after close, receives yield zero values and
// range terminates.
func TestClosedChannelYieldsZeroValueThenFalse(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 7
	close(ch)

	if v, ok := <-ch; !ok || v != 7 {
		t.Fatalf("first receive = (%d, %v), want (7, true)", v, ok)
	}
	v, ok := <-ch
	if ok || v != 0 {
		t.Fatalf("receive on closed = (%d, %v), want (0, false)", v, ok)
	}
	// Receiving again is still fine — closed channels never block.
	if _, ok := <-ch; ok {
		t.Fatal("third receive must still report ok=false")
	}
}

func TestRangeTerminatesWhenSenderCloses(t *testing.T) {
	jobs := []int{1, 2, 3, 4}
	ch := make(chan int)
	done := make(chan []int, 1)
	go func() { done <- Consume(ch) }()
	Produce(jobs, ch)

	select {
	case got := <-done:
		if !reflect.DeepEqual(got, jobs) {
			t.Fatalf("got %v, want %v", got, jobs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Consume never returned: range did not terminate")
	}
}

// A nil channel is never ready — in a select, the default wins.
func TestNilChannelIsNeverReady(t *testing.T) {
	var nilCh <-chan int
	if Ready(nilCh) {
		t.Fatal("receive from nil channel must never be ready")
	}
	if _, ok := TryReceive(nilCh); ok {
		t.Fatal("nil channel must not deliver")
	}
}

// select picks a case that is already ready, deterministically.
func TestSelectTakesTheReadyCase(t *testing.T) {
	ready := make(chan int, 1)
	ready <- 9
	var nilCh <-chan int // never ready: select must skip it

	select {
	case v := <-ready:
		if v != 9 {
			t.Fatalf("got %d, want 9", v)
		}
	case <-nilCh:
		t.Fatal("nil case must never be chosen")
	case <-time.After(time.Second):
		t.Fatal("select blocked despite a ready case")
	}
}

func TestTimeoutCaseFiresWhenNoValueArrives(t *testing.T) {
	ch := make(chan int) // nobody sends
	select {
	case v := <-ch:
		t.Fatalf("unexpected value %d", v)
	case <-time.After(50 * time.Millisecond):
		// expected: the timeout case fired
	}
}
