// Package gossip implements SWIM-lite membership: how a cluster
// learns who is alive without any node talking to a central
// directory, and how it detects and disseminates failure — how
// "node 7 is gone" spreads to everyone else.
//
// One round = every live node probes every peer (real SWIM samples
// one peer per round; full probing keeps this model deterministic
// and is the same logic at a smaller scale). Every successful probe
// is also an anti-entropy exchange: each side merges the other's
// full membership view — the "gossip" in gossip: every conversation
// spreads everything both ways.
//
// Design decisions made explicit here:
//
//   - Incarnation numbers beat suspicions. Any node may hear "X is
//     dead"; only X, with a higher incarnation, can refute it. A
//     stale accusation can never outvote a fresher truth, which is
//     what makes revival safe after a false death.
//   - States escalate on a fixed precedence at equal incarnation —
//     Alive < Suspect < Dead — so two nodes hearing contradictory
//     reports converge on the same answer instead of ping-ponging.
//   - Miss counters escalate Suspect → Dead along the same detector
//     path (3 consecutive misses → Suspect, 6 → Dead), so a suspicion
//     is always observed before a death; tests assert both stages.
//   - Detection and dissemination are separate concerns: misses find
//     the failure locally, gossip tells everyone else.
//   - Liveness ("is the packet lost or is the node gone?") is an
//     oracle injected by the tests (Network deliverable). In
//     production it would be real timeouts plus the election
//     package; keeping it out is what makes the merge logic
//     testable without simulating networks.
package gossip

// State is a node's health as reported by a member.
type State uint8

const (
	// Alive: responding normally.
	Alive State = iota
	// Suspect: missed probes — believed failing, not confirmed gone.
	Suspect
	// Dead: missed probes long enough to be considered gone.
	Dead
)

func (s State) String() string {
	switch s {
	case Alive:
		return "alive"
	case Suspect:
		return "suspect"
	default:
		return "dead"
	}
}

// Member is one node's record as held in some member's view.
type Member struct {
	ID          int
	Incarnation uint64
	State       State
}

// Merge returns what a local record becomes after hearing remote —
// the convergence rule at the heart of the design:
//
//	higher incarnation wins outright;
//	at equal incarnation the worse state wins (Alive < Suspect < Dead).
//
// This is why every disagreement in the cluster is a *bounded*
// disagreement: whoever has the freshest information wins, and ties
// resolve the same way for everyone.
func Merge(local, remote Member) Member {
	if remote.Incarnation > local.Incarnation {
		return remote
	}
	if remote.Incarnation < local.Incarnation {
		return local
	}
	if remote.State > local.State {
		return remote
	}
	return local
}

const (
	suspectAfter = 3 // consecutive missed probes before Suspect
	deadAfter    = 6 // consecutive missed probes before Dead
)

// Node is one cluster member's membership state.
type Node struct {
	id          int
	incarnation uint64
	peers       map[int]Member // every member except self
	misses      map[int]int    // this node's consecutive probe misses per target
}

// self is the record this node gossip about itself — including it in
// every exchange is how a revived node's refutation travels.
func (n *Node) self() Member {
	return Member{ID: n.id, Incarnation: n.incarnation, State: Alive}
}

// hear folds one remote report into this node's view.
func (n *Node) hear(remote Member) {
	if remote.ID == n.id {
		// A report about myself:
		//   - a fresher incarnation than mine is adopted;
		//   - an accusation at incarnation >= mine is refuted by
		//     bumping my incarnation (standard SWIM refutation) —
		//     only I can say I am alive, and only with more proof.
		if remote.State != Alive && remote.Incarnation >= n.incarnation {
			n.incarnation = remote.Incarnation + 1
			return
		}
		if remote.Incarnation > n.incarnation {
			n.incarnation = remote.Incarnation
		}
		return
	}
	cur, ok := n.peers[remote.ID]
	if !ok {
		n.peers[remote.ID] = remote
		return
	}
	n.peers[remote.ID] = Merge(cur, remote)
}

// escalate applies this detector's miss thresholds for target.
func (n *Node) escalate(target int) {
	misses := n.misses[target]
	cur, ok := n.peers[target]
	if !ok {
		cur = Member{ID: target, Incarnation: 1, State: Alive}
	}
	switch {
	case misses >= deadAfter && cur.State < Dead:
		cur.State = Dead
	case misses >= suspectAfter && cur.State < Suspect:
		cur.State = Suspect
	}
	n.peers[target] = cur
}

// Network wires nodes together and models reachability — the failure
// oracle tests drive. Kill/Revive flip reachability; the membership
// logic never sees a packet, only "answered / did not answer".
type Network struct {
	nodes       map[int]*Node
	deliverable map[int]bool
	ids         []int
}

// NewNetwork builds a cluster of ids where every member starts alive
// at incarnation 1 and already knows everyone else.
func NewNetwork(ids ...int) *Network {
	if len(ids) == 0 {
		panic("gossip: need at least one node")
	}
	n := &Network{
		nodes:       make(map[int]*Node, len(ids)),
		deliverable: make(map[int]bool, len(ids)),
		ids:         append([]int(nil), ids...),
	}
	for _, id := range ids {
		n.deliverable[id] = true
	}
	for _, id := range ids {
		peers := make(map[int]Member, len(ids)-1)
		for _, other := range ids {
			if other != id {
				peers[other] = Member{ID: other, Incarnation: 1, State: Alive}
			}
		}
		n.nodes[id] = &Node{id: id, incarnation: 1, peers: peers,
			misses: make(map[int]int)}
	}
	return n
}

// Kill makes id unreachable: probes to it are dropped and it stops
// probing others (a dead process runs no code).
func (n *Network) Kill(id int) { n.deliverable[id] = false }

// Revive makes id reachable again and wakes it with an incarnation
// strictly greater than any accusation on record — the refutation
// that lets a wrongly-declared-dead node come back.
func (n *Network) Revive(id int) {
	maxSeen := uint64(0)
	for _, node := range n.nodes {
		if m, ok := node.peers[id]; ok && m.Incarnation > maxSeen {
			maxSeen = m.Incarnation
		}
		if node.id == id && node.incarnation > maxSeen {
			maxSeen = node.incarnation
		}
	}
	n.deliverable[id] = true
	self := n.nodes[id]
	self.incarnation = maxSeen + 1
	self.misses = make(map[int]int)
}

// View returns id's record of target (or its self-record when
// id == target). id must be live for the view to mean anything.
func (n *Network) View(id, target int) Member {
	node := n.nodes[id]
	if target == id {
		return node.self()
	}
	return node.peers[target]
}

// ViewIs reports whether every currently live node sees target in
// state (at incarnation inc, or any incarnation when inc == 0).
func (n *Network) ViewIs(target int, state State, inc uint64) bool {
	seen := false
	for _, id := range n.ids {
		if !n.deliverable[id] {
			continue
		}
		m := n.View(id, target)
		if inc != 0 && m.Incarnation != inc {
			return false
		}
		if m.State != state {
			return false
		}
		seen = true
	}
	return seen
}

// LiveIDs returns the ids that are currently reachable.
func (n *Network) LiveIDs() []int {
	var live []int
	for _, id := range n.ids {
		if n.deliverable[id] {
			live = append(live, id)
		}
	}
	return live
}

// Round runs one membership round: every live node probes every peer;
// a drop counts as a miss (escalating Suspect → Dead), an answer is a
// full anti-entropy exchange of both nodes' views.
func (n *Network) Round() {
	for _, detectorID := range n.ids {
		if !n.deliverable[detectorID] {
			continue // a dead process probes nothing
		}
		detector := n.nodes[detectorID]
		for _, targetID := range n.ids {
			if targetID == detectorID {
				continue
			}
			if !n.deliverable[targetID] {
				detector.misses[targetID]++
				detector.escalate(targetID)
				continue
			}
			delete(detector.misses, targetID)
			exchange(detector, n.nodes[targetID])
		}
	}
}

// exchange is one gossip conversation: each side learns the other's
// self-record (fresh incarnations, refutations) and full peer view.
func exchange(a, b *Node) {
	a.hear(b.self())
	b.hear(a.self())
	for _, m := range b.peers {
		a.hear(m)
	}
	for _, m := range a.peers {
		b.hear(m)
	}
}
