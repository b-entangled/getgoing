# Concurrency Patterns (Go-native)

Four patterns that are idiomatic Go rather than GoF imports. Every package
in this directory passes `go test -race`.

```bash
go test -race ./concurrency/...
```

No external dependencies are used: where `golang.org/x/sync/errgroup` would
normally appear, [`taskgroup`](taskgroup) implements the same idea locally
so the semantics are visible in the source.

---

## 23. Pipeline — [`pipeline/`](pipeline)

**Why.** Log ingestion, ETL and request handling are chains of independent
transforms that should not share one loop or one lock — parsing, filtering
and counting each run at their own pace. The need is stages joined by
channels: testable alone, cancellable from the consumer end, and
parallel where the work is heavy.

**Intent.** Chain stages connected by channels; each stage is independent,
cancellable, and reuses code.

**Go shape.** A stage is a generic func that reads `in` and writes `out`;
`Run` wires the goroutine and the channels.

```go
type Stage[I, O any] func(ctx context.Context, in <-chan I, out chan<- O)

in  := pipeline.Source(ctx, lines)                        // Line values
mid := pipeline.Run(ctx, in, pipeline.ParseStage(count))  // Line → Entry
out := pipeline.Run(ctx, mid, pipeline.Filter("warn"))    // drops lower levels
```

Domain: log lines parsed into level-tagged entries, filtered, then counted
or collected.

**Use when.** Work decomposes into sequential transforms with natural
concurrency between stages, or you want bounded parallelism on one stage.

**Do not use when.** Stages share mutable state — pipeline structure does
not fix data races; the stages here are deliberately **pure** so they can be
unit-tested without any goroutine at all (`TestStageIsPureSoItIsTestableWithoutGoroutines`).

**The Go-specific decision.** The rules that keep pipelines leak-free:

1. **The sender closes.** A receiver closing a channel it does not own
   panics; ownership makes it a discipline you can audit.
2. **Every stage selects on `ctx.Done()`** so an early `break` by the
   consumer cancels upstream producers instead of stranding them
   (`TestCancellationStopsPipeline`).
3. **Fan-out is a helper, not a shared channel** — `FanOut(ctx, n, stage, in)`
   runs N copies of one stage and merges their outputs, so each copy owns
   its own writes. Zero workers is handled rather than hanging.

**Walkthrough.** Log lines flow parse → filter → count with one shared
`ctx`; cancelling the caller tears down every stage's goroutine.

```go
var rejected pipeline.Counter

in := pipeline.Source(ctx, lines)
parsed := pipeline.Run(ctx, in, pipeline.ParseStage(&rejected))
warns := pipeline.Run(ctx, parsed, pipeline.Filter("warn"))

counts, _ := pipeline.CountByLevel(ctx, warns)
// stages own their channels — nothing shares a slice behind a lock
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestPipelineRunsToCompletion` | the data arrives transformed, end to end |
| `TestCancellationStopsPipeline` | no goroutine leak when the consumer stops early |
| `TestFanOutProcessesEverythingExactlyOnce` | no drops, no duplicates under N workers |
| `TestFanOutWithZeroWorkersStillWorks` | degenerate input does not hang |
| `TestStageIsPureSoItIsTestableWithoutGoroutines` | a stage is testable as a plain func |

---

## 24. Worker Pool — [`workerpool`](workerpool)

**Why.** One goroutine per task is fine until the tasks are 100,000 files:
unbounded fan-out exhausts memory and descriptors, and hand-rolled
`WaitGroup` shutdown gets the termination protocol subtly wrong. The need
is a fixed set of workers pulling from one queue, with a shutdown you can
prove.

**Intent.** Bound the number of goroutines doing a shared queue of work.

**Go shape.** A `Pool` object: jobs in a channel, N workers, results out
another channel, `ctx` for cancellation.

```go
p := workerpool.New(8)
res := p.Run(ctx, workerpool.Hash, workerpool.Produce(ctx, jobs))
all := p.Drain(res)
ok, failed := p.Summary()   // reports come from the results, not a side channel
```

**Use when.** More work than you are willing to have goroutines for —
file hashing, HTTP fan-out, CPU-bound tasks.

**Do not use when.** Work is I/O-bound and cheap (goroutines are cheap
enough), or the set of tasks is tiny.

**The Go-specific decision.** The termination protocol is the whole
trick: the **producer closes the jobs channel** (`Produce` does it, and
`TestProducerClosesJobsAndWorkersExit` asserts workers exit because of
that), workers exit when jobs close, and the last worker closing the results
channel is what lets `Drain` `range` instead of counting. Convenience
wrappers — `RunJobs` (one call for the whole shape) and `Errors` (joins
failed results with `errors.Join`) — exist because that protocol is easy to
get subtly wrong and boring to repeat. `New(0)` raises the worker count to 1
rather than deadlocking.

**Walkthrough.** Ten thousand jobs, eight goroutines: every result
carries its own outcome, and the pool is the only shared thing.

```go
p := workerpool.New(8) // 8 workers, not 100 000 goroutines
jobs := workerpool.Produce(ctx, work)

res := p.Run(ctx, workerpool.Hash, jobs)
p.Drain(res)

_, ok, failed := p.Summary() // reports come from the results, not a side channel
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestRunProcessesEveryJobExactlyOnce` | no drops, no duplicates |
| `TestResultsChannelCloses` | consumers can `range` without hanging |
| `TestProducerClosesJobsAndWorkersExit` | the termination protocol holds |
| `TestCancellationStopsWorkersPromptly` | `ctx` stops work mid-queue |
| `TestErrorsJoinsJobFailures` | all failures surfaced through one error |
| `TestConcurrentPoolsDoNotRace` | `-race` clean with several pools live |

---

## 25. Task Group (errgroup) — [`taskgroup`](taskgroup)

**Why.** Fail-fast parallel work needs three things at once — the first
error, cancellation of the siblings, and a bound on concurrency — and
hand-wiring `WaitGroup` + a shared `err` + `context` for each call leaves
room for every classic bug. The need is to write that wiring once and
reuse it.

**Intent.** Run several goroutines, return the **first** error, cancel the
rest — with a concurrency limit.

**Go shape.** A small `Group` mirroring `golang.org/x/sync/errgroup`
without the dependency:

```go
g := taskgroup.WithLimit(ctx, 4)
g.Go(func(ctx context.Context) error { return fetch(ctx, u) })
err := g.Wait()
```

**Use when.** Fail-fast parallel work: fetch a set of URLs, warm a set of
caches, validate a set of inputs.

**Do not use when.** You need **every** error, not just the first — that is
`TryAll`, which is deliberately a different function rather than a flag on
`Group`.

**The Go-specific decision.** Three properties, each a common bug source:

1. `Go` **stops starting tasks after a failure** (`TestTasksNotStartedAfterFailure`),
   so wasted work is bounded by the limit, not by the queue length.
2. The group's context is **derived and cancelled on first error**
   (`TestFirstErrorCancelsSiblingContext`), so siblings see
   `context.Canceled` instead of running to completion — and
   `Wait` returns only after every task has finished, so no goroutine
   outlives the call.
3. The limit is a buffered-channel semaphore, and misuse is loud:
   `Go` after `Wait` **panics** with `ErrConcurrentUse` rather than
   silently starting orphan work.

Helpers `RunAll` (names the failing task in the error), `TryAll`
(`errors.Join` every failure) and `FetchAll` (bounded concurrent fetch with
a time budget) sit on top of the same primitive.

**Walkthrough.** Fetch a set of URLs fail-fast: the first error wins and
cancels the rest, while the limit caps how many run at once.

```go
g := taskgroup.WithLimit(ctx, 4)
for _, u := range urls {
    u := u
    g.Go(func(ctx context.Context) error { return fetch(ctx, u) })
}

if err := g.Wait(); err != nil {
    return err // siblings saw cancellation — no stragglers keep working
}
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestFirstErrorIsReturned` / `TestOnlyOneErrorIsRecorded` | one error, not a bag |
| `TestFirstErrorCancelsSiblingContext` | siblings observe cancellation |
| `TestConcurrencyLimitIsEnforced` | measured in-flight never exceeds N |
| `TestTasksNotStartedAfterFailure` | no new goroutines after the first error |
| `TestContextIsCancelledWhenWaitReturns` | `Wait` does not return early |
| `TestTryAllCollectsEveryFailure` | the opposite contract, explicitly |

---

## 26. Context Cancellation — [`contextdemo`](contextdemo)

**Why.** Work that can outlive its request — an RPC, a background fan-out,
a server draining on SIGTERM — otherwise keeps running for a caller that
is already gone, holding resources for an answer nobody will read. The
need is one value that carries deadlines, cancellation and request
identity down the whole call tree.

**Intent.** Propagate deadlines, cancellation, and request-scoped values
through a call tree.

**Go shape.** `context.Context` as the **first parameter**; never stored in
a struct; always cancelled by the creator.

```go
ctx, cancel := context.WithTimeout(parent, 2*time.Second)
defer cancel()
errs, err := contextdemo.Budget(ctx, time.Second, work)
```

**Use when.** Anything that can outlive a request: RPCs, work with
deadlines, fan-out jobs, servers that must drain before exiting.

**Do not use when.** The operation is short and synchronous — passing a
`context` nobody will honour is ceremony.

**The Go-specific decision.** The package documents the rules the runtime
relies on, each as its own scenario:

- **`cancel` is called exactly once, in a `defer`** — `DoOnce` uses a
  derived context and `TestDoOnceReleasesDerivedContext` proves the
  derivation is released, not just abandoned.
- **Deadlines nest and inherit** — `Budget` rejects a batch whose total
  exceeds the remaining budget *before* starting; `EachItem` applies a
  per-item deadline while preserving input order.
- **Cancellation stops work between items, not after the last one** —
  `Simulate` checks `ctx.Err()` and `RunAll` stops on parent cancellation.
- **Values are for request-scoped data only** (`WithRequestID`/`RequestID`)
  — configuration and options belong in parameters, not a context. A typed
  private `key` type prevents collisions with other packages' keys.
- **Graceful shutdown** — `Server.Shutdown` stops accepting, drains
  in-flight work, and reports stragglers when the grace period expires
  rather than pretending everything finished.

**Walkthrough.** A request's budget is shared by all its work: when it
runs out, every worker discovers that through `ctx.Done()`.

```go
ctx, cancel := context.WithTimeout(parent, 2*time.Second)
defer cancel()

errs, err := contextdemo.Budget(ctx, time.Second, work)
if err != nil { ... } // the deadline itself, as a value

_ = errs // per-item outcomes — no goroutine outlived the request
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestSimulateHonoursCancellation` | blocked work returns `context.Canceled` |
| `TestBudgetRejectsOverlongBatch` | the deadline is checked before work starts |
| `TestDoOncePropagatesParentCancellation` | cancellation flows down the tree |
| `TestRequestIDRoundTrip` / `TestRequestIDIsNotOverwrittenByOtherStringKeys` | values propagate and cannot collide |
| `TestRunAllStopsOnParentCancellation` | no work is attempted after cancel |
| `TestServerDrainsInFlightBeforeShutdownCompletes` | in-flight requests finish; stragglers are reported |
