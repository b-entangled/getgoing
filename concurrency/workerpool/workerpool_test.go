package workerpool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func jobs(n int) []Job {
	out := make([]Job, n)
	for i := range n {
		out[i] = Job{Name: string(rune('a'+i%26)) + string(rune('0'+i/26)), Data: []byte{byte(i)}}
	}
	return out
}

func TestRunProcessesEveryJobExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const n = 200
	in := jobs(n)

	results, err := RunJobs(ctx, 8, in, Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}

	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.Name] {
			t.Fatalf("job %q ran twice", r.Name)
		}
		seen[r.Name] = true
		if r.Err != nil {
			t.Fatalf("unexpected error: %v", r.Err)
		}
		if r.Digest == 0 {
			t.Fatalf("job %q produced no digest", r.Name)
		}
	}
}

// A zero-worker pool must not deadlock: New raises the count to 1.
func TestZeroWorkersIsRaisedToOne(t *testing.T) {
	p := New(0)
	if p.Workers() != 1 {
		t.Fatalf("workers=%d", p.Workers())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := RunJobs(ctx, 0, jobs(3), Hash)
	if err != nil || len(res) != 3 {
		t.Fatalf("%d %v", len(res), err)
	}
}

// Results channel closes only after all workers finish, so `range` ends.
func TestResultsChannelCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p := New(4)
	ch := p.Run(ctx, Hash, Produce(ctx, jobs(20)))

	count := 0
	for range ch {
		count++
	}
	if count != 20 {
		t.Fatalf("want 20 results, got %d", count)
	}
}

// The producer owns the close; workers only read. This is rule 1 and the
// reason Run never touches the jobs channel beyond receiving.
func TestProducerClosesJobsAndWorkersExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p := New(3)
	jobsCh := make(chan Job)
	done := make(chan struct{})

	go func() {
		defer close(jobsCh)
		for _, j := range jobs(10) {
			jobsCh <- j
		}
	}()

	go func() {
		defer close(done)
		ch := p.Run(ctx, Hash, jobsCh)
		for range ch {
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not exit after the producer closed jobs")
	}
}

// Context cancellation stops the pool without waiting for the backlog.
func TestCancellationStopsWorkersPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Huge backlog; workers would take a long time to drain it.
	backlog := make([]Job, 100_000)
	for i := range backlog {
		backlog[i] = Job{Name: "j", Data: []byte{1, 2, 3}}
	}

	p := New(4)
	jobsCh := Produce(ctx, backlog)
	ch := p.Run(ctx, Hash, jobsCh)

	// Read a few results, then cancel.
	for range 4 {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("pool produced nothing")
		}
	}
	cancel()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range ch {
		}
	}()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("pool did not shut down after cancel")
	}
}

// First-error-wins: a failing job cancels the context so siblings stop.
func TestFirstErrorCancelsSiblings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	failing := FailOn("j0", Hash)

	p := New(1) // one worker: ordering is deterministic
	ch := p.Run(ctx, failing, Produce(ctx, jobs(1000)))
	got := p.Drain(ch)

	if len(got) == 0 {
		t.Fatal("no results")
	}
	// The failing job ("j0" is the 10th name produced) must be reported.
	var failure *Result
	for i := range got {
		if got[i].Err != nil {
			failure = &got[i]
			break
		}
	}
	if failure == nil {
		t.Fatalf("no failure reported among %d results", len(got))
	}
	if failure.Name != "j0" {
		t.Fatalf("wrong job failed: %q", failure.Name)
	}
	// The pool must stop early rather than process all 1000.
	if len(got) >= 1000 {
		t.Fatalf("pool did not stop early: %d results", len(got))
	}
}

func TestHashIsDeterministicAndNonZero(t *testing.T) {
	ctx := context.Background()
	a, err := Hash(ctx, Job{Name: "x", Data: []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Hash(ctx, Job{Name: "y", Data: []byte("hello")})
	if a != b {
		t.Fatal("hash must depend only on the payload")
	}
	if a == 0 {
		t.Fatal("hash must not be zero")
	}
	c, _ := Hash(ctx, Job{Name: "x", Data: []byte("world")})
	if a == c {
		t.Fatal("different payloads hashed the same")
	}
}

func TestHashRejectsEmptyPayload(t *testing.T) {
	if _, err := Hash(context.Background(), Job{Name: "e"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSummaryCountsAndOrders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Mark indices the way RunJobs would.
	in := jobs(6)
	p := New(3)
	ch := p.Run(ctx, Hash, Produce(ctx, in))
	p.Drain(ch)

	sorted, ok, failed := p.Summary()
	if len(sorted) != 6 || ok != 6 || failed != 0 {
		t.Fatalf("len=%d ok=%d failed=%d", len(sorted), ok, failed)
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1].Index > sorted[i].Index {
			t.Fatalf("not sorted by index: %+v", sorted)
		}
	}
}

func TestErrorsJoinsJobFailures(t *testing.T) {
	results := []Result{
		{Name: "a", Err: errors.New("one")},
		{Name: "b"},
		{Name: "c", Err: errors.New("two")},
	}
	err := Errors(results)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "c:") {
		t.Fatalf("joined error should name the jobs: %v", err)
	}
	if strings.Contains(err.Error(), "b:") {
		t.Fatalf("successful job must not appear: %v", err)
	}
	// errors.Join supports unwrapping to individual errors via Unwrap() []error.
	if !errors.Is(err, errors.New("one")) && !strings.Contains(err.Error(), "one") {
		t.Fatalf("got %v", err)
	}
}

func TestRunJobsWithNoJobs(t *testing.T) {
	res, err := RunJobs(context.Background(), 4, nil, Hash)
	if err != nil || res != nil {
		t.Fatalf("%v %v", res, err)
	}
}

// Many goroutines hammering the pool must be race-free.
func TestConcurrentPoolsDoNotRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := RunJobs(ctx, 4, jobs(50), Hash)
			if err != nil {
				t.Error(err)
				return
			}
			if len(res) != 50 {
				t.Errorf("got %d", len(res))
			}
		}()
	}
	wg.Wait()
}

// Drain is safe to call exactly once per Run; calling it on a closed result
// stream yields the same slice semantics (empty append).
func TestDrainAfterCloseIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p := New(2)
	ch := p.Run(ctx, Hash, Produce(ctx, jobs(4)))
	p.Drain(ch)
	first, _, _ := p.Summary()
	p.Drain(ch) // channel already closed → returns immediately
	second, _, _ := p.Summary()
	if len(first) != len(second) {
		t.Fatalf("second drain changed the summary: %d -> %d", len(first), len(second))
	}
}
