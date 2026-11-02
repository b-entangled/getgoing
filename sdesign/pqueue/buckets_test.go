package pqueue

import (
	"math/rand"
	"testing"
)

// mustPanic runs fn and returns the panic message (t.Fatal if nothing
// panicked).
func mustPanic(t *testing.T, what string, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s: no panic", what)
		}
		msg, _ = r.(string)
	}()
	fn()
	return msg
}

// The heap does not promise FIFO among equals; buckets do — one FIFO
// slice per level — and this script interleaves levels to prove the
// order is insertion order, not arrival luck.
func TestBucketsPreserveFIFOWithinOnePriority(t *testing.T) {
	q := NewBuckets[int](4, func(v int) int { return v % 4 })

	// Push three rounds interleaving levels: within each level the
	// values must keep their insertion order.
	var want []int
	for round := 0; round < 3; round++ {
		for p := 0; p < 4; p++ {
			v := p + round*4 // level p, round-th member
			q.Push(v)
		}
	}
	for p := 0; p < 4; p++ {
		for round := 0; round < 3; round++ {
			want = append(want, p+round*4)
		}
	}

	var got []int
	for {
		v, ok := q.Pop()
		if !ok {
			break
		}
		got = append(got, v)
	}
	if len(got) != len(want) {
		t.Fatalf("popped %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pop %d = %d, want %d (levels ascending, FIFO within level)", i, got[i], want[i])
		}
	}
}

// The bounded range is the structure's contract: a priority outside
// [0, levels) is a programmer error (wrong bound, wrong priority
// func), and programmer errors fail loudly at the call — never
// silently drop work. The degenerate construction panics too.
func TestBucketsRejectAPriorityOutsideTheRange(t *testing.T) {
	q := NewBuckets[int](4, func(v int) int { return v })

	if msg := mustPanic(t, "Push(-1)", func() { q.Push(-1) }); !contains(msg, "priority") {
		t.Errorf("panic %q does not name the priority problem", msg)
	}
	if msg := mustPanic(t, "Push(4)", func() { q.Push(4) }); !contains(msg, "priority") {
		t.Errorf("panic %q does not name the priority problem", msg)
	}
	if msg := mustPanic(t, "NewBuckets(0, ...)", func() { NewBuckets[int](0, func(v int) int { return v }) }); !contains(msg, "level") {
		t.Errorf("panic %q does not explain the level requirement", msg)
	}
	if q.Len() != 0 {
		t.Fatalf("Len = %d after refused pushes, want 0: a panic must not half-insert", q.Len())
	}
}

// The reason to pick buckets over a heap: pop's cost is the number of
// levels, not the number of items. 100k items over 8 levels must pop
// in priority order without ever walking the queue — a scan-per-pop
// implementation would turn this test into ~10^10 operations and
// time out.
func TestBucketsServeHundredThousandItemsInPriorityOrder(t *testing.T) {
	const (
		levels = 8
		n      = 100_000
	)
	q := NewBuckets[int](levels, func(v int) int { return v })
	r := rand.New(rand.NewSource(11))

	counts := make([]int, levels)
	for i := 0; i < n; i++ {
		v := r.Intn(levels)
		q.Push(v)
		counts[v]++
	}
	if q.Len() != n {
		t.Fatalf("Len = %d, want %d", q.Len(), n)
	}

	prev := -1
	seen := make([]int, levels)
	for popped := 0; ; popped++ {
		v, ok := q.Pop()
		if !ok {
			if popped != n {
				t.Fatalf("drained after %d pops, want %d", popped, n)
			}
			break
		}
		if v < prev {
			t.Fatalf("pop %d returned level %d after %d — priorities went backwards", popped, v, prev)
		}
		prev = v
		seen[v]++
	}
	for p := 0; p < levels; p++ {
		if seen[p] != counts[p] {
			t.Fatalf("level %d: popped %d, pushed %d", p, seen[p], counts[p])
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
