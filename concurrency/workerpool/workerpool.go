// Package workerpool demonstrates the Go-native Worker Pool pattern.
//
// A fixed number of goroutines consume from a shared jobs channel. The pool
// bounds concurrency — without it, a server that spawns one goroutine per
// request will exhaust memory or file descriptors under load.
//
// Correctness rules (each corresponds to a classic bug):
//  1. the *jobs channel* is closed by the producer, never by a worker —
//     closing from a worker makes a second worker's close panic;
//  2. workers exit on `v, ok := <-jobs` with ok == false, not by a sentinel
//     value (a sentinel forces every job type to carry a magic value);
//  3. a failing job cancels the shared context so siblings stop early
//     (first-error-wins) — otherwise the pool keeps burning work on a task
//     that can no longer matter;
//  4. results are collected through a channel or a mutex-guarded slice, and
//     Wait() must not return before every worker has finished, or a caller
//     will read a half-written result.
//
// Domain: fetching and hashing a batch of files.
package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Job is one unit of work. Index is filled in by Produce so results can be
// reported in input order even though they arrive out of order.
type Job struct {
	Name  string
	Data  []byte
	Index int
}

// Result is the outcome of one job. Failed jobs carry Err and no Digest.
type Result struct {
	Name   string
	Digest uint64
	Err    error
	Index  int // position in the input, so output order is recoverable
}

// Pool runs a fixed number of workers over a jobs channel.
type Pool struct {
	workers int
	results chan Result

	mu        sync.Mutex
	collected []Result
	closed    bool
}

// New creates a pool with the given worker count. n < 1 is raised to 1 —
// a zero-worker pool would deadlock silently, which is never what the
// caller meant.
func New(n int) *Pool {
	if n < 1 {
		n = 1
	}
	return &Pool{
		workers: n,
		results: make(chan Result, 64),
	}
}

// Workers reports the configured concurrency.
func (p *Pool) Workers() int { return p.workers }

// Run starts the workers and returns a channel of results. The caller owns
// jobs and must close it when every job has been submitted — closing from
// inside a worker is the single most common pool panic.
//
// Policy: the first job failure cancels a derived context, so siblings stop
// pulling new work instead of burning CPU on a batch that has already
// failed. The failing result itself is delivered *before* cancellation, so
// the caller always learns which job broke. Caller-side cancellation is
// honoured the same way.
func (p *Pool) Run(ctx context.Context, fn func(context.Context, Job) (uint64, error), jobs <-chan Job) <-chan Result {
	ctx, cancel := context.WithCancel(ctx)
	out := make(chan Result, p.workers)

	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j, ok := <-jobs:
					if !ok {
						return // producer closed the channel: clean exit
					}
					if ctx.Err() != nil {
						return // cancelled while queued: do not start it
					}
					digest, err := fn(ctx, j)
					res := Result{Name: j.Name, Digest: digest, Err: err, Index: j.Index}

					// Deliver the outcome first — including the failure —
					// so cancelling never hides why we stopped.
					select {
					case out <- res:
					case <-ctx.Done():
					}

					if err != nil {
						cancel() // first error wins
						return
					}
				}
			}
		}()
	}

	// Closer: results is closed only after every worker has returned, so a
	// consumer can `range out` with no wait group of its own. cancel() here
	// releases the derived context's resources once the pool is done.
	go func() {
		defer close(out)
		defer cancel()
		wg.Wait()
	}()

	return out
}

// Drain reads every result into the pool's slice so callers can ask for the
// summary later. It is idempotent.
func (p *Pool) Drain(results <-chan Result) []Result {
	var got []Result
	for r := range results {
		got = append(got, r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.collected = append(p.collected, got...)
	p.closed = true
	return append([]Result(nil), got...)
}

// Summary returns the drained results sorted by input index, plus counts.
func (p *Pool) Summary() (sorted []Result, ok, failed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]Result(nil), p.collected...)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	for _, r := range out {
		if r.Err != nil {
			failed++
		} else {
			ok++
		}
	}
	return out, ok, failed
}

// --- ready-made worker functions ---

// Hash is the trivial successful worker: it returns a stable 64-bit FNV-1a
// hash of the payload so tests can assert on real work having happened.
func Hash(_ context.Context, j Job) (uint64, error) {
	if len(j.Data) == 0 {
		return 0, fmt.Errorf("workerpool: %s: empty payload", j.Name)
	}
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, b := range j.Data {
		h ^= uint64(b)
		h *= prime64
	}
	return h, nil
}

// FailOn makes a worker function that fails for the named job — used to
// exercise first-error cancellation without real I/O.
func FailOn(name string, fn func(context.Context, Job) (uint64, error)) func(context.Context, Job) (uint64, error) {
	return func(ctx context.Context, j Job) (uint64, error) {
		if j.Name == name {
			return 0, errors.New("workerpool: simulated failure for " + name)
		}
		return fn(ctx, j)
	}
}

// Produce feeds jobs into a channel and closes it. The producer is the
// sole owner and closer — this is rule 1.
func Produce(ctx context.Context, jobs []Job) <-chan Job {
	ch := make(chan Job, len(jobs))
	go func() {
		defer close(ch)
		for i, j := range jobs {
			j.Index = i // stamp on the copy; never mutate the caller's slice
			select {
			case <-ctx.Done():
				return
			case ch <- j:
			}
		}
	}()
	return ch
}

// RunJobs is the one-call convenience: produce, pool, drain. Most callers
// want this rather than wiring the three pieces by hand.
func RunJobs(ctx context.Context, workers int, jobs []Job, fn func(context.Context, Job) (uint64, error)) ([]Result, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	p := New(workers)
	ch := p.Run(ctx, fn, Produce(ctx, jobs))
	got := p.Drain(ch)

	if err := ctx.Err(); err != nil {
		return got, err
	}
	return got, nil
}

// Errors joins every job failure for a single report.
func Errors(results []Result) error {
	var errs []error
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Name, r.Err))
		}
	}
	return errors.Join(errs...)
}
