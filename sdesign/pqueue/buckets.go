package pqueue

// Buckets is a queue for priorities that are small dense integers:
// one FIFO slice per level, pop scans levels from most urgent (0)
// upward. The cost that matters — how far pop walks — is O(P) in the
// number of levels and independent of the number of queued items, so
// a queue of 100k jobs over 8 classes pops in 8 comparisons, not 17.
//
// The range must be bounded at construction: levels is the count of
// valid priorities [0, levels), and Push panics outside it — an
// unknown or unbounded priority range is a design error, not input.
//
// Equal priorities are FIFO (the per-level slice is a real queue),
// which the heap does not provide. Not goroutine-safe.
type Buckets[T any] struct {
	levels   int
	priority func(T) int
	buckets  [][]T // buckets[p] is the FIFO for priority p
	size     int
}

// NewBuckets returns an empty bucket queue with the given number of
// priority levels (must be ≥ 1). priority extracts a T's level and
// must not be nil.
func NewBuckets[T any](levels int, priority func(T) int) *Buckets[T] {
	if levels < 1 {
		panic("pqueue: NewBuckets requires at least one priority level")
	}
	if priority == nil {
		panic("pqueue: NewBuckets requires a priority function")
	}
	return &Buckets[T]{
		levels:   levels,
		priority: priority,
		buckets:  make([][]T, levels),
	}
}

// Push appends v to its level's queue. It panics if priority(v) is
// outside [0, levels).
func (b *Buckets[T]) Push(v T) {
	p := b.priority(v)
	if p < 0 || p >= b.levels {
		panic("pqueue: priority out of range")
	}
	b.buckets[p] = append(b.buckets[p], v)
	b.size++
}

// Pop removes the most urgent item — the oldest one at the lowest
// non-empty level. It inspects at most len(levels) buckets. ok is
// false when empty.
func (b *Buckets[T]) Pop() (T, bool) {
	var zero T
	for p := 0; p < b.levels; p++ {
		bk := &b.buckets[p]
		if len(*bk) == 0 {
			continue
		}
		v := (*bk)[0]
		*bk = (*bk)[1:]
		if len(*bk) == 0 {
			b.buckets[p] = nil // release the backing array on drain
		}
		b.size--
		return v, true
	}
	return zero, false
}

// Peek returns the most urgent item without removing it; ok is false
// when empty.
func (b *Buckets[T]) Peek() (T, bool) {
	var zero T
	for p := 0; p < b.levels; p++ {
		if len(b.buckets[p]) > 0 {
			return b.buckets[p][0], true
		}
	}
	return zero, false
}

// Len reports the number of queued items (kept as a counter — summing
// the levels would make it O(P) per call).
func (b *Buckets[T]) Len() int { return b.size }
