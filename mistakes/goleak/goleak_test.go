package goleak

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the deadline passes.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// The leak, observed directly: the worker never exits.
func TestLeakedWorkerNeverExits(t *testing.T) {
	exited, release := BadSpawn()
	t.Cleanup(release) // test hygiene only — production callers lack this

	select {
	case <-exited:
		t.Fatal("worker exited: the leak premise is broken")
	case <-time.After(200 * time.Millisecond):
		// expected: still blocked
	}
}

// The symptom in the wild: leaked workers pile up. Ten spawns, ten extra
// goroutines, and they are still there after the fact.
func TestLeakedWorkersAccumulate(t *testing.T) {
	baseline := runtime.NumGoroutine()

	const n = 10
	releases := make([]func(), 0, n)
	t.Cleanup(func() {
		for _, r := range releases {
			r()
		}
	})
	for i := 0; i < n; i++ {
		_, release := BadSpawn()
		releases = append(releases, release)
	}

	if !waitFor(2*time.Second, func() bool { return runtime.NumGoroutine() >= baseline+n }) {
		t.Fatalf("expected at least %d goroutines, have %d", baseline+n, runtime.NumGoroutine())
	}
	// And they do not go away on their own.
	time.Sleep(200 * time.Millisecond)
	if got := runtime.NumGoroutine(); got < baseline+n {
		t.Fatalf("goroutines dropped to %d: the workers should still be stuck", got)
	}
}

// The fix: cancellation ends the worker, and the count comes back down.
func TestCancelledWorkerExitsAndCountRecovers(t *testing.T) {
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exited := GoodSpawn(ctx)

	// Alive while the context lives.
	select {
	case <-exited:
		t.Fatal("worker exited before cancellation")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case <-exited:
		// exited: clean shutdown
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after cancel")
	}

	if !waitFor(2*time.Second, func() bool { return runtime.NumGoroutine() <= baseline }) {
		t.Fatalf("goroutine count %d did not return to baseline %d", runtime.NumGoroutine(), baseline)
	}
}

// The bounded-work shape: the caller gets an answer instead of hanging.
func TestBoundedWorkReturnsAndDoesNotHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Work that finishes inside the budget returns its own result...
	err := GoodSpawnAndWait(ctx, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	// ...and work that overruns is reported, not awaited forever.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) }) // let the leftover goroutine go
	err = GoodSpawnAndWait(ctx2, func(context.Context) error {
		<-block
		return errors.New("never reached")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
