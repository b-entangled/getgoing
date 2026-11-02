package slicecap

import "testing"

// The proof of aliasing: the original slice's element changed.
func TestAppendWithSpareCapacityWritesIntoTheOriginal(t *testing.T) {
	orig, shared := BadSharedAppend()
	if orig[0] != 99 {
		t.Fatalf("orig[0] = %d, want 99 — append must have shared the array", orig[0])
	}
	if shared[0] != 99 || len(shared) != 3 {
		t.Fatalf("shared = %v", shared)
	}
	// Both views report different lengths over the same storage.
	if cap(orig) != cap(shared[:2]) && cap(shared) != 4 {
		t.Fatalf("expected the shared capacity story, got cap(orig)=%d cap(shared)=%d", cap(orig), cap(shared))
	}
}

// The fix: a zero-capacity slice as the append target forces a new array.
func TestZeroCapacityAppendIsolatesTheCopy(t *testing.T) {
	orig, independent := GoodCopy()
	if orig[0] != 1 {
		t.Fatalf("orig[0] = %d, want 1 (untouched)", orig[0])
	}
	if independent[0] != 99 || independent[1] != 2 || independent[2] != 3 {
		t.Fatalf("independent = %v", independent)
	}
	if cap(independent) == cap(orig) && &independent[0] == &orig[0] {
		t.Fatal("backing arrays still shared")
	}
}

// The fix: cap the view so append has nowhere to write in place.
func TestFullSliceExpressionPreventsInPlaceAppend(t *testing.T) {
	orig, view := GoodFullSliceExpression()
	if orig[0] != 1 {
		t.Fatalf("orig[0] = %d, want 1 (untouched)", orig[0])
	}
	if view[0] != 99 || len(view) != 3 {
		t.Fatalf("view = %v", view)
	}
}

// When there is no spare capacity, append allocates — no aliasing.
func TestAppendAtFullCapacityIsSafe(t *testing.T) {
	orig, grown := AppendAtCapacityAllocates()
	if orig[0] != 1 {
		t.Fatalf("orig[0] = %d, want 1", orig[0])
	}
	if grown[0] != 99 || grown[2] != 3 {
		t.Fatalf("grown = %v", grown)
	}
}

func TestAppendToNilSliceIsSafe(t *testing.T) {
	got := SafeAppendToNil(1, 2, 3)
	if len(got) != 3 || got[0] != 1 {
		t.Fatalf("got %v", got)
	}
}
