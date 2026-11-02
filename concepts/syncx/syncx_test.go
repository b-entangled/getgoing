package syncx

import (
	"sync"
	"testing"
	"time"
)

// Both primitives must produce an exact count from many goroutines —
// and the race detector watches for unsynchronised access underneath.
func TestMutexAndAtomicCountersAreExact(t *testing.T) {
	const goroutines, perG = 64, 100

	var mu Counter
	var atom AtomicCounter
	RunN(goroutines, func() {
		for i := 0; i < perG; i++ {
			mu.Inc()
			atom.Inc()
		}
	})

	want := goroutines * perG
	if got := mu.Value(); got != want {
		t.Fatalf("mutex counter = %d, want %d", got, want)
	}
	if got := atom.Value(); got != want {
		t.Fatalf("atomic counter = %d, want %d", got, want)
	}
}

func TestWaitGroupWaitsForEveryGoroutine(t *testing.T) {
	var wg sync.WaitGroup
	const n = 100
	done := make([]bool, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			done[i] = true
		}()
	}
	wg.Wait()
	for i, ok := range done {
		if !ok {
			t.Fatalf("goroutine %d never finished", i)
		}
	}
}

func TestOnceInitializesExactlyOnce(t *testing.T) {
	ResetForTest()

	const n = 64
	results := make([]*initResult, n)

	// Each goroutine owns one slot: writing another goroutine's slot
	// would be a data race the race detector would (rightly) flag.
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			var last *initResult
			for k := 0; k < n; k++ {
				// Every goroutine calls Init; only the first runs the body.
				last = Init()
			}
			results[i] = last
		}(i)
	}
	wg.Wait()

	first := Init()
	if first == nil || first.Calls != 1 {
		t.Fatalf("initializer ran %v times, want exactly 1", first)
	}
	for i, r := range results {
		if r == nil {
			t.Fatalf("goroutine %d got nil", i)
		}
		if r != first {
			t.Fatalf("goroutine %d got a different instance", i)
		}
	}
}

// A writer cannot acquire the lock while a reader holds it, and
// proceeds as soon as the reader releases — the deterministic form of
// "RWMutex blocks".
func TestWriterBlocksUntilReaderReleases(t *testing.T) {
	var mu sync.RWMutex
	mu.RLock() // held by this goroutine

	acquired := make(chan struct{})
	go func() {
		mu.Lock()
		close(acquired)
		mu.Unlock()
	}()

	// The writer cannot have the lock while we hold a read lock.
	select {
	case <-acquired:
		t.Fatal("writer acquired the lock while a read lock was held")
	default:
	}

	mu.RUnlock()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not proceed after RUnlock")
	}
}
