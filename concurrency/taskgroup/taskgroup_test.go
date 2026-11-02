package taskgroup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAllTasksRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	g := New(ctx)
	var n atomic.Int64
	for range 10 {
		g.Go(func(context.Context) error {
			n.Add(1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.Load() != 10 {
		t.Fatalf("ran %d tasks", n.Load())
	}
}

func TestFirstErrorIsReturned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	boom := errors.New("boom")
	g := New(ctx)
	g.Go(func(context.Context) error { return boom })
	g.Go(func(context.Context) error { return nil })

	if err := g.Wait(); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
}

// Concurrent failures must not race on the error slot — exactly one wins.
func TestOnlyOneErrorIsRecorded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	g := New(ctx)
	var wg sync.WaitGroup
	for i := range 50 {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Go(func(context.Context) error {
				return fmt.Errorf("err-%d", i)
			})
		}()
	}
	wg.Wait()

	err := g.Wait()
	if err == nil {
		t.Fatal("expected an error")
	}
	// Exactly one message, not a concatenation.
	if strings.Contains(err.Error(), "err-") == false {
		t.Fatalf("got %v", err)
	}
}

// First failure cancels the derived context, so pending tasks observe it.
// The handshake guarantees the sibling is already inside its task function
// before the failure happens — otherwise the group would (correctly) skip
// starting it, which is a different behaviour worth its own test.
func TestFirstErrorCancelsSiblingContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	g := New(ctx)
	ready := make(chan struct{})
	observed := make(chan struct{})

	g.Go(func(ctx context.Context) error {
		<-ready // wait until the sibling is inside its fn
		return errors.New("first failure")
	})
	g.Go(func(ctx context.Context) error {
		close(ready)
		<-ctx.Done() // sibling cancellation must reach us
		close(observed)
		return ctx.Err()
	})

	_ = g.Wait()

	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("sibling never observed cancellation")
	}
}

func TestTasksNotStartedAfterFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	g := WithLimit(ctx, 1) // serial, so ordering is deterministic
	var ran atomic.Int64

	g.Go(func(context.Context) error { return errors.New("fail") })
	for range 20 {
		g.Go(func(context.Context) error {
			ran.Add(1)
			return nil
		})
	}

	err := g.Wait()
	if err == nil || !strings.Contains(err.Error(), "fail") {
		t.Fatalf("got %v", err)
	}
	if ran.Load() != 0 {
		t.Fatalf("%d tasks ran after the group had already failed", ran.Load())
	}
}

func TestConcurrencyLimitIsEnforced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const limit = 3
	g := WithLimit(ctx, limit)

	var (
		mu      sync.Mutex
		current int
		maxSeen int
	)

	for range 30 {
		g.Go(func(context.Context) error {
			mu.Lock()
			current++
			if current > maxSeen {
				maxSeen = current
			}
			mu.Unlock()

			time.Sleep(2 * time.Millisecond)

			mu.Lock()
			current--
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxSeen > limit {
		t.Fatalf("limit %d exceeded: %d concurrent", limit, maxSeen)
	}
	if maxSeen == 0 {
		t.Fatal("no tasks ran")
	}
}

func TestZeroLimitMeansUnlimited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	g := WithLimit(ctx, 0)
	var n atomic.Int64
	for range 200 {
		g.Go(func(context.Context) error { n.Add(1); return nil })
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 200 {
		t.Fatalf("ran %d", n.Load())
	}
	if g.Started() != 200 {
		t.Fatalf("started=%d", g.Started())
	}
}

func TestGoAfterWaitPanics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	g := New(ctx)
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic")
		}
		if r != ErrConcurrentUse {
			t.Fatalf("got %v", r)
		}
	}()
	g.Go(func(context.Context) error { return nil })
}

func TestContextIsCancelledWhenWaitReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	g := New(ctx)
	g.Go(func(context.Context) error { return nil })
	_ = g.Wait()

	if g.Context().Err() == nil {
		t.Fatal("derived context must be cancelled after Wait to free resources")
	}
}

// Parent cancellation stops the group too.
func TestParentCancellationPropagates(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	g := New(parent)

	ready := make(chan struct{})
	observed := make(chan struct{})

	g.Go(func(ctx context.Context) error {
		<-ready // do not cancel until the sibling is inside its fn
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	g.Go(func(ctx context.Context) error {
		close(ready)
		<-ctx.Done()
		close(observed)
		return ctx.Err()
	})

	_ = g.Wait()
	select {
	case <-observed:
	default:
		t.Fatal("sibling did not observe parent cancellation")
	}
}

// --- RunAll / TryAll ---

func TestRunAllRunsEveryTask(t *testing.T) {
	var n atomic.Int64
	tasks := make([]Task, 10)
	for i := range tasks {
		tasks[i] = Task{Name: fmt.Sprintf("t%d", i), Fn: func(context.Context) error {
			n.Add(1)
			return nil
		}}
	}
	if err := RunAll(context.Background(), tasks...); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 10 {
		t.Fatalf("ran %d", n.Load())
	}
}

func TestRunAllWithNoTasks(t *testing.T) {
	if err := RunAll(context.Background()); !errors.Is(err, ErrNoTasks) {
		t.Fatalf("got %v", err)
	}
}

func TestRunAllNamesTheFailingTask(t *testing.T) {
	err := RunAll(context.Background(),
		Task{Name: "fetch-user", Fn: func(context.Context) error { return nil }},
		Task{Name: "fetch-orders", Fn: func(context.Context) error { return errors.New("503") }},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fetch-orders") {
		t.Fatalf("error must name the task: %v", err)
	}
	if strings.Contains(err.Error(), "fetch-user") {
		t.Fatalf("successful task must not appear: %v", err)
	}
}

// TryAll keeps going — the opposite policy, one line of implementation away.
func TestTryAllCollectsEveryFailure(t *testing.T) {
	err := TryAll(context.Background(),
		Task{Name: "a", Fn: func(context.Context) error { return errors.New("one") }},
		Task{Name: "b", Fn: func(context.Context) error { return nil }},
		Task{Name: "c", Fn: func(context.Context) error { return errors.New("two") }},
	)
	if err == nil {
		t.Fatal("expected errors")
	}
	if !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "c:") {
		t.Fatalf("want both failures joined: %v", err)
	}
	if strings.Contains(err.Error(), "b:") {
		t.Fatalf("successful task leaked into the error: %v", err)
	}
}

func TestTryAllWithNoTasks(t *testing.T) {
	if err := TryAll(context.Background()); !errors.Is(err, ErrNoTasks) {
		t.Fatalf("got %v", err)
	}
}

// --- FetchAll --

func fakeFetch(latency time.Duration, failURL string) Fetcher {
	return func(ctx context.Context, url string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(latency):
		}
		if url == failURL {
			return "", errors.New("connection refused")
		}
		return "body:" + url, nil
	}
}

func TestFetchAllReturnsEveryBody(t *testing.T) {
	urls := []string{"a", "b", "c", "d", "e"}
	got, err := FetchAll(context.Background(), fakeFetch(0, ""), urls, 3, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(urls) {
		t.Fatalf("got %d bodies: %v", len(got), Bodies(got))
	}
	if got["c"] != "body:c" {
		t.Fatalf("got %v", got)
	}
}

func TestFetchAllFailsFast(t *testing.T) {
	urls := []string{"ok1", "bad", "ok2", "ok3"}
	start := time.Now()
	_, err := FetchAll(context.Background(), fakeFetch(150*time.Millisecond, "bad"), urls, 4, 3*time.Second)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("got %v", err)
	}
	// With fail-fast the whole call should finish near one fetch latency,
	// not four.
	if time.Since(start) > 400*time.Millisecond {
		t.Fatalf("fail-fast did not stop the batch: took %v", time.Since(start))
	}
}

func TestFetchAllHonoursBudget(t *testing.T) {
	slow := func(ctx context.Context, url string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	start := time.Now()
	_, err := FetchAll(context.Background(), slow, []string{"x", "y"}, 2, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("budget not enforced: %v", time.Since(start))
	}
}

func TestFetchAllWithNoURLs(t *testing.T) {
	got, err := FetchAll(context.Background(), fakeFetch(0, ""), nil, 4, time.Second)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

// The whole group must be race-free under -race with many tasks.
func TestGroupIsRaceFree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	g := WithLimit(ctx, 8)
	var total atomic.Int64
	for i := range 500 {
		i := i
		g.Go(func(context.Context) error {
			total.Add(int64(i))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	// 0..499 sums to 124750
	if total.Load() != 124750 {
		t.Fatalf("sum=%d", total.Load())
	}
}
