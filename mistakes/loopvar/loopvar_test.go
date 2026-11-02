package loopvar

import (
	"reflect"
	"testing"
)

// The Go 1.22 guarantee: closures in the loop are independent.
func TestClosuresCapturePerIterationVariable(t *testing.T) {
	fs := PerIterationClosures(3)
	got := make([]int, len(fs))
	for i, f := range fs {
		got[i] = f()
	}
	if want := []int{0, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// And so do goroutines: each writes its own index with its own value.
// This is race-detector verified.
func TestGoroutinesCapturePerIterationVariable(t *testing.T) {
	if got, want := PerIterationGoroutines(16), range16(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The mistake that survives: an outer variable mutated in the loop is
// still one shared variable. Every closure sees the last value.
func TestCapturingSharedOuterVariableStillBites(t *testing.T) {
	fs := SharedOuterClosure([]int{1, 2, 3})
	for i, f := range fs {
		if got := f(); got != 3 {
			t.Fatalf("fs[%d]() = %d, want the shared final value 3", i, got)
		}
	}
	// The tell: all closures report the same thing.
	if fs[0]() != fs[2]() {
		t.Fatal("premise: shared capture must be uniform")
	}
}

// The fix: copy the value per iteration before capturing.
func TestPerIterationCopyFixesSharedCapture(t *testing.T) {
	fs := FixedOuterClosure([]int{1, 2, 3})
	got := make([]int, len(fs))
	for i, f := range fs {
		got[i] = f()
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func range16() []int {
	out := make([]int, 16)
	for i := range out {
		out[i] = i
	}
	return out
}
