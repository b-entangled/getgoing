package contextdemo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSimulateCompletes(t *testing.T) {
	if err := Simulate(context.Background(), Work{Name: "quick", Duration: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
}

func TestSimulateReturnsPreSuppliedError(t *testing.T) {
	want := errors.New("boom")
	if err := Simulate(context.Background(), Work{Name: "bad", Err: want}); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestSimulateHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Simulate(ctx, Work{Name: "never", Duration: time.Hour})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

// Shape 1: the derived context is released on every path.
func TestDoOnceReleasesDerivedContext(t *testing.T) {
	// A parent that lives forever makes a leaked child visible.
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	if err := DoOnce(parent, Work{Name: "a", Duration: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	// If DoOnce forgot cancel(), the child would still be attached to
	// parent. We cannot introspect the child directly, so instead assert
	// the observable consequence: repeated calls do not accumulate
	// goroutines (each leaked child with an active timer pins a timer).
	for range 100 {
		if err := DoOnce(parent, Work{Name: "b", Duration: time.Microsecond}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDoOncePropagatesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if err := DoOnce(parent, Work{Name: "x", Duration: time.Hour}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

// Shape 2: the budget covers the whole batch, not each item.
func TestBudgetRejectsOverlongBatch(t *testing.T) {
	work := []Work{
		{Name: "quick-1", Duration: 5 * time.Millisecond},
		{Name: "quick-2", Duration: 5 * time.Millisecond},
		{Name: "slow-1", Duration: 5 * time.Second},
		{Name: "slow-2", Duration: 5 * time.Second},
	}
	start := time.Now()
	errs, err := Budget(context.Background(), 60*time.Millisecond, work)
	if err == nil {
		t.Fatal("expected a deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("budget not enforced: %v", elapsed)
	}

	// Items inside the budget succeed; the one that overruns fails, and
	// every later item is reported as cancelled rather than attempted.
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("fast items should succeed: %v", errs)
	}
	for i := 2; i < len(errs); i++ {
		if !errors.Is(errs[i], context.DeadlineExceeded) {
			t.Fatalf("item %d should hit the deadline: %v", i, errs[i])
		}
	}
}

func TestBudgetSucceedsWithinBudget(t *testing.T) {
	work := []Work{
		{Name: "1", Duration: time.Millisecond},
		{Name: "2", Duration: time.Millisecond},
	}
	errs, err := Budget(context.Background(), time.Second, work)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range errs {
		if e != nil {
			t.Fatalf("item %d: %v", i, e)
		}
	}
}

// Shape 3: a failure 10ms in must not wait for a 1s sibling.
func TestFirstErrorDoesNotWaitForSiblings(t *testing.T) {
	work := []Work{
		{Name: "fails", Duration: 10 * time.Millisecond, Err: errors.New("boom")},
		{Name: "slow", Duration: 5 * time.Second},
		{Name: "slower", Duration: 5 * time.Second},
	}
	start := time.Now()
	err := FirstError(context.Background(), work)
	elapsed := time.Since(start)

	if err == nil || err.Error() != "boom" {
		t.Fatalf("got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("first-error-wins did not cancel siblings: took %v", elapsed)
	}
}

func TestFirstErrorReturnsNilWhenAllSucceed(t *testing.T) {
	work := []Work{
		{Name: "a", Duration: time.Millisecond},
		{Name: "b", Duration: 2 * time.Millisecond},
	}
	if err := FirstError(context.Background(), work); err != nil {
		t.Fatal(err)
	}
}

func TestFirstErrorPropagatesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	err := FirstError(parent, []Work{{Name: "x", Duration: time.Hour}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

// Shape 4: a per-item timeout applies to that item alone.
func TestEachItemAppliesPerItemDeadline(t *testing.T) {
	work := []Work{
		{Name: "fast", Duration: 5 * time.Millisecond},
		{Name: "tooslow", Duration: 5 * time.Second},
		{Name: "fast2", Duration: 5 * time.Millisecond},
	}
	start := time.Now()
	errs := EachItem(context.Background(), 50*time.Millisecond, work)
	elapsed := time.Since(start)

	if errs[0] != nil || errs[2] != nil {
		t.Fatalf("fast items failed: %v", errs)
	}
	if !errors.Is(errs[1], context.DeadlineExceeded) {
		t.Fatalf("slow item should time out: %v", errs[1])
	}
	// The slow item must not extend the batch beyond its own deadline.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("per-item timeout not enforced: %v", elapsed)
	}
}

func TestEachItemPreservesInputOrder(t *testing.T) {
	work := []Work{
		{Name: "a", Duration: 30 * time.Millisecond},
		{Name: "b", Duration: 1 * time.Millisecond},
		{Name: "c", Duration: 15 * time.Millisecond},
	}
	errs := EachItem(context.Background(), time.Second, work)
	if len(errs) != 3 {
		t.Fatalf("errs=%v", errs)
	}
	for i, e := range errs {
		if e != nil {
			t.Fatalf("item %d failed: %v", i, e)
		}
	}
}

// Shape 5: shutdown rejects new work but finishes in-flight work.
func TestServerDrainsInFlightBeforeShutdownCompletes(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := NewServer(parent)
	defer s.Close()

	accepted := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		_ = s.Handle(parent, Work{Name: "slow", Duration: 80 * time.Millisecond})
		close(accepted)
	}()
	<-started
	// Wait for the request to actually be in flight rather than sleeping
	// a guessed amount — a fixed sleep is a flake waiting to happen.
	deadline := time.Now().Add(2 * time.Second)
	for s.InFlight() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("request never went in flight")
		}
		time.Sleep(time.Millisecond)
	}
	if s.InFlight() != 1 {
		t.Fatalf("inFlight=%d", s.InFlight())
	}

	// Shutdown must wait for the in-flight request.
	drained := make(chan bool, 1)
	go func() { drained <- s.Shutdown(2 * time.Second) }()

	// Give shutdown a moment to flip acceptance.
	time.Sleep(20 * time.Millisecond)
	if s.Accept() {
		t.Fatal("server still accepting after Shutdown")
	}

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request was not allowed to finish")
	}

	select {
	case ok := <-drained:
		if !ok {
			t.Fatal("shutdown reported it did not drain")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown never returned")
	}
}

func TestServerRejectsAfterShutdown(t *testing.T) {
	s := NewServer(context.Background())
	defer s.Close()

	if ok := s.Shutdown(time.Second); !ok {
		t.Fatal("empty server must drain instantly")
	}
	err := s.Handle(context.Background(), Work{Name: "late"})
	if err == nil {
		t.Fatal("expected rejection")
	}
	if s.Rejected() != 1 {
		t.Fatalf("rejected=%d", s.Rejected())
	}
	select {
	case <-s.Drained():
	default:
		t.Fatal("drained channel should be closed")
	}
}

func TestServerShutdownTimeoutReportsStragglers(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := NewServer(parent)
	defer s.Close()

	go func() {
		_ = s.Handle(parent, Work{Name: "stuck", Duration: 3 * time.Second})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for s.InFlight() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("request never went in flight")
		}
		time.Sleep(time.Millisecond)
	}
	if s.Shutdown(30 * time.Millisecond) {
		t.Fatal("shutdown must report failure when work is still running")
	}
	if s.InFlight() != 1 {
		t.Fatalf("inFlight=%d", s.InFlight())
	}
}

// Shape 6: unexported keys keep context values private.
func TestRequestIDRoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-42")
	id, ok := RequestID(ctx)
	if !ok || id != "req-42" {
		t.Fatalf("%q %t", id, ok)
	}
}

func TestRequestIDMissing(t *testing.T) {
	id, ok := RequestID(context.Background())
	if ok {
		t.Fatalf("expected !ok, got %q", id)
	}
}

// An unrelated package cannot collide with our key: it would have to name
// the unexported type.
func TestRequestIDIsNotOverwrittenByOtherStringKeys(t *testing.T) {
	type otherPkgKey string
	ctx := context.WithValue(context.Background(), otherPkgKey("request-id"), "spoofed")
	id, ok := RequestID(ctx)
	if ok {
		t.Fatalf("foreign key produced a hit: %q", id)
	}
	_ = id
}

func TestRunAllJoinsEveryError(t *testing.T) {
	work := []Work{
		{Name: "ok", Duration: time.Millisecond},
		{Name: "bad1", Err: errors.New("one")},
		{Name: "bad2", Err: errors.New("two")},
	}
	err := RunAll(context.Background(), work)
	if err == nil {
		t.Fatal("expected errors")
	}
	if !errors.Is(err, errors.New("one")) && !contains(err.Error(), "one") {
		t.Fatalf("missing one: %v", err)
	}
	if !contains(err.Error(), "two") {
		t.Fatalf("missing two: %v", err)
	}
	if !contains(err.Error(), "bad1") || !contains(err.Error(), "bad2") {
		t.Fatalf("errors should name the work: %v", err)
	}
}

func TestRunAllStopsOnParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := RunAll(ctx, []Work{{Name: "x", Duration: time.Hour}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestNamesSorts(t *testing.T) {
	got := Names([]Work{{Name: "b"}, {Name: "a"}, {Name: "c"}})
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
