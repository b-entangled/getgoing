package clock

import (
	"testing"
	"time"
)

// Time does not move on its own: a manual clock only reports what the
// test advanced it by.
func TestManualTimeOnlyMovesWhenAdvanced(t *testing.T) {
	m := NewManual(time.Unix(1000, 0))

	if got := m.Now(); !got.Equal(time.Unix(1000, 0)) {
		t.Fatalf("Now = %v", got)
	}
	m.Advance(5 * time.Second)
	if got := m.Now(); !got.Equal(time.Unix(1005, 0)) {
		t.Fatalf("after Advance(5s) Now = %v", got)
	}
}

// A pending After must not fire early — and must fire the moment the
// deadline is reached, with no sleeping in between.
func TestAfterFiresOnlyWhenTheDeadlineIsReached(t *testing.T) {
	m := NewManual(time.Unix(0, 0))
	ch := m.After(2 * time.Second)

	m.Advance(1 * time.Second)
	select {
	case <-ch:
		t.Fatal("fired after 1s of a 2s deadline")
	default:
	}

	m.Advance(1 * time.Second)
	select {
	case at := <-ch:
		if want := time.Unix(2, 0); !at.Equal(want) {
			t.Fatalf("fired at %v, want %v", at, want)
		}
	default:
		t.Fatal("did not fire at the deadline")
	}
}

// Several sleepers all wake, earliest deadline first.
func TestWaitersFireInDeadlineOrder(t *testing.T) {
	m := NewManual(time.Unix(0, 0))
	late := m.After(3 * time.Second)
	early := m.After(1 * time.Second)
	mid := m.After(2 * time.Second)

	m.Advance(3 * time.Second)

	for i, ch := range []<-chan time.Time{early, mid, late} {
		select {
		case <-ch:
		default:
			t.Fatalf("waiter %d did not fire", i)
		}
	}
}

// The production clock is usable as itself — a smoke test that the
// interface is satisfied both ways.
func TestRealClockSatisfiesTheInterface(t *testing.T) {
	var c Clock = Real{}
	before := c.Now()
	after := c.Now()
	if after.Before(before) {
		t.Fatal("Real clock went backwards")
	}
	select {
	case <-c.After(time.Millisecond):
	case <-time.After(time.Second):
		t.Fatal("Real clock After(1ms) never fired")
	}
}
