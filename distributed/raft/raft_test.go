package raft

import (
	"errors"
	"slices"
	"testing"
)

// leadersIn returns the leaders among the given nodes — during a
// partition the isolated side keeps a leader of its own, so tests
// that care about the majority must ask about the majority only.
func leadersIn(c *Cluster, nodes ...int) []int {
	var out []int
	for _, id := range nodes {
		if c.Role(id) == Leader {
			out = append(out, id)
		}
	}
	return out
}

func filterOut(ids []int, banned int) []int {
	var out []int
	for _, id := range ids {
		if id != banned {
			out = append(out, id)
		}
	}
	return out
}

// A one-node cluster elects itself and commits with a majority of one.
func TestSingleNodeElectsItselfAndCommits(t *testing.T) {
	c := NewCluster(1, 7)
	c.Run(25)

	leaders := c.Leaders()
	if len(leaders) != 1 || leaders[0] != 7 || c.Role(7) != Leader {
		t.Fatalf("leaders = %v, role = %v; want node 7 leading", leaders, c.Role(7))
	}
	if _, err := c.SubmitFrom(7, "hello"); err != nil {
		t.Fatalf("SubmitFrom = %v", err)
	}
	c.Run(2)
	if got := c.CommittedCommands(7); !slices.Equal(got, []string{"hello"}) {
		t.Fatalf("committed = %v, want [hello]", got)
	}
}

// After quiescence exactly one leader exists and every node agrees
// on its term — the convergence property raft exists to provide.
func TestFiveNodesElectExactlyOneLeaderInAgreement(t *testing.T) {
	c := NewCluster(99, 1, 2, 3, 4, 5)
	c.Run(60)

	leader, err := c.Leader()
	if err != nil {
		t.Fatalf("Leader() = %v (leaders found: %v)", err, c.Leaders())
	}
	lt := c.Term(leader)
	for _, id := range []int{1, 2, 3, 4, 5} {
		if c.Role(id) == Candidate {
			t.Fatalf("node %d still a candidate after quiescence", id)
		}
		if c.Term(id) != lt {
			t.Fatalf("node %d term = %d, want %d: everyone converges on the leader's term", id, c.Term(id), lt)
		}
	}
}

// A submitted command reaches a majority and commits — replication
// plus the majority rule, end to end.
func TestLeaderReplicatesAndCommitsOnMajority(t *testing.T) {
	c := NewCluster(3, 1, 2, 3, 4, 5)
	c.Run(60)
	leader, err := c.Leader()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubmitFrom(leader, "transfer:10"); err != nil {
		t.Fatal(err)
	}
	c.Run(10)

	committed := 0
	for _, id := range []int{1, 2, 3, 4, 5} {
		if slices.Contains(c.CommittedCommands(id), "transfer:10") {
			committed++
		}
	}
	if committed < 3 {
		t.Fatalf("%d/5 nodes committed, want a majority (3)", committed)
	}
}

// Leader failure: the entries that were committed survive the
// change of leadership, because every electable candidate must hold
// them (the up-to-date vote rule).
func TestCommittedEntrySurvivesLeaderFailure(t *testing.T) {
	c := NewCluster(11, 1, 2, 3, 4, 5)
	c.Run(60)
	old, err := c.Leader()
	if err != nil {
		t.Fatal(err)
	}
	c.SubmitFrom(old, "first")
	c.Run(10) // replicate everywhere, commit watermark on all

	c.Sever([]int{old}, []int{1, 2, 3, 4, 5})
	c.Run(60) // majority re-elects without the old leader

	// The isolated old leader may still believe it leads (raft: leaders
	// step down only on contact with a newer term) — the majority side
	// must have exactly one, and it must not be the partitioned node.
	side := leadersIn(c, 2, 3, 4, 5)
	side = filterOut(side, old)
	if len(side) != 1 {
		t.Fatalf("majority side leaders = %v, want exactly one", side)
	}
	newLeader := side[0]
	got := c.CommittedCommands(newLeader)
	if !slices.Contains(got, "first") {
		t.Fatalf("new leader %d committed %v, missing the pre-failure entry", newLeader, got)
	}
	if _, err := c.SubmitFrom(newLeader, "second"); err != nil {
		t.Fatal(err)
	}
	c.Run(10)
	got = c.CommittedCommands(newLeader)
	if !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("new leader committed %v, want [first second]", got)
	}
}

// The safety crown jewel: a partitioned old leader accepting writes
// it can never commit, then healing — its uncommitted tail must be
// discarded, the committed entry must not be lost, and the cluster
// must end with one log.
func TestPartitionedFormerLeaderCannotOverwriteCommittedState(t *testing.T) {
	c := NewCluster(42, 1, 2, 3, 4, 5)
	c.Campaign(1)
	c.Run(5)
	if leader, err := c.Leader(); err != nil || leader != 1 {
		t.Fatalf("after Campaign(1): leader %v, err %v", c.Leaders(), err)
	}

	c.SubmitFrom(1, "a")
	c.Run(10)
	for _, id := range []int{1, 2, 3, 4, 5} {
		if !slices.Equal(c.CommittedCommands(id), []string{"a"}) {
			t.Fatalf("node %d committed %v, want [a] everywhere before the partition", id, c.CommittedCommands(id))
		}
	}

	// Old leader alone: it happily appends, but nothing can replicate.
	c.Sever([]int{1}, []int{2, 3, 4, 5})
	if _, err := c.SubmitFrom(1, "b"); err != nil {
		t.Fatal(err)
	}
	c.Run(5)
	if !slices.Equal(c.CommittedCommands(1), []string{"a"}) {
		t.Fatalf("isolated leader committed %v: 'b' must stay uncommitted", c.CommittedCommands(1))
	}
	if !slices.Equal(c.LogCommands(1), []string{"a", "b"}) {
		t.Fatalf("isolated leader log = %v, want [a b]", c.LogCommands(1))
	}

	// The majority side moves on under a new leader and term. The
	// isolated node 1 is still a "leader" in its own bubble — by
	// design — so we ask the majority side only.
	c.Campaign(3)
	c.Run(40)
	side := leadersIn(c, 2, 3, 4, 5)
	if len(side) != 1 {
		t.Fatalf("majority side leaders = %v, want exactly one", side)
	}
	newLeader := side[0]
	c.SubmitFrom(newLeader, "c")
	c.Run(10)
	if !slices.Contains(c.CommittedCommands(newLeader), "c") {
		t.Fatalf("majority leader committed %v, missing 'c'", c.CommittedCommands(newLeader))
	}

	// Heal: the stale leader rejoins, steps down, loses "b",
	// and converges on the majority log.
	c.Heal()
	c.Run(30)
	leaders := c.Leaders()
	if len(leaders) != 1 || leaders[0] == 1 {
		t.Fatalf("after heal: leaders = %v, want exactly the majority leader", leaders)
	}
	want := []string{"a", "c"}
	if got := c.LogCommands(1); !slices.Equal(got, want) {
		t.Fatalf("former leader log = %v, want %v ('b' discarded)", got, want)
	}
	if got := c.CommittedCommands(1); !slices.Equal(got, want) {
		t.Fatalf("former leader committed = %v, want %v", got, want)
	}
}

// RPC-level: a vote is remembered for exactly one term — the second
// candidate in the same term loses, a new term resets the ballot.
func TestVoteIsRememberedForTheTermOnly(t *testing.T) {
	c := NewCluster(1, 1, 2, 3)

	reps := c.Send(Message{Type: RequestVote, From: 2, To: 1, Term: 5})
	if len(reps) != 1 || !reps[0].VoteGranted {
		t.Fatalf("first candidate: %+v, want a granted vote", reps)
	}

	reps = c.Send(Message{Type: RequestVote, From: 3, To: 1, Term: 5})
	if len(reps) != 1 || reps[0].VoteGranted {
		t.Fatalf("second candidate in the same term got %v, want refusal", reps)
	}

	reps = c.Send(Message{Type: RequestVote, From: 3, To: 1, Term: 6})
	if len(reps) != 1 || !reps[0].VoteGranted {
		t.Fatalf("new term: %+v, want a granted vote (ballot reset)", reps)
	}
	if c.Term(1) != 6 {
		t.Fatalf("term = %d, want 6: the higher term was adopted", c.Term(1))
	}
}

// RPC-level: campaigns from an older term cannot win or even move
// the receiver — the echoed term is the correction.
func TestStaleTermVoteIsRejected(t *testing.T) {
	c := NewCluster(2, 1, 2, 3)
	c.Campaign(1)
	c.Run(5) // 1 leads at some term >= 1

	reps := c.Send(Message{Type: RequestVote, From: 2, To: 1, Term: 0})
	if len(reps) != 1 || reps[0].VoteGranted {
		t.Fatalf("stale campaign got %+v, want refusal", reps)
	}
	if reps[0].Term < 1 {
		t.Fatalf("refusal echoes term %d, want the leader's current term", reps[0].Term)
	}
}

// RPC-level: any higher-term message steps a leader down — the one
// rule that ends stale leadership without a consensus protocol.
func TestHigherTermMessageStepsDownLeader(t *testing.T) {
	c := NewCluster(5, 1, 2, 3)
	c.Campaign(1)
	c.Run(5)
	if c.Role(1) != Leader {
		t.Fatalf("node 1 role = %v, want leader", c.Role(1))
	}

	c.Send(Message{
		Type: AppendEntries, From: 3, To: 1, Term: 99,
		PrevLogIndex: 0, PrevLogTerm: 0, LeaderCommit: 0,
	})
	if c.Role(1) != Follower {
		t.Fatalf("node 1 role = %v, want follower after a higher term arrived", c.Role(1))
	}
	if c.Term(1) != 99 {
		t.Fatalf("term = %d, want 99", c.Term(1))
	}
}

// The up-to-date vote rule: a candidate with a shorter log cannot
// collect votes, so no majority can elect away its committed history.
func TestCandidateWithStaleLogIsDeniedVote(t *testing.T) {
	c := NewCluster(7, 1, 2, 3)

	// Give node 1 a two-entry log of term 2.
	c.Send(Message{
		Type: AppendEntries, From: 3, To: 1, Term: 2,
		PrevLogIndex: 0, PrevLogTerm: 0,
		Entries: []Entry{{Term: 2, Command: "a"}, {Term: 2, Command: "b"}},
	})
	if got := c.LogCommands(1); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("setup failed: log = %v", got)
	}

	// An empty-log candidate at a fresh term: denied.
	reps := c.Send(Message{Type: RequestVote, From: 2, To: 1, Term: 3})
	if len(reps) != 1 || reps[0].VoteGranted {
		t.Fatalf("stale-log candidate got %+v, want refusal", reps)
	}

	// A candidate with an identical log: granted.
	reps = c.Send(Message{
		Type: RequestVote, From: 2, To: 1, Term: 4,
		LastLogIndex: 2, LastLogTerm: 2,
	})
	if len(reps) != 1 || !reps[0].VoteGranted {
		t.Fatalf("up-to-date candidate got %+v, want grant", reps)
	}
}

// Log matching: a divergent suffix is replaced wholesale — the
// mechanism that erases a stale leader's uncommitted tail.
func TestDivergentSuffixIsTruncated(t *testing.T) {
	c := NewCluster(9, 1, 2)

	c.Send(Message{
		Type: AppendEntries, From: 2, To: 1, Term: 2,
		PrevLogIndex: 0, PrevLogTerm: 0,
		Entries: []Entry{{Term: 2, Command: "old"}},
	})
	if got := c.LogCommands(1); !slices.Equal(got, []string{"old"}) {
		t.Fatalf("setup: log = %v", got)
	}

	// A newer leader's entry at the same index, different term.
	reps := c.Send(Message{
		Type: AppendEntries, From: 2, To: 1, Term: 3,
		PrevLogIndex: 0, PrevLogTerm: 0,
		Entries: []Entry{{Term: 3, Command: "new"}},
	})
	if len(reps) != 1 || !reps[0].Success {
		t.Fatalf("replacement push refused: %+v", reps)
	}
	if got := c.LogCommands(1); !slices.Equal(got, []string{"new"}) {
		t.Fatalf("log = %v, want [new]: the divergent suffix was replaced", got)
	}
}

// Terms never move backwards on any node, even across many
// elections and a healing partition — the ordering backbone of the
// whole protocol.
func TestTermsAreMonotonic(t *testing.T) {
	c := NewCluster(13, 1, 2, 3, 4, 5)
	c.Run(60)
	before := map[int]int{}
	for _, id := range []int{1, 2, 3, 4, 5} {
		before[id] = c.Term(id)
	}

	for round := 0; round < 3; round++ {
		leader, err := c.Leader()
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		c.Sever([]int{leader}, []int{1, 2, 3, 4, 5})
		c.Run(50)
		c.Heal()
		c.Run(20)
	}

	for _, id := range []int{1, 2, 3, 4, 5} {
		if c.Term(id) < before[id] {
			t.Fatalf("node %d term went %d → %d: terms only move forward", id, before[id], c.Term(id))
		}
	}
	if _, err := c.Leader(); err != nil {
		t.Fatalf("no leader after churn: %v", err)
	}
}

// Submitting on a non-leader is a client error, not a silent
// divergence: the write must be refused.
func TestSubmitOnNonLeaderIsRefused(t *testing.T) {
	c := NewCluster(17, 1, 2, 3)
	c.Run(60)
	leader, err := c.Leader()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 2, 3} {
		if id == leader {
			continue
		}
		if _, err := c.SubmitFrom(id, "x"); !errors.Is(err, ErrNotLeader) {
			t.Fatalf("SubmitFrom(%d) = %v, want ErrNotLeader", id, err)
		}
	}
}
