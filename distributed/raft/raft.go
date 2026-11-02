// Package raft implements single-decree Raft: how a replicated log
// agrees on an ordered sequence of commands without any node
// trusting any other node — how state machines stay identical across
// machines that crash, partition, and come back with stale memories.
//
// The shape: a follower times out and campaigns (term++, self-vote,
// RequestVote to peers); a candidate that collects a majority becomes
// leader; the leader appends client commands and pushes them with
// AppendEntries; an entry is committed — safe to apply — once a
// majority stores it *in the leader's current term*; a lagging or
// divergent follower is repaired by the log-matching rule: walk
// nextIndex back until prevLogIndex/prevLogTerm agree, then replace
// everything after it.
//
// Design decisions made explicit here:
//
//   - Terms totally order history: any message carrying a higher
//     term forces the receiver to step down and reset its vote — one
//     comparison ends every split-brain and stale-leader story.
//   - Election-timeout jitter comes from a seeded RNG, not the wall
//     clock, so every "who wins the next election?" is reproducible
//     while split votes still resolve.
//   - Votes go only to candidates whose log is at least as up-to-date
//     (last term, then length) — the invariant that makes a newly
//     elected leader hold every committed entry, because no majority
//     will vote against its own committed history.
//   - Commit only advances over entries from the *current* term
//     (Raft's figure-8 rule): counting a prior term's entry by
//     replication alone can commit a log state no leader has ever
//     seen. Committing an index commits its whole prefix, so later
//     entries still carry the watermark over earlier ones.
//   - The transport is an in-process FIFO queue with an injectable
//     partition (Sever/Heal): tests build network failure out of
//     drops instead of out of time.
package raft

import (
	"errors"
	"fmt"
	"math/rand"
)

// Role is a node's current raft role.
type Role int

const (
	// Follower: accepts log entries and votes when asked.
	Follower Role = iota
	// Candidate: campaigning in its current term.
	Candidate
	// Leader: accepting commands, replicating, committing.
	Leader
)

func (r Role) String() string {
	switch r {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	default:
		return "leader"
	}
}

// Entry is one log entry: the term that created it and its command.
type Entry struct {
	Term    int
	Command string
}

// MsgType labels a raft message.
type MsgType int

const (
	// RequestVote: a candidate asking for a vote.
	RequestVote MsgType = iota
	// RequestVoteResp: the answer.
	RequestVoteResp
	// AppendEntries: leader replication or heartbeat.
	AppendEntries
	// AppendEntriesResp: acceptance or rejection of the push.
	AppendEntriesResp
)

// Message is one raft RPC carried by the transport.
type Message struct {
	Type MsgType
	From int
	To   int
	Term int

	// RequestVote fields:
	LastLogIndex int
	LastLogTerm  int
	VoteGranted  bool

	// AppendEntries fields:
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []Entry
	LeaderCommit int

	// Response fields:
	Success    bool
	MatchIndex int // follower's log high-water on success
}

// node is one participant's raft state.
type node struct {
	id   int
	role Role
	term int

	votedFor int // -1 = nobody yet in this term

	log         []Entry // log[0] is a dummy at index 0, term 0
	commitIndex int

	electionElapsed int
	electionTimeout int

	heartbeatElapsed int
	heartbeatTimeout int

	nextIndex  map[int]int // leader only
	matchIndex map[int]int // leader only

	votes map[int]bool // candidate only
}

func (n *node) lastLogIndex() int { return len(n.log) - 1 }

func (n *node) lastLogTerm() int { return n.log[len(n.log)-1].Term }

// stepDown abandons leadership/candidacy; the caller has already
// adopted the newer term.
func (n *node) stepDown() {
	n.role = Follower
	n.votes = nil
	n.nextIndex, n.matchIndex = nil, nil
}

// Cluster is an in-process raft cluster: nodes, a FIFO transport, a
// partition switch, and a seeded election clock.
type Cluster struct {
	nodes   map[int]*node
	ids     []int
	queue   []Message
	rng     *rand.Rand
	blocked map[[2]int]bool
}

var (
	// ErrNotLeader: the target node is not the leader.
	ErrNotLeader = errors.New("raft: not the leader")
	// ErrNoLeader: no leader exists yet.
	ErrNoLeader = errors.New("raft: no leader")
)

const (
	electionBase   = 10
	electionJitter = 10
	heartbeatEvery = 3
	maxDeliveries  = 10000
)

// NewCluster builds a cluster of ids: all followers at term 0 with
// empty logs. seed drives election-timeout jitter so elections are
// reproducible.
func NewCluster(seed int64, ids ...int) *Cluster {
	if len(ids) == 0 {
		panic("raft: need at least one node")
	}
	c := &Cluster{
		nodes:   make(map[int]*node, len(ids)),
		ids:     append([]int(nil), ids...),
		rng:     rand.New(rand.NewSource(seed)),
		blocked: make(map[[2]int]bool),
	}
	for _, id := range ids {
		c.nodes[id] = &node{
			id:               id,
			role:             Follower,
			votedFor:         -1,
			log:              []Entry{{}}, // dummy at index 0
			electionTimeout:  electionBase + c.rng.Intn(electionJitter),
			heartbeatTimeout: heartbeatEvery,
		}
	}
	return c
}

// Role returns id's current role.
func (c *Cluster) Role(id int) Role { return c.nodes[id].role }

// Term returns id's current term.
func (c *Cluster) Term(id int) int { return c.nodes[id].term }

// Leaders returns every node currently in the Leader role — tests
// assert the length is at most one after quiescence.
func (c *Cluster) Leaders() []int {
	var out []int
	for _, id := range c.ids {
		if c.nodes[id].role == Leader {
			out = append(out, id)
		}
	}
	return out
}

// Leader returns the single leader if exactly one exists.
func (c *Cluster) Leader() (int, error) {
	leaders := c.Leaders()
	switch len(leaders) {
	case 1:
		return leaders[0], nil
	case 0:
		return 0, ErrNoLeader
	default:
		return 0, fmt.Errorf("raft: split brain: leaders %v", leaders)
	}
}

// LogCommands returns the command sequence of id's full log.
func (c *Cluster) LogCommands(id int) []string {
	log := c.nodes[id].log
	out := make([]string, 0, len(log)-1)
	for _, e := range log[1:] {
		out = append(out, e.Command)
	}
	return out
}

// CommittedCommands returns the command prefix id has committed —
// the part safe to apply to a state machine.
func (c *Cluster) CommittedCommands(id int) []string {
	n := c.nodes[id]
	out := make([]string, 0, n.commitIndex)
	for _, e := range n.log[1 : n.commitIndex+1] {
		out = append(out, e.Command)
	}
	return out
}

// Campaign forces id to start an election immediately — the test
// oracle for "leader X is gone, now what".
func (c *Cluster) Campaign(id int) error {
	n, ok := c.nodes[id]
	if !ok {
		return fmt.Errorf("raft: unknown node %d", id)
	}
	if n.role == Leader {
		return nil
	}
	c.startElection(n)
	return nil
}

// SubmitFrom appends command to id's log (as leader) and starts
// pushing it. Returns the log index.
func (c *Cluster) SubmitFrom(id int, command string) (int, error) {
	n, ok := c.nodes[id]
	if !ok {
		return 0, fmt.Errorf("raft: unknown node %d", id)
	}
	if n.role != Leader {
		return 0, ErrNotLeader
	}
	n.log = append(n.log, Entry{Term: n.term, Command: command})
	// A one-node cluster has a majority of one: commit right away.
	// In larger clusters this is a no-op until responses arrive.
	c.advanceCommit(n)
	c.broadcastAppend(n)
	return n.lastLogIndex(), nil
}

// Sever blocks all traffic between groupA and groupB in both
// directions until Heal. Messages already in flight are dropped at
// delivery time.
func (c *Cluster) Sever(groupA, groupB []int) {
	for _, a := range groupA {
		for _, b := range groupB {
			c.blocked[edge(a, b)] = true
		}
	}
}

// Heal removes every partition.
func (c *Cluster) Heal() { c.blocked = make(map[[2]int]bool) }

// Send injects one message synchronously and returns its replies —
// a raw protocol handle for RPC-level unit tests (Sever does not
// apply to injected messages).
func (c *Cluster) Send(m Message) []Message {
	var replies []Message
	if reply := c.handle(m); reply != nil {
		replies = append(replies, *reply)
	}
	return replies
}

// DeliverAll drains the transport queue (dropping partitioned
// messages) and returns how many were processed.
func (c *Cluster) DeliverAll() int {
	delivered := 0
	for len(c.queue) > 0 {
		m := c.queue[0]
		c.queue = c.queue[1:]
		delivered++
		if delivered > maxDeliveries {
			panic("raft: delivery did not quiesce — likely a retry loop")
		}
		if c.blocked[edge(m.From, m.To)] {
			continue // network drop
		}
		if reply := c.handle(m); reply != nil && !c.blocked[edge(reply.From, reply.To)] {
			c.queue = append(c.queue, *reply)
		}
	}
	return delivered
}

// Tick advances every node's clock by one tick: election timeouts
// fire (starting campaigns), leader heartbeats go out.
func (c *Cluster) Tick() {
	for _, id := range c.ids {
		n := c.nodes[id]
		n.electionElapsed++
		if n.role != Leader && n.electionElapsed >= n.electionTimeout {
			c.startElection(n)
		}
		if n.role == Leader {
			n.heartbeatElapsed++
			if n.heartbeatElapsed >= n.heartbeatTimeout {
				n.heartbeatElapsed = 0
				c.broadcastAppend(n)
			}
		}
	}
}

// Run ticks and fully delivers, `ticks` times — the "let the
// cluster run for a while" driver every integration test uses.
func (c *Cluster) Run(ticks int) {
	for i := 0; i < ticks; i++ {
		c.Tick()
		c.DeliverAll()
	}
}

// startElection moves a node into a new campaign: bump term, vote for
// self, ask everyone else.
func (c *Cluster) startElection(n *node) {
	n.term++
	n.votedFor = n.id
	n.role = Candidate
	n.votes = map[int]bool{n.id: true}
	n.electionElapsed = 0
	n.electionTimeout = electionBase + c.rng.Intn(electionJitter)

	for _, peer := range c.ids {
		if peer == n.id {
			continue
		}
		c.queue = append(c.queue, Message{
			Type:         RequestVote,
			From:         n.id,
			To:           peer,
			Term:         n.term,
			LastLogIndex: n.lastLogIndex(),
			LastLogTerm:  n.lastLogTerm(),
		})
	}
	c.checkVotes(n) // a one-node cluster wins immediately
}

// checkVotes promotes a candidate that has reached a majority.
func (c *Cluster) checkVotes(n *node) {
	if n.role == Candidate && len(n.votes)*2 > len(c.ids) {
		c.becomeLeader(n)
	}
}

// broadcastAppend pushes AppendEntries from a leader to every peer,
// using each peer's nextIndex.
func (c *Cluster) broadcastAppend(n *node) {
	for _, peer := range c.ids {
		if peer == n.id {
			continue
		}
		c.sendAppend(n, peer)
	}
}

func (c *Cluster) sendAppend(n *node, to int) {
	next := n.nextIndex[to]
	if next < 1 {
		next = 1
	}
	if next > len(n.log) {
		next = len(n.log)
	}
	prev := next - 1
	entries := append([]Entry(nil), n.log[next:]...)
	c.queue = append(c.queue, Message{
		Type:         AppendEntries,
		From:         n.id,
		To:           to,
		Term:         n.term,
		PrevLogIndex: prev,
		PrevLogTerm:  n.log[prev].Term,
		Entries:      entries,
		LeaderCommit: n.commitIndex,
	})
}

// handle routes one message to its target and returns a reply (nil
// for fire-and-forget messages). The term comparison at the top runs
// for *every* message — that is the global step-down rule.
func (c *Cluster) handle(m Message) *Message {
	n := c.nodes[m.To]
	if n == nil {
		return nil // reply addressed to an injected, non-existent peer
	}
	if m.Term > n.term {
		n.term = m.Term
		n.votedFor = -1
		n.electionElapsed = 0
		if n.role != Follower {
			n.stepDown()
		}
	}

	switch m.Type {
	case RequestVote:
		return c.handleVote(n, &m)
	case AppendEntries:
		return c.handleAppend(n, &m)
	case RequestVoteResp:
		c.handleVoteResp(n, &m)
	case AppendEntriesResp:
		c.handleAppendResp(n, &m)
	}
	return nil
}

// handleVote implements the grant rules: fresh enough term, not yet
// voted elsewhere, and the candidate's log at least as up-to-date as
// ours (last term, then length).
func (c *Cluster) handleVote(n *node, m *Message) *Message {
	resp := Message{Type: RequestVoteResp, From: n.id, To: m.From, Term: n.term}
	if m.Term < n.term {
		return &resp // stale campaign: refused, current term echoed back
	}

	upToDate := m.LastLogTerm > n.lastLogTerm() ||
		(m.LastLogTerm == n.lastLogTerm() && m.LastLogIndex >= n.lastLogIndex())

	if upToDate && (n.votedFor == -1 || n.votedFor == m.From) {
		n.votedFor = m.From
		n.electionElapsed = 0
		resp.VoteGranted = true
	}
	return &resp
}

func (c *Cluster) handleVoteResp(n *node, m *Message) {
	if n.role != Candidate || m.Term != n.term || !m.VoteGranted {
		return
	}
	n.votes[m.From] = true
	c.checkVotes(n)
}

// becomeLeader initializes replication state and pushes immediately:
// an idle new leader proving liveness is what followers watch for.
func (c *Cluster) becomeLeader(n *node) {
	n.role = Leader
	n.votes = nil
	n.heartbeatElapsed = 0
	n.nextIndex = make(map[int]int, len(c.ids))
	n.matchIndex = make(map[int]int, len(c.ids))
	for _, peer := range c.ids {
		if peer == n.id {
			continue
		}
		n.nextIndex[peer] = n.lastLogIndex() + 1
		n.matchIndex[peer] = 0
	}
	c.broadcastAppend(n)
}

// handleAppend implements replication and the log-matching rule.
func (c *Cluster) handleAppend(n *node, m *Message) *Message {
	resp := Message{Type: AppendEntriesResp, From: n.id, To: m.From, Term: n.term}
	if m.Term < n.term {
		return &resp // stale leader: refused, term echoed back
	}

	if n.role != Follower {
		n.stepDown()
	}
	n.electionElapsed = 0
	n.electionTimeout = electionBase + c.rng.Intn(electionJitter)

	// Log matching: the follower must actually hold prevLogIndex at
	// prevLogTerm, or the push is rejected and the leader walks back.
	if m.PrevLogIndex > n.lastLogIndex() ||
		n.log[m.PrevLogIndex].Term != m.PrevLogTerm {
		return &resp // Success stays false
	}

	// Append, truncating at the first divergent entry — a stale
	// leader's uncommitted tail dies here.
	for i, e := range m.Entries {
		idx := m.PrevLogIndex + 1 + i
		if idx <= n.lastLogIndex() {
			if n.log[idx].Term == e.Term {
				continue // same index, same term: same entry (log matching)
			}
			n.log = append(n.log[:idx:idx], m.Entries[i:]...)
			break
		}
		n.log = append(n.log, m.Entries[i:]...)
		break
	}

	if m.LeaderCommit > n.commitIndex {
		n.commitIndex = min(m.LeaderCommit, n.lastLogIndex())
	}
	resp.Success = true
	resp.MatchIndex = n.lastLogIndex()
	return &resp
}

// handleAppendResp advances replication on the leader and recomputes
// the commit watermark.
func (c *Cluster) handleAppendResp(n *node, m *Message) {
	if n.role != Leader || m.Term != n.term {
		return
	}
	if m.Success {
		if m.MatchIndex > n.lastLogIndex() {
			m.MatchIndex = n.lastLogIndex() // never match past our own log
		}
		if m.MatchIndex > n.matchIndex[m.From] {
			n.matchIndex[m.From] = m.MatchIndex
		}
		n.nextIndex[m.From] = n.matchIndex[m.From] + 1
		c.advanceCommit(n)
		if n.nextIndex[m.From] <= n.lastLogIndex() {
			c.sendAppend(n, m.From) // keep streaming the backlog
		}
		return
	}
	// Rejected: walk back one index and retry immediately —
	// convergence to the match point happens within one DeliverAll.
	if n.nextIndex[m.From] > 1 {
		n.nextIndex[m.From]--
	}
	c.sendAppend(n, m.From)
}

// advanceCommit moves commitIndex to the highest index stored on a
// majority *of the current term* (Raft figure 8). Committing an index
// commits its whole prefix.
func (c *Cluster) advanceCommit(n *node) {
	if n.role != Leader {
		return
	}
	majority := len(c.ids)/2 + 1
	for idx := n.lastLogIndex(); idx > n.commitIndex; idx-- {
		if n.log[idx].Term != n.term {
			continue // only current-term entries may anchor a commit
		}
		stored := 1 // ourselves
		for _, peer := range c.ids {
			if peer != n.id && n.matchIndex[peer] >= idx {
				stored++
			}
		}
		if stored >= majority {
			n.commitIndex = idx
			return
		}
	}
}

// edge is the undirected pair key for partition checks.
func edge(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}
