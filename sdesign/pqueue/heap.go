package pqueue

// Heap is a binary min-heap of T ordered by less — the general-purpose
// choice: arbitrary keys, unbounded size, O(log n) push and pop, O(1)
// peek. Not goroutine-safe; not stable (equal priorities pop in
// unspecified order).
type Heap[T any] struct {
	items []T
	less  func(a, b T) bool
}

// NewHeap returns an empty heap ordered by less (must not be nil).
func NewHeap[T any](less func(a, b T) bool) *Heap[T] {
	if less == nil {
		panic("pqueue: NewHeap requires a less function")
	}
	return &Heap[T]{less: less}
}

// NewHeapFrom heapifies items in O(n) comparisons (Floyd) and takes
// ownership of the slice — it is reordered in place. Building n items
// one NewHeap.Push at a time costs ~n·log n comparisons instead; see
// TestHeapifyIsLinearWhileRepeatedPushIsNot.
func NewHeapFrom[T any](items []T, less func(a, b T) bool) *Heap[T] {
	if less == nil {
		panic("pqueue: NewHeapFrom requires a less function")
	}
	h := &Heap[T]{items: items, less: less}
	for i := len(h.items)/2 - 1; i >= 0; i-- {
		h.siftDown(i)
	}
	return h
}

// Push adds v and restores the heap invariant (sift up).
func (h *Heap[T]) Push(v T) {
	h.items = append(h.items, v)
	i := len(h.items) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !h.less(h.items[i], h.items[parent]) {
			break
		}
		h.items[i], h.items[parent] = h.items[parent], h.items[i]
		i = parent
	}
}

// Pop removes the most urgent item; ok is false when empty.
func (h *Heap[T]) Pop() (T, bool) {
	var zero T
	if len(h.items) == 0 {
		return zero, false
	}
	n := len(h.items) - 1
	h.items[0], h.items[n] = h.items[n], h.items[0]
	v := h.items[n]
	h.items = h.items[:n]
	if len(h.items) > 0 {
		h.siftDown(0)
	}
	return v, true
}

// Peek returns the most urgent item without removing it; ok is false
// when empty.
func (h *Heap[T]) Peek() (T, bool) {
	var zero T
	if len(h.items) == 0 {
		return zero, false
	}
	return h.items[0], true
}

// Len reports the number of queued items.
func (h *Heap[T]) Len() int { return len(h.items) }

// siftDown restores the invariant below i (heapify and Pop's
// root-swap).
func (h *Heap[T]) siftDown(i int) {
	for {
		left := 2*i + 1
		if left >= len(h.items) {
			return
		}
		min := left
		if right := left + 1; right < len(h.items) && h.less(h.items[right], h.items[left]) {
			min = right
		}
		if !h.less(h.items[min], h.items[i]) {
			return
		}
		h.items[i], h.items[min] = h.items[min], h.items[i]
		i = min
	}
}
