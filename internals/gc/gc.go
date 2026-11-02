// Package gc documents Go's garbage collector: a concurrent, tracing
// collector that answers "is this object reachable from a root?" instead
// of counting references.
//
// The consequences that matter:
//
//   - Cycles are collected. A group of objects that reference only each
//     other is unreachable once the program drops its last external
//     reference — refcounting would leak it, tracing does not. Prove it
//     with weak.Pointer: it goes nil once the referent is collected.
//   - An object's finalizer (runtime.SetFinalizer) runs *after* the
//     object becomes unreachable, on a dedicated goroutine. Tests
//     therefore poll with runtime.GC() rather than calling a single GC
//     and assuming.
//   - Finalizers run in dependency order: for an unreachable chain
//     a -> b with finalizers on both, a's runs first and b's only after
//     a is freed. A finalizer inside a *cycle* is the opposite case —
//     there is no valid order, so the runtime documents that neither the
//     cycle's collection nor the finalizer's execution is guaranteed.
//     Weak pointers, not finalizers, are how you observe a cycle die.
//   - Freed memory returns to the heap allocator, but HeapAlloc is only
//     sampled on read; tests must collect, then measure, and leave
//     generous margins (the collector runs concurrently in the
//     background).
//
// runtime.KeepAlive is what makes these measurements honest: without it
// a variable whose value is never read again is dead at its last read,
// and the collector is free to reclaim it before we measure.
package gc

import "runtime"

// Tracked is a heap object a test can watch for reclamation.
type Tracked struct {
	ID int
}

// Track arranges for done to be closed once t has been found unreachable
// by a collection. The caller must drop every reference to t afterwards,
// otherwise the finalizer can never run.
func Track(t *Tracked, done chan<- struct{}) {
	runtime.SetFinalizer(t, func(*Tracked) {
		close(done)
	})
}

// NewTracked allocates a Tracked with ID id.
func NewTracked(id int) *Tracked { return &Tracked{ID: id} }

// node is one link in a self-referential cycle.
type node struct {
	next *node
	id   int
}

// NewCycle builds a cycle of n nodes and returns the head. Every node is
// reachable from the head and the head from the last node; nothing else
// refers to them once the caller drops the head.
func NewCycle(n int) *node {
	if n <= 0 {
		return nil
	}
	head := &node{id: 0}
	cur := head
	for i := 1; i < n; i++ {
		cur.next = &node{id: i}
		cur = cur.next
	}
	cur.next = head
	return head
}

// NewChain builds an acyclic two-node chain a -> b (no back edge): the
// shape for observing finalizers run in dependency order.
func NewChain() (*node, *node) {
	a := &node{id: 1}
	b := &node{id: 2}
	a.next = b
	return a, b
}

// TrackChain puts a finalizer on each link of a chain; each finalizer
// closes its channel when it runs.
func TrackChain(a, b *node, doneA, doneB chan<- struct{}) {
	runtime.SetFinalizer(a, func(*node) { close(doneA) })
	runtime.SetFinalizer(b, func(*node) { close(doneB) })
}

// Collect forces a full garbage collection cycle.
func Collect() { runtime.GC() }

// HeapBytes forces a collection and reports HeapAlloc afterwards. The
// keep argument must be the live set the caller wants measured — it is
// passed to runtime.KeepAlive *after* the collection so the compiler
// cannot let it die before we read the heap.
func HeapBytes(keep any) uint64 {
	runtime.GC()
	runtime.KeepAlive(keep)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}
