# System designs (composed from `sdesign/`)

Four end-to-end sketches that assemble the eight building blocks into
systems you can point at in an interview or a design review. Each one
names which package owns which decision — the point of the split into
small packages is that the *decisions* compose even when the glue code
does not come for free. Code here is wiring, not a product: every
primitive it leans on is fully implemented and race-tested next door
([`README.md`](README.md)).

---

## 1. API gateway (admission → backpressure → upstream → observe)

**Problem.** Accept public traffic, cap per-tenant load, absorb bursts
without unbounded memory, bound the connections you open upstream, and
measure all of it — with tests that run in milliseconds.

**Composition.**

```
request → ratelimit.Bucket   (per-tenant admission)
        → queue.Queue        (bounded wait: shed or slow down, never grow)
        → pool.Pool          (bounded upstream connections)
        → handler
        → metrics.Registry   (count + latency histogram)
```

**Where each decision lives.**

- *Admission policy*: `ratelimit` — token bucket gives each tenant a
  burst plus a steady rate; the refill uses the injected `clock.Clock`,
  so "refill over 3 seconds" is an `Advance`, not a sleep.
- *Overload behaviour*: `queue` — the bounded queue turns "producer
  faster than consumer" into a **wait** (with the caller's context, so
  timeouts still work) instead of an ever-growing slice. The queue's
  capacity *is* the gateway's latency/memory trade-off knob.
- *Upstream protection*: `pool` — `maxOpen` caps connections per
  upstream; exhaustion blocks the queue's consumer instead of opening
  connection 10,001.
- *Observability*: `metrics` — `requests_total{tenant}`, a gauge for
  queue depth, a latency histogram. Counter is atomic: the hot path
  takes no lock.

**Sketch.**

```go
clk := clock.NewManual(time.Now())
limiter := ratelimit.NewBucket(clk, 100 /* req/s */, 50 /* burst */)
q := queue.New[*http.Request](100)
conns := pool.New(pool.Config[*Upstream]{MaxOpen: 32, MaxIdle: 8, Factory: dialUpstream})
reg := metrics.NewRegistry()

func handle(tenant string, r *http.Request) error {
    if !limiter.Allow() {
        return errRateLimited // 429 — before any work
    }
    ctx := r.Context()
    if err := q.Put(ctx, r); err != nil { // blocks; honours client timeouts
        return err
    }
    up, err := conns.Get(ctx) // blocks at 32 instead of opening #33
    if err != nil {
        return err
    }
    defer conns.Put(up)
    start := time.Now()
    err = up.Do(ctx, r)
    reg.Counter("requests_total").Inc() // name-keyed: dimensions = names
    reg.Histogram("latency_seconds", []float64{.005, .01, .025, .05, .1}).
        Observe(time.Since(start).Seconds())
    return err
}
```

**Failure story.** Traffic spike → bucket denies first (cheap, 429);
bucket admits but consumer lags → queue blocks (bounded memory, client
sees latency); upstream flaps → pool factory failure releases its slot
(`TestFactoryFailureDoesNotWedgeThePool`) rather than wedging every
waiting producer; shutdown → `Close` drains buffered work first
(`TestCloseDrainsBufferedItemsFirst`).

**How you'd test it.** Manual clock for refill, `-race` concurrency for
the bound (`TestNeverExceedsMaxOpenUnderContention`,
`TestConcurrentProducersConsumersLoseNothing`) — no `time.Sleep`
anywhere.

---

## 2. Read-through cache with invalidation fan-out

**Problem.** A service-side cache: bounded memory, TTL expiry,
recency-aware eviction, and a way to tell *other* processes when a key
goes stale — without the publisher waiting on slow subscribers.

**Composition.**

```
Get:  lru.Cache.Get     → miss → load from origin → Put
TTL:  lru TTL over clock.Manual   (expiry tested by Advance)
Stamp: metrics counters           (hits / misses / evictions)
Invalidate: pubsub.Broker (DropOldest, live state) → other nodes' lru.Delete
```

**Where each decision lives.**

- *What to forget*: `lru` — capacity is the hard memory bound; TTL uses
  the injected clock so `TestTTLExpiresEntriesWhenTimeAdvances` is the
  expiry spec. `Get` refreshes recency: an item that keeps being read
  never ages out.
- *How invalidation spreads*: `pubsub` — publishers never block; a node
  that stopped consuming its invalidation stream gets `DropOldest`
  (keep the newest window = latest invalidations win — right choice
  because an invalidation is a fact about *now*, dropping an old one is
  harmless). `TestDropOldestKeepsTheNewestWindow` is exactly that
  policy decision, executable.
- *What to report*: `metrics` — hit ratio is the cache's health number.

**Sketch.**

```go
clk := clock.NewManual(time.Now())
c := lru.New[string, *Blob](1024, 5*time.Minute, clk)
broker := pubsub.New[*Invalidation]()
invalidations, cancelListener := broker.Subscribe(64, pubsub.DropOldest)
defer cancelListener() // unsubscribe returns the cancel func

go func() { // this node's invalidation listener
    for inv := range invalidations {
        c.Delete(inv.Key) // targeted removal, not a sweep
        reg.Counter("invalidations_applied").Inc()
    }
}()

func get(key string) (*Blob, error) {
    if v, ok := c.Get(key); ok {
        reg.Counter("cache_hits").Inc()
        return v, nil
    }
    reg.Counter("cache_misses").Inc()
    v, err := loadFromOrigin(key)
    if err != nil {
        return nil, err
    }
    c.Put(key, v)
    return v, nil
}
```

**Failure story.** Slow subscriber → publisher unaffected (non-blocking
send); expired keys never served (clock-driven TTL, no background
sweeper needed until `Purge`); a hot key at capacity survives because
reads refresh recency (`TestGetRefreshesRecency`).

---

## 3. Durable job queue (persist before ack)

**Problem.** Accept jobs, survive a crash without losing or duplicating
accepted work, bound in-flight work, and retry safely.

**Composition.**

```
accept:  wal.Append (fsync record)     → ack to producer ONLY after append
ready:   queue.Queue (bounded RAM)     → worker demand backpressure
work:    pool.Pool (bounded workers/conns)
observe: metrics (accepted / failed / latency)
recovery: wal.Open at boot → re-feed every replayed job
```

**Where each decision lives.**

- *Durability*: `wal` — the record is on disk (length + CRC framed)
  before the producer is told yes; a crash mid-append loses only the
  torn tail (`TestOpenRecoversTornTailWithoutLosingDurableRecords`),
  and recovery truncates to the last intact record and keeps going
  (`TestAppendAfterRecoveryContinuesAfterThePrefix`). Boot sequence =
  `Open` → `Replay` → push every record into the ready queue.
- *Load shape*: `queue` — jobs that fit in RAM wait; beyond the bound
  `Put` blocks the producer (or fails on its context), which is the
  admission control for the *job* stream.
- *Work bound*: `pool` — N concurrent workers; exhaustion waits for a
  slot, never spawns an unbounded goroutine set.
- *Replay safety*: jobs are at-least-once on recovery — the consumer's
  side (in production) would pair this with the `distributed/idempotency`
  package's key-based dedup; see [`distributed/DESIGNS.md`](../distributed/DESIGNS.md).

**Sketch.**

```go
log, err := wal.Open("jobs.wal") // recovers a torn tail on open
if err != nil { return err }

ready := queue.New[Job](256)
workers := pool.New(pool.Config[Worker]{MaxOpen: 16, MaxIdle: 16, Factory: newWorker})

// Recovery: everything durable comes back into the ready queue.
if err := log.Replay(func(payload []byte) error {
    return ready.Put(context.Background(), decode(payload))
}); err != nil {
    return err
}

func accept(ctx context.Context, j Job) error {
    if err := log.Append(marshal(j)); err != nil { // durable first
        return err // producer never told "yes" for a lost job
    }
    return ready.Put(ctx, j) // may block: bounded RAM is the point
}
```

**Failure story.** Crash between `Append` and `Put` → replay closes the
gap; crash mid-`Append` → torn tail truncated, prefix intact, previous
acks stand; worker dies → `pool` slot frees, job stays queued; the whole
suite runs with `-race` and the WAL tests use real files under `t.TempDir()`.

---

## 4. Live notification fan-out (fan-out with slow-consumer policy)

**Problem.** Deliver a firehose of events to many concurrent
subscribers; some are fast, some stall; the firehose must not wait for
anyone, and each subscriber must receive a coherent stream (no
unbounded buffering).

**Composition.**

```
ingress → queue.Queue        (single dispatcher: bounded ingress)
        → pubsub.Broker      (per-subscriber policy: DropOldest / DropNewest)
        → metrics            (fan-out count, drop count, lag gauge)
```

**Where each decision lives.**

- *Ingress*: `queue` — one dispatcher goroutine drains a bounded queue,
  so a burst applies backpressure to producers instead of ballooning.
- *Policy per subscriber*: `pubsub` — the hard part of the design, made
  explicit at `Subscribe` time. Live dashboards pick `DropOldest`
  (always the latest state, `TestDropOldestKeepsTheNewestWindow`);
  audit-style consumers pick `DropNewest` (never skip ahead in what
  they already had, `TestDropNewestKeepsWhatAlreadyArrived`). One size
  does not fit both — that's the package's core claim.
- *Visibility*: `metrics` — a `drops_total{subscriber}` counter per
  policy makes the trade-off observable instead of silent.

**Sketch.**

```go
type Subscriber struct{ C chan Event; policy pubsub.Overflow }

func fanout(dispatcher *queue.Queue[Event]) {
    b := pubsub.New[Event]()
    for ev := range drain(dispatcher) { // one bounded ingress stream
        b.Publish(ev) // never blocks: slow consumers drop by policy
        reg.Counter("events_published").Inc()
    }
}

dash, cancelDash := b.Subscribe(32, pubsub.DropOldest) // latest-value UI
audit, cancelAudit := b.Subscribe(1024, pubsub.DropNewest) // gapless-if-possible
defer cancelDash()
defer cancelAudit()
```

**Failure story.** One stuck subscriber → everyone else unaffected
(`TestConcurrentPublishSubscribeCancel`); subscriber walks away →
channel closes, no goroutine leak (`TestCancelStopsDeliveryAndClosesTheChannel`);
shutdown → `Close` ends every subscriber once (`TestCloseEndsEverySubscriber`).

---

## Cross-cutting rules these designs share

1. **Time is injected** (`clock.Manual`): every TTL, refill and timeout
   test is an `Advance` away — no sleeps, no flakes.
2. **Bounds are explicit**: queue capacity, pool `maxOpen`, cache
   capacity, pubsub buffer — each is a named knob with a test proving
   the bound holds under `-race`.
3. **Close is owned and terminal**: the channel/queue/pool owner closes;
   close drains, then unblocks, then refuses — in that order.
4. **The policy you hid is the policy you test**: `DropOldest` vs
   `DropNewest`, deny-vs-block at the limiter, commit-before-ack in the
   WAL — each appears as a named test, not as a comment.
