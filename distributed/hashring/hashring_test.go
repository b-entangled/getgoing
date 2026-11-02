package hashring

import (
	"fmt"
	"testing"
)

func testRing(nodes ...string) *Ring {
	r := New(150)
	for _, n := range nodes {
		r.Add(n)
	}
	return r
}

func testKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}
	return keys
}

// Determinism: the same key on the same ring always maps the same
// way — otherwise replicas would disagree about ownership.
func TestGetNodeIsDeterministic(t *testing.T) {
	r := testRing("a", "b", "c")

	for _, k := range testKeys(200) {
		first := r.GetNode(k)
		if first == "" {
			t.Fatalf("no owner for %q", k)
		}
		for i := 0; i < 5; i++ {
			if got := r.GetNode(k); got != first {
				t.Fatalf("GetNode(%q) = %q then %q", k, first, got)
			}
		}
	}
}

// The defining property of consistent hashing: when a node joins,
// only keys that move end up on the *new* node. No key is shuffled
// from one old node to another — that would be migration the
// rebalance never asked for.
func TestAddingANodeOnlyMovesKeysOntoTheNewNode(t *testing.T) {
	r := testRing("a", "b", "c")
	keys := testKeys(2000)

	before := make(map[string]string, len(keys))
	for _, k := range keys {
		before[k] = r.GetNode(k)
	}

	r.Add("d")

	movedToOther := 0
	movedToD := 0
	for _, k := range keys {
		after := r.GetNode(k)
		if after == before[k] {
			continue
		}
		if after != "d" {
			movedToOther++
			t.Errorf("key %q moved %q → %q: only the joining node may receive keys",
				k, before[k], after)
		}
		movedToD++
	}
	if movedToOther != 0 {
		t.Fatalf("%d keys moved between existing nodes", movedToOther)
	}
	if movedToD == 0 {
		t.Fatal("no keys moved to the new node: it owns nothing")
	}
}

// Symmetrically: a node leaving takes *its* keys and only its keys —
// survivors keep their data.
func TestRemovingANodeOnlyMovesItsKeys(t *testing.T) {
	r := testRing("a", "b", "c", "d")
	keys := testKeys(2000)

	before := make(map[string]string, len(keys))
	for _, k := range keys {
		before[k] = r.GetNode(k)
	}

	r.Remove("b")

	for _, k := range keys {
		after := r.GetNode(k)
		if before[k] == "b" {
			continue // evicted keys may land anywhere else
		}
		if after != before[k] {
			t.Fatalf("key %q moved %q → %q though its node stayed: survivors must keep their keys",
				k, before[k], after)
		}
	}
}

// Virtual nodes smooth the distribution: no node starves or dominates
// by a wide margin. The band is generous because hashing is random —
// the point is "roughly even", not "exactly equal".
func TestDistributionIsRoughlyEvenAcrossNodes(t *testing.T) {
	const (
		nodes = 4
		keys  = 5000
	)
	r := testRing("n0", "n1", "n2", "n3")

	counts := map[string]int{}
	for _, k := range testKeys(keys) {
		counts[r.GetNode(k)]++
	}
	for _, n := range []string{"n0", "n1", "n2", "n3"} {
		share := float64(counts[n]) / keys
		if share < 0.10 || share > 0.45 {
			t.Errorf("node %s owns %.0f%% of keys: want roughly 25%% (10–45%% band)", n, share*100)
		}
	}
}

// Ring bookkeeping: membership and size stay consistent through
// add/remove churn (including re-adds and removing what is not there).
func TestMembershipSurvivesChurn(t *testing.T) {
	r := New(10)
	r.Add("x")
	r.Add("x") // duplicate: no-op
	r.Add("y")
	r.Remove("absent") // no-op
	r.Remove("x")

	got := r.Nodes()
	if len(got) != 1 || got[0] != "y" {
		t.Fatalf("Nodes = %v, want [y]", got)
	}
	if r.Size() != 10 {
		t.Fatalf("Size = %d, want 10 (only y's tokens)", r.Size())
	}

	r.Remove("y")
	if r.GetNode("anything") != "" {
		t.Fatal("empty ring must return no owner")
	}
}
