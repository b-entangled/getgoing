package vclock

import "testing"

// Lamport's guarantee: if one event happened before another, its
// timestamp is smaller — including across a message send/receive.
func TestHappensBeforeImpliesSmallerLamportTimestamp(t *testing.T) {
	a, b := &Lamport{}, &Lamport{}

	send := a.Tick()        // A sends a message
	recv := b.Receive(send) // B receives it

	if recv <= send {
		t.Fatalf("receive %d not after send %d: causal order invisible", recv, send)
	}
	// And a later local event at B is larger still.
	if later := b.Tick(); later <= recv {
		t.Fatalf("later tick %d not after receive %d", later, recv)
	}
}

// Lamport's documented limitation: the converse does not hold.
// Independent events on two processes can share a timestamp, so a
// Lamport clock cannot tell "concurrent" from "before".
func TestConcurrentEventsCanShareLamportTimestamps(t *testing.T) {
	a, b := &Lamport{}, &Lamport{}

	ta := a.Tick() // A: event with no communication at all
	tb := b.Tick() // B: independent event

	if ta != tb {
		t.Fatalf("expected the classic tie: %d vs %d", ta, tb)
	}
	// Equal timestamps prove nothing: neither event caused the other,
	// yet the clock cannot express that. (The vector below can.)
	if Compare(Vector{"a": ta}, Vector{"b": tb}) != Concurrent {
		t.Fatal("vector comparison should report these as concurrent")
	}
}

// A chain of messages orders the whole chain: A → B → C means every
// earlier timestamp is smaller.
func TestReceivePropagatesOrderingAcrossProcesses(t *testing.T) {
	a, b, c := &Lamport{}, &Lamport{}, &Lamport{}

	m1 := a.Tick()
	m2 := b.Receive(m1)
	m3 := c.Receive(m2)

	if !(m1 < m2 && m2 < m3) {
		t.Fatalf("chain not ordered: %d → %d → %d", m1, m2, m3)
	}
}

// The vector-clock guarantee in action: after A's message reaches B,
// B's vector strictly dominates A's — Compare says "after", not
// "concurrent".
func TestMessageExchangeMakesVectorsOrdered(t *testing.T) {
	a := NewVector("a", "b")
	b := NewVector("a", "b")

	a.Increment("a") // A writes
	msg := a.Clone()

	b.Merge(msg) // B receives
	b.Increment("b")

	if got := Compare(b, a); got != After {
		t.Fatalf("Compare(B, A) = %s, want after (B observed A's event)", got)
	}
	if got := Compare(a, b); got != Before {
		t.Fatalf("Compare(A, B) = %s, want before", got)
	}
}

// The answer Lamport cannot give: two writes with no communication
// are Concurrent — the signal that a merge/resolve policy must run.
func TestIndependentWritesAreDetectedAsConcurrent(t *testing.T) {
	a := NewVector("a", "b")
	b := NewVector("a", "b")

	a.Increment("a") // writer A, offline
	b.Increment("b") // writer B, offline

	if got := Compare(a, b); got != Concurrent {
		t.Fatalf("Compare = %s, want concurrent: these writes raced", got)
	}
}

// Merging is monotone and idempotent: replaying the same message
// changes nothing, and a merge never loses progress.
func TestMergeIsIdempotentAndMonotone(t *testing.T) {
	local := Vector{"a": 3, "b": 1}
	incoming := Vector{"a": 1, "b": 5, "c": 2}

	local.Merge(incoming)
	if Compare(local, Vector{"a": 3, "b": 5, "c": 2}) != Equal {
		t.Fatalf("merge result = %v, want {a:3 b:5 c:2}", local)
	}
	snapshot := local.Clone()
	local.Merge(incoming) // duplicate delivery
	if Compare(local, snapshot) != Equal {
		t.Fatalf("re-merging changed the vector: %v → %v", snapshot, local)
	}
}

// A vector always compares Equal to itself — the reflexivity the
// three-valued comparison depends on.
func TestVectorComparesEqualToItself(t *testing.T) {
	v := NewVector("a", "b")
	v.Increment("a")
	v.Increment("b")
	if got := Compare(v, v.Clone()); got != Equal {
		t.Fatalf("Compare(v, v) = %s, want equal", got)
	}
}
