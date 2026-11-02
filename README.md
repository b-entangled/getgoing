# Go Design Patterns

Explore the notes as a searchable, interactive [public learning guide](https://b-entangled.github.io/getgoing/).

Working implementations of every pattern decided in
[`adr/0001-golang-design-patterns.md`](adr/0001-golang-design-patterns.md),
plus five companion collections: [`concepts/`](concepts/README.md)
(language features), [`mistakes/`](mistakes/README.md) (pitfalls with
their fixes), [`internals/`](internals/README.md) (runtime behaviour,
measured), [`sdesign/`](sdesign/README.md) (system-design building
blocks) and [`distributed/`](distributed/README.md) (distributed-system
mechanisms).

73 packages in total — 28 pattern packages, each with a different domain,
and 45 companion packages — each with a package doc comment and a
race-checked test suite that names the property it proves. The two
design collections add [`DESIGNS.md`](sdesign/DESIGNS.md) files that
compose them into end-to-end systems.

Prose collections without packages carry the rest —
[networking](networking/README.md), [linux](linux/README.md),
[database](database/README.md), [gpu](gpu/README.md),
[compilers](compilers/README.md), [program execution](execution/README.md),
[web services](webservices/README.md), [Rust](rust/README.md),
[rl](rl/README.md),
[llm](llm/README.md) (with a [vLLM serving guide](llm/vllm.md)) — and
[`agentictools/`](agentictools/README.md)
documents the four instruments these notes are read, written, trained
and inspected with. The [`android/`](android/README.md) project packages
the interactive field guide as an offline Android reader.

The [deep reinforcement learning reference](rl/README.md) covers major
value-based, policy-gradient, actor–critic, model-based, offline, and
imitation-learning algorithms; its [physical robotics companion](rl/ROBOTICS.md)
walks through task design, simulation-to-real transfer, deployment
boundaries, and staged hardware evaluation.

For a fundamentals-to-advanced order across **all** learning documents,
including [Go basics](concepts/BASICS.md), follow
[`LEARNING_PATH.md`](LEARNING_PATH.md). It groups the material by
prerequisites and includes suggested paths for application, systems, LLM
inference and agent engineering.

## Run it

```bash
make check    # gofmt + vet + go test -race (the full gate)
```

Individual targets: `make build`, `make test`, `make race`, `make vet`,
`make fmt` / `make fmt-check`, `make cover`, `make tidy`, `make clean`.

Or by hand:

```bash
gofmt -l .            # formatting
go vet ./...          # static checks
go test -race ./...   # all patterns, race detector on
```

## Index

Verdicts come from the ADR: **adopted** means we use it in Go form,
**rejected** means the GoF shape is refused and a Go replacement is shown.

### Creational — [`creational/`](creational/README.md)

| Pattern | Package | Verdict | Example domain |
|---|---|---|---|
| Singleton | [`singleton`](creational/singleton) | adopted, scoped | process-wide metrics meter |
| Factory Method | [`factory`](creational/factory) | adopted | key-value `Store` drivers |
| Abstract Factory | [`abstractfactory`](creational/abstractfactory) | adopted as registry | cloud provider bundles |
| Builder | [`builder`](creational/builder) | adopted, two idioms | server options + SQL builder |
| Prototype | [`prototype`](creational/prototype) | rejected as registry, adopted as `Clone` | documents with nested refs |

### Structural — [`structural/`](structural/README.md)

| Pattern | Package | Verdict | Example domain |
|---|---|---|---|
| Adapter | [`adapter`](structural/adapter) | adopted | legacy store + map → one interface |
| Bridge | [`bridge`](structural/bridge) | adopted as interface fields | alert policy × delivery channel |
| Composite | [`composite`](structural/composite) | adopted | filesystem tree + widget panel |
| Decorator | [`decorator`](structural/decorator) | adopted, two idioms | store wrappers + HTTP middleware |
| Facade | [`facade`](structural/facade) | adopted | media transcoder pipeline |
| Flyweight | [`flyweight`](structural/flyweight) | adopted, bounded | shared sprite textures |
| Proxy | [`proxy`](structural/proxy) | adopted | cache / authz / circuit breaker |

### Behavioral — [`behavioral/`](behavioral/README.md)

| Pattern | Package | Verdict | Example domain |
|---|---|---|---|
| Chain of Responsibility | [`chain`](behavioral/chain) | adopted as slice of funcs | registration validation rules |
| Command | [`command`](behavioral/command) | adopted | editor undo/redo |
| Iterator | [`iterator`](behavioral/iterator) | GoF rejected, `iter.Seq` adopted | tree traversal + adapters |
| Mediator | [`mediator`](behavioral/mediator) | channels instead of Colleague links | vehicle event loop + hub |
| Memento | [`memento`](behavioral/memento) | adopted, unexported fields | drawing editor snapshots |
| Observer | [`observer`](behavioral/observer) | adopted as bus + subject | topic pub/sub |
| State | [`state`](behavioral/state) | adopted | vending machine FSM |
| Strategy | [`strategy`](behavioral/strategy) | adopted as `func` values | shipping-rate calculation |
| Template Method | [`template`](behavioral/template) | GoF rejected, functions adopted | ETL job skeleton |
| Visitor | [`visitor`](behavioral/visitor) | GoF rejected, type switch adopted | expression tree operations |

### Concurrency (Go-native) — [`concurrency/`](concurrency/README.md)

| Pattern | Package | Example domain |
|---|---|---|
| Pipeline | [`pipeline`](concurrency/pipeline) | log parsing stages + fan-out |
| Worker pool | [`workerpool`](concurrency/workerpool) | parallel file hashing |
| Task group (errgroup) | [`taskgroup`](concurrency/taskgroup) | fail-fast parallel fetch |
| Context cancellation | [`contextdemo`](concurrency/contextdemo) | budgets, drain, propagation |

### Idioms (Go-native) — [`idioms/`](idioms/README.md)

| Pattern | Package | Example domain |
|---|---|---|
| Functional options | [`options`](idioms/options) | HTTP client configuration + presets |
| Option-struct config | [`config`](idioms/config) | JSON/YAML-loadable settings |

### Concepts — [`concepts/`](concepts/README.md)

Start with the [Go basics guide](concepts/BASICS.md), then use these
packages as focused examples of language and library behavior.

| Topic | Package | What it pins down |
|---|---|---|
| Closures | [`closures`](concepts/closures) | shared capture, per-iteration loop variables |
| Deferred calls | [`deferred`](concepts/deferred) | LIFO, argument timing, panic/recover |
| Interfaces | [`iface`](concepts/iface) | structural satisfaction, method sets |
| Generics | [`generics`](concepts/generics) | inference, constraints, compile-time checks |
| Errors | [`errx`](concepts/errx) | `Is`/`As`, `%w` wrapping, `Join` |
| Channels | [`channels`](concepts/channels) | buffer, close, range, select, nil |
| Sync | [`syncx`](concepts/syncx) | Mutex, WaitGroup, Once, RWMutex |
| JSON | [`jsonx`](concepts/jsonx) | precision, `omitempty`, `RawMessage` |
| Text | [`stringsx`](concepts/stringsx) | bytes vs runes, UTF-8, Builder |
| Testing | [`testingx`](concepts/testingx) | tables, `t.Helper`, scoping, fuzz, examples |

### Mistakes — [`mistakes/`](mistakes/README.md)

Each package is a **Bad/Good** pair proving one pitfall.

| Mistake | Package | The difference |
|---|---|---|
| Loop variable capture | [`loopvar`](mistakes/loopvar) | shared capture vs per-iteration |
| Variable shadowing | [`shadows`](mistakes/shadows) | hidden inner error vs explicit assignment |
| Slice aliasing | [`slicecap`](mistakes/slicecap) | in-place `append` vs pinned view |
| Defer timing | [`deftime`](mistakes/deftime) | early argument eval, loop-held resources |
| Closing channels | [`closechan`](mistakes/closechan) | double/receiver close vs sender-only + `Once` |
| Typed nil | [`typednil`](mistakes/typednil) | non-nil interface holding a nil pointer |
| Swallowed errors | [`swallowerr`](mistakes/swallowerr) | dropped/overwritten errors vs checked |
| Goroutine leaks | [`goleak`](mistakes/goleak) | stuck workers vs cancellation/drain |

### Internals — [`internals/`](internals/README.md)

Measured examples of what the toolchain does. For the wider runtime
architecture, reflection, Green Tea GC and arena status, see the
[runtime deep dive](internals/RUNTIME.md).

| Topic | Package | Measured with |
|---|---|---|
| Escape analysis | [`escape`](internals/escape) | exact alloc totals (stack vs heap) |
| Memory layout | [`memlayout`](internals/memlayout) | field offsets, sizes, alignment |
| Interface repr | [`iface`](internals/iface) | `{type,data}` words, boxing allocations |
| Defer cost | [`defer`](internals/defer) | open-coded (0) vs loop path (1/iteration) |
| String memory | [`stringmem`](internals/stringmem) | shared bytes, concat vs Builder |
| Slices | [`slice`](internals/slice) | sharing, geometric growth, pinning |
| Maps | [`map`](internals/map) | nil behaviour, randomised order, spec deletes |
| GC | [`gc`](internals/gc) | weak pointers, finalizers, heap fall |
| Reflection | [`reflectx`](internals/reflectx) | type iteration, settable values, checked assignment |
| Goroutines | [`goroutine`](internals/goroutine) | spawn cost, stack growth |

### System design — [`sdesign/`](sdesign/README.md)

The single-node building blocks almost every system design is assembled
from; composed end-to-end in [`sdesign/DESIGNS.md`](sdesign/DESIGNS.md).

| Component | Package | The decision it owns |
|---|---|---|
| Clock | [`clock`](sdesign/clock) | time injected: TTLs tested by `Advance`, never by sleep |
| LRU cache | [`lru`](sdesign/lru) | capacity bound + TTL + recency eviction |
| Rate limiting | [`ratelimit`](sdesign/ratelimit) | token bucket vs sliding window |
| Blocking queue | [`queue`](sdesign/queue) | bounded backpressure instead of unbounded buffering |
| Priority queues | [`pqueue`](sdesign/pqueue) | structure follows workload: heap vs buckets vs sorted |
| Resource pool | [`pool`](sdesign/pool) | `maxOpen` ceiling, factory-failure slot release |
| Write-ahead log | [`wal`](sdesign/wal) | durability: CRC frames, torn-tail recovery |
| Pub/sub | [`pubsub`](sdesign/pubsub) | slow-subscriber policy: DropOldest vs DropNewest |
| Metrics | [`metrics`](sdesign/metrics) | atomic counters, gauges, quantile histograms |

### Distributed systems — [`distributed/`](distributed/README.md)

How independent machines agree, share work, detect failure and stay
consistent; composed end-to-end in
[`distributed/DESIGNS.md`](distributed/DESIGNS.md).

| Mechanism | Package | The decision it owns |
|---|---|---|
| Consistent hashing | [`hashring`](distributed/hashring) | joins/leaves move only the affected keys |
| Quorums | [`quorum`](distributed/quorum) | `R + W > N` proved by enumeration |
| Logical clocks | [`vclock`](distributed/vclock) | Lamport limits, vectors' three-valued order |
| Idempotency | [`idempotency`](distributed/idempotency) | at-least-once in → effectively-once execution |
| Election | [`election`](distributed/election) | one leader per ring, terms beat stale claims |
| Gossip | [`gossip`](distributed/gossip) | suspicion + incarnation refutation |
| Raft | [`raft`](distributed/raft) | majority commit, log matching, figure-8 rule |
| Saga | [`saga`](distributed/saga) | reverse compensation on a detached context |

### Agentic tools — [`agentictools/`](agentictools/README.md)

The instruments around the rest of this repository: no packages, prose with
the **Why / What / How / Advantages / Disadvantages / Usage** contract.

| Tool | What it owns |
|---|---|
| [graphify](agentictools/README.md#1-graphify-a-persistent-knowledge-graph-over-any-corpus) | a persistent knowledge graph over any corpus: AST for code, semantic for docs, EXTRACTED/INFERRED/AMBIGUOUS audit trail |
| [Obsidian](agentictools/README.md#2-obsidian-the-vault-the-map-and-the-notes-live-in) | the durable markdown vault: backlinks computed from files, graphify's graph opened as notes |
| [Soup](agentictools/README.md#3-soup-fine-tuning-and-post-training-in-one-command) | one `soup.yaml` and one command from base model + JSONL to a merged, exportable model |
| [REA](agentictools/README.md#4-rea-reverse-engineering-shipped-artefacts) | cited evidence about shipped binaries, APKs, firmware and captures — not about source |

## Conventions

1. **Interfaces are declared at the consumer**, 1–3 methods, verified with
   `var _ Iface = (*Impl)(nil)`.
2. **Errors are values**: returned, wrapped with `%w`, joined with
   `errors.Join`. Panics only for programmer errors at init.
3. **Constructors are `NewX(...) (X, error)`**; options are `WithY(...) Option`.
4. **Each package owns one domain.** No example is reused across patterns, so
   trade-offs are visible in context.
5. **Tests assert properties**, not implementations: *"unsubscribe closes the
   channel so `range` terminates"*, not *"Unsubscribe calls remove"*.
6. **Concurrency packages must pass `-race`.**

## Layout

```
adr/            the decision record
creational/     how objects are made        (5 patterns)
structural/     how objects are composed     (7 patterns)
behavioral/     how objects collaborate      (10 patterns)
concurrency/    Go-native parallel shapes    (4 patterns)
idioms/         Go-native construction       (2 patterns)
concepts/       language features            (10 packages)
mistakes/       pitfalls and their fixes     (8 packages)
internals/      runtime behaviour, measured  (9 packages)
sdesign/        system-design blocks         (9 packages + DESIGNS)
distributed/    distributed mechanisms       (8 packages + DESIGNS)
agentictools/   the instruments, in prose    (graphify, Obsidian, Soup, REA)
```

Every directory has its own README with per-package intent,
"when to use / when not to use", and pointers to the tests that prove the
interesting property.
