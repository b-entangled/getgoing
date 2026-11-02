package gossip

import "testing"

// The design-critical story: a node declared dead comes back with a
// higher incarnation, and that refutation beats the stale Dead
// accusation cluster-wide within a single round of gossip.
func TestRevivedNodeRefutesAndReturnsToAlive(t *testing.T) {
	n := NewNetwork(1, 2, 3, 4, 5)
	n.Kill(4)
	for r := 0; r < 6; r++ {
		n.Round() // 4 is Dead everywhere, incarnation 1
	}
	if !n.ViewIs(4, Dead, 1) {
		t.Fatal("precondition: 4 should be dead everywhere")
	}

	n.Revive(4)
	n.Round()

	if !n.ViewIs(4, Alive, 2) {
		for _, id := range n.LiveIDs() {
			t.Logf("view of 4 at %d: %+v", id, n.View(id, 4))
		}
		t.Fatal("after revival every live node should see 4 alive at incarnation 2")
	}
}

// Reviving before any suspicion leaves the cluster clean: same
// incarnation, no residue of the brief outage.
func TestReviveBeforeDetectionLeavesNoTrace(t *testing.T) {
	n := NewNetwork(1, 2, 3)
	n.Kill(2)
	n.Revive(2) // no rounds in between: nobody noticed

	for r := 0; r < 3; r++ {
		n.Round()
	}
	if !n.ViewIs(2, Alive, 0) {
		t.Fatal("brief outage should leave no suspicion behind")
	}
}

// Even *while* a live node is being slandered, its own next
// exchange refutes the claim: the refutation rides on its self-record.
func TestLiveNodeRefutesASuspicionAgainstItself(t *testing.T) {
	n := NewNetwork(1, 2, 3)
	// Forge the accusation directly into 1's view — as if gossip
	// carried a stale rumour from a partitioned era.
	n.nodes[1].peers[2] = Member{ID: 2, Incarnation: 1, State: Suspect}

	n.Round() // 2 probes 1 and 3: 2's self-record (inc 1, Alive) is heard

	// hear() on 2: accusation state Suspect at incarnation >= its own
	// triggers refutation only when heard *about itself* — which
	// happens when 1 gossips the forged record back to 2 via 1↔2
	// exchange (1 tells 2 its own forged view of 2).
	// Two rounds: first spreads the forgery to its victim, second
	// carries the refutation back out.
	n.Round()

	if got := n.View(2, 2); got.State != Alive {
		t.Fatalf("node 2 believes it is %s", got.State)
	}
	if m := n.View(1, 2); m.State != Alive || m.Incarnation <= 1 {
		t.Fatalf("view of 2 at node 1 = %+v, want alive at incarnation > 1 (refuted)", m)
	}
}

// Merge precedence, stated as a truth table:
//   - equal incarnation: worse state wins (converge on the pessimist);
//   - higher incarnation: wins regardless of state (fresher beats worse).
func TestMergePrecedenceRules(t *testing.T) {
	m := func(inc uint64, s State) Member { return Member{ID: 7, Incarnation: inc, State: s} }

	type c struct {
		local, remote, want Member
	}
	proper := []c{
		{m(1, Suspect), m(1, Alive), m(1, Suspect)},
		{m(1, Alive), m(1, Suspect), m(1, Suspect)},
		{m(1, Suspect), m(1, Dead), m(1, Dead)},
		{m(1, Dead), m(2, Alive), m(2, Alive)},
		{m(2, Alive), m(1, Dead), m(2, Alive)},
		{m(3, Dead), m(3, Suspect), m(3, Dead)},
	}
	for i, tc := range proper {
		if got := Merge(tc.local, tc.remote); got != tc.want {
			t.Fatalf("case %d: Merge(%+v, %+v) = %+v, want %+v", i, tc.local, tc.remote, got, tc.want)
		}
	}
}

// Two kill/revive cycles in a row: incarnations keep climbing and the
// cluster never settles on a stale verdict — history cannot win
// against a fresher incarnation, however old the argument.
func TestRepeatedChurnConvergesEachTime(t *testing.T) {
	n := NewNetwork(1, 2, 3)

	n.Kill(3)
	for r := 0; r < 6; r++ {
		n.Round()
	}
	if !n.ViewIs(3, Dead, 1) {
		t.Fatal("first death not detected")
	}
	n.Revive(3)
	n.Round()
	if !n.ViewIs(3, Alive, 2) {
		t.Fatal("first revival not accepted")
	}

	n.Kill(3)
	for r := 0; r < 6; r++ {
		n.Round()
	}
	if !n.ViewIs(3, Dead, 2) {
		t.Fatal("second death not detected at the new incarnation")
	}
	n.Revive(3)
	n.Round()
	if !n.ViewIs(3, Alive, 3) {
		t.Fatal("second revival not accepted")
	}
}
