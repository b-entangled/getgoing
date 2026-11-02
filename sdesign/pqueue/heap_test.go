package pqueue

import (
	"container/heap"
	"math/rand"
	"slices"
	"strconv"
	"testing"
)

// countingLess returns a less function that tallies every comparison
// it is asked to make — the only honest way to test asymptotics
// without a stopwatch.
func countingLess(c *int) func(a, b int) bool {
	return func(a, b int) bool {
		*c++
		return a < b
	}
}

func descending(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = n - i
	}
	return out
}

// Floyd-heapify builds n items in ≤ 2n comparisons; pushing the same
// n items one at a time costs ~n·log n on adversarial input. Both
// bounds are measured, not asserted loosely: n=1024 gives heapify
// 2028 ≤ 2048 and push-all 8204 ≥ 5120.
func TestHeapifyIsLinearWhileRepeatedPushIsNot(t *testing.T) {
	const n = 1024
	items := descending(n)

	var heapifyCmps int
	NewHeapFrom(slices.Clone(items), countingLess(&heapifyCmps))
	if heapifyCmps > 2*n {
		t.Errorf("heapify used %d comparisons, want ≤ 2n = %d (Floyd's bound)", heapifyCmps, 2*n)
	}

	var pushCmps int
	q := NewHeap(countingLess(&pushCmps))
	for _, v := range items {
		q.Push(v)
	}
	// n·log₂n / 2 at n=1024: pushing a monotonically decreasing
	// sequence sifts every item to the root — the push path's worst
	// case, ≥ 5n measured 8204.
	if pushCmps < 5*n {
		t.Errorf("descending push-all used %d comparisons, want ≥ 5n = %d (~n·log n territory)", pushCmps, 5*n)
	}
	if heapifyCmps >= pushCmps {
		t.Errorf("heapify (%d) not cheaper than push-all (%d)", heapifyCmps, pushCmps)
	}
}

// The heap invariant, checked directly after every operation: no
// child outranks its parent. Interleaved random pushes and pops is
// where a broken sift shows up.
func TestHeapInvariantHoldsUnderInterleavedPushAndPop(t *testing.T) {
	h := NewHeap[int](func(a, b int) bool { return a < b })
	r := rand.New(rand.NewSource(3))

	check := func(step int) {
		for i := 1; i < len(h.items); i++ {
			parent := (i - 1) / 2
			if h.less(h.items[i], h.items[parent]) {
				t.Fatalf("step %d: child %d=%d outranks parent %d=%d — invariant broken",
					step, i, h.items[i], parent, h.items[parent])
			}
		}
	}

	for step := 0; step < 5000; step++ {
		if r.Intn(3) != 0 {
			h.Push(r.Intn(1000))
		} else {
			h.Pop()
		}
		check(step)
	}
	if h.Len() == 0 {
		t.Fatal("script drained the heap: nothing left to check")
	}
}

// boxHeap is the same queue written the container/heap way, so the
// comparison is like-for-like: one generic heap, one stdlib interface
// heap, same pushes.
type boxHeap struct{ items []string }

func (h boxHeap) Len() int           { return len(h.items) }
func (h boxHeap) Less(i, j int) bool { return h.items[i] < h.items[j] }
func (h boxHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *boxHeap) Push(x any)        { h.items = append(h.items, x.(string)) }
func (h *boxHeap) Pop() any {
	n := len(h.items) - 1
	v := h.items[n]
	h.items = h.items[:n]
	return v
}

// The Go-specific reason this package exists instead of
// container/heap: boxing every element in `any`. AllocsPerRun with a
// single run counts the loop's *exact* total (the runtime divides
// whole allocations by runs): generic = 0, stdlib = 1 per push.
func TestHeapPushesAvoidTheBoxingAllocationsOfContainerHeap(t *testing.T) {
	const n = 100
	strs := make([]string, n)
	for i := range strs {
		strs[i] = "key-" + strconv.Itoa(i)
	}

	g := NewHeapFrom(make([]string, 0, n), func(a, b string) bool { return a < b })
	generic := testing.AllocsPerRun(1, func() {
		g.items = g.items[:0]
		for _, s := range strs {
			g.Push(s)
		}
	})

	b := &boxHeap{items: make([]string, 0, n)}
	boxed := testing.AllocsPerRun(1, func() {
		b.items = b.items[:0]
		for _, s := range strs {
			heap.Push(b, s)
		}
	})

	if generic != 0 {
		t.Errorf("generic heap: %v allocations for %d pushes, want 0", generic, n)
	}
	if boxed < n {
		t.Errorf("container/heap: %v allocations for %d pushes, want ≥ %d (one per boxed element)", boxed, n, n)
	}
}

// The less function is the whole ordering: struct keys with a
// multi-field comparison drain in exactly the sorted order.
func TestHeapOrdersStructKeysByTheSuppliedLess(t *testing.T) {
	type task struct {
		prio int
		id   int
	}
	byPrioThenID := func(a, b task) int {
		if a.prio != b.prio {
			return a.prio - b.prio
		}
		return a.id - b.id
	}
	less := func(a, b task) bool { return byPrioThenID(a, b) < 0 }

	r := rand.New(rand.NewSource(5))
	var items []task
	for i := 0; i < 50; i++ {
		items = append(items, task{prio: r.Intn(4), id: r.Intn(1000)})
	}
	want := slices.Clone(items)
	slices.SortFunc(want, byPrioThenID)

	q := NewHeap(less)
	for _, v := range items {
		q.Push(v)
	}
	var got []task
	for {
		v, ok := q.Pop()
		if !ok {
			break
		}
		got = append(got, v)
	}
	if len(got) != len(want) {
		t.Fatalf("popped %d tasks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pop %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// NewHeapFrom takes ownership: the heap reorders the caller's slice
// in place (no copy) and keeps every item — the multiset that comes
// out equals the multiset that went in.
func TestNewHeapFromTakesOwnershipAndKeepsEveryItem(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	items := make([]int, 100)
	for i := range items {
		items[i] = r.Intn(1000)
	}
	want := slices.Clone(items)
	slices.Sort(want)

	h := NewHeapFrom(items, func(a, b int) bool { return a < b })
	if &h.items[0] != &items[0] {
		t.Fatal("NewHeapFrom copied the slice: ownership was promised, not a clone")
	}

	var got []int
	for {
		v, ok := h.Pop()
		if !ok {
			break
		}
		got = append(got, v)
	}
	if !slices.Equal(got, want) {
		t.Fatal("heapify+drain lost, duplicated or reordered items")
	}
}

// A nil ordering is a programmer error — fail at construction, not
// at the first push.
func TestNewHeapPanicsWithoutALessFunction(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewHeap(nil) did not panic")
		}
	}()
	NewHeap[int](nil)
}
