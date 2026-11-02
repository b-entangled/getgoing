package quorum

import "testing"

// The quorum guarantee, proved exhaustively: with N=3 and R=W=2
// (R+W=4 > 3), *every* read set intersects *every* write set — so no
// read can miss the write.
func TestReadWriteSetsAlwaysIntersectWhenRPlusWExceedsN(t *testing.T) {
	const n, w, r = 3, 2, 2

	writeSets := combinations(n, w)
	readSets := combinations(n, r)
	if len(writeSets) != 3 || len(readSets) != 3 {
		t.Fatalf("enumeration produced %d/%d sets, want 3/3", len(writeSets), len(readSets))
	}

	for _, ws := range writeSets {
		for _, rs := range readSets {
			if !Overlap(ws, rs) {
				t.Fatalf("disjoint sets with R+W=%d > N=%d: write=%v read=%v", r+w, n, ws, rs)
			}
		}
	}
}

// The end-to-end statement of the same fact: after a write acked by W
// replicas, every admissible read observes the new value.
func TestReadAlwaysSeesTheLatestWriteWhenQuorumsOverlap(t *testing.T) {
	const n, w, r = 3, 2, 2
	s := New(n)

	v := s.Write("new", []int{0, 1}) // acked by 2 of 3

	for _, rs := range combinations(n, r) {
		got := s.Read(rs)
		if got.Version != v || got.Data != "new" {
			t.Fatalf("read sample %v saw %+v, want version %d — the intersection guarantees it",
				rs, got, v)
		}
	}
}

// The counter-example that makes the constraint meaningful: when
// R + W ≤ N there exist disjoint sets, and the read is stale. This is
// why "just use quorums" is not a consistency argument by itself.
func TestStaleReadIsPossibleWhenRPlusWNDoesNotExceedN(t *testing.T) {
	const n, w, r = 3, 1, 1
	s := New(n)

	v := s.Write("new", []int{0}) // acked by replica 0 only

	got := s.Read([]int{1}) // sample misses replica 0
	if got.Version == v {
		t.Fatalf("read unexpectedly saw version %d: the disjoint-set stale read must exist", v)
	}
	if got.Data == "new" {
		t.Fatal("stale read returned the new value")
	}
}

// Enumeration of the violation: with R=W=1 on N=3, disjoint pairs are
// constructible — for each write set there is at least one read set
// that misses it.
func TestDisjointSetsExistExactlyWhenTheQuorumConditionFails(t *testing.T) {
	const n = 3

	// R+W > N: no disjoint pair exists.
	foundDisjointWithQuorum := false
	for _, ws := range combinations(n, 2) {
		for _, rs := range combinations(n, 2) {
			if !Overlap(ws, rs) {
				foundDisjointWithQuorum = true
			}
		}
	}
	if foundDisjointWithQuorum {
		t.Fatal("found disjoint R=W=2 sets on N=3: quorum condition would be wrong")
	}

	// R+W <= N: disjoint pairs do exist.
	foundDisjointWithoutQuorum := false
	for _, ws := range combinations(n, 1) {
		for _, rs := range combinations(n, 1) {
			if !Overlap(ws, rs) {
				foundDisjointWithoutQuorum = true
			}
		}
	}
	if !foundDisjointWithoutQuorum {
		t.Fatal("no disjoint R=W=1 sets on N=3: stale reads would be impossible")
	}
}

// Versions increase with every write, and a read that overlaps the
// newest write sees exactly it — the ordering clients rely on for
// read-your-writes within one quorum-reading client.
func TestSequentialWritesAreTotallyOrdered(t *testing.T) {
	s := New(3)

	first := s.Write("a", []int{0, 1})
	second := s.Write("b", []int{1, 2})
	if second <= first {
		t.Fatalf("versions not increasing: %d then %d", first, second)
	}

	got := s.Read([]int{1, 2})
	if got.Version != second || got.Data != "b" {
		t.Fatalf("read = %+v, want the second write", got)
	}
}
