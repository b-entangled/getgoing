# Distributed system design

Eight packages for the mechanisms that make independent machines agree,
share work, detect failure and stay consistent — each with the design
decision made explicit in its doc comment and proven by a named test.
End-to-end compositions live in [`DESIGNS.md`](DESIGNS.md).

```bash
go test -race ./distributed/...
```

---

## Consistent hashing — [`hashring/`](hashring)

**Intent.** Shard assignment for distributed caches and partitioned
stores. `key % N` remaps *every* key when a node joins; a consistent
hash ring remaps only the keys whose successor token changed — and since
only the joining node adds tokens, those keys are exactly the ones the
new node now owns. Virtual nodes (150 tokens per node) smooth the
randomness: fairer shares, smaller migration bands.

**Proving tests.**

| Test | Property |
|---|---|
| `TestGetNodeIsDeterministic` | the same key always maps to the same node |
| `TestAddingANodeOnlyMovesKeysOntoTheNewNode` | join moves nothing between old nodes |
| `TestRemovingANodeOnlyMovesItsKeys` | leave moves only the departed node's keys |
| `TestDistributionIsRoughlyEvenAcrossNodes` | vnodes keep hotspots away |
| `TestMembershipSurvivesChurn` | the invariant survives repeated joins/leaves |

## Quorums — [`quorum/`](quorum)

**Intent.** Read/write quorums over N replicas — the consistency
mechanism Dynamo-style systems use *instead of* consensus. If every read
set and write set must intersect (R + W > N), a read always observes the
latest write; if R + W ≤ N a disjoint pair exists and the read is stale —
not a bug, the price of not using consensus. Proved by **enumerating
every admissible pair of sets**, not by assertion.

**Proving tests.**

| Test | Property |
|---|---|
| `TestReadWriteSetsAlwaysIntersectWhenRPlusWExceedsN` | enumeration: overlap is forced |
| `TestReadAlwaysSeesTheLatestWriteWhenQuorumsOverlap` | the overlap replica carries the latest version |
| `TestStaleReadIsPossibleWhenRPlusWNDoesNotExceedN` | a stale read is actually constructed |
| `TestDisjointSetsExistExactlyWhenTheQuorumConditionFails` | the condition is tight, both directions |
| `TestSequentialWritesAreTotallyOrdered` | versions totally order the writes |

## Logical clocks — [`vclock/`](vclock)

**Intent.** Answering "did A happen before B?" with no shared clock:
**Lamport timestamps** — cheap, one-directional (A → B implies L(A) <
L(B), but equal timestamps do *not* prove concurrency — the exact
limitation the tests pin down), and **vector clocks** — one counter per
process, three-valued comparison (Before / After / Concurrent) that
catches what Lamport cannot.

**Proving tests.**

| Test | Property |
|---|---|
| `TestHappensBeforeImpliesSmallerLamportTimestamp` | Lamport's guarantee holds |
| `TestConcurrentEventsCanShareLamportTimestamps` | and its documented limitation too |
| `TestReceivePropagatesOrderingAcrossProcesses` | a receive jumps past the sender |
| `TestMessageExchangeMakesVectorsOrdered` | vectors order what Lamport only bounds |
| `TestIndependentWritesAreDetectedAsConcurrent` | the three-valued answer finds true concurrency |
| `TestMergeIsIdempotentAndMonotone` | merge never loses or oscillates |
| `TestVectorComparesEqualToItself` | reflexivity before anything else |

## Idempotency — [`idempotency/`](idempotency)

**Intent.** Turning at-least-once delivery into effectively-once
execution: concurrent callers for one key share a *single* execution
(one attempt, N results); successes are cached until TTL while failures
are not (a failed attempt produced no effect, so the next caller must be
allowed to try); waiters that abandon their context do not cancel the
shared work; in-flight entries are never mistaken for expired ones.

**Proving tests.**

| Test | Property |
|---|---|
| `TestConcurrentCallersShareOneExecution` | N callers, exactly one execution |
| `TestDifferentKeysRunIndependently` | dedup is scoped to the key |
| `TestFailuresAreNotCachedForLaterCallers` | errors don't become permanent |
| `TestSuccessIsCachedUntilTheTTLExpires` | the success window is exactly the TTL |
| `TestWaiterCancellationDoesNotCancelTheExecution` | giving up doesn't kill the shared attempt |
| `TestInFlightTracksEntries` | in-flight work is visible and countable |

## Election — [`election/`](election)

**Intent.** Agreeing on exactly one leader without a lock service:
a candidate token sweeps the ring collecting `max(id)` among live nodes
(dead ones skipped, so they cannot stall the ring), the originator
announces the winner under a fresh **term** — stale claims become
detectable — and every live node records the same leader and term.

**Proving tests.**

| Test | Property |
|---|---|
| `TestHighestLiveNodeBecomesLeader` | one election, one answer, everywhere |
| `TestElectionVisitsAndAnnouncesEachLiveNodeOnce` | the full ring is walked, not shortcut |
| `TestRingSkipsDeadNodes` | failures neither win nor block |
| `TestLeaderFailureTriggersUnanimousReElection` | new leader, newer term, no split-brain |
| `TestDeadCandidateCannotStartAnElection` | the failure detector is law at the entry point |
| `TestRevivedNodeKeepsTheLastAgreedViewUntilTheNextElection` | stale claims wait for the next term |
| `TestSingleNodeElectsItself` | degenerate clusters don't deadlock |

## Gossip membership — [`gossip/`](gossip)

**Intent.** SWIM-lite: full-mesh probing per round plus anti-entropy
exchange of each node's whole view. Misses escalate Suspect → Dead along
one detector path (3 → 6); **incarnation numbers beat suspicions** — any
node may accuse, only the accused (at a higher incarnation) can refute,
which is what makes revival after a false death safe; at equal
incarnation Alive < Suspect < Dead converges every disagreement the same
way.

**Proving tests.**

| Test | Property |
|---|---|
| `TestHealthyClusterReportsNoFalsePositives` | a perfect network invents nothing |
| `TestFailedNodeIsSuspectedAtThreeMisses` | detection at the documented threshold |
| `TestSuspectBecomesDeadAtSixMisses` | escalation at the documented threshold |
| `TestKilledNodeStopsProbingAndSharing` | a dead process runs no code |
| `TestRevivedNodeRefutesAndReturnsToAlive` | the refutation outruns the stale Dead |
| `TestReviveBeforeDetectionLeavesNoTrace` | brief outages leave no residue |
| `TestLiveNodeRefutesASuspicionAgainstItself` | refutation rides the self-record |
| `TestMergePrecedenceRules` | the convergence rule, as a truth table |
| `TestRepeatedChurnConvergesEachTime` | kill/revive cycles keep climbing, never stick |

## Raft (single-decree) — [`raft/`](raft)

**Intent.** A replicated log that agrees on command order across
crashing, partitioning machines: randomized (seeded) election timeouts
with majority votes and the **up-to-date rule** (no majority can elect
away its committed history); AppendEntries replication with
log-matching repair (walk `nextIndex` back, truncate the divergent
suffix); commit only over **current-term** entries (figure 8); any
higher-term message steps a leader down — terms totally order history.
Partitions are injected drops (`Sever`/`Heal`), not sleeps.

**Proving tests.**

| Test | Property |
|---|---|
| `TestSingleNodeElectsItselfAndCommits` | majority-of-one commits immediately |
| `TestFiveNodesElectExactlyOneLeaderInAgreement` | convergence: one leader, one term |
| `TestLeaderReplicatesAndCommitsOnMajority` | replication + the majority rule |
| `TestCommittedEntrySurvivesLeaderFailure` | committed data outlives its leader |
| `TestPartitionedFormerLeaderCannotOverwriteCommittedState` | the safety crown jewel |
| `TestVoteIsRememberedForTheTermOnly` | one ballot per term, reset across terms |
| `TestStaleTermVoteIsRejected` | old campaigns cannot win or move anyone |
| `TestHigherTermMessageStepsDownLeader` | the step-down rule in one message |
| `TestCandidateWithStaleLogIsDeniedVote` | the up-to-date rule at the RPC level |
| `TestDivergentSuffixIsTruncated` | stale tails are replaced wholesale |
| `TestTermsAreMonotonic` | terms only move forward, across churn |
| `TestSubmitOnNonLeaderIsRefused` | client writes are refused, not diverged |

## Saga — [`saga/`](saga)

**Intent.** Long-running workflows without distributed locks: steps run
forward; the first failure stops the line and compensates every
executed step **in reverse order** — undoing exactly the work that
happened, in the only order that ever existed. Compensations run on a
**detached context** (`context.WithoutCancel`) so a caller hanging up
mid-saga cannot leave half-applied state behind; a failing compensation
doesn't stop the rest — every error is joined, forward cause first.

**Proving tests.**

| Test | Property |
|---|---|
| `TestAllStepsSucceedAndNothingIsCompensated` | success runs no undo |
| `TestFailureCompensatesExecutedStepsInReverseOrder` | reverse prefix, and nothing more |
| `TestCompensationFailureStillRunsTheRestAndIsReported` | partial undo reports all errors |
| `TestCompensationsSurviveCallerCancellation` | cancellation doesn't skip the undo |
| `TestTraceRecordsEveryTransition` | the audit trail is complete and ordered |
