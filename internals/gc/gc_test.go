package gc

import (
	"runtime"
	"testing"
	"time"
	"weak"
)

// waitFor polls with forced collections until done closes or the
// deadline passes — finalizers run asynchronously after a collection,
// so a single GC and an immediate check would race the finalizer
// goroutine.
func waitFor(t *testing.T, done chan struct{}, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-done:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never happened within 5s", what)
		}
		Collect()
		time.Sleep(10 * time.Millisecond)
	}
}

// A dropped object's finalizer runs after a collection notices it is
// unreachable.
func TestFinalizerRunsAfterTheObjectBecomesUnreachable(t *testing.T) {
	done := make(chan struct{})
	func() {
		x := NewTracked(1)
		Track(x, done)
		// x dies with this frame — no reference survives.
	}()
	waitFor(t, done, "finalizer for a dropped object")
}

// Documented dependency order: for an unreachable chain a -> b with
// finalizers on both, a's runs first (b is retained while a's
// finalizer is pending) and b's runs only once a has been freed.
func TestFinalizersRunInDependencyOrder(t *testing.T) {
	doneA, doneB := make(chan struct{}), make(chan struct{})
	func() {
		a, b := NewChain()
		TrackChain(a, b, doneA, doneB)
	}()
	waitFor(t, doneA, "finalizer for the head of the chain")

	select {
	case <-doneB:
		t.Fatal("b's finalizer ran before a's — dependency order violated")
	default:
	}
	waitFor(t, doneB, "finalizer for the tail of the chain")
}

// Cycles are unreachable when the program drops its last external
// reference, and the tracing collector reclaims them — a reference
// counter would keep them alive forever. weak.Pointer is the observation
// tool built for this: it goes nil exactly when its referent is
// collected. (Finalizers cannot observe this: the runtime documents that
// a finalizer inside a cycle may never run.)
func TestUnreachableCyclesAreCollected(t *testing.T) {
	// Control: a cycle that stays reachable must stay alive.
	keep := NewCycle(4)
	weakKept := weak.Make(keep)
	Collect()
	if weakKept.Value() == nil {
		t.Fatal("a strongly reachable cycle was collected")
	}
	runtime.KeepAlive(keep)

	// The real claim: a cycle with no external references is collected.
	weakDropped := func() weak.Pointer[node] {
		head := NewCycle(4)
		return weak.Make(head) // head itself dies with this frame
	}()

	deadline := time.Now().Add(5 * time.Second)
	for weakDropped.Value() != nil {
		if time.Now().After(deadline) {
			t.Fatal("unreachable cycle was not collected within 5s")
		}
		Collect()
		time.Sleep(10 * time.Millisecond)
	}
}

// Dropping a large live set and collecting returns the memory to the
// heap: measured with generous margins because the collector works
// concurrently with the measurement.
func TestHeapFallsAfterDroppingGarbage(t *testing.T) {
	before := func() uint64 {
		garbage := make([][]byte, 4000)
		for i := range garbage {
			garbage[i] = make([]byte, 4096)
			garbage[i][0] = byte(i)
		}
		return HeapBytes(garbage) // garbage kept alive across the measurement
	}()

	after := HeapBytes(nil)

	if after >= before {
		t.Fatalf("heap after drop = %d bytes, before = %d: expected a fall", after, before)
	}
	// 4000 * 4096 ≈ 16 MiB of garbage; require at least half collected.
	if after > before/2 {
		t.Fatalf("heap fell only to %d of %d bytes: expected at least half collected", after, before)
	}
}
