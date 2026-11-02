package observer

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- bus ---

func TestBusDeliversToTopicSubscribersOnly(t *testing.T) {
	b := NewBus()
	defer b.Close()

	arrivals := b.Subscribe("arrivals", 8)
	defer arrivals.Unsubscribe()
	departures := b.Subscribe("departures", 8)
	defer departures.Unsubscribe()

	if n := b.Publish("arrivals", "UA1"); n != 1 {
		t.Fatalf("want 1 delivery, got %d", n)
	}
	b.Publish("departures", "BA2")

	select {
	case ev := <-arrivals.Chan():
		if ev.Payload != "UA1" || ev.Topic != "arrivals" {
			t.Fatalf("got %+v", ev)
		}
	default:
		t.Fatal("no event delivered")
	}
}

func TestBusFansOutToAllSubscribers(t *testing.T) {
	b := NewBus()
	defer b.Close()

	var subs []Subscription
	for range 5 {
		subs = append(subs, b.Subscribe("t", 8))
	}
	defer func() {
		for _, s := range subs {
			s.Unsubscribe()
		}
	}()

	if n := b.Publish("t", "hello"); n != 5 {
		t.Fatalf("want 5 deliveries, got %d", n)
	}
	for i, s := range subs {
		select {
		case ev := <-s.Chan():
			if ev.Payload != "hello" {
				t.Fatalf("sub %d got %+v", i, ev)
			}
		default:
			t.Fatalf("sub %d got nothing", i)
		}
	}
}

func TestBusNonBlockingPublishDropsAndCounts(t *testing.T) {
	b := NewBus()
	defer b.Close()

	// buffer 1, never drained.
	s := b.Subscribe("busy", 1)
	defer s.Unsubscribe()

	b.Publish("busy", "1")
	b.Publish("busy", "2") // buffer full → dropped
	b.Publish("busy", "3") // dropped

	published, delivered, dropped := b.Stats()
	if published != 3 || delivered != 1 || dropped != 2 {
		t.Fatalf("published=%d delivered=%d dropped=%d", published, delivered, dropped)
	}
}

// The critical property: a subscriber that never reads cannot wedge the
// publisher. Without non-blocking sends this test would hang forever.
func TestBusSlowSubscriberCannotStallPublisher(t *testing.T) {
	b := NewBus()
	defer b.Close()

	done := make(chan int, 1)
	go func() {
		count := 0
		for range 1000 {
			b.Publish("flood", "x")
			count++
		}
		done <- count
	}()

	select {
	case n := <-done:
		if n != 1000 {
			t.Fatalf("published %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publisher stalled on a slow subscriber")
	}
}

func TestUnsubscribeClosesChannelSoRangeTerminates(t *testing.T) {
	b := NewBus()
	defer b.Close()

	s := b.Subscribe("t", 4)
	b.Publish("t", "one")

	received := 0
	for range s.Chan() {
		received++
		if received == 1 {
			s.Unsubscribe()
		}
	}
	if received != 1 {
		t.Fatalf("range did not terminate: %d", received)
	}
	if b.Subscribers("t") != 0 {
		t.Fatal("unsubscribe did not detach")
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	b := NewBus()
	defer b.Close()
	s := b.Subscribe("t", 1)
	s.Unsubscribe()
	s.Unsubscribe() // must not panic (double close)
	if b.Subscribers("t") != 0 {
		t.Fatal("topic should be gone")
	}
}

func TestBusCloseTerminatesAllConsumers(t *testing.T) {
	b := NewBus()
	s1 := b.Subscribe("a", 4)
	s2 := b.Subscribe("b", 4)

	b.Close()
	for i, s := range []Subscription{s1, s2} {
		if _, ok := <-s.Chan(); ok {
			t.Fatalf("subscription %d channel should be closed", i)
		}
	}
	if b.Publish("a", "late") != 0 {
		t.Fatal("publish after close must deliver nothing")
	}
}

func TestConcurrentPublishAndUnsubscribeIsRaceFree(t *testing.T) {
	b := NewBus()
	defer b.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Publisher.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				b.Publish("t", "x")
			}
		}
	}()

	// Subscribers that subscribe and immediately unsubscribe from their own
	// receive loops — the classic deadlock scenario for a naive mutex.
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				s := b.Subscribe("t", 2)
				select {
				case <-s.Chan():
				default:
				}
				s.Unsubscribe()
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestTopicsAreListedAndSorted(t *testing.T) {
	b := NewBus()
	defer b.Close()
	z := b.Subscribe("zeta", 1)
	defer z.Unsubscribe()
	a := b.Subscribe("alpha", 1)
	defer a.Unsubscribe()

	got := b.Topics()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("want sorted [alpha zeta], got %v", got)
	}
}

func TestZeroValueSubscriptionIsSafe(t *testing.T) {
	var s Subscription
	if _, ok := <-s.Chan(); ok {
		t.Fatal("zero-value Chan should be closed")
	}
	s.Unsubscribe() // must not panic
}

func TestWaitForObservesAsyncDelivery(t *testing.T) {
	b := NewBus()
	defer b.Close()

	s := b.Subscribe("t", 4)
	defer s.Unsubscribe()

	go func() {
		time.Sleep(20 * time.Millisecond)
		b.Publish("t", "late")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := WaitFor(ctx, func() bool {
		select {
		case <-s.Chan():
			return true
		default:
			return false
		}
	})
	if err != nil {
		t.Fatalf("event never arrived: %v", err)
	}
}

// --- synchronous subject ---

func TestSubjectNotifiesInAttachmentOrder(t *testing.T) {
	s := NewSubject()
	var order []string
	s.Attach(func(Event) { order = append(order, "first") })
	s.Attach(func(Event) { order = append(order, "second") })

	if panics := s.Notify(Event{Payload: "x"}); len(panics) != 0 {
		t.Fatalf("unexpected panics: %v", panics)
	}
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("got %v", order)
	}
}

func TestSubjectDetachStopsNotification(t *testing.T) {
	s := NewSubject()
	calls := 0
	detach := s.Attach(func(Event) { calls++ })

	s.Notify(Event{})
	detach()
	detach() // idempotent
	s.Notify(Event{})

	if calls != 1 {
		t.Fatalf("want 1 call, got %d", calls)
	}
	if s.Count() != 0 {
		t.Fatalf("count=%d", s.Count())
	}
}

func TestDetachOneDoesNotRemoveAnother(t *testing.T) {
	s := NewSubject()
	a, b := 0, 0
	detachA := s.Attach(func(Event) { a++ })
	_ = s.Attach(func(Event) { b++ })

	detachA()
	s.Notify(Event{})

	if a != 0 {
		t.Fatalf("listener A still attached: %d", a)
	}
	if b != 1 {
		t.Fatalf("listener B was removed by A's detach: %d", b)
	}
}

// One bad subscriber must not take down the others — this is why Notify
// recovers per listener rather than once around the loop.
func TestSubjectPanicIsIsolated(t *testing.T) {
	s := NewSubject()
	healthy := 0
	s.Attach(func(Event) { panic("bad listener") })
	s.Attach(func(Event) { healthy++ })

	panics := s.Notify(Event{})
	if len(panics) != 1 {
		t.Fatalf("want 1 recovered panic, got %v", panics)
	}
	if !strings.Contains(panics[0].Error(), "bad listener") {
		t.Fatalf("got %v", panics[0])
	}
	if healthy != 1 {
		t.Fatalf("later listener was not notified: %d", healthy)
	}
}

func TestSubjectDetachDuringNotifyIsSafe(t *testing.T) {
	s := NewSubject()
	var detach func()
	detach = s.Attach(func(Event) {
		detach()
	})
	s.Notify(Event{})
	s.Notify(Event{})
	if s.Count() != 0 {
		t.Fatalf("count=%d", s.Count())
	}
}

func TestSubjectConcurrentAttachNotify(t *testing.T) {
	s := NewSubject()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				d := s.Attach(func(Event) {})
				s.Notify(Event{})
				d()
			}
		}()
	}
	wg.Wait()
}
