package slice

import (
	"reflect"
	"testing"
	"unsafe"
)

func TestSlicingSharesTheBackingArray(t *testing.T) {
	base := []int{10, 20, 30, 40}
	v := View(base, 1, 3)

	if len(v) != 2 || cap(v) != 3 { // cap runs to the end of base
		t.Fatalf("len=%d cap=%d, want 2 and 3", len(v), cap(v))
	}
	// Same storage: writing through the view changes the base.
	v[0] = 99
	if base[1] != 99 {
		t.Fatalf("base[1] = %d, want 99 — views must share the array", base[1])
	}
	// No data was copied: the view's first element *is* base[1].
	if unsafe.Pointer(&v[0]) != unsafe.Pointer(&base[1]) {
		t.Fatal("view must point into the base array")
	}
}

// The full slice expression pins capacity: append through it allocates
// instead of writing into the shared array.
func TestPinnedViewIsolatesAppends(t *testing.T) {
	base := []int{10, 20, 30, 40}
	v := PinnedView(base, 1, 3)
	if cap(v) != 2 {
		t.Fatalf("cap = %d, want 2 (pinned to length)", cap(v))
	}
	grown := append(v, 999)
	if base[2] != 30 {
		t.Fatalf("base changed to %v — append wrote through the view", base)
	}
	if len(grown) != 3 || grown[2] != 999 {
		t.Fatalf("grown = %v", grown)
	}
}

// Capacity grows geometrically: far fewer reallocs than appends.
func TestGrowthIsGeometricNotLinear(t *testing.T) {
	const n = 1000
	vals := make([]int, n)
	for i := range vals {
		vals[i] = i
	}
	out, growths := Grow(vals...)

	if len(out) != n {
		t.Fatalf("len = %d", len(out))
	}
	// n appends, but only a logarithmic number of reallocations.
	if len(growths) >= 30 {
		t.Fatalf("%d reallocations for %d appends: growth is not geometric", len(growths), n)
	}
	if len(growths) == 0 {
		t.Fatal("expected at least one growth")
	}
	// Each new capacity is larger than the old one, by a sane factor.
	prev := 1
	for _, c := range growths {
		if c <= prev {
			t.Fatalf("capacity shrank: %d after %d", c, prev)
		}
		if c > prev*2+16 {
			t.Fatalf("capacity jumped from %d to %d: more than doubling", prev, c)
		}
		prev = c
	}
	if prev < n {
		t.Fatalf("final capacity %d < length %d", prev, n)
	}
}

// nil and empty slices behave the same for iteration and append.
func TestNilAndEmptySlicesAreBothUsable(t *testing.T) {
	var nilSlice []int
	empty := make([]int, 0)

	if nilSlice != nil || empty == nil {
		t.Fatal("nil and empty must differ in identity")
	}
	if len(nilSlice) != 0 || len(empty) != 0 {
		t.Fatal("both must have length 0")
	}
	if Sum(nilSlice) != 0 || Sum(empty) != 0 {
		t.Fatal("range over both must be a no-op")
	}
	if got := append(nilSlice, 1); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("append to nil = %v", got)
	}
}

func TestCopyProducesAnIndependentArray(t *testing.T) {
	base := []int{1, 2, 3}
	c := CopyInto(base)
	c[0] = 42
	if base[0] != 1 {
		t.Fatalf("copy aliased the original: %v", base)
	}
	if unsafe.Pointer(&c[0]) == unsafe.Pointer(&base[0]) {
		t.Fatal("copy must use a fresh backing array")
	}
}
