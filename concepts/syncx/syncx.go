// Package syncx demonstrates the synchronization primitives from the
// standard library and when each is the right tool:
//
//   - **sync.Mutex** — one goroutine at a time; protects a struct's
//     invariants. Always pair Lock/Unlock with defer.
//   - **sync.RWMutex** — many readers *or* one writer. Use it when reads
//     dominate and the critical section is not tiny.
//   - **sync.WaitGroup** — "wait for N goroutines"; a counter, not a
//     queue. Add before starting the goroutine, never concurrently with Wait.
//   - **sync.Once** — run initialization exactly once, safely, with a
//     lock-free fast path afterwards.
//   - **atomic.Int64** — a single counter with no lock at all. Correct
//     only for that: compound invariants still need a mutex.
//
// Domain: a hit counter used by concurrent request handlers.
package syncx

import (
	"sync"
	"sync/atomic"
)

// Counter guards n with a mutex: Inc and Value are both atomic with
// respect to each other.
type Counter struct {
	mu sync.Mutex
	n  int
}

// Inc adds one under the lock.
func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

// Value reads under the lock, so it never sees a half-updated counter.
func (c *Counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// AtomicCounter does the same job with no lock: the counter itself is
// the invariant, so atomic loads and stores are enough.
type AtomicCounter struct {
	n atomic.Int64
}

// Inc adds one atomically.
func (c *AtomicCounter) Inc() { c.n.Add(1) }

// Value reads atomically.
func (c *AtomicCounter) Value() int { return int(c.n.Load()) }

// RunN runs f n times, spread over n goroutines, and returns only when
// every goroutine has finished. Add happens *before* Go, so Wait can
// never observe a counter that is still growing.
func RunN(n int, f func()) {
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			f()
		}()
	}
	wg.Wait()
}

// initResult is what the once-guarded initializer produces.
type initResult struct {
	Calls int // how many times init really ran
}

var (
	once   sync.Once
	result *initResult
	calls  atomic.Int64
)

// Init runs the initializer exactly once, no matter how many goroutines
// call it, and always returns the same pointer.
func Init() *initResult {
	once.Do(func() {
		calls.Add(1)
		result = &initResult{Calls: int(calls.Load())}
	})
	return result
}

// ResetForTest clears the once state. Only tests need this — real
// programs initialize once and keep the value.
func ResetForTest() {
	once = sync.Once{}
	result = nil
	calls.Store(0)
}
