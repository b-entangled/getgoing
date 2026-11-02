// Package goroutine documents goroutines as Go's unit of concurrency:
// a cheap, independently scheduled stack plus registers, multiplexed
// onto OS threads by the runtime (M:N scheduling).
//
// Observable facts:
//
//   - Spawning costs heap: each `go` statement allocates a g (goroutine
//     state, a few hundred bytes in practice) that the allocator hands
//     back to a per-processor cache when the goroutine exits. Measured
//     cost on this toolchain is ~170–270 bytes per spawn-and-run, so
//     thousands of goroutines are routine while millions are a decision.
//   - Each goroutine starts with a small stack that grows (and shrinks)
//     on demand — deep recursion in a goroutine works; it fails only if
//     it exceeds the maximum stack size.
//   - GOMAXPROCS bounds how many goroutines execute simultaneously on
//     OS threads; it is at least 1 and defaults to the machine's CPU
//     count. Concurrency (many goroutines) is not parallelism (running
//     at the same instant).
//   - The goroutine that runs main exiting terminates the program —
//     everything else must be waited for explicitly.
package goroutine

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// Spawn starts n goroutines running fn and waits for all of them.
func Spawn(n int, fn func()) {
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	wg.Wait()
}

// Count runs n goroutines, each incrementing counter once, and waits.
func Count(counter *atomic.Int64, n int) {
	Spawn(n, func() { counter.Add(1) })
}

// SpawnCostBytes returns the total heap bytes allocated while starting
// and running n goroutines that hand back on a channel. It includes the
// goroutine state, the per-iteration closure, and channel bookkeeping —
// the realistic cost of a spawn, not a microbenchmark of the `go`
// instruction alone.
func SpawnCostBytes(n int) uint64 {
	ready := make(chan struct{}, n)

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	for i := 0; i < n; i++ {
		go func() {
			ready <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-ready
	}
	runtime.ReadMemStats(&m1)
	return m1.TotalAlloc - m0.TotalAlloc
}

// GOMAXPROCS reports the current limit on executing OS threads.
func GOMAXPROCS() int { return runtime.GOMAXPROCS(0) }

// deep recurses depth times with a real local frame per level; returns
// the number of frames entered. If stacks could not grow, this would die
// with "stack overflow" long before returning.
func deep(depth int) int {
	if depth == 0 {
		return 0
	}
	frame := [64]byte{}
	frame[0] = byte(depth)
	return 1 + deep(depth-1+int(frame[0])*0)
}
