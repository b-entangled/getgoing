package idempotency

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/b-entangled/projects/learnings/sdesign/clock"
)

// The core guarantee: however many callers race with the same key,
// the work runs exactly once and everyone gets the same answer.
func TestConcurrentCallersShareOneExecution(t *testing.T) {
	d := New[string](time.Minute, nil)
	var runs atomic.Int64

	const callers = 20
	results := make([]string, callers)
	errs := make([]error, callers)

	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait() // maximise the race window
			results[i], errs[i] = d.Do(context.Background(), "charge-42", func(context.Context) (string, error) {
				runs.Add(1)
				time.Sleep(20 * time.Millisecond) // the slow effect
				return "receipt-1", nil
			})
		}(i)
	}
	start.Done()
	wg.Wait()

	if r := runs.Load(); r != 1 {
		t.Fatalf("work ran %d times, want exactly 1", r)
	}
	for i := range results {
		if errs[i] != nil || results[i] != "receipt-1" {
			t.Fatalf("caller %d got (%q, %v)", i, results[i], errs[i])
		}
	}
}

// Different keys are different requests: they must not deduplicate
// against each other.
func TestDifferentKeysRunIndependently(t *testing.T) {
	d := New[int](time.Minute, nil)
	var runs atomic.Int64

	ctx := context.Background()
	for _, key := range []string{"a", "b", "c"} {
		_, err := d.Do(ctx, key, func(context.Context) (int, error) {
			runs.Add(1)
			return 1, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if r := runs.Load(); r != 3 {
		t.Fatalf("ran %d times for 3 keys, want 3", r)
	}
}

// A failure is not cached: the effect never happened, so the next
// caller must be allowed to try again. (Caching errors would pin one
// transient failure forever.)
func TestFailuresAreNotCachedForLaterCallers(t *testing.T) {
	d := New[string](time.Minute, nil)
	var runs atomic.Int64

	ctx := context.Background()
	boom := errors.New("upstream 503")

	_, err := d.Do(ctx, "write", func(context.Context) (string, error) {
		runs.Add(1)
		return "", boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("first Do = %v, want the fn error", err)
	}

	_, err = d.Do(ctx, "write", func(context.Context) (string, error) {
		runs.Add(1)
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("second Do = %v: a failed attempt must be retryable", err)
	}
	if r := runs.Load(); r != 2 {
		t.Fatalf("work ran %d times, want 2 (retry after failure)", r)
	}
}

// Successes are cached until the TTL expires, then execution resumes.
func TestSuccessIsCachedUntilTheTTLExpires(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	d := New[string](time.Minute, clk)
	var runs atomic.Int64

	run := func() (string, error) {
		return d.Do(context.Background(), "k", func(context.Context) (string, error) {
			runs.Add(1)
			return "v", nil
		})
	}

	if _, err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := run(); err != nil {
		t.Fatal(err)
	}
	if r := runs.Load(); r != 1 {
		t.Fatalf("within TTL: ran %d times, want 1", r)
	}

	clk.Advance(time.Minute + time.Second) // past the TTL
	if _, err := run(); err != nil {
		t.Fatal(err)
	}
	if r := runs.Load(); r != 2 {
		t.Fatalf("after TTL: ran %d times, want 2", r)
	}
}

// A waiter giving up is its own business: it stops waiting, but the
// executing caller's work continues and later callers still get the
// cached success.
func TestWaiterCancellationDoesNotCancelTheExecution(t *testing.T) {
	d := New[string](time.Minute, nil)
	var runs atomic.Int64
	release := make(chan struct{})
	executed := make(chan struct{})

	go func() {
		d.Do(context.Background(), "slow", func(context.Context) (string, error) {
			runs.Add(1)
			close(executed)
			<-release
			return "done", nil
		})
	}()
	<-executed

	// A second caller waits, then gives up.
	impatient, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := d.Do(impatient, "slow", func(context.Context) (string, error) {
		t.Error("impatient caller became the executor: it must attach to the running attempt")
		return "", nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("impatient caller = %v, want DeadlineExceeded", err)
	}

	close(release)

	// A third caller rides on the finished (and kept) result.
	got, err := d.Do(context.Background(), "slow", nil)
	if err != nil || got != "done" {
		t.Fatalf("third caller = (%q, %v), want (done, nil)", got, err)
	}
	if r := runs.Load(); r != 1 {
		t.Fatalf("work ran %d times, want 1 (cancellation must not re-run it)", r)
	}
}

// After Close-equivalent state (entry left behind), InFlight reflects
// the cache bookkeeping used by tests and metrics.
func TestInFlightTracksEntries(t *testing.T) {
	d := New[string](time.Minute, nil)
	ctx := context.Background()

	if _, err := d.Do(ctx, "k", func(context.Context) (string, error) { return "v", nil }); err != nil {
		t.Fatal(err)
	}
	if got := d.InFlight(); got != 1 {
		t.Fatalf("InFlight = %d after a cached success, want 1", got)
	}
}
