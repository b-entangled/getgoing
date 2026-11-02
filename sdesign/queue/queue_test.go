package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func ctxFor(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// FIFO order under ordinary traffic.
func TestPutAndGetPreserveFIFOOrder(t *testing.T) {
	q := New[int](4)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if err := q.Put(ctx, i); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
	for want := 0; want < 4; want++ {
		got, err := q.Get(ctx)
		if err != nil || got != want {
			t.Fatalf("Get = (%d, %v), want (%d, nil)", got, err, want)
		}
	}
}

// Backpressure: the (capacity+1)-st Put cannot complete until a Get
// frees space — the bound is what keeps memory finite.
func TestPutBlocksWhenFullUntilGet(t *testing.T) {
	q := New[int](1)
	ctx := context.Background()
	q.Put(ctx, 1)

	done := make(chan error, 1)
	go func() { done <- q.Put(ctx, 2) }()

	select {
	case err := <-done:
		t.Fatalf("Put on a full queue returned %v instead of blocking", err)
	case <-time.After(20 * time.Millisecond):
		// still blocked, as designed
	}

	if _, err := q.Get(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Put after space freed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Put never completed after space freed")
	}
}

// The symmetric case: a consumer waits for the producer.
func TestGetBlocksWhenEmptyUntilPut(t *testing.T) {
	q := New[int](1)
	ctx := context.Background()

	type result struct {
		v   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := q.Get(ctx)
		done <- result{v, err}
	}()

	select {
	case r := <-done:
		t.Fatalf("Get on an empty queue returned early: %+v", r)
	case <-time.After(20 * time.Millisecond):
	}

	q.Put(ctx, 42)
	select {
	case r := <-done:
		if r.err != nil || r.v != 42 {
			t.Fatalf("Get = %+v, want {42 nil}", r)
		}
	case <-time.After(time.Second):
		t.Fatal("Get never completed after Put")
	}
}

// Giving up unblocks the caller — neither Put nor Get may hang past
// the caller's deadline.
func TestContextCancellationUnblocksWaiters(t *testing.T) {
	q := New[int](1)
	ctx := context.Background()
	q.Put(ctx, 1)

	putCtx, cancelPut := ctxFor(30 * time.Millisecond)
	defer cancelPut()
	errCh := make(chan error, 1)
	go func() { errCh <- q.Put(putCtx, 2) }()

	getCtx, cancelGet := ctxFor(30 * time.Millisecond)
	defer cancelGet()
	empty := New[int](1)
	getErrCh := make(chan error, 1)
	go func() {
		_, err := empty.Get(getCtx)
		getErrCh <- err
	}()

	if err := <-errCh; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked Put = %v, want DeadlineExceeded", err)
	}
	if err := <-getErrCh; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked Get = %v, want DeadlineExceeded", err)
	}
}

// Close is a shutdown signal, not data loss: blocked Puts fail fast
// and a blocked Get on an empty queue reports ErrClosed.
func TestCloseUnblocksWaiters(t *testing.T) {
	q := New[int](1)
	ctx := context.Background()
	q.Put(ctx, 1)

	putErr := make(chan error, 1)
	go func() { putErr <- q.Put(ctx, 2) }()

	empty := New[int](1)
	getErr := make(chan error, 1)
	go func() {
		_, err := empty.Get(ctx)
		getErr <- err
	}()

	time.Sleep(20 * time.Millisecond) // let both block
	q.Close()                         // wakes the blocked Put
	empty.Close()                     // wakes the blocked Get

	if err := <-putErr; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked Put after Close = %v, want ErrClosed", err)
	}
	if err := <-getErr; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked Get after Close = %v, want ErrClosed", err)
	}
}

// Shutdown drains: items queued before Close are still delivered.
func TestCloseDrainsBufferedItemsFirst(t *testing.T) {
	q := New[int](3)
	ctx := context.Background()
	q.Put(ctx, 1)
	q.Put(ctx, 2)
	q.Close()

	for want := 1; want <= 2; want++ {
		got, err := q.Get(ctx)
		if err != nil || got != want {
			t.Fatalf("Get = (%d, %v), want (%d, nil)", got, err, want)
		}
	}
	if _, err := q.Get(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get on drained closed queue = %v, want ErrClosed", err)
	}
}

// No loss, no duplication under a real producer/consumer race — the
// property the race detector is here to watch.
func TestConcurrentProducersConsumersLoseNothing(t *testing.T) {
	const (
		producers = 4
		perProd   = 250
		total     = producers * perProd
		capacity  = 8
	)
	q := New[int](capacity)
	ctx := context.Background()

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProd; i++ {
				if err := q.Put(ctx, p*perProd+i); err != nil {
					t.Errorf("Put: %v", err)
					return
				}
			}
		}(p)
	}

	seen := make(map[int]int, total)
	var seenMu sync.Mutex
	var cwg sync.WaitGroup
	for c := 0; c < 2; c++ {
		cwg.Add(1)
		go func() {
			defer cwg.Done()
			for {
				v, err := q.Get(ctx)
				if err != nil {
					return
				}
				seenMu.Lock()
				seen[v]++
				seenMu.Unlock()
			}
		}()
	}

	wg.Wait()
	q.Close()
	cwg.Wait()

	if len(seen) != total {
		t.Fatalf("received %d distinct values, want %d", len(seen), total)
	}
	for v, n := range seen {
		if n != 1 {
			t.Fatalf("value %d received %d times, want exactly 1", v, n)
		}
	}
}
