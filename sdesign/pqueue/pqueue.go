// Package pqueue implements priority queues behind one small
// interface, in three different structures — because "priority queue"
// is a contract, not a data structure: the right container depends on
// the shape of the priorities and the size of the queue, and that
// choice is the difference between a scheduler that scales and one
// that falls over at 50k items.
//
// The three structures, and the workload each is for:
//
//	Structure   Push        Pop        Peek   Pick it when
//	Heap        O(log n)    O(log n)   O(1)   general case: arbitrary or
//	                                          unbounded keys, large n
//	Buckets     O(1)        O(P)       O(P)   priorities are a small dense
//	                                          integer range; P (levels),
//	                                          not n, decides the cost
//	Sorted      O(n)        O(1) cmp   O(1)   n is tiny (~32), or equal
//	                                          priorities must stay FIFO
//
// n = items queued, P = priority levels (a constant in practice: 2–64).
// "O(1) cmp" = no comparisons, though the backing slice still shifts.
//
// Why these three — and not the others:
//
//   - container/heap, the standard library, has the right shape but
//     pays for `any`: every Push boxes the element (measured: 100
//     pushes = 100 allocations — see
//     TestHeapPushesAvoidTheBoxingAllocationsOfContainerHeap) and every
//     operation goes through non-inlinable interface dispatch. This
//     generic heap pushes with zero allocations.
//   - A binary heap is the default because it lives in one contiguous
//     array (cache-friendly, no per-node pointers), needs no
//     rebalancing, and its worst case is its typical case: O(log n)
//     both ways. Floyd-heapify builds from n items in ≤ 2n
//     comparisons (measured 2028 for n=1024 —
//     TestHeapifyIsLinearWhileRepeatedPushIsNot), where pushing one by
//     one on adversarial input costs ~n·log n.
//   - A bucket queue wins exactly when priorities are small integers:
//     pop scans at most P buckets instead of log n levels — for 8
//     schedulable classes that is ≤ 8 integer comparisons no matter
//     how deep the queue is
//     (TestBucketsServeHundredThousandItemsInPriorityOrder). The price:
//     the range must be known and bounded at construction, and an
//     out-of-range priority panics — a wrong bound is a programmer
//     error, not runtime input.
//   - A sorted slice loses asymptotically — O(n) insert, ~n²/4
//     comparisons to drain 1024 items
//     (TestComparisonCountsShowWhyTheHeapBeatsASortedSlice) — but at
//     tiny n it is unbeatable: one backward scan, no sift machinery,
//     O(1) peek. It is also the only structure whose equal priorities
//     stay in insertion order for free
//     (TestSortedSliceKeepsEqualPrioritiesInInsertionOrder).
//   - Deliberately absent: Fibonacci heaps (O(1) decrease-key is real,
//     but the constants and pointer-chasing lose to a plain binary
//     heap at every size you can actually allocate), balanced trees
//     and skip lists (they buy ordered iteration and delete-by-handle —
//     the right answer when you need range queries, which a queue does
//     not), and radix heaps (correct for integer keys in Dijkstra, but
//     a whole structure for one algorithm; the bucket/heap trade above
//     covers the same ground).
//
// Design decisions made explicit here:
//
//   - Queue is three methods — Push, Pop, Len — the substitution
//     point: write the scheduler against the contract, hand it the
//     structure the workload deserves. Peek exists on every structure
//     but stays out of the interface to keep it minimal.
//   - Lower priority value pops first (0 = most urgent), matching
//     min-heap and scheduler conventions. Buckets extracts the value
//     through an injected priority func, so any T fits.
//   - Not goroutine-safe, on purpose: these are data structures, not
//     services. A single-threaded scheduler pays nothing for locks it
//     does not take; wrap in a mutex when shared. (The blocking queue
//     package is concurrent because blocking is its contract — here
//     synchronization is the caller's decision.)
//   - Equal priorities: Heap promises no order (its tests never
//     assume any), Sorted and Buckets are FIFO within a priority.
//     Know which one your consumer needs before choosing.
package pqueue

// Queue is the contract all three structures satisfy: push an item,
// pop the most urgent one, ask the size. Lower priority value first.
type Queue[T any] interface {
	// Push adds v to the queue.
	Push(v T)
	// Pop removes and returns the most urgent item; ok is false when
	// the queue is empty (the zero value is returned alongside).
	Pop() (T, bool)
	// Len reports the number of queued items.
	Len() int
}

// Compile-time proof: every structure in this package is substitutable
// through the contract.
var (
	_ Queue[int] = (*Heap[int])(nil)
	_ Queue[int] = (*Buckets[int])(nil)
	_ Queue[int] = (*Sorted[int])(nil)
)
