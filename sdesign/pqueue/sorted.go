package pqueue

// Sorted is a queue kept in ascending order by less: Push inserts at
// the exact position (upper bound, so equal priorities append after
// their peers), Pop takes the front. It is the small-n choice — no
// heap machinery, a single scan per op — and the only structure here
// that keeps equal priorities in insertion order without extra work.
// O(n) insert makes it the wrong answer past a few dozen items; see
// TestComparisonCountsShowWhyTheHeapBeatsASortedSlice. Not
// goroutine-safe.
type Sorted[T any] struct {
	items []T
	less  func(a, b T) bool
}

// NewSorted returns an empty sorted queue ordered by less (must not
// be nil).
func NewSorted[T any](less func(a, b T) bool) *Sorted[T] {
	if less == nil {
		panic("pqueue: NewSorted requires a less function")
	}
	return &Sorted[T]{less: less}
}

// Push inserts v after every item that does not outrank it: scan
// back from the tail until an item is ≤ v (equality stops the scan —
// that upper-bound rule is what makes equal priorities FIFO), then
// shift the tail right by one. The scan is the O(n) insert; at the
// tiny sizes this structure is for, a backward scan beats binary
// search (no mid division, straight-line compares).
func (s *Sorted[T]) Push(v T) {
	i := len(s.items)
	for i > 0 && s.less(v, s.items[i-1]) {
		i--
	}
	s.items = append(s.items, v)
	copy(s.items[i+1:], s.items[i:])
	s.items[i] = v
}

// Pop removes the most urgent item; ok is false when empty.
func (s *Sorted[T]) Pop() (T, bool) {
	var zero T
	if len(s.items) == 0 {
		return zero, false
	}
	v := s.items[0]
	if len(s.items) == 1 {
		s.items = nil
		return v, true
	}
	s.items = s.items[1:]
	return v, true
}

// Peek returns the most urgent item without removing it; ok is false
// when empty.
func (s *Sorted[T]) Peek() (T, bool) {
	var zero T
	if len(s.items) == 0 {
		return zero, false
	}
	return s.items[0], true
}

// Len reports the number of queued items.
func (s *Sorted[T]) Len() int { return len(s.items) }
