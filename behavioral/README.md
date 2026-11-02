# Behavioral Patterns

How objects collaborate. Ten packages, ten different domains.

```bash
go test -race ./behavioral/...
```

---

## 13. Chain of Responsibility — [`chain/`](chain)

**Why.** Validation arrives as a growing list of rules where the first
failure should skip the rest, and the set of rules differs per endpoint —
a god-function or an `if` ladder hard-codes both. The need is a pipeline
whose steps can be assembled and reordered at runtime while the caller
still gets one outcome.

**Intent.** Pass a request along a sequence of handlers until one handles it.

**Go shape.** A **slice of handlers** plus a driver function. `Handler` is a
plain func type; the driver decides the stop condition.

```go
chain := chain.Stop(chain.RequireEmail, chain.ValidateEmail, chain.RequireAge(18), chain.ValidatePlan)
if err := chain(ctx, req); err != nil { ... }   // first error wins
```

**Use when.** Validation or pre-processing pipelines where later steps must
be skipped after a failure, and the caller needs one outcome.

**Do not use when.** Every handler must run — that is `chain.Collect`, or a
plain loop.

**The Go-specific decision.** The GoF version has each handler hold a
`next` pointer, which buries the chain's shape in wiring code. A slice makes
ordering inspectable and the chain **assembled at runtime**
(`TestChainsAreBuiltDynamically`). Two drivers — `Stop` (first error wins)
and `Collect` (`errors.Join`s every failure) — differ by one line of loop
body, which is exactly why the slice beats pointers. `Terminal` marks the
end of a chain, `Signature` renders it for assertions, and each handler can
annotate the request via `req.Note(...)`.

**Walkthrough.** Sign-up validation: the first failing rule answers, later
rules never run, and the caller gets exactly one error.

```go
validate := chain.Stop(chain.RequireEmail, chain.ValidateEmail,
    chain.RequireAge(18), chain.ValidatePlan)

req := &chain.Request{Email: "signup@corp.io", Age: 15, Plan: "free"}
if err := validate(ctx, req); err != nil {
    // RequireAge fails: ValidatePlan was never consulted —
    // and req.Note(...) records how far the request got
}
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestStopShortCircuitsOnFirstError` | later handlers never run |
| `TestStopReportsHandlerIndex` | the failing position is identifiable |
| `TestCollectReportsAllErrors` | every problem surfaced in one pass |
| `TestChainsAreBuiltDynamically` | the chain is a value, not a wiring graph |
| `TestCancellationStopsTheChain` | `ctx` is honoured between handlers |

---

## 14. Command — [`command/`](command)

**Why.** Undo/redo, audit trails, retries and task queues all need an
operation to exist *as data* — storable, invertible and replayable after
the caller has moved on. Without that, the code running the operation has
to understand its internals, and reversal logic scatters across the system.

**Intent.** Encapsulate an operation as a value: queue it, audit it, undo it.

**Go shape.** `Command{Name, Do(ctx), Undo(ctx)}`, concrete structs holding
their own reversal data, and two generic invokers: `History` and `Queue`.

```go
h := command.NewHistory(100)
h.Run(ctx, command.NewInsertText(doc, 0, "hi"))
h.Undo(ctx)
```

**Use when.** Undo/redo, audit logs, retries, or a task queue.

**Do not use when.** The operation needs nothing done to it later — and for
"just queue this work" the package already shows the answer: a
`Runnable func(context.Context) error`, no interface at all.

**The Go-specific decision.** Each command stores **its own** reversal
data (`InsertText` records `at`; `DeleteAt` stores the deleted runes;
`Replace` stores both old and new text), so the invoker stays generic and
never inspects the document. `Do` and `Undo` both take `context.Context`,
so a slow command is cancellable. Commands that fail are **not** recorded
(`TestFailedCommandIsNotRecorded`), and a new action clears the redo stack
(`TestNewActionClearsRedoStack`) — the two classic stack invariants.

**Walkthrough.** An editor session: each edit is a value that can be
undone, redone, and printed in the audit log.

```go
doc := command.NewDoc("hello world")
h := command.NewHistory(100)

h.Run(ctx, command.NewInsertText(doc, 11, " there")) // "hello world there"
h.Run(ctx, command.NewDeleteAt(doc, 0, 6))           // "world there"

h.Undo(ctx) // the delete rolls back
h.Redo(ctx) // ... and reapplies
h.Audit()   // ["insert", "delete"] — operations exist as data
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestDeleteDoUndoRestoresExactly` | round-trip identity, not "roughly back" |
| `TestHistoryUndoRedoCycle` | undo→redo is a no-op on document state |
| `TestNewActionClearsRedoStack` | redo cannot resurrect a replaced branch |
| `TestHistoryCapDropsOldestSteps` | the bound is enforced from the front |
| `TestQueueStopsOnError` | queued work halts at the first failure |

---

## 15. Iterator — [`iterator/`](iterator)

**Why.** Callers need to walk a tree, a log or a result set without
learning how it is stored, and they need `break` to actually stop the
work. Exposing the slice leaks the representation while hand-rolled
cursors bring `HasNext` boilerplate, so the need is a lazy traversal the
caller drives.

**Intent.** Traverse without exposing representation, with caller-controlled
termination.

**Go shape.** GoF cursor object **rejected; `iter.Seq`** adopted.

```go
for n := range node.Preorder() {     // `break` just works, lazily
    ...
}
```

**Use when.** Traversal order matters and callers need `break`, `continue`,
or adapters.

**Do not use when.** You need to resume across goroutines or rewind — use
a channel or a slice.

**The Go-specific decision.** Three properties beat the cursor object:
termination is an ordinary `break` (no `HasNext`, no sentinel); the walk is
**lazy** — nothing runs until the consumer asks (`TestSequenceIsLazy`); and
the yield function composes, so adapters like `Take`, `Filter`, `Any` and
`Pull` are ordinary functions. A second shape — `SliceIter` with an
explicit `Next()/Reset()` — is included for callers that cannot use
`range`, and a third (`EachLine`) shows the pre-generics closure-pull
idiom that returns `bool` from the callback for older toolchains.

**Walkthrough.** Words from a log line, filtered lazily — the consumer
decides when to stop and no cursor needs closing.

```go
for w := range iterator.Words("the quick brown fox") {
    if w == "brown" {
        break // nothing after "brown" is ever produced
    }
}

short := iterator.Collect(iterator.Filter(
    iterator.Words("the quick brown fox"),
    func(w string) bool { return len(w) <= 3 },
))
// short == []string{"the", "fox"} — adapters compose over one sequence
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestEarlyBreakStopsTraversal` | the walk stops, lazily, with no leak |
| `TestSequenceIsLazy` | no work before the first `next` |
| `TestPreorderVisitsRootFirst` / `TestInorderIsSortedForBST` | order is specified |
| `TestNilNodeYieldsNothing` | nil in, zero iterations out |
| `TestAdaptorsCompose` | iterators are values you can chain |

---

## 16. Mediator — [`mediator/`](mediator)

**Why.** When vehicles, signals and screens each hold references to one
another, the interaction graph becomes unreviewable — cycles, deadlocks,
and no single place to change the choreography. The need is one
coordinator that owns the interactions, so participants talk to it instead
of to each other.

**Intent.** Centralise communication so colleagues do not reference each
other.

**Go shape.** Two shapes, both channel-based.

*Event loop* — commands in, state transitions and events out:

```go
loop := mediator.NewEventLoop(32)
go loop.Run(ctx)
loop.Publish(ctx, mediator.MoveVehicle{Vehicle: "v1", Position: 7})
```

*Hub* — many-to-many fan-out over topics, each subscriber owning a channel:

```go
h := mediator.NewHub()
ch, off := h.Subscribe("alerts", 16)
defer off()
h.Publish(mediator.Fact{Topic: "alerts", Payload: "disk low"})
```

**Use when.** Many components must react to each other, but you want no
cyclic references and one place where the choreography is visible.

**Do not use when.** There are two participants — call them directly.

**The Go-specific decision.** The GoF shape gives every colleague a
reference to the mediator, which cycles and deadlocks easily. Channels
invert ownership: the `EventLoop` owns its output channel and closes it on
`ctx` cancellation (`TestRunStopsOnCancelAndClosesEvents`); `Hub.Subscribe`
returns **an unsubscribe function**, so you cannot pass the wrong id, and
publishing is non-blocking — slow subscribers have facts dropped and counted
(`Hub.Dropped()`) rather than stalling the whole hub.

**Walkthrough.** Vehicles, signals and screens stop referencing each
other: commands go in one side, facts come out the other.

```go
loop := mediator.NewEventLoop(32)
go loop.Run(ctx)

loop.Publish(ctx, mediator.MoveVehicle{Vehicle: "v1", Position: 7})
// the loop owns the choreography: routes update, FuelLow is emitted

ev := <-loop.Events() // subscribers react to shared facts, not to peers
_ = ev
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestLoopHandlesCommandsInOrder` | the loop is serial and deterministic |
| `TestHubFansOutToManySubscribers` | every subscriber sees the fact |
| `TestHubIsolatesTopics` | filtering is real |
| `TestHubNonBlockingPublishDropsForSlowSubscribers` | a stuck reader cannot wedge the hub |
| `TestComponentsAreDecoupled` | publish and subscribe with no shared type |
| `TestPublishHonoursCancellation` | `ctx` stops the loop |

---

## 17. Memento — [`memento/`](memento)

**Why.** Undo and checkpoints must freeze and later restore an object's
state — while the code *holding* the history has no business reading or
constructing that state. The need is an opaque token created only by the
owner and meaningless to everyone else.

**Intent.** Capture and restore an object's state without exposing it.

**Go shape.** `Snapshot() Snapshot` / `Restore(s Snapshot)`, with the
memento's **fields unexported**.

```go
s := d.Snapshot()
d.Move(0, 10, 10)
d.Restore(s)
```

**Use when.** Undo, checkpoints, or rolling a value back on failure.

**Do not use when.** The value is a few scalars — `*copy = *orig` is enough.

**The Go-specific decision.** The type name `Snapshot` is exported but its
**fields are not**, which is the Go equivalent of a private memento: outside
the package there is no way to build or tamper with one
(`TestCaretakerCannotForgeSnapshots` reaches for reflection and finds zero
exported fields). `Snapshot` **copies the shapes slice** rather than aliasing
it, so later `Add`/`Move` calls cannot mutate an existing snapshot. The
`Editor` caretaker holds undo/redo stacks of mementos with a configurable
limit, plus an event log.

**Walkthrough.** A drawing snapshots before each edit; undo restores an
opaque token the editor never looks inside.

```go
ed := memento.NewEditor(memento.NewDrawing("logo"), 50)

ed.Add(memento.Shape{Kind: memento.Rect, X: 10, Y: 10, W: 40, H: 20})
before := ed.Drawing().Snapshot()
ed.Move(0, 5, 5) // shift the rectangle
ed.Undo()        // back before the move — via Restore, fields unexported

before.Size() // 1 — the snapshot knows only what it will restore
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestUndoRedoRoundTripIsLossless` | restore is exact |
| `TestSnapshotIsIndependentOfLaterEdits` | edits after snapshot do not leak into it |
| `TestCaretakerCannotForgeSnapshots` | encapsulation is a compiler fact |
| `TestRestoreIsRepeatable` | restoring twice gives the same result |
| `TestHistoryLimitDropsOldestSnapshots` | the bound is enforced |

---

## 18. Observer — [`observer/`](observer)

**Why.** One event — a config change, a disk alert — must reach a set of
consumers that varies at runtime and lives in parts of the system the
subject knows nothing about. Calling them directly couples subject to
consumers, so the need is to publish once and let subscribers attach and
detach by themselves.

**Intent.** Notify a dynamic set of subscribers when a subject changes.

**Go shape.** Two idioms, one package.

*Subject + `Listener`* (classic, synchronous):

```go
sub := observer.NewSubject()
detach := sub.Attach(func(e observer.Event) { ... })   // notified in attach order
defer detach()
sub.Notify(observer.Event{Topic: "disk", Payload: "space low"})
```

*Topic bus* (the practical, non-blocking form):

```go
b := observer.NewBus()
sub := b.Subscribe("alerts", 16)   // Subscription: .Chan() to range, .Unsubscribe() to stop
defer sub.Unsubscribe()
b.Publish("alerts", "disk low")    // returns how many received it
```

**Use when.** One source of truth, many independent reactions, subscribers
that come and go.

**Do not use when.** There is one fixed subscriber — call it directly.

**The Go-specific decision.** Two shapes for two needs, and the package
says which is which: `Subject` is **synchronous** — correct for cache
invalidation or audit logging that must complete before the publisher
continues — while `Bus` is **asynchronous and never blocks**. Detachment is
a returned closure (`Attach` gives you `detach func()`) because function
values are not comparable in Go, so identity comes from a monotonic ID:
that is the workaround the GoF pattern needs in a language without object
identity. On the bus, `Unsubscribe` closes the channel, so `range` over
`.Chan()` terminates; publishing uses a `select`/`default` and **drops** to a
full buffer while counting it in `Stats()` — a stuck reader cannot wedge the
publisher. A panicking listener in `Notify` is recovered and returned as an
error while the remaining listeners still run.

**Walkthrough.** A disk monitor attaches a listener and gets back its own
detacher — attach once, and the handle is the bookkeeping.

```go
sub := observer.NewSubject()
detach := sub.Attach(func(e observer.Event) {
    log.Printf("%s: %s", e.Topic, e.Payload) // listeners run in attach order
})
defer detach() // no ids to mistype

errs := sub.Notify(observer.Event{Topic: "disk", Payload: "space low"})
// errs is a value: a panicking listener is recovered, the rest still ran
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestUnsubscribeClosesChannelSoRangeTerminates` | the core promise: `range` ends |
| `TestBusDeliversToTopicSubscribersOnly` | filtering is real |
| `TestUnsubscribeIsIdempotent` | double unsubscribe is safe |
| `TestBusSlowSubscriberCannotStallPublisher` | notify happens outside the lock |
| `TestSubjectPanicIsIsolated` | one bad observer cannot kill the rest |
| `TestConcurrentPublishAndUnsubscribeIsRaceFree` | `-race` clean |

---

## 19. State — [`state/`](state)

**Why.** Once a control flow has several states — idle, credited, sold
out — an `if m.phase == ...` ladder grows with every operation and illegal
transitions pass silently. The need is for the current state to decide
what each operation means, making the state × operation table complete by
construction.

**Intent.** Give an object a state object so behaviour changes when the
state changes.

**Go shape.** `State` interface, one struct per state (`Idle`, `Credited`,
`SoldOut`, `Maintenance`), and a `Machine` context that performs the swap.

```go
m := state.NewMachine(...)
m.InsertCoin(25)   // Idle → Credited
m.Select("A1")     // Credited → Idle (or SoldOut)
```

**Use when.** A control flow has several states with per-state rules and
illegal-transition checks, and an `if m.phase == ...` ladder keeps growing.

**Do not use when.** There are two booleans — use flags.

**The Go-specific decision.** Every operation (`InsertCoin`, `Select`,
`Refund`, `ToggleMaintenance`) is implemented **by every state**, and
`TestEveryStateImplementsEveryOperation` enforces it — so adding an
operation is a compile error in four places rather than a silent gap. Legal
transitions run through `Machine.transition`, which appends to a transition
log you can assert on; illegal ones return sentinel errors (`ErrNoCredit`,
`ErrSoldOut`, `ErrMaintenance`) that `errors.Is` can match. The money-safety
invariant is explicit: a sold-out machine refuses coins so credit cannot be
trapped.

**Walkthrough.** A vending machine: each state answers `InsertCoin`,
`Select` and `Refund` itself, so an illegal transition cannot be forgotten.

```go
m := state.NewMachine(state.Item{SKU: "A1", Name: "Cola", Price: 25, Stock: 3})

m.InsertCoin(state.Quarter) // Idle → Credited
m.Select("A1")              // dispenses → Idle again
m.StateName()               // "idle" — the machine reports its own phase

m.Refund() // ErrNothingToRefund — errors.Is matches it
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestCoinTransitionsToCredited` / `TestSuccessfulPurchaseReturnsToIdle` | the happy cycle |
| `TestIdleRejectsSelection` | illegal input errors, it does not panic |
| `TestEveryStateImplementsEveryOperation` | the state × operation table is complete |
| `TestSoldOutRejectsCoinSoMoneyIsNotTrapped` | the domain invariant holds |
| `TestTransitionLogIsRecorded` | transitions are auditable |

---

## 20. Strategy — [`strategy/`](strategy)

**Why.** Pricing, retry and routing rules change by customer, region or
experiment — usually at runtime, without a deploy. An `if` ladder grows at
every call site and copied guards drift, so the need is one named seam
where the algorithm swaps without touching its callers.

**Intent.** Select an algorithm at runtime and swap it without changing clients.

**Go shape.** A named **func type** — the cheapest interface that exists.

```go
type Rate func(ctx context.Context, p Package) (RateQuote, error)

s := strategy.NewShipper("express", strategy.PerKm("km", 500, 40, 5))
s.SetStrategy(strategy.CheapestOf(flat, weight))   // swap at runtime
```

**Use when.** Several interchangeable algorithms differ only in their body:
flat vs weight-based vs per-km vs insured-value pricing.

**Do not use when.** The variants share most of their code — build them
from one base instead of four copies.

**The Go-specific decision.** `Rate` is a func type, so a strategy is
**no interface, no wrapper struct, no method set**. Strategies are closures
(`Flat(price, name)`, `WeightBased(name, tiers)`) configured at the call
site, and they **compose**: `WithSurcharge(inner, pct)` wraps one strategy
in another, `CheapestOf(...)` builds a meta-strategy, and a `Catalogue`
registry selects by name. Validation and cancellation are shared *by
composition* too — `Package.Validate()` runs in the shipper, not in each
rate — which is the real payoff: adding a fourth strategy adds zero
duplicated guard code.

**Walkthrough.** Quote the same package under two pricing rules and swap
the rule at runtime — the shipper never changes.

```go
flat := strategy.Flat(1200, "flat")
perKm := strategy.PerKm("km", 500, 40, 5)

s := strategy.NewShipper("express", flat)
s.Quote(ctx, pkg) // one algorithm

s.SetStrategy(strategy.CheapestOf(flat, perKm))
s.Quote(ctx, pkg) // same call, different body — zero new guard code
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestWeightBasedPicksCorrectTier` | the algorithm's numbers are right |
| `TestValidationIsSharedAcrossStrategies` | one guard covers all four |
| `TestCancellationIsSharedAcrossStrategies` | one `ctx` check covers all four |
| `TestShipperSwapsStrategyAtRuntime` | the other axis swaps untouched |
| `TestCatalogueSelectsByName` | string-based selection, unknown names listed |

---

## 21. Template Method — [`template/`](template)

**Why.** Several jobs share one skeleton — extract → validate → transform →
load, with the same error handling around it — and copying that skeleton
means every fix is applied N times, differently. The need is to fix the
order once and let each job supply only its steps.

**Intent.** Fix an algorithm's skeleton; let callers fill in steps.

**Go shape.** GoF form (abstract base class with overridable methods)
**rejected; function fields on a struct** adopted.

```go
rep, err := template.Run(ctx, template.Steps{
    Extract:   template.FromRows(rows),
    Validate:  template.RequireFields("id", "ts"),
    Transform: template.Uppercase,
    Load:      load,
})
```

**Use when.** Several jobs share a structure — extract → validate →
transform → load, or encode → upload — with the same error handling and
instrumentation around it.

**Do not use when.** There is one job — write it directly.

**The Go-specific decision.** Hook methods are **fields of type
`Extract`/`Validate`/`Transform`/`Load`**, not inherited methods: each step
is individually constructible and testable (`FromRows`, `RequireFields`,
`CountingLoad` are shipped as reusable pieces), and a missing required step
is detected **before any work runs** via `ErrMissingStep`. Optional steps
are simply `nil` and skipped. A second template — `Exporter{Encode, Upload}`
— shows the same skeleton for a different shape. No `super` call, no
diamond, no base class to embed.

**Walkthrough.** One ETL skeleton runs in fixed order; each job supplies
its own extract, validate, transform and load functions.

```go
rep, err := template.Run(ctx, template.Steps{
    Extract:   template.FromRows(rows),
    Validate:  template.RequireFields("id", "ts"),
    Transform: template.Uppercase,
    Load:      load,
})
if err != nil { ... } // the failing step, in order — no super call to remember

rep.RejectedRows() // the report every run of the skeleton returns
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestStepOrderIsRecorded` | extract → validate → transform → load |
| `TestMissingStepIsDetectedBeforeAnyWork` | configuration errors never start the job |
| `TestExtractFailureIsFatal` | early failure skips downstream stages |
| `TestOptionalHookIsSkippedWhenNil` | optional really is optional |
| `TestCancellationStopsBetweenStages` | `ctx` is checked between steps |
| `TestSwappingOneStepChangesBehaviour` | steps vary independently |

---

## 22. Visitor — [`visitor/`](visitor)

**Why.** An AST or document model rarely changes shape, but what you do to
it keeps growing — evaluate, render, fold, lint. Sprinkling those
operations across every node type bloats it with methods most callers
never use, so the need is to add a whole operation without editing the
structure.

**Intent.** Add operations to a stable object structure without editing its
classes.

**Go shape.** The GoF version (double dispatch) is **rejected**; two Go
replacements ship side by side, and choosing between them is the point.

*A. Method on the element* — `Evaluate(n, env)`, `Render(n)`, `String()`:

```go
sum, err := visitor.Evaluate(expr, env)
```

*B. Visitor interface + type switch* — one struct per operation:

```go
err := visitor.Walk(&visitor.Counter{}, expr)   // counts by node kind
```

**Use when.** The structure is stable and you routinely add *new operations*
over it (evaluate, render, fold, lint, count).

**Do not use when.** You routinely add *new element types* — every visitor
must then be updated, which is the pattern's well-known back side.

**The Go-specific decision.** Go has no overloading and no virtual methods,
so the GoF form degenerates into type switches anyway — and loses the
"forgetting a case is an error" property. Here that property is **recovered
on purpose**: `WalkChecked` has a `default:` arm that returns `ErrUnknownNode`
naming the offending type, so an unhandled case fails loudly instead of
silently doing nothing. The trade-off is stated plainly: form A is better at
adding types (compiler flags it), form B is better at adding operations
(one new struct, zero edits to elements). Four operations over one tree —
`Evaluate`, `Render`, `Folder` (constant folding), `Linter` — demonstrate the
payoff.

**Walkthrough.** Four operations over one expression tree — evaluate,
render, fold, lint — each a separate type; the tree itself never changes.

```go
e := &visitor.Add{L: &visitor.Lit{V: 2}, R: &visitor.Mul{
    L: &visitor.Lit{V: 3}, R: &visitor.Lit{V: 4},
}}

sum, _ := visitor.Evaluate(e, nil) // 14 — operation A over the tree

c := visitor.NewCounter()
visitor.Walk(c, e)
c.Counts() // {"add":1, "lit":3, "mul":1} — one new struct per operation
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestEvaluate` / `TestEvaluateUndefinedVariable` | operation A is correct |
| `TestRenderMinimalParentheses` | a second, independent operation |
| `TestWalkCheckedRejectsUnknownNodeType` | the missing-case guard fails loudly |
| `TestOperationsAreIndependent` | adding an operation changed no element |
| `TestFolderFoldsAdditionOfLiterals` | a mutating-in-place visitor still works |
| `TestLinterCollectsEveryIssueInOnePass` | one traversal, many findings |
