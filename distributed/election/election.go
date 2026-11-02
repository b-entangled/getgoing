// Package election implements a token-ring leader election — how a
// cluster with no shared coordinator agrees on exactly one node to
// drive a critical section (a shard's primary, a scheduler, a
// metadata writer) without any external lock service.
//
// The algorithm, synchronous and deterministic:
//
//  1. A live node injects a candidate token carrying its own id.
//  2. The token walks the ring clockwise; every *live* node it visits
//     replaces the candidate with max(candidate, own id) — the dead
//     are skipped, so they cannot hold the token hostage.
//  3. When the token returns home, the originator announces the
//     winner: every live node records leader = max, term++.
//
// Design decisions made explicit here:
//
//   - Terms make stale announcements detectable: a leader claim from
//     an older term can never overwrite a newer one. Elections always
//     move the term forward.
//   - The failure detector is an oracle supplied by the cluster (in
//     production it would be the gossip package's suspicion). Keeping
//     it out of the algorithm is what makes election logic testable
//     without simulating networks.
//   - Ring order, not id order, decides traversal — but max(id) wins
//     the token, so a fixed, explainable winner rule survives any
//     ring layout.
//
// The invariant the tests hammer: after every election, every live
// node reports the same leader and the same term. Split-brain — two
// nodes believing they lead — cannot be produced by this algorithm
// under a single orchestrating election at a time.
package election

import (
	"fmt"
	"sort"
)

// EventKind labels one step of an election.
type EventKind int

const (
	// Visited: the candidate token passed through a node.
	Visited EventKind = iota
	// Announced: the winner was declared to a node.
	Announced
)

// Event is one observable step of an election.
type Event struct {
	Kind   EventKind
	Node   int
	Leader int
}

// Node is one cluster member's election state.
type Node struct {
	ID     int
	Leader int // 0 = no leader learned yet
	Term   int
}

// Cluster holds membership and liveness — the oracle.
type Cluster struct {
	ids   []int // sorted
	alive map[int]bool
	nodes map[int]*Node
	term  int
}

// New builds a cluster where every node starts alive, with no leader
// and term 0.
func New(ids ...int) *Cluster {
	if len(ids) == 0 {
		panic("election: need at least one node")
	}
	sorted := append([]int(nil), ids...)
	sort.Ints(sorted)
	c := &Cluster{
		ids:   sorted,
		alive: make(map[int]bool, len(sorted)),
		nodes: make(map[int]*Node, len(sorted)),
	}
	for _, id := range sorted {
		c.alive[id] = true
		c.nodes[id] = &Node{ID: id}
	}
	return c
}

// Kill marks a node failed: the ring skips it and it holds no state
// anyone trusts.
func (c *Cluster) Kill(id int) {
	if _, ok := c.nodes[id]; !ok {
		panic(fmt.Sprintf("election: unknown node %d", id))
	}
	c.alive[id] = false
}

// Revive marks a previously killed node live again (its learned
// leader/term stay as they were until the next election).
func (c *Cluster) Revive(id int) {
	if _, ok := c.nodes[id]; !ok {
		panic(fmt.Sprintf("election: unknown node %d", id))
	}
	c.alive[id] = true
}

// Alive reports whether id is currently live.
func (c *Cluster) Alive(id int) bool { return c.alive[id] }

// Start runs one election round begun by candidate and returns the
// event trace: the token's visits around the ring, then the winner's
// announcements. The candidate must be live.
func (c *Cluster) Start(candidate int) ([]Event, error) {
	if !c.alive[candidate] {
		return nil, fmt.Errorf("election: node %d is dead and cannot start an election", candidate)
	}

	var events []Event

	// Phase 1: the candidate token sweeps the ring, collecting the
	// maximum id among live nodes (dead ones are skipped).
	winner := candidate
	for _, id := range c.ringFrom(candidate) {
		winner = max(winner, id)
		events = append(events, Event{Kind: Visited, Node: id, Leader: winner})
	}

	// Phase 2: announce under a fresh term.
	c.term++
	for _, id := range c.ringFrom(candidate) {
		n := c.nodes[id]
		if c.term > n.Term {
			n.Leader = winner
			n.Term = c.term
		}
		events = append(events, Event{Kind: Announced, Node: id, Leader: winner})
	}
	// The originator itself records the outcome too.
	origin := c.nodes[candidate]
	if c.term > origin.Term {
		origin.Leader = winner
		origin.Term = c.term
	}
	return events, nil
}

// Leader returns the leader id node currently believes in (0 if none).
func (c *Cluster) Leader(id int) int { return c.nodes[id].Leader }

// Term returns the election term node last recorded.
func (c *Cluster) Term(id int) int { return c.nodes[id].Term }

// Agreement reports the shared view every live node has — the
// invariant tests assert after each election.
func (c *Cluster) Agreement() (leader, term int, unanimous bool) {
	first := true
	for _, id := range c.ids {
		if !c.alive[id] {
			continue
		}
		n := c.nodes[id]
		if first {
			leader, term, first = n.Leader, n.Term, false
			continue
		}
		if n.Leader != leader || n.Term != term {
			return 0, 0, false
		}
	}
	return leader, term, !first
}

// ringFrom lists live node ids clockwise starting *after* origin —
// the route the candidate token travels.
func (c *Cluster) ringFrom(origin int) []int {
	pos := sort.SearchInts(c.ids, origin) + 1
	var route []int
	for i := 0; i < len(c.ids); i++ {
		id := c.ids[(pos+i)%len(c.ids)]
		if id != origin && c.alive[id] {
			route = append(route, id)
		}
	}
	return route
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
