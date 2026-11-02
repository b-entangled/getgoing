package escape

import (
	"testing"
)

// The stack case: 100 calls to a pure function perform zero allocations.
func TestNonEscapingWorkAllocatesNothing(t *testing.T) {
	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			if Pure(i) < 0 {
				t.Fatal("unreachable")
			}
		}
	}))
	if allocs != 0 {
		t.Fatalf("100 stack-only calls allocated %d times, want 0", allocs)
	}
}

// Passing a local by pointer does not by itself escape: the callee never
// keeps it.
func TestPointerArgumentsThatDoNotEscapeStayOnTheStack(t *testing.T) {
	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			if TakeByPointer(i) < 0 {
				t.Fatal("unreachable")
			}
		}
	}))
	if allocs != 0 {
		t.Fatalf("100 pointer-passing calls allocated %d times, want 0", allocs)
	}
}

// The heap case: storing the local where someone else can see it costs an
// allocation every call.
func TestEscapingWorkAllocatesPerCall(t *testing.T) {
	t.Cleanup(ResetSink)
	ResetSink()

	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			_ = Leaky(i)
		}
	}))
	if allocs < 50 {
		t.Fatalf("100 escaping calls allocated %d times, want at least 50", allocs)
	}
}

// Returning a value is free; returning its address is not.
func TestReturningByPointerAllocates(t *testing.T) {
	t.Cleanup(ResetSink)
	ResetSink()

	// Note: runtime.KeepAlive does NOT make a value escape — the compiler
	// treats it as leaking nothing. Only storing where others can see it
	// (here: package sinks) forces the heap.
	byValue := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			sink = ReturnedByValue(i)
		}
	}))
	byPointer := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			ptrSink = ReturnedByPointer(i)
		}
	}))
	if byValue < 50 {
		t.Fatalf("storing a returned value in a package var allocated %d times, want at least 50", byValue)
	}
	if byPointer < 50 {
		t.Fatalf("storing a returned pointer in a package var allocated %d times, want at least 50", byPointer)
	}
}
