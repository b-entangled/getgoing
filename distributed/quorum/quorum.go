// Package quorum models read/write quorums over N replicas — the
// consistency mechanism Dynamo-style systems use instead of consensus:
// a read overlaps some replica the last write reached, provided
// R + W > N.
//
// The model, deliberately small: a single register replicated on N
// nodes. A write reaches a chosen set of replicas (the ones that
// acknowledged); a read samples a chosen set and takes the
// highest version it sees. Version numbers are assigned by the writer
// (a client sequence), exactly as Dynamo's vector of context does.
//
// What the tests prove — the whole point of the package:
//
//   - If every read set and every write set are guaranteed to
//     intersect (R + W > N), a read *must* observe the latest write:
//     the intersection replica has it. This is proved by enumeration,
//     not assertion — every admissible pair of sets is checked.
//   - If R + W ≤ N, a disjoint read/write pair exists, and the read
//     returns stale data. That is not a bug in this implementation;
//     it is the price of not using consensus, and the reason real
//     systems pair quorums with conflict resolution (last-writer-wins,
//     merge, read-repair) on top.
//
// The trade-off in one line: quorums give you "latest if the sets
// overlap" without electing a leader; they do not give you a single
// linearisable register when writes can be concurrent.
package quorum

// Value is one replica's view of the register. Version 0 means the
// replica has never been written.
type Value struct {
	Data    string
	Version int
}

// Store is the register replicated on N nodes.
type Store struct {
	replicas []Value
	seq      int // writer's version counter
}

// New returns a store on n replicas, all empty.
func New(n int) *Store {
	if n < 1 {
		panic("quorum: need at least one replica")
	}
	return &Store{replicas: make([]Value, n)}
}

// N reports the replica count.
func (s *Store) N() int { return len(s.replicas) }

// Write assigns the next version and places the value on exactly the
// replicas in `reached` (those are the ones that acknowledged; the
// caller's W is the size of that set).
func (s *Store) Write(data string, reached []int) int {
	s.seq++
	for _, i := range reached {
		s.replicas[i] = Value{Data: data, Version: s.seq}
	}
	return s.seq
}

// Read returns the highest-version value among the sampled replicas —
// what a coordinator with R = len(sample) observes.
func (s *Store) Read(sample []int) Value {
	best := Value{}
	for _, i := range sample {
		if v := s.replicas[i]; v.Version > best.Version {
			best = v
		}
	}
	return best
}

// Overlap reports whether two replica sets share at least one node.
func Overlap(a, b []int) bool {
	set := make(map[int]bool, len(a))
	for _, i := range a {
		set[i] = true
	}
	for _, j := range b {
		if set[j] {
			return true
		}
	}
	return false
}

// combinations returns every k-sized subset of [0, n) in order — the
// enumeration the quorum proofs walk.
func combinations(n, k int) [][]int {
	var out [][]int
	var build func(start int, cur []int)
	build = func(start int, cur []int) {
		if len(cur) == k {
			out = append(out, append([]int(nil), cur...))
			return
		}
		for i := start; i < n; i++ {
			build(i+1, append(cur, i))
		}
	}
	build(0, nil)
	return out
}
