package goroutine

import (
	"sync/atomic"
	"testing"
)

// Every goroutine actually runs: n spawns, n completions, no loss.
func TestEverySpawnedGoroutineRuns(t *testing.T) {
	const n = 100
	var counter atomic.Int64
	Count(&counter, n)
	if got := counter.Load(); got != n {
		t.Fatalf("counter = %d, want %d", got, n)
	}
}

// Spawning is cheap enough to be ordinary: a few hundred bytes per
// goroutine (state + closure + channel handoff), well under a
// kilobyte. Bounds are generous so they hold under -race (which
// instruments every goroutine).
func TestSpawnCostIsHundredsOfBytesPerGoroutine(t *testing.T) {
	const n = 1000
	total := SpawnCostBytes(n)
	per := total / n
	if per < 32 {
		t.Fatalf("spawn cost = %d bytes per goroutine, want at least 32 (goroutine state)", per)
	}
	if per > 4096 {
		t.Fatalf("spawn cost = %d bytes per goroutine, want at most 4096", per)
	}
}

// The scheduler limit on parallel OS threads exists and is sane.
func TestGOMAXPROCSIsAtLeastOne(t *testing.T) {
	if p := GOMAXPROCS(); p < 1 {
		t.Fatalf("GOMAXPROCS = %d, want >= 1", p)
	}
}

// Stacks grow on demand: thousands of real frames (each with a 64-byte
// local, ~8 MB total against an initial stack of kilobytes) must
// complete without overflow.
func TestDeepRecursionGrowsTheStack(t *testing.T) {
	const depth = 100000
	if got := deep(depth); got != depth {
		t.Fatalf("deep(%d) = %d", depth, got)
	}
}
