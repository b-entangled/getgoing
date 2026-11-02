package defer_

import "testing"

// The open-coded path: defers in the function body cost nothing. 100
// calls must total exactly 0 allocations.
func TestTopLevelDefersDoNotAllocate(t *testing.T) {
	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			_ = OpenCoded()
			_ = OpenCodedMany()
		}
	}))
	if allocs != 0 {
		t.Fatalf("100 open-coded calls allocated %d times, want 0", allocs)
	}
}

// The heap path: one allocation per deferred call in a loop. 100 calls of
// InLoop(10) must total at least 1000.
func TestDefersInLoopAllocatePerIteration(t *testing.T) {
	const (
		calls = 100
		n     = 10
	)
	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < calls; i++ {
			InLoop(n)
		}
	}))
	if want := calls * n; allocs < want {
		t.Fatalf("%d loop-defer calls allocated %d times, want at least %d", calls, allocs, want)
	}
}

// Only the loop defers pay: the top-level one in Mixed stays open-coded,
// so the total is n per call, not n+1.
func TestOnlyLoopDefersAllocateInAMixedFunction(t *testing.T) {
	const (
		calls = 100
		n     = 3
	)
	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < calls; i++ {
			Mixed(n)
		}
	}))
	lo, hi := calls*n, calls*(n+1)
	if allocs < lo {
		t.Fatalf("Mixed allocated %d times, want at least %d (one per loop defer)", allocs, lo)
	}
	if allocs > hi {
		t.Fatalf("Mixed allocated %d times, want at most %d (the top-level defer must stay free)", allocs, hi)
	}
}

// Both implementations run their calls in LIFO order — the mechanism
// differs, the observable behaviour does not.
func TestBothShapesRunInLIFOOrder(t *testing.T) {
	var got []int
	collect := func(v int) { got = append(got, v) }

	func() {
		defer collect(3)
		defer collect(2)
		defer collect(1)
	}()
	want := []int{1, 2, 3}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
