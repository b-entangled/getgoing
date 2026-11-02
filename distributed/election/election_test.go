package election

import "testing"

// Every live node agrees on the highest-id node — one election, one
// answer, everywhere.
func TestHighestLiveNodeBecomesLeader(t *testing.T) {
	c := New(1, 2, 3, 4)

	if _, err := c.Start(1); err != nil {
		t.Fatal(err)
	}
	leader, term, ok := c.Agreement()
	if !ok || leader != 4 || term != 1 {
		t.Fatalf("agreement = (leader %d, term %d, ok %v), want (4, 1, true)", leader, term, ok)
	}
}

// The token visits every live node exactly once, then announces to
// every live node exactly once — the ring is walked, not shortcut.
func TestElectionVisitsAndAnnouncesEachLiveNodeOnce(t *testing.T) {
	c := New(10, 20, 30)

	events, err := c.Start(10)
	if err != nil {
		t.Fatal(err)
	}

	visits, announces := map[int]int{}, map[int]int{}
	for _, e := range events {
		switch e.Kind {
		case Visited:
			visits[e.Node]++
		case Announced:
			announces[e.Node]++
		}
	}
	for _, id := range []int{20, 30} {
		if visits[id] != 1 {
			t.Fatalf("node %d visited %d times, want 1", id, visits[id])
		}
		if announces[id] != 1 {
			t.Fatalf("node %d announced-to %d times, want 1", id, announces[id])
		}
	}
	if visits[10] != 0 {
		t.Fatalf("origin visited %d times: the token starts at its neighbour", visits[10])
	}
}

// A dead node must not win and must not stall the ring: the token
// skips it and the next-highest live node leads.
func TestRingSkipsDeadNodes(t *testing.T) {
	c := New(1, 2, 3, 4)
	c.Kill(4)

	if _, err := c.Start(1); err != nil {
		t.Fatal(err)
	}
	leader, term, ok := c.Agreement()
	if !ok || leader != 3 || term != 1 {
		t.Fatalf("agreement = (leader %d, term %d, ok %v), want (3, 1, true) — 4 is dead", leader, term, ok)
	}

	events, _ := c.Start(2)
	for _, e := range events {
		if e.Node == 4 {
			t.Fatal("the ring visited a dead node")
		}
	}
}

// Failure of the leader triggers a fresh election: a new leader, a
// newer term, still unanimous — and never the failed node again.
func TestLeaderFailureTriggersUnanimousReElection(t *testing.T) {
	c := New(1, 2, 3, 4)
	c.Start(1)
	first, firstTerm, _ := c.Agreement()
	if first != 4 {
		t.Fatalf("first leader = %d, want 4", first)
	}

	c.Kill(4)
	if _, err := c.Start(2); err != nil {
		t.Fatal(err)
	}
	leader, term, ok := c.Agreement()
	if !ok {
		t.Fatal("nodes disagree after re-election: split-brain")
	}
	if leader != 3 {
		t.Fatalf("new leader = %d, want 3 (the highest still alive)", leader)
	}
	if term <= firstTerm {
		t.Fatalf("term = %d after re-election, want > %d: stale claims must be detectable", term, firstTerm)
	}
}

// A dead candidate cannot run an election — the failure detector's
// word is law at the entry point.
func TestDeadCandidateCannotStartAnElection(t *testing.T) {
	c := New(1, 2, 3)
	c.Kill(3)

	if _, err := c.Start(3); err == nil {
		t.Fatal("dead candidate started an election")
	}
}

// Reviving a node does not retroactively change history: the old
// leader stands until someone runs a new election, and the revived
// node still reports the last agreed term.
func TestRevivedNodeKeepsTheLastAgreedViewUntilTheNextElection(t *testing.T) {
	c := New(1, 2, 3)
	c.Start(1) // leader 3, term 1

	c.Kill(3)
	c.Start(1) // leader 2, term 2
	c.Revive(3)

	// It missed the second election while dead: its view is stale —
	// including a claim of its own leadership. Acting on that without
	// the term check is exactly the split-brain terms exist to prevent.
	if got := c.Leader(3); got != 3 {
		t.Fatalf("revived node believes leader = %d, want its stale view (3)", got)
	}
	if got := c.Term(3); got != 1 {
		t.Fatalf("revived node term = %d, want the stale term 1 (it missed the election)", got)
	}

	// A later election with 3 back includes it in the sweep again.
	if _, err := c.Start(3); err != nil {
		t.Fatal(err)
	}
	leader, _, ok := c.Agreement()
	if !ok || leader != 3 {
		t.Fatalf("after re-election: leader %d unanimous %v, want 3", leader, ok)
	}
}

// Degenerate case: one live node elects itself — clusters must not
// deadlock waiting for peers that do not exist.
func TestSingleNodeElectsItself(t *testing.T) {
	c := New(7)
	if _, err := c.Start(7); err != nil {
		t.Fatal(err)
	}
	leader, term, ok := c.Agreement()
	if !ok || leader != 7 || term != 1 {
		t.Fatalf("agreement = (%d, %d, %v), want (7, 1, true)", leader, term, ok)
	}
}
