package pqueue

import (
	"math/rand"
	"slices"
	"testing"
)

// structures builds all three queues pre-loaded with items — the same
// inputs through the same contract, so any behavioural difference is
// the structure's, not the test's. Items must lie in [0,8) for
// Buckets' fixed range.
func structures(items []int) map[string]Queue[int] {
	h := NewHeap[int](func(a, b int) bool { return a < b })
	b := NewBuckets[int](8, func(v int) int { return v })
	s := NewSorted[int](func(a, b int) bool { return a < b })
	for _, v := range items {
		h.Push(v)
		b.Push(v)
		s.Push(v)
	}
	return map[string]Queue[int]{"Heap": h, "Buckets": b, "Sorted": s}
}

func script(n int, levels int, seed int64) []int {
	r := rand.New(rand.NewSource(seed))
	out := make([]int, n)
	for i := range out {
		out[i] = r.Intn(levels)
	}
	return out
}

// The contract: popping drains the queue in priority order, and every
// item that went in comes out exactly once — for all three structures.
func TestAllStructuresPopInPriorityOrder(t *testing.T) {
	items := script(200, 8, 1)
	want := slices.Clone(items)
	slices.Sort(want)

	for name, q := range structures(items) {
		got := make([]int, 0, len(items))
		for {
			v, ok := q.Pop()
			if !ok {
				break
			}
			got = append(got, v)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s drained %v, want %v (sorted: order and completeness)", name, got, want)
		}
	}
}

// The contract: Len tracks pushes and pops exactly, and Pop on an
// empty queue reports false with the zero value — never garbage.
func TestAllStructuresReportLengthAndEmptyCorrectly(t *testing.T) {
	for name, q := range structures([]int{3, 1, 4, 1, 5}) {
		if q.Len() != 5 {
			t.Errorf("%s.Len = %d, want 5", name, q.Len())
		}
		q.Pop()
		q.Pop()
		if q.Len() != 3 {
			t.Errorf("%s.Len after 2 pops = %d, want 3", name, q.Len())
		}
		for q.Len() > 0 {
			q.Pop()
		}
		if q.Len() != 0 {
			t.Errorf("%s.Len after drain = %d, want 0", name, q.Len())
		}
		v, ok := q.Pop()
		if ok || v != 0 {
			t.Errorf("%s.Pop on empty = (%d, %v), want (0, false)", name, v, ok)
		}
	}
}

// peekable is the consumer-side extension: Peek is on every structure
// but stays out of Queue to keep the contract minimal — a consumer
// that needs it narrows the interface itself.
type peekable interface {
	Queue[int]
	Peek() (int, bool)
}

// The contract: Peek reports the next Pop without consuming it.
func TestPeekLeavesEveryStructureIntact(t *testing.T) {
	items := []int{5, 1, 7, 3}
	for name, q := range structures(items) {
		pk, ok := q.(peekable)
		if !ok {
			t.Errorf("%s does not implement Peek", name)
			continue
		}
		first, ok := pk.Peek()
		if !ok || first != 1 {
			t.Errorf("%s.Peek = (%d, %v), want (1, true)", name, first, ok)
		}
		again, _ := pk.Peek()
		if again != first {
			t.Errorf("%s.Peek is not stable: %d then %d", name, first, again)
		}
		if pk.Len() != len(items) {
			t.Errorf("%s.Len after Peek = %d, want %d", name, pk.Len(), len(items))
		}
		v, _ := pk.Pop()
		if v != first {
			t.Errorf("%s.Pop after Peek = %d, want the peeked %d", name, v, first)
		}
	}
}

// One scripted stream of interleaved pushes and pops through all
// three structures, checked against a count-array model after every
// operation: the structures are interchangeable under interleaving,
// not just under bulk load.
func TestAllStructuresHandleTheSameInterleavedScript(t *testing.T) {
	const levels = 8
	r := rand.New(rand.NewSource(7))
	type op struct {
		push bool
		val  int
	}
	script := make([]op, 2000)
	for i := range script {
		if r.Intn(3) != 0 {
			script[i] = op{push: true, val: r.Intn(levels)}
		} else {
			script[i] = op{push: false}
		}
	}

	for name, q := range structures(nil) {
		var model [levels]int
		queued := 0
		for i, o := range script {
			if o.push {
				q.Push(o.val)
				model[o.val]++
				queued++
				continue
			}
			v, ok := q.Pop()
			if queued == 0 {
				if ok {
					t.Fatalf("%s op %d: Pop = (%d, true) on empty model", name, i, v)
				}
				continue
			}
			if !ok {
				t.Fatalf("%s op %d: Pop failed with %d items modeled", name, i, queued)
			}
			want := 0
			for model[want] == 0 {
				want++
			}
			if v != want {
				t.Fatalf("%s op %d: Pop = %d, want %d (the most urgent modeled)", name, i, v, want)
			}
			model[v]--
			queued--
			if q.Len() != queued {
				t.Fatalf("%s op %d: Len = %d, model = %d", name, i, q.Len(), queued)
			}
		}
	}
}
