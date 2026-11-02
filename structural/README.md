# Structural Patterns

How objects are composed. Seven packages, seven different domains.

```bash
go test -race ./structural/...
```

---

## 6. Adapter — [`adapter/`](adapter)

**Why.** You depend on types you do not own — a legacy SDK with pointer
returns, a client with a different error convention — and rewriting or
forking them is rarely an option. The need is to reconcile a foreign API
with the interface your code wants without editing either side.

**Intent.** Make an unrelated type satisfy the interface *your* code needs.

**Go shape.** A wrapper struct with the right method set, plus a compile-time
assertion. The interface is declared on the **consumer** side.

```go
var s Store = adapter.FromLegacy(legacyStore)   // awkward third-party type
var s Store = adapter.FromMap(m)                // a plain map
```

**Use when.** A type you do not control has the wrong method names, value
types, or error convention.

**Do not use when.** You own the type — just add the methods.

**The Go-specific decision.** Structural typing makes adapters free: no
registration, no codegen, no `implements` keyword. `var _ Store = ...{}`
turns drift into a build failure. Two *unrelated* adaptees are wrapped here
to show the point — one interface, two worlds.

**Walkthrough.** A third-party store with the wrong shape joins new code
through one wrapper; a plain map gets the same treatment in tests.

```go
var s adapter.Store = adapter.FromLegacy(legacyStore) // or adapter.FromMap(m)
c := adapter.NewCache(s)

c.Get(ctx, "k") // the legacy API stops at the adapter's edge
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestConsumerIsAdapteeAgnostic` | one consumer works over both adaptees |
| `TestLegacyAdapterDoesNotExposeLegacyVersion` | the wrapper delegates, it does not reimplement |
| `TestMapAdapterCopiesBuffers` | the adapter does not alias caller memory |

---

## 7. Bridge — [`bridge/`](bridge)

**Why.** Two independently varying dimensions — alert severity × delivery
channel, shape × renderer — combined by inheritance explode into N×M
classes maintained in lockstep. The need is to vary each axis separately,
so adding a channel never means touching every policy.

**Intent.** Let an abstraction and its implementation vary independently.

**Go shape.** A struct with an interface field on one axis; its own methods
carry the other. Composition, not inheritance.

```go
p := bridge.NewEscalationPolicy(&EmailNotifier{}, 3)   // policy × channel
p = bridge.NewEscalationPolicy(&SMSNotifier{}, 3)      // swap the axis
```

**Use when.** You have two orthogonal dimensions of change and would
otherwise need N×M subclasses.

**Do not use when.** There is one dimension — that is just a field.

**The Go-specific decision.** No `Abstraction`/`Implementor` class pair.
3 severities × 3 channels would be 9 classes in the GoF shape; here it is
3 policies + 3 notifiers, and the retry budget varies by severity *without*
the notifier knowing severities exist.

**Walkthrough.** The escalation policy is configured once per severity,
and the channel is swappable — two axes that change independently.

```go
email := bridge.NewEscalationPolicy(&bridge.EmailNotifier{}, 3)
sms := bridge.NewEscalationPolicy(&bridge.SMSNotifier{}, 3) // swap the axis

email.Notify(ctx, msg) // retries by severity — EmailNotifier never sees Severity
sms.Notify(ctx, msg)    // same policy code, different channel
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestPolicyWithDifferentNotifiers` | same policy, two implementations |
| `TestRetryBudgetVariesBySeverity` | the abstraction axis changes behaviour, not the implementation |
| `TestImplementationIsSwappableAtRuntime` | the other axis swaps without touching the policy |

---

## 8. Composite — [`composite/`](composite)

**Why.** In a tree — filesystem, widget panel, AST — every client step
starts with "leaf or branch?", so each new operation grows another type
switch over every node kind. The need is one interface that lets clients
walk and aggregate the structure without knowing its shape.

**Intent.** Treat individual objects and groups uniformly.

**Go shape.** An interface, leaf structs, and a container holding `[]Entry`
that recurses over it.

```go
root := composite.NewDir("root").Add(sub, file)
root.Size()        // aggregates over leaves and subtrees alike
```

**Use when.** You have a tree (filesystem, UI, org chart, AST) and clients
should not type-switch to tell a leaf from a branch.

**Do not use when.** There is exactly one node kind.

**The Go-specific decision.** Two shapes are shipped — a filesystem and a
widget panel — to show the pattern is not file-specific. Pointer receivers
are used across the whole component set so a `*Dir` is never copied by
accident, and `var _ Entry = (*Dir)(nil)` catches drift.

**Walkthrough.** A tree mixes files and subdirectories; one `Size()` call
walks it without any client knowing which is which.

```go
root := composite.NewDir("root").
    Add(composite.NewFile("README.md", 200)).
    Add(
        composite.NewDir("src").Add(composite.NewFile("main.go", 800)),
        composite.NewFile("a.log", 1200),
    )

root.Size() // 2200 — leaves and branches answer the same question
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestClientIsUniform` | `e.Size()` works with no type switch |
| `TestCompositeAggregates` / `TestMixedDepthAggregation` | sums correct at any depth |
| `TestFindWalksBothLeavesAndSubtrees` | one algorithm covers both node kinds |
| `TestChildrenReturnsCopy` | the container does not leak its slice |

---

## 9. Decorator — [`decorator/`](decorator)

**Why.** Logging, retries, timeouts and recovery must wrap hundreds of call
sites, and editing each one (or inheriting them from a base class) couples
every concern to every caller. The need is to add behaviour around calls as
values that compose, reorder and test independently of the work they wrap.

**Intent.** Add behaviour to every call by wrapping, without modifying.

**Go shape.** Two idioms.

*Struct wrapper* when the subject has several methods:

```go
var s KeyValue = decorator.WithTTL(decorator.WithLogging(base, &logs), time.Minute)
```

*Higher-order function* when the unit of work is a `func` — this is the
whole middleware convention:

```go
h := decorator.Chain(echo, decorator.Recover, decorator.Prefix("a/"), decorator.Upper)
```

**Use when.** Cross-cutting behaviour (logging, retries, TTL, recovery,
timeouts) that you want to compose and test in isolation.

**Do not use when.** The behaviour must sometimes *skip* the call entirely —
that is a proxy.

**The Go-specific decision.** Because `Handler` is a `func`, decorators are
just functions returning functions: no wrapper type, no interface, no
allocation. `Wrap` applies several struct decorators innermost-first.

**Walkthrough.** Logging and a TTL wrap the store as behaviour, not
subclasses — `MapStore` itself never learns either concept exists.

```go
var logs []string
base := decorator.NewMapStore()

kv := decorator.WithTTL(decorator.WithLogging(base, &logs), time.Minute)
kv.Put(ctx, "sid", "abc") // stored, one line appended to logs
kv.Get(ctx, "sid")        // a minute later: expired — base untouched
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestChainRunsOutsideIn` | enter/exit order is exactly nesting order |
| `TestTTLDecoratorExpires` | the decorator masks state without mutating the wrapped value |
| `TestRecoverConvertsPanicToError` | a panic becomes an error, not a crash |
| `TestRetryDoesNotRetryMisses` | transient vs permanent failures are distinguished |

---

## 10. Facade — [`facade/`](facade)

**Why.** A caller that wants one outcome — convert this file — otherwise
has to learn four collaborating packages, their error conventions and their
ordering rules. The need is a single entry point that owns the choreography
so the subsystem can evolve without breaking every caller.

**Intent.** One simple entry point over a subsystem of collaborating types.

**Go shape.** One exported function/struct in a package whose other types
are **unexported**. The compiler enforces the boundary — no architecture
tests needed.

```go
sum, err := facade.Convert(ctx, "in.mov", facade.Options{Target: "mp4", Width: 1280})
```

**Use when.** A caller needs one outcome but the implementation is 4–5
cooperating stages with their own error conventions.

**Do not use when.** The subsystem is already one function.

**The Go-specific decision.** `prober`, `decoder`, `transformer` and
`encoder` are all unexported: no sibling package can even name them. The
facade validates first (cheap failure before expensive work) and wraps every
stage error with context while preserving `errors.Is`.

**Walkthrough.** One call validates, probes, decodes, scales and encodes;
the caller sees a single `Summary` and one error chain.

```go
sum, err := facade.Convert(ctx, "holiday.mov",
    facade.Options{Target: "mp4", Width: 1280})
if err != nil { ... } // the failing stage, wrapped — errors.Is still matches

_ = sum // bytes written; the five internal stages stay unexported
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestValidateFailsBeforeAnyWork` | configuration errors never reach the subsystem |
| `TestCancellationPropagates` | `errors.Is(err, context.Canceled)` survives four layers of wrapping |
| `TestCallerSeesOnlyTheFacade` | one import, one call, five subsystems |

---

## 11. Flyweight — [`flyweight/`](flyweight)

**Why.** A thousand sprites each owning a decoded texture, or a million
rows each copying the same dictionary string, exhaust memory while holding
almost no unique data. The need is to build the expensive shared payload
once and keep only cheap per-use state on each object.

**Intent.** Share the expensive *intrinsic* state; keep only the cheap
*extrinsic* state per use.

**Go shape.** A mutex-guarded intern pool returning `*TextureData`, plus a
bounded LRU variant.

```go
tex, _ := atlas.Intern("hero", build)   // 1000 sprites, 1 texture
```

**Use when.** Many objects differ only in a few fields but share a large
payload — and you have measured the memory saving.

**Do not use when.** `string` interning or plain values already cover it.
Go's GC makes naive flyweights rarely worth the complexity.

**The Go-specific decision.** Three details that make it correct rather
than merely plausible: the pooled value must be **documented immutable**
(callers share the pointer); a `RWMutex` keeps readers parallel; and a
**double-checked** read under the write lock avoids a redundant store. An
unbounded intern pool is a leak, so `BoundedAtlas` caps it with LRU
eviction and counts rebuilds.

**Walkthrough.** A thousand sprites share one decoded texture: the second
`Intern` for the same key returns the pooled pointer without rebuilding.

```go
atlas := flyweight.NewAtlas()
build := func() *flyweight.TextureData {
    return &flyweight.TextureData{Name: "hero", Pixels: make([]byte, 4<<20)}
}

t1, _ := atlas.Intern("hero", build) // miss: build runs once
t2, _ := atlas.Intern("hero", build) // hit: t2 == t1, build skipped

atlas.Stats() // hits, misses, pooled — the saving is measured, not assumed
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestInternBuildsOnlyOnce` | 100 calls → 1 construction |
| `TestSpritesShareIntrinsicState` | 1000 sprites, 1 texture pointer |
| `TestMemorySavingIsReal` | 500 logical × 1 MiB → 3 MiB resident |
| `TestBoundedAtlasNeverExceedsCapacity` | the cap holds, even concurrently |
| `TestConcurrentInternIsRaceFree` | `-race` clean |

---

## 12. Proxy — [`proxy/`](proxy)

**Why.** Caching, authorisation, rate limits and circuit breaking must
apply to *every* call to a service — but the checks are not the service's
job, and scattering them across call sites means one forgotten site is a
hole. The need is a stand-in that decides whether a call happens, in one
place, without the caller noticing.

**Intent.** Stand in for a subject and *control access* — refuse, cache,
or fail fast.

**Go shape.** Same wrapper shape as a decorator, different intent.

```go
svc := proxy.Protect(dir)   // auth → circuit breaker → cache → subject
```

**Use when.** Caching, authorisation, rate limiting, circuit breaking —
anything that decides *whether* a call happens.

**Do not use when.** You only add behaviour to every call — that is a
decorator.

**The Go-specific decision.** Three proxies are implemented over one
subject and they compose, because each only knows the interface. The
ordering matters and is documented: auth before breaker, so an anonymous
caller cannot trip the breaker. The circuit breaker is a small FSM
(closed → open → half-open) with an explicit time-based transition.

**Walkthrough.** `Protect` stacks auth, breaker and cache in front of a
directory; ordering is documented so an anonymous caller cannot trip the
breaker.

```go
svc := proxy.Protect(proxy.NewUserDirectory())

svc.Lookup(ctx, "u1") // miss → real call → cached
svc.Lookup(ctx, "u1") // hit: the directory's call counter does not move
// after `threshold` failures the breaker opens: calls fail fast until the
// reset window passes — and not one line of caller code changes
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestCacheShortCircuitsSecondRead` | second read never reaches the subject |
| `TestAuthRefusesNonAdminWithoutCallingSubject` | forbidden calls stop at the proxy |
| `TestBreakerOpensAfterThreshold` | after N failures, the subject is not called |
| `TestBreakerHalfOpensAfterTimeout` / `...ClosesOnSuccessfulProbe` | recovery path works |
