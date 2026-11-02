# Creational Patterns

How objects are made. Five packages, five different domains.

```bash
go test -race ./creational/...
```

---

## 1. Singleton — [`singleton/`](singleton)

**Why.** Some state really is one-per-process — a metrics meter, a config
snapshot — and when every package invents its own init guard, half of them
race. The need is a single authoritative instance any goroutine can reach
without threading it through every constructor.

**Intent.** Exactly one instance per process, created lazily and safely.

**Go shape.** Package-level `var` + `sync.Once` + unexported fields.

```go
meter := singleton.GetMeter()   // identical pointer from every goroutine
```

**Use when.** The resource is genuinely process-wide and immutable after
init: a metrics registry, a config snapshot, a connection-pool handle.

**Do not use when.** The state is mutable and the package is tested in
parallel — a singleton makes tests order-dependent. Prefer passing a value.

**The Go-specific decision.** The Java form (`getInstance()` with a
nil-check and a `synchronized` block) races under concurrency. `sync.Once`
gives a lock-free fast path after the first call, and the first call runs
exactly once even with 64 goroutines arriving together.

**Walkthrough.** A metrics endpoint joins a request path hammered by 64
goroutines; they all count against the same meter from the first call.

```go
meter := singleton.GetMeter()

var wg sync.WaitGroup
for i := 0; i < 64; i++ {
    wg.Add(1)
    go func() {
        defer wg.Done()
        meter.Inc("requests", 1)
    }()
}
wg.Wait()

meter.Count("requests") // 64 — one pointer, one counter, no init race
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestConcurrentCallersShareOneInstance` | 64 goroutines, one pointer |
| `TestStateIsSharedAcrossCallers` | counters accumulate across goroutines |
| `TestInitRunsExactlyOnce` | the timestamp never moves after first call |
| `TestUptimeIsNonZero` | zero-value instance is usable |

---

## 2. Factory Method — [`factory/`](factory)

**Why.** Callers pick behaviour from configuration — a driver name, a URL
scheme, a feature flag — but must not depend on the type that implements
it. Without a factory, every call site grows its own `switch` over concrete
structs, leaking the choice into code that only wanted an interface.

**Intent.** Choose a concrete type from input; return a narrow interface.

**Go shape.** A plain function returning an interface. The concrete types
are unexported, so callers cannot depend on them even by accident.

```go
s, err := factory.NewStore("memory")   // returns factory.Store
```

**Use when.** Construction varies by configuration (driver name, URL scheme,
a feature flag) and callers should not know which type they got.

**Do not use when.** There is exactly one implementation — a function that
never branches is just a constructor.

**The Go-specific decision.** Two forms are implemented:

- `NewStore(driver)` — a `switch`, the factory method proper.
- `Register` / `Open` — a plugin registry, so adding a backend does not
  edit this file. Duplicate names **panic at init**, because a duplicate
  registration is a programming error, not a runtime condition.

**Walkthrough.** The driver name arrives in config; the call site stays
ignorant of which type it received and still gets a usable error.

```go
s, err := factory.NewStore("memory") // config picks the concrete type
if err != nil { ... }                // ErrUnknownDriver survives errors.Is

_ = s.Put(ctx, "user:1", []byte("ada"))
s.Get(ctx, "user:1") // through factory.Store — the memory type never leaks
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestNewStoreUnknownDriver` | `errors.Is(err, ErrUnknownDriver)` survives wrapping |
| `TestStoreDoesNotAliasCallerBuffer` | the store copies, so callers cannot corrupt it |
| `TestRegisterDuplicatePanics` | duplicate drivers fail at start-up |
| `TestPluginRegistryUnknown` | unregistered names are reported, not defaulted |

---

## 3. Abstract Factory — [`abstractfactory/`](abstractfactory)

**Why.** Some objects are only correct *together*: an AWS store with an SQS
queue is a system, an AWS store with a GCP queue is a bug. When each piece
is constructed separately, nothing prevents mixing families — the need is
one call that makes the mismatch unrepresentable.

**Intent.** Create a *family* of related objects that must be used together.

**Go shape.** A `Bundle` struct of interfaces returned by a registered
constructor — no `AbstractFactory` class hierarchy.

```go
b, _ := abstractfactory.Open("aws", "prod")
b.Store.Location("k")   // s3://prod/k
b.Queue.Name()          // sqs:prod
```

**Use when.** Products from different families are incompatible — an AWS
queue with a GCP topic is a bug you want to make unrepresentable.

**Do not use when.** You need one object; that is `factory`.

**The Go-specific decision.** The registry pattern from `factory` is reused
for providers, but the unit of construction is the whole bundle. Because a
single call produces all three products, they *cannot* be mixed.

**Walkthrough.** A project opens its cloud provider once; every product
that comes back belongs to the same family, so mixing cannot happen.

```go
b, err := abstractfactory.Open("aws", "prod")
if err != nil { ... }

b.Store.Location("avatar") // "s3://prod/avatar"
b.Queue.Name()             // "sqs:prod"
b.Topic.Name()             // "sns:prod" — one call, one family, no mix-ups
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestBundleIsInternallyConsistent` | every product comes from the same family |
| `TestBundleProductsShareTheProject` | all three carry the same project |
| `TestDuplicateRegistrationPanics` | conflicting providers fail at start-up |

---

## 4. Builder — [`builder/`](builder)

**Why.** A value with ten optional fields cannot be built positionally — a
wall of booleans, telescoping constructors, or fields set after the fact
all let a half-built object escape. The need is construction that stays
readable at any size and cannot yield an invalid value.

**Intent.** Construct a value with many optional fields, legibly and safely.

**Go shape.** Two idioms in one package.

*Idiom A — functional options* (preferred for optional fields):

```go
s := builder.New(
    builder.WithAddr(":9090"),
    builder.WithTimeout(5*time.Second),
)
```

*Idiom B — stepwise builder* (for validated, must-not-escape values):

```go
q, err := builder.From("users").
    Where("age > 18").
    Limit(10).
    Build()          // SQL's fields are unexported: Build is the only exit
```

**Use when.** More than ~3 optional fields, or when a partially built value
would be a bug.

**Do not use when.** Most fields are required — then a plain struct
parameter (see [`idioms/config`](../idioms/config)) is clearer.

**The Go-specific decision.** Defaults are applied *before* options, so an
option can always override a default and no default can ever overwrite an
option. `Build()` validates and returns a copy, so later mutation of the
builder cannot affect an already-built value.

**Walkthrough.** A server starts with defaults and three overrides — order
irrelevant — while a query can only exist once it is valid.

```go
srv := builder.New(
    builder.WithAddr(":8080"),
    builder.WithTimeout(5*time.Second),
    builder.WithMiddleware("log", "recovery"),
)
srv.Addr() // ":8080" — readable however many options are added later

q, err := builder.From("users").
    Where("age > 18").
    Limit(10).
    Build() // the only exit: validated, and a copy — later edits can't reach it
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestOptionOrderIndependence` | `New(WithA, WithB)` ≡ `New(WithB, WithA)` |
| `TestBuildReportsAllErrorsAtOnce` | `errors.Join` surfaces every problem |
| `TestBuildIsImmutable` | building twice is unaffected by intermediate edits |
| `TestPartiallyBuiltValueCannotEscape` | zero `SQL` is inert |

---

## 5. Prototype — [`prototype/`](prototype)

**Why.** The next object is usually a variation of one you already have —
duplicate this document, spawn a report from a template — and rebuilding
from scratch repeats setup you already paid for. Plain `=` looks like a
copy while silently sharing slices, maps and pointers, so the need is an
explicit deep copy.

**Intent.** Copy an object rather than construct one.

**Go shape.** *Rejected* as a registry; adopted as an explicit deep `Clone`.

```go
cp := doc.Clone()          // deep: slices, maps and nested refs rebuilt
tpl, _ := reg.Make("report")
```

**Use when.** The type contains reference fields (slices, maps, pointers)
and callers must mutate their copy without affecting the original — or when
you have a template registry (`reg.Register("invoice", tmpl)`).

**Do not use when.** The value is all value-typed fields: plain `=` already
copies it, and a `Clone` method would be noise.

**The Go-specific decision.** Reflect-based deep-copy helpers are refused:
they are slow, silently wrong across unexported fields, and unreviewable.
`Clone` is written by hand so a reviewer can audit every field. The
value receiver on `Clone` makes the *shallow* copy explicit, which is what
makes the deep one meaningful.

**Walkthrough.** Documents are duplicated rather than rebuilt: register a
template once, then every copy can be edited freely.

```go
reg := prototype.NewRegistry()
reg.Register("report", prototype.Document{Title: "Quarterly report"})

cp, ok := reg.Make("report")
if !ok { ... }
cp.Title = "Q3 appendix" // mutate the duplicate freely

again, _ := reg.Make("report")
// again.Title is still "Quarterly report" — clones share no state
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestCloneIsDeepForSlices` / `...Maps` / `...NestedRefs` | mutations do not leak back |
| `TestShallowCopyWouldHaveAliased` | the premise: plain `=` shares state |
| `TestRegistryMakeReturnsIndependentCopies` | two `Make` calls never share state |
| `TestCloneHandlesZeroValue` / `...NilRefs` | nil in, nil out — no panics |
