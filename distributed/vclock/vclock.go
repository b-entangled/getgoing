// Package vclock implements the two logical clocks distributed systems
// use to answer "did A happen before B?" when there is no shared clock:
// Lamport timestamps and vector clocks.
//
// Lamport's rule is cheap and one-directional: ticks only grow, a
// receive jumps the counter past the sender's. It guarantees
//
//	if A happens-before B, then L(A) < L(B)
//
// but not the converse — concurrent events can (and do) share a
// timestamp, so a smaller timestamp does not prove causality. The
// package proves exactly that limitation in a test; it is the reason
// vector clocks exist.
//
// Vector clocks keep one counter per process. Sending increments your
// entry; receiving merges (per-entry max) then increments. The
// comparison is then three-valued:
//
//	Before      every entry of A ≤ B and at least one <   (A → B)
//	After       every entry of B ≤ A and at least one <   (B → A)
//	Equal       identical
//	Concurrent  neither dominates the other               (A ∥ B)
//
// "Concurrent" is the valuable answer: it is the signal that two
// writes raced and must be merged or resolved by policy — the whole
// basis of conflict detection in multi-master replication.
package vclock

import "sync"

// Lamport is a Lamport timestamp counter for one process.
type Lamport struct {
	mu sync.Mutex
	c  uint64
}

// Tick records a local event and returns its timestamp.
func (l *Lamport) Tick() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.c++
	return l.c
}

// Receive folds a remote timestamp in on message arrival and returns
// this process's timestamp for the receive event — strictly greater
// than the sender's, so send → receive is always visible in the
// ordering.
func (l *Lamport) Receive(remote uint64) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if remote > l.c {
		l.c = remote
	}
	l.c++
	return l.c
}

// Now reports the current counter without advancing it.
func (l *Lamport) Now() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.c
}

// Vector is a clock with one counter per process — the map keys are
// process IDs.
type Vector map[string]uint64

// NewVector returns a zeroed vector for the given participants.
func NewVector(processes ...string) Vector {
	v := Vector{}
	for _, p := range processes {
		v[p] = 0
	}
	return v
}

// Increment records a local event by self.
func (v Vector) Increment(self string) {
	v[self]++
}

// Merge folds in another vector by taking the per-entry maximum —
// never going backwards on any process's counter.
func (v Vector) Merge(other Vector) {
	for p, c := range other {
		if c > v[p] {
			v[p] = c
		}
	}
}

// Clone returns an independent copy (a message payload, say).
func (v Vector) Clone() Vector {
	out := make(Vector, len(v))
	for p, c := range v {
		out[p] = c
	}
	return out
}

// Relation is the outcome of comparing two vectors.
type Relation int

const (
	// Before means a happened-before b (a dominates b? no: a is
	// strictly less — a → b).
	Before Relation = iota
	// After means b happened-before a.
	After
	// Equal means neither is behind.
	Equal
	// Concurrent means the two are causally unrelated — a conflict
	// a policy must resolve.
	Concurrent
)

func (r Relation) String() string {
	switch r {
	case Before:
		return "before"
	case After:
		return "after"
	case Equal:
		return "equal"
	default:
		return "concurrent"
	}
}

// Compare returns how a and b stand relative to each other.
func Compare(a, b Vector) Relation {
	aLess, bLess := false, false
	for p, ca := range a {
		cb := b[p]
		switch {
		case ca < cb:
			aLess = true
		case ca > cb:
			bLess = true
		}
	}
	for p, cb := range b {
		if _, ok := a[p]; !ok {
			if cb > 0 {
				aLess = true
			}
		}
	}
	switch {
	case aLess && bLess:
		return Concurrent
	case aLess:
		return Before
	case bLess:
		return After
	default:
		return Equal
	}
}
