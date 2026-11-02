package ratelimit

import (
	"testing"
	"time"

	"github.com/b-entangled/projects/learnings/sdesign/clock"
)

// The bucket starts full: the whole burst is available immediately,
// then admission stops dead.
func TestBucketAllowsTheBurstThenDenies(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	b := NewBucket(clk, 1, 5) // 1 token/s, burst 5

	for i := 0; i < 5; i++ {
		if !b.Allow() {
			t.Fatalf("burst token %d denied", i+1)
		}
	}
	if b.Allow() {
		t.Fatal("6th event allowed with an empty bucket")
	}
}

// Tokens refill proportionally to elapsed time — advancing 2s at 1/s
// grants exactly 2 more events.
func TestBucketRefillsWithElapsedTime(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	b := NewBucket(clk, 1, 2) // 1 token/s, burst 2

	if !b.Allow() || !b.Allow() {
		t.Fatal("initial burst of 2 denied")
	}
	if b.Allow() {
		t.Fatal("third event must wait for refill")
	}

	clk.Advance(2 * time.Second) // exactly 2 tokens back (capped at burst 2)
	if !b.Allow() {
		t.Fatal("first refilled token not granted after 2s")
	}
	if !b.Allow() {
		t.Fatal("second refilled token not granted after 2s")
	}
	if b.Allow() {
		t.Fatal("third event allowed: only 2 tokens refilled")
	}
}

// Refill is capped at the burst: idle time does not accumulate an
// unbounded credit line.
func TestBucketRefillIsCappedAtBurst(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	b := NewBucket(clk, 10, 3)

	clk.Advance(time.Hour) // an hour of idling at 10/s
	if n := b.Tokens(); n > 3 {
		t.Fatalf("tokens = %v, want cap of 3", n)
	}
	allowed := 0
	for b.Allow() {
		allowed++
	}
	if allowed != 3 {
		t.Fatalf("allowed %d events, want exactly the burst of 3", allowed)
	}
}

// Whole-batch admission: a 3-event request is denied when only 2
// tokens remain, and no tokens are consumed by the denial.
func TestAllowNGrantsOrDeniesAsABatch(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	b := NewBucket(clk, 1, 2)

	if !b.AllowN(2) {
		t.Fatal("batch of 2 denied with 2 tokens")
	}
	if b.AllowN(1) {
		t.Fatal("event allowed with an empty bucket")
	}
	clk.Advance(time.Second)
	if b.AllowN(3) { // only 1 token available
		t.Fatal("batch of 3 granted with 1 token")
	}
	if !b.AllowN(1) {
		t.Fatal("denied batch must not consume the token")
	}
}

// The window admits exactly `limit` events, refuses the next, and the
// refusal consumes nothing — the count stays put.
func TestWindowAllowsLimitThenDenies(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	w := NewWindow(clk, 3, time.Minute)

	for i := 0; i < 3; i++ {
		if !w.Allow() {
			t.Fatalf("event %d denied inside the limit", i+1)
		}
	}
	if w.Allow() {
		t.Fatal("4th event admitted inside the same window")
	}
	if got := w.Count(); got != 3 {
		t.Fatalf("Count = %d after denial, want 3", got)
	}
}

// Once the whole burst ages out of the window, admission resumes —
// this is what makes the bound *sliding*, not fixed.
func TestWindowSlidesWhenOldEventsAgeOut(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	w := NewWindow(clk, 2, time.Minute)

	w.Allow()
	w.Allow()
	if w.Allow() {
		t.Fatal("3rd event admitted while 2 are inside the window")
	}

	clk.Advance(59 * time.Second) // still both inside
	if w.Allow() {
		t.Fatal("3rd event admitted: old events have not aged out yet")
	}

	clk.Advance(2 * time.Second) // first event now 61s old
	if !w.Allow() {
		t.Fatal("event denied after the oldest aged out of the window")
	}
}

// The window bound holds across arbitrary interleavings of time and
// events: in any sliding window the count never exceeds the limit.
func TestWindowNeverExceedsLimitAcrossSlidingTime(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	w := NewWindow(clk, 3, 10*time.Second)

	// A pattern that would break a fixed-window counter: 3 events at
	// the end of one window and 3 at the start of the next — 6 in two
	// real-time seconds.
	if !w.Allow() || !w.Allow() || !w.Allow() {
		t.Fatal("first batch denied")
	}
	clk.Advance(9 * time.Second)
	allowed := 0
	for i := 0; i < 3; i++ {
		if w.Allow() {
			allowed++
		}
	}
	if allowed != 0 {
		t.Fatalf("sliding window admitted %d of 6 events inside 10s (limit 3)", allowed)
	}
	clk.Advance(10 * time.Second) // everything aged out
	if !w.Allow() {
		t.Fatal("window did not slide after all events aged out")
	}
}
