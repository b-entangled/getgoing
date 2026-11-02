package mediator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// unknownCmd has no registered handler; the loop must log and move on.
type unknownCmd struct{}

func (unknownCmd) Type() string { return "unknown" }

// --- event loop ---

func runLoop(t *testing.T) (*EventLoop, context.CancelFunc) {
	t.Helper()
	e := NewEventLoop(32)
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	return e, cancel
}

func drain(e *EventLoop, n int) []Event {
	var out []Event
	for len(out) < n {
		select {
		case ev := <-e.Events():
			out = append(out, ev)
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
	return out
}

func TestLoopHandlesCommandsInOrder(t *testing.T) {
	e, cancel := runLoop(t)
	defer cancel()

	ctx := context.Background()
	for i := range 5 {
		if err := e.Publish(ctx, MoveVehicle{Vehicle: "v1", Position: i}); err != nil {
			t.Fatal(err)
		}
	}

	// Wait for the last command to be processed.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(e.Log()) == 5 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := e.Log(); len(got) != 5 {
		t.Fatalf("want 5 handled, got %v", got)
	}
	for i, typ := range e.Log() {
		if typ != "move" {
			t.Fatalf("log[%d]=%q", i, typ)
		}
	}
}

func TestLoopMaintainsStateSafely(t *testing.T) {
	e, cancel := runLoop(t)
	defer cancel()
	ctx := context.Background()

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				_ = e.Publish(ctx, MoveVehicle{Vehicle: "shared", Position: g*100 + i})
			}
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.Log()) == 400 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(e.Log()); got != 400 {
		t.Fatalf("want 400 handled, got %d", got)
	}
	if r, ok := e.RouteOf("shared"); !ok || r.Vehicle != "shared" {
		t.Fatalf("route not maintained: %+v %t", r, ok)
	}
}

func TestReactionEmitsEvent(t *testing.T) {
	e, cancel := runLoop(t)
	defer cancel()

	_ = e.Publish(context.Background(), FuelLow{Vehicle: "truck", Percent: 10})
	evs := drain(e, 1)
	if len(evs) != 1 || evs[0].Topic != "refuel" {
		t.Fatalf("got %+v", evs)
	}
	if !strings.Contains(evs[0].Text, "truck") {
		t.Fatalf("got %+v", evs[0])
	}
}

func TestReactionIgnoresNonCriticalFact(t *testing.T) {
	e, cancel := runLoop(t)
	defer cancel()

	_ = e.Publish(context.Background(), FuelLow{Vehicle: "truck", Percent: 80})
	evs := drain(e, 1)
	if len(evs) != 0 {
		t.Fatalf("expected no event, got %+v", evs)
	}
}

func TestUnknownCommandTypeIsIgnored(t *testing.T) {
	e, cancel := runLoop(t)
	defer cancel()

	_ = e.Publish(context.Background(), unknownCmd{})
	// Give the loop a moment; it must not crash or block.
	time.Sleep(20 * time.Millisecond)
	if got := e.Log(); len(got) != 1 || got[0] != "unknown" {
		t.Fatalf("got %v", got)
	}
}

func TestPublishHonoursCancellation(t *testing.T) {
	// Unbuffered input and no reader: Publish must give up on cancel.
	e := &EventLoop{in: make(chan Command), out: make(chan Event, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Publish(ctx, MoveVehicle{Vehicle: "v"}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestRunStopsOnCancelAndClosesEvents(t *testing.T) {
	e := NewEventLoop(4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
	if _, ok := <-e.Events(); ok {
		t.Fatal("Events channel should be closed")
	}
}

// --- hub ---

func TestHubFansOutToManySubscribers(t *testing.T) {
	h := NewHub()
	defer h.Close()

	ch1, cancel1 := h.Subscribe("arrivals", 8)
	defer cancel1()
	ch2, cancel2 := h.Subscribe("arrivals", 8)
	defer cancel2()
	_, cancel3 := h.Subscribe("departures", 8)
	defer cancel3()

	if got := h.Publish(Fact{Topic: "arrivals", Payload: "UA1"}); got != 2 {
		t.Fatalf("want 2 deliveries, got %d", got)
	}

	for i, ch := range []<-chan Fact{ch1, ch2} {
		select {
		case f := <-ch:
			if f.Payload != "UA1" {
				t.Fatalf("sub %d got %+v", i, f)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub %d got nothing", i)
		}
	}
}

func TestHubIsolatesTopics(t *testing.T) {
	h := NewHub()
	defer h.Close()

	arrivals, cancel := h.Subscribe("arrivals", 8)
	defer cancel()

	h.Publish(Fact{Topic: "departures", Payload: "x"})
	select {
	case f := <-arrivals:
		t.Fatalf("wrong topic delivered: %+v", f)
	default:
	}
}

func TestHubNonBlockingPublishDropsForSlowSubscribers(t *testing.T) {
	h := NewHub()
	defer h.Close()

	// buffer 1, never drained → second publish must be dropped, not block.
	_, cancel := h.Subscribe("busy", 1)
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- h.Publish(Fact{Topic: "busy", Payload: "1"}) }()
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("first publish delivered %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}

	go func() { done <- h.Publish(Fact{Topic: "busy", Payload: "2"}) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked")
	}
	if h.Dropped() == 0 {
		t.Fatal("drop was not counted")
	}
}

func TestHubCancelClosesChannel(t *testing.T) {
	h := NewHub()
	defer h.Close()

	ch, cancel := h.Subscribe("t", 4)
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("cancel should close the channel")
	}
	// Double cancel must not panic.
	cancel()
}

func TestHubCloseTerminatesAllSubscriptions(t *testing.T) {
	h := NewHub()
	ch1, _ := h.Subscribe("a", 4)
	ch2, _ := h.Subscribe("b", 4)

	h.Close()
	if _, ok := <-ch1; ok {
		t.Fatal("ch1 should be closed")
	}
	if _, ok := <-ch2; ok {
		t.Fatal("ch2 should be closed")
	}
	if h.Publish(Fact{Topic: "a"}) != 0 {
		t.Fatal("publish after close must deliver nothing")
	}
}

func TestHubTopicsListed(t *testing.T) {
	h := NewHub()
	defer h.Close()
	_, c1 := h.Subscribe("zeta", 1)
	defer c1()
	_, c2 := h.Subscribe("alpha", 1)
	defer c2()

	got := h.Topics()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("want sorted topics, got %v", got)
	}
}

// Components never reference each other — only the hub.
func TestComponentsAreDecoupled(t *testing.T) {
	h := NewHub()
	defer h.Close()

	// "sensor" publishes; "recorder" subscribes. Neither knows the other.
	recorderCh, cancel := h.Subscribe("sensor", 16)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 3 {
			h.Publish(Fact{Topic: "sensor", Payload: "reading"})
			_ = i
		}
	}()

	got := 0
	for f := range recorderCh {
		if f.Topic != "sensor" {
			t.Fatalf("bad topic %+v", f)
		}
		got++
		if got == 3 {
			break
		}
	}
	wg.Wait()
	if got != 3 {
		t.Fatalf("got %d", got)
	}
}
