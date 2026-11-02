package pubsub

import (
	"testing"
	"time"
)

// Fan-out: one publish, every subscriber receives it.
func TestPublishFansOutToEverySubscriber(t *testing.T) {
	b := New[int]()
	defer b.Close()

	ch1, cancel1 := b.Subscribe(4, DropOldest)
	defer cancel1()
	ch2, cancel2 := b.Subscribe(4, DropOldest)
	defer cancel2()
	ch3, cancel3 := b.Subscribe(4, DropOldest)
	defer cancel3()

	if n := b.Publish(42); n != 3 {
		t.Fatalf("Publish delivered to %d, want 3", n)
	}
	for i, ch := range []<-chan int{ch1, ch2, ch3} {
		select {
		case v := <-ch:
			if v != 42 {
				t.Fatalf("subscriber %d got %d, want 42", i, v)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d never received", i)
		}
	}
}

// Slow consumer, DropOldest: the newest window survives — this is the
// live-feed contract (freshest wins).
func TestDropOldestKeepsTheNewestWindow(t *testing.T) {
	b := New[int]()
	defer b.Close()

	ch, cancel := b.Subscribe(2, DropOldest)
	defer cancel()

	for v := 1; v <= 4; v++ {
		b.Publish(v)
	}

	// Buffer holds exactly the last two published values.
	got := []int{<-ch, <-ch}
	if got[0] != 3 || got[1] != 4 {
		t.Fatalf("buffer = %v, want [3 4]: oldest two must have been dropped", got)
	}
}

// Slow consumer, DropNewest: what already arrived stays — this is the
// catch-up contract (earliest command wins).
func TestDropNewestKeepsWhatAlreadyArrived(t *testing.T) {
	b := New[int]()
	defer b.Close()

	ch, cancel := b.Subscribe(2, DropNewest)
	defer cancel()

	for v := 1; v <= 4; v++ {
		b.Publish(v)
	}

	got := []int{<-ch, <-ch}
	if got[0] != 1 || got[1] != 2 {
		t.Fatalf("buffer = %v, want [1 2]: newest two must have been dropped", got)
	}
}

// A cancelled subscriber stops receiving and its channel closes —
// `range` over it terminates, and later publishes skip it.
func TestCancelStopsDeliveryAndClosesTheChannel(t *testing.T) {
	b := New[int]()
	defer b.Close()

	ch, cancel := b.Subscribe(4, DropOldest)
	b.Publish(1)
	if _, ok := <-ch; !ok {
		t.Fatal("channel closed before cancel")
	}

	cancel()
	cancel() // idempotent

	if _, ok := <-ch; ok {
		t.Fatal("channel still open after cancel: range would not end")
	}
	if n := b.Publish(2); n != 0 {
		t.Fatalf("Publish delivered to %d subscribers after cancel, want 0", n)
	}
}

// Close is end-of-stream for everyone: all channels close, publishes
// become no-ops, nothing panics.
func TestCloseEndsEverySubscriber(t *testing.T) {
	b := New[int]()
	ch1, _ := b.Subscribe(1, DropOldest)
	ch2, _ := b.Subscribe(1, DropOldest)
	b.Publish(1) // buffered before close

	b.Close()
	b.Close() // idempotent

	for i, ch := range []<-chan int{ch1, ch2} {
		// drain whatever was buffered, then the close must surface
		for {
			_, ok := <-ch
			if !ok {
				break
			}
		}
		_ = i
	}
	if n := b.Publish(9); n != 0 {
		t.Fatalf("Publish after Close delivered to %d, want 0", n)
	}
}

// Under concurrent publishers and subscribers the broker stays
// consistent: no send on a closed channel, no lost cancel (-race
// verifies the bookkeeping).
func TestConcurrentPublishSubscribeCancel(t *testing.T) {
	b := New[int]()
	defer b.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			b.Publish(i)
		}
	}()

	for i := 0; i < 50; i++ {
		ch, cancel := b.Subscribe(8, DropOldest)
		go func(ch <-chan int) {
			for range ch { // drain until cancel closes it
			}
		}(ch)
		cancel()
	}
	<-done
}
