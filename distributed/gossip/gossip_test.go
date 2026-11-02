package gossip

import "testing"

// In a perfect network no node is ever suspected — gossip does not
// invent failures it has not observed.
func TestHealthyClusterReportsNoFalsePositives(t *testing.T) {
	n := NewNetwork(1, 2, 3, 4, 5)

	for r := 0; r < 6; r++ {
		n.Round()
	}
	if !n.ViewIs(1, Alive, 1) || !n.ViewIs(4, Alive, 1) || !n.ViewIs(5, Alive, 1) {
		t.Fatal("healthy cluster reported something other than alive")
	}
}

// Detection stage: three consecutive misses mark Suspect — and every
// live node's view agrees, both from its own probes and from gossip.
func TestFailedNodeIsSuspectedAtThreeMisses(t *testing.T) {
	n := NewNetwork(1, 2, 3, 4, 5)
	n.Kill(4)

	for r := 0; r < 3; r++ {
		n.Round()
	}
	if !n.ViewIs(4, Suspect, 0) {
		for _, id := range n.LiveIDs() {
			t.Logf("view of 4 at %d: %s", id, n.View(id, 4).State)
		}
		t.Fatal("after 3 missed probes every live node should see 4 suspect")
	}
	// Still the original incarnation: suspicion is an opinion, not a
	// re-identity.
	if m := n.View(1, 4); m.Incarnation != 1 {
		t.Fatalf("suspect record incarnation = %d, want 1", m.Incarnation)
	}
}

// Escalation stage: the same miss counters reach Dead at 6 — and the
// whole live cluster converges on it.
func TestSuspectBecomesDeadAtSixMisses(t *testing.T) {
	n := NewNetwork(1, 2, 3, 4, 5)
	n.Kill(4)

	for r := 0; r < 3; r++ {
		n.Round()
	}
	if !n.ViewIs(4, Suspect, 0) {
		t.Fatal("round 3 should show suspect, not yet dead")
	}
	for r := 0; r < 3; r++ {
		n.Round()
	}
	if !n.ViewIs(4, Dead, 1) {
		for _, id := range n.LiveIDs() {
			t.Logf("view of 4 at %d: %+v", id, n.View(id, 4))
		}
		t.Fatal("after 6 missed probes every live node should see 4 dead at incarnation 1")
	}
}

// A dead process runs no code: its own view never advances either.
func TestKilledNodeStopsProbingAndSharing(t *testing.T) {
	n := NewNetwork(1, 2, 3)
	n.Kill(1)

	for r := 0; r < 6; r++ {
		n.Round()
	}
	// Its view of 2 is still the bootstrap record — it never exchanged.
	if m := n.View(1, 2); m.Incarnation != 1 || m.State != Alive {
		t.Fatalf("dead node's view changed: %+v", m)
	}
	// Meanwhile the live nodes found it dead.
	if !n.ViewIs(1, Dead, 1) {
		t.Fatal("live nodes should see 1 dead")
	}
}
