# System design (single-node building blocks)

The eight components almost every system design is assembled from —
time, caching, admission control, backpressure, pooling, durability,
fan-out, observability — each as a small package with the design
decision made explicit in its doc comment and proven by a named test.
Composed into end-to-end designs in [`DESIGNS.md`](DESIGNS.md).

```bash
go test -race ./sdesign/...
```

---

## Clock — [`clock/`](clock)

**Intent.** Abstract time out of everything with timeouts, TTLs or refill
rates so tests are driven by a manual clock (`Advance`) instead of
`time.Sleep` — slow, flaky sleep-based tests become instant and exact.
Dependency runs one way: production code depends on the `Clock` interface,
never on `time` directly.

**Proving tests.**

| Test | Property |
|---|---|
| `TestManualTimeOnlyMovesWhenAdvanced` | manual time is fully test-controlled |
| `TestAfterFiresOnlyWhenTheDeadlineIsReached` | waiters fire exactly at the deadline |
| `TestWaitersFireInDeadlineOrder` | multiple waiters come due in time order |
| `TestRealClockSatisfiesTheInterface` | the production clock satisfies the same interface |

## LRU cache — [`lru/`](lru)

**Intent.** Fixed-capacity, optionally time-expiring cache with
least-recent-used eviction — the memory bound every caching design starts
from (application caches, CDN edges, buffer pools). Reads and writes take
recency; TTL expiry is evaluated against an injected clock, so expiry is
tested by advancing time, not by waiting.

**Proving tests.**

| Test | Property |
|---|---|
| `TestEvictsLeastRecentlyUsedNotOldest` | eviction follows recency, not insertion time |
| `TestGetRefreshesRecency` | a read refreshes an entry's claim to stay |
| `TestUpdateInPlaceDoesNotEvict` | overwriting a present key costs no capacity |
| `TestTTLExpiresEntriesWhenTimeAdvances` | entries die exactly at their TTL |
| `TestPurgeRemovesOnlyExpiredEntries` | purge sweeps time, not the whole cache |
| `TestDeleteRemovesSpecificKey` | invalidation drops exactly one key |
| `TestConcurrentAccessKeepsCapacityBound` | the hard bound holds under `-race` contention |

## Rate limiting — [`ratelimit/`](ratelimit)

**Intent.** The two classic admission controls:
a **token bucket** — a burst of `burst` then a steady `rate` per second,
refilling continuously (API gateways, per-tenant quotas; degrades
gracefully, one slow second does not permanently penalise a caller), and
a **sliding window** — at most `limit` events whose timestamps all stay
inside the trailing window (hard quotas). Both are evaluated against an
injected clock: the tests *are* the passing of time.

**Proving tests.**

| Test | Property |
|---|---|
| `TestBucketAllowsTheBurstThenDenies` | burst capacity then steady state |
| `TestBucketRefillsWithElapsedTime` | tokens return with elapsed (advanced) time |
| `TestBucketRefillIsCappedAtBurst` | refill never exceeds the burst cap |
| `TestAllowNGrantsOrDeniesAsABatch` | a batch is all-or-nothing, never a partial grant |
| `TestWindowAllowsLimitThenDenies` | the limit holds exactly, then denies |
| `TestWindowSlidesWhenOldEventsAgeOut` | old events leave the window as time passes |
| `TestWindowNeverExceedsLimitAcrossSlidingTime` | sliding never admits more than `limit` |

## Blocking queue — [`queue/`](queue)

**Intent.** Bounded, blocking, generic FIFO — the backpressure primitive
at the heart of every pipeline: a producer that outruns its consumer must
*wait*, not silently buffer without limit. Fixed capacity at
construction; `Put` blocks when full, `Get` blocks when empty, both are
context-cancellable, and `Close` (owner-closes) drains what is buffered
before unblocking everyone with an `ErrClosed`-wrapped error.

**Proving tests.**

| Test | Property |
|---|---|
| `TestPutAndGetPreserveFIFOOrder` | the queue is a queue |
| `TestPutBlocksWhenFullUntilGet` | the bound produces real backpressure |
| `TestGetBlocksWhenEmptyUntilPut` | consumers wait instead of spinning |
| `TestContextCancellationUnblocksWaiters` | a cancelled caller always escapes |
| `TestCloseUnblocksWaiters` | close ends every pending wait with a wrapped error |
| `TestCloseDrainsBufferedItemsFirst` | close does not discard buffered work |
| `TestConcurrentProducersConsumersLoseNothing` | under `-race`, everything put is got |

## Priority queues — [`pqueue/`](pqueue)

**Intent.** "Priority queue" is a contract, not a data structure: three
containers behind one three-method interface — a binary **heap** (the
general case), a **bucket** queue (small dense integer levels: cost is
O(levels), independent of queue depth), and a **sorted slice** (tiny n,
FIFO among equals) — so a scheduler picks the container its workload
deserves instead of inheriting one. Lower priority value pops first; not
goroutine-safe by design. The full trade-off table and what is
deliberately absent live in the package doc and in
["Why different data structures are used"](#why-different-data-structures-are-used)
below.

**Proving tests.**

| Test | Property |
|---|---|
| `TestAllStructuresPopInPriorityOrder` | one contract: drain in order, nothing lost or duplicated |
| `TestAllStructuresReportLengthAndEmptyCorrectly` | Len is exact; Pop on empty is (0, false), never garbage |
| `TestPeekLeavesEveryStructureIntact` | Peek is a pure read — through a consumer-side narrowed interface |
| `TestAllStructuresHandleTheSameInterleavedScript` | 2000 interleaved ops match a count-array model, all three |
| `TestHeapifyIsLinearWhileRepeatedPushIsNot` | heapify ≤ 2n (Floyd); descending push-all ≥ 5n — measured |
| `TestHeapInvariantHoldsUnderInterleavedPushAndPop` | no child ever outranks its parent, op by op |
| `TestHeapPushesAvoidTheBoxingAllocationsOfContainerHeap` | generic: 0 allocs per 100 pushes; container/heap: 100 |
| `TestHeapOrdersStructKeysByTheSuppliedLess` | arbitrary T with a multi-field ordering |
| `TestNewHeapFromTakesOwnershipAndKeepsEveryItem` | in-place heapify (no copy), multiset preserved |
| `TestNewHeapPanicsWithoutALessFunction` | a nil ordering fails at construction |
| `TestBucketsPreserveFIFOWithinOnePriority` | level queues are FIFO — which the heap is not |
| `TestBucketsRejectAPriorityOutsideTheRange` | out-of-range panics before half-inserting |
| `TestBucketsServeHundredThousandItemsInPriorityOrder` | 100k pops bounded by 8 levels, order exact |
| `TestSortedSliceKeepsEqualPrioritiesInInsertionOrder` | stability: the drain equals a stable sort |
| `TestComparisonCountsShowWhyTheHeapBeatsASortedSlice` | ~n²/4 vs ~n·log n comparisons — ≥10× apart at n=1024 |

## Resource pool — [`pool/`](pool)

**Intent.** Bounded set of reusable resources — connections, workers,
file handles — so expensive setup is paid once and a traffic spike cannot
open an unbounded number of them. `maxOpen` is a hard ceiling (`Get`
blocks on the caller's context instead), idle resources are bounded too,
and a factory failure releases its slot rather than wedging the pool.

**Proving tests.**

| Test | Property |
|---|---|
| `TestGetReusesReturnedResources` | returns go back into circulation |
| `TestGetBlocksWhenExhaustedThenHonoursContext` | exhaustion blocks, cancellation frees it |
| `TestFactoryFailureDoesNotWedgeThePool` | a failed open does not leak its slot |
| `TestExcessReturnsAreDestroyed` | idle beyond `maxIdle` is closed, not leaked |
| `TestCloseDestroysIdleAndFailsNewGets` | close is terminal for idle and new |
| `TestNeverExceedsMaxOpenUnderContention` | the ceiling holds under `-race` |

## Write-ahead log — [`wal/`](wal)

**Intent.** Append-only, checksummed record file that survives process
crashes — the durability primitive underneath databases (commit before
page), queues (persist before ack) and log-shipping replicas.
`[4B length][4B CRC-32][payload]` framing; `Open` recovers a torn tail by
truncating to the last intact record and stops replay at the first error
the callback returns, so recovery is explicit rather than silent.

**Proving tests.**

| Test | Property |
|---|---|
| `TestAppendReplayRoundTrip` | what was appended is what replays |
| `TestOpenRecoversTornTailWithoutLosingDurableRecords` | a crash mid-write loses only the torn record |
| `TestCorruptTailFrameIsDetectedAndTruncated` | CRC failure is caught, prefix kept |
| `TestReplayStopsOnCallbackError` | the caller controls when replay aborts |
| `TestAppendAfterRecoveryContinuesAfterThePrefix` | the log stays usable post-recovery |
| `TestAppendRejectsOversizedRecords` | over-size writes fail instead of corrupting framing |

## Pub/sub broker — [`pubsub/`](pubsub)

**Intent.** Fan-out broker with an explicit policy for slow subscribers —
the decision every notification system must make and most code hides.
Publishers never block (one stuck consumer must not stall the others);
the buffer policy is chosen at subscribe time: **DropOldest** keeps the
newest window (live state, latest-value feeds) or **DropNewest** keeps
what already arrived (event streams that prefer not to skip ahead).

**Proving tests.**

| Test | Property |
|---|---|
| `TestPublishFansOutToEverySubscriber` | every subscriber sees every message |
| `TestDropOldestKeepsTheNewestWindow` | overflow discards history, not the head |
| `TestDropNewestKeepsWhatAlreadyArrived` | overflow discards new, not buffered |
| `TestCancelStopsDeliveryAndClosesTheChannel` | unsubscribe is prompt and closes cleanly |
| `TestCloseEndsEverySubscriber` | broker close terminates all channels |
| `TestConcurrentPublishSubscribeCancel` | all three operations race safely |

## Metrics — [`metrics/`](metrics)

**Intent.** The three instruments every observability layer is built
from — counters (monotonic), gauges (up/down), bucketed histograms
(latency quantiles, mean) — plus a name registry. Counters and gauges are
atomic because incrementing is on the hot path; histogram quantiles are
estimates within a known bucket width while the mean stays exact;
the registry hands out one instrument per name and rejects conflicting
histogram bounds for the same name.

**Proving tests.**

| Test | Property |
|---|---|
| `TestCounterConcurrentIncrementsAreExact` | atomic counting under `-race` loses nothing |
| `TestCounterRefusesToDecrease` | counters are monotonic by construction |
| `TestGaugeTracksRisingAndFallingValues` | gauges go both ways |
| `TestHistogramBucketAssignmentIsExact` | every value lands in its defined bucket |
| `TestQuantileStaysWithinItsBucket` | the estimate never lies beyond bucket width |
| `TestMeanIsExactEvenThoughQuantilesAreEstimates` | sum/count keep the mean exact |
| `TestRegistrySharesOneInstrumentPerName` | same name → same instrument (idempotent get) |
| `TestRegistryRejectsConflictingHistogramBounds` | same name with different bounds is an error |
| `TestRegistryIsRaceFree` | concurrent registration is safe |

---

## Why different data structures are used

The priority queue is the clearest case in this folder: the *same*
interface, three containers, and a wrong pick that stays invisible
until load. The questions the three answer — and the general rule they
demonstrate:

| The question | The answer | Structure |
|---|---|---|
| What does the key look like? | arbitrary / unbounded | heap (comparison-based) |
| | small dense integers, known range | bucket queue: cost is O(levels), not O(n) |
| How deep will the queue get? | large | heap: O(log n) both ways, one array, no rebalancing |
| | tiny (≤ ~32) | sorted slice: one backward scan, no sift machinery |
| Must equals stay FIFO? | yes | sorted slice or buckets (per-level FIFO) |
| | no | heap — cheapest, ties unspecified |
| Need delete-by-handle, range queries, decrease-key? | yes | balanced tree or skip list — deliberately not here |
| Worried about allocations (Go)? | yes | generic heap pushes with 0 allocs; `container/heap` boxes every element (measured 1 per push) |

Deliberately absent, and why: a **Fibonacci heap**'s O(1)
decrease-key is real on paper but its constants and pointer-chasing
lose to a plain binary heap at every size you can actually allocate. A
**radix heap** is right for integer keys inside Dijkstra — a whole
structure for one algorithm; the bucket/heap trade above covers the
same ground. **Balanced trees and skip lists** buy ordered iteration
and delete-by-handle: the correct answer the moment you need more than
pop-min, and the reason "priority queue" is a contract rather than a
type.

The same reasoning, scaled up, is why the rest of this folder differs:
the blocking FIFO in [`queue/`](queue) is chosen for *backpressure and
fairness*; a priority queue is chosen for *class separation* — and then
admission control ([`ratelimit/`](ratelimit)) is what keeps low
priorities from starving; a map would be chosen for *lookup*, a slice
for *order*. Structure follows the access pattern, and the pattern is a
design decision you should be able to state before writing the code.
