# Distributed designs (composed from `distributed/`)

Four end-to-end sketches assembling the eight mechanisms into systems
you can argue about in a design review — consistent hashing for
placement, gossip for membership, quorums for consistency, raft for
consensus, sagas for rollback, election for single-writer coordination,
idempotency for retry safety, vector clocks for causality. Each sketch
names which package owns which decision; the glue is wiring (and in two
places, deliberately left as the honest gaps production closes — marked
as such). The primitives are all fully implemented and race-tested next
door ([`README.md`](README.md)).

---

## 1. Sharded key-value store (place, see, agree, retry)

**Problem.** Store keys across N nodes: add a node without migrating
the world; know who is alive; read and write with a *stated*
consistency level; let clients retry writes without double-applying.

**Composition.**

```
placement: hashring.Ring      key → node (150 vnodes each)
membership: gossip.Network    who is alive / suspect / dead
consistency: quorum           R + W > N ⇒ read sees latest write
retries: idempotency          one attempt per key, N callers
```

**Where each decision lives.**

- *Placement*: `hashring` — `TestAddingANodeOnlyMovesKeysOntoTheNewNode`
  is the rebalance budget in executable form: joining moves only the
  new node's keys, leaving moves only its keys. When gossip says a node
  is `Dead`, the ring drops its tokens and the same test covers the
  removal direction.
- *Membership*: `gossip` — suspicion (3 misses) then death (6), with
  incarnation refutation so a node wrongly declared dead can return
  (`TestRevivedNodeRefutesAndReturnsToAlive`). The store's ring
  membership is driven by `Alive`/`Dead` transitions, never by a
  single observer's timeout.
- *Consistency*: `quorum` — the client picks R and W; the package's
  enumeration *proves* the intersection claim instead of asserting it.
  Choosing R + W ≤ N is a documented availability trade (stale reads
  possible), not an implementation bug.
- *Retry safety*: `idempotency` — a put retried after a timeout lands
  on the same execution (`TestConcurrentCallersShareOneExecution`):
  at-least-once RPC in, effectively-once apply.

**Sketch.**

```go
ring := hashring.New(nodeIDs, 150)
net := gossip.NewNetwork(nodeIDs...)
reg := quorum.New(5) // N

// On gossip transitions, recompute membership:
for _, id := range net.LiveIDs() {
    if net.View(id, id).State == gossip.Dead {
        ring.Remove(id) // only its keys move (hashring test)
    }
}

func get(ctx context.Context, key string, r int) (Value, error) {
    nodes := ring.GetN(key, r)              // read set of size R
    vals := fanOutRead(ctx, nodes)          // highest version wins
    return quorum.Latest(vals), nil         // R + W > N ⇒ latest
}

func put(ctx context.Context, key string, v Value) error {
    return idempotent.Do(ctx, "put:"+key, func(ctx context.Context) error {
        nodes := ring.GetN(key, w)          // write set of size W
        return fanOutWrite(ctx, nodes, v)   // versions assigned by writer
    }) // retry after timeout reuses the first attempt
}
```

**Failure story.** Node dies → gossip marks it Dead → ring moves only
its keys; node resurrects → refutation wins → keys rebalance back;
write ack lost → client retries → idempotency shares one execution;
R + W ≤ N chosen for availability → a stale read is *possible* and the
quorum package has already constructed the exact scenario.

**Honest gaps.** Real stores need hinted handoff/read repair (the
disjointness test is the motivation), and gossip's oracle here is the
test's `Kill`/`Revive` rather than real UDP timeouts.

---

## 2. Replicated ledger (consensus + durability + safe retries)

**Problem.** An append-only financial ledger that survives leader
crashes, network partitions and client retries — identical on every
replica, exactly once per transaction.

**Composition.**

```
consensus: raft.Cluster     leader, terms, majority commit
disk:      sdesign/wal      each node persists before ack (see sdesign)
retries:   idempotency      client txn-id → single execution
observe:   sdesign/metrics  commits, term changes, retry rate
```
(The last two live in [`sdesign/`](../sdesign/README.md); the ledger is
the canonical reason both folders exist.)

**Where each decision lives.**

- *Agreement*: `raft` — the whole safety story is in its tests:
  committed entries survive leader failure
  (`TestCommittedEntrySurvivesLeaderFailure`), a partitioned former
  leader cannot overwrite committed state
  (`TestPartitionedFormerLeaderCannotOverwriteCommittedState`),
  terms only move forward (`TestTermsAreMonotonic`).
- *Durability*: `wal` — raft's log on disk: `Append` before ack,
  torn-tail recovery on boot (`TestOpenRecoversTornTailWithoutLosingDurableRecords`).
  In production, each node persists an entry before voting or
  acknowledging it — the WAL frames give you that primitive.
- *Client retries*: `idempotency` — the transaction id is the key;
  a retry that lands while the first attempt is still in flight waits
  for it (`TestConcurrentCallersShareOneExecution`) instead of
  appending twice. Failures aren't cached, so a rejected txn may be
  corrected and resubmitted (`TestFailuresAreNotCachedForLaterCallers`).

**Sketch.**

```go
func submit(txnID string, cmd Command) error {
    return idempotent.Do(ctx, "txn:"+txnID, func(ctx context.Context) error {
        idx, err := raftCluster.SubmitFrom(leaderID, marshal(cmd))
        if err != nil {
            return err // not-leader: caller re-resolves, same key
        }
        if err := nodeWAL.Append(entryFor(idx, cmd)); err != nil {
            return err // durable before anyone is told "yes"
        }
        return awaitCommitted(ctx, idx) // majority in current term
    })
}
```

**Failure story.** Leader dies after majority-replicate but before ack →
client retries → same key → same execution → no second append; leader
dies after ack → committed entry survives (`raft` test), WAL replays it
on recovery; partition heals → stale leader steps down, its uncommitted
tail truncated (the partition test, line by line).

**Honest gaps.** The sketch appends to the WAL *after* raft accepted —
production persists **before** voting/acking (fig-8 / durable
majorities), which the in-process raft model doesn't simulate; and
membership changes (joint consensus) are out of scope for
single-decree raft.

---

## 3. Single-scheduler job orchestration (one leader, safe steps)

**Problem.** A cluster must run a scheduled workflow (billing runs,
backfills) with exactly one active scheduler, compensate when a step
fails, survive duplicate triggers, and answer "which of these events
caused which?" during an incident.

**Composition.**

```
coordination: election   exactly one scheduler per cluster
workflow:     saga       forward steps, reverse compensations
dup safety:   idempotency  duplicate triggers share one execution
forensics:    vclock     causal order for the incident timeline
```

**Where each decision lives.**

- *Single writer*: `election` — after any election every live node
  reports the same leader and term (`TestHighestLiveNodeBecomesLeader`),
  a dead leader triggers unanimous re-election with a fresh term
  (`TestLeaderFailureTriggersUnanimousReElection`), and a revived node
  keeps its stale view only until the next election — terms make the
  staleness detectable. Only the elected node runs the workflow.
- *Rollback*: `saga` — a failure at step k compensates exactly the
  executed prefix in reverse (`TestFailureCompensatesExecutedStepsInReverseOrder`),
  on a context detached from the caller's so a hung-up HTTP request
  can't leave half-applied state
  (`TestCompensationsSurviveCallerCancellation`).
- *Duplicate triggers*: `idempotency` — leadership flaps or a retried
  admin "run now" button must not start two billing runs for the same
  period: same key, one execution.
- *Causality*: `vclock` — each step's vector-clock context rides along
  (`TestIndependentWritesAreDetectedAsConcurrent`): when two schedulers
  briefly overlap during a leadership change, the three-valued compare
  says "concurrent" instead of guessing an order.

**Sketch.**

```go
func schedulerLoop(c *election.Cluster) {
    for {
        if leader, _, ok := c.Agreement(); ok && leader == myID {
            runTick() // only the agreed leader reaches here
        }
        c.Start(myID) // elect / re-elect after failures
    }
}

func runTick() {
    err := idempotent.Do(ctx, "run:"+periodKey, func(ctx context.Context) error {
        s := saga.New(reserveStep, chargeStep, notifyStep, ledgerStep)
        return s.Run(ctx) // reverse compensation on failure #saga
    })
    if err != nil { /* alert with s.Trace() as the audit log */ }
}
```

**Failure story.** Scheduler dies mid-saga → new leader elected (new
term) → retried key waits/shares rather than double-runs; step 3 fails →
steps 2..1 undo in reverse, every error joined; two schedulers overlap
during handover → `vclock.Compare` reports Concurrent, so the incident
timeline doesn't lie about ordering.

---

## 4. Service discovery & failover (see the failure, pick one owner)

**Problem.** Services register themselves; callers need to know who is
alive *and* route to exactly one writer — and a false death must be
correctable without splitting the cluster in two.

**Composition.**

```
detect & spread: gossip    alive / suspect / dead with incarnations
single owner:    election  one primary per shard, terms for staleness
refute:          gossip    higher incarnation beats any accusation
```

**Where each decision lives.**

- *Detection + dissemination*: `gossip` — misses escalate 3 → 6 along
  one detector path, every live node converges
  (`TestSuspectBecomesDeadAtSixMisses`), a dead process's own view
  freezes (`TestKilledNodeStopsProbingAndSharing`) so nothing pretends
  it still participates.
- *False positives are correctable*: the incarnation/refutation rule —
  the exact reason revival is safe
  (`TestRevivedNodeRefutesAndReturnsToAlive`, `TestMergePrecedenceRules`).
- *Who owns the write path*: `election` — callers take the leader from
  `Agreement()`; a *suspect* owner is still the owner until a new
  election says otherwise (suspicion informs, it doesn't failover —
  failover is a consensus decision, not an observation). Terms mean an
  announcement from before the failover can never be mistaken for one
  after it.

**Sketch.**

```go
net := gossip.NewNetwork(ids...)
ring := election.New(ids...)

func primary(shard int) (int, error) {
    if !net.ViewIs(shard /*owner*/, gossip.Dead, 0) {
        // owner still believed alive — but failover is election's call
    }
    leader, term, ok := ring.Agreement()
    if !ok {
        return 0, errNoAgreement // never guess during a transition
    }
    return leader, nil
}

// gossip transitions drive re-elections:
func onDead(id int) { ring.Start(anyLivePeer()) } // new term, new leader
```

**Failure story.** Owner crashes → gossip: suspect at 3, dead at 6 →
re-election under a fresh term → exactly one new primary
(`TestLeaderFailureTriggersUnanimousReElection`); owner was only
partitioned → it returns with incarnation+1, the refutation outruns the
stale accusation, no needless failover; during the gap,
`Agreement()` reports not-unanimous and callers fail fast rather than
write to two primaries.

**Honest gaps.** Real deployments separate "observed dead" (gossip) from
"stepped down" (consensus) with a lease or epoch check in front of the
write path; this sketch keeps that boundary visible instead of
collapsing it.

---

## Cross-cutting rules these designs share

1. **Observations are suspicions; decisions need quorums.** Gossip and
   election never conflate "I think it's dead" with "the shard moved".
2. **Every identity has an ordering**: raft terms, quorum versions,
   gossip incarnations, election terms — stale claims are always
   detectable, never silently accepted.
3. **Retries are a first-class input**: whatever clients do under
   timeouts (repeat, race, abandon) lands in `idempotency`, not in
   ad-hoc "we only call it once" assumptions.
4. **Failure paths are named tests**: reverse compensation, truncated
   stale tails, refuted deaths, disjoint quorums — each appears as a
   test name you can read before you read the code.
