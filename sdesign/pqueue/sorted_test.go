package pqueue

import (
	"math/rand"
	"slices"
	"testing"
)

// Sorted's distinguishing offer: equal priorities come out in
// insertion order (upper-bound insert), which the heap does not
// provide — verified by comparing the drain against a stable sort of
// the same push sequence.
func TestSortedSliceKeepsEqualPrioritiesInInsertionOrder(t *testing.T) {
	type item struct {
		prio int
		seq  int // position in the push sequence
	}
	byPrio := func(a, b item) int { return a.prio - b.prio }

	r := rand.New(rand.NewSource(13))
	var pushed []item
	for i := 0; i < 30; i++ {
		pushed = append(pushed, item{prio: r.Intn(3), seq: i})
	}
	want := slices.Clone(pushed)
	slices.SortStableFunc(want, byPrio)

	q := NewSorted(func(a, b item) bool { return byPrio(a, b) < 0 })
	for _, v := range pushed {
		q.Push(v)
	}
	var got []item
	for {
		v, ok := q.Pop()
		if !ok {
			break
		}
		got = append(got, v)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("drain = %v, want the stable order %v", got, want)
	}
}

// Why the sorted slice is the small-n choice and nothing else:
// measured on n=1024 random keys, insert-all costs ~n²/4 comparisons
// (bounded below at n²/8) against the heap's ~n·log n — over 10×
// apart on identical input.
func TestComparisonCountsShowWhyTheHeapBeatsASortedSlice(t *testing.T) {
	const n = 1024
	r := rand.New(rand.NewSource(42))
	vals := make([]int, n)
	for i := range vals {
		vals[i] = r.Intn(1 << 30)
	}

	var sortedCmps int
	s := NewSorted(countingLess(&sortedCmps))
	for _, v := range vals {
		s.Push(v)
	}

	var heapCmps int
	h := NewHeap(countingLess(&heapCmps))
	for _, v := range vals {
		h.Push(v)
	}

	if sortedCmps < n*n/8 {
		t.Errorf("sorted insert-all = %d comparisons, want ≥ n²/8 = %d (measured ~n²/4)", sortedCmps, n*n/8)
	}
	if sortedCmps < heapCmps*10 {
		t.Errorf("sorted=%d vs heap=%d: the gap must be ≥10× at n=%d (measured ~117×)", sortedCmps, heapCmps, n)
	}
}
