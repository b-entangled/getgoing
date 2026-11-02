# Go runtime internals: scheduler, allocator, GC, reflection and arenas

This guide explains how the standard Go toolchain runs native Go code.
The “runtime” is a library linked into each program, not a bytecode VM.
It supplies goroutine scheduling, stacks, allocation, garbage collection,
map/channel operations, panic handling, timers and network polling.

Implementation details move between Go releases. Examples here target
the repository's Go 1.26.4 toolchain. The smaller measured packages in
[`README.md`](README.md) pin language/runtime properties with tests;
this guide follows the larger mechanisms and how to measure them. For the
CPU, virtual-memory and Go-versus-Rust background, see
[`MEMORY.md`](MEMORY.md).

---

## 1. The runtime is part of the executable

**Why.** “The Go runtime” is often mistaken for a managed virtual
machine. That confusion leads people to expect a separate process,
bytecode interpreter or JIT that is not present in an ordinary Go
program.

**What.** The compiler emits native machine code. The linker includes
runtime code and metadata required by that binary. At startup the runtime
initializes memory, the scheduler, GC, signal handling and package
initializers, then invokes `main.main`. Runtime services continue to run
alongside application code in the same process and address space.

**How.** The linker/runtime metadata records stack maps, type descriptors,
function boundaries, pointer layouts and GC roots. The collector uses
pointer maps to find references; the scheduler uses function metadata to
stop goroutines at safe points; reflection consults type descriptors; and
panic unwinding follows frame metadata. These mechanisms are compiled
into the program and cooperate with the kernel through syscalls,
`mmap`, signals, timers and epoll/kqueue/IOCP as appropriate.

```text
source → compiler → native instructions + runtime + metadata → executable
                                               │
                      scheduler / GC / allocator / reflection / netpoll
                                               │
                                      OS threads + kernel syscalls
```

**Advantages.** Native code has no VM startup layer and the runtime can
specialize around Go's type and stack metadata. A single executable can
ship the language services it needs.

**Disadvantages.** Runtime code contributes binary size and memory. A
Go process still depends on OS facilities, shared libraries when
dynamically linked, and a compatible CPU/ABI. Runtime details are not
language guarantees and can change between releases.

---

## 2. G, M and P: goroutines are scheduled, not kernel threads

**Why.** A server may have hundreds of thousands of goroutines but only a
small number of OS threads executing Go code. Understanding the mapping
explains why goroutine-per-request works, why blocking calls matter and
why increasing `GOMAXPROCS` changes CPU parallelism rather than the
number of goroutines.

**What.** The runtime scheduler is commonly described with three objects:
**G** is a goroutine and its stack/state; **M** is an OS thread; **P** is
the runtime resource/token required to execute Go code, including
allocator and scheduler-local state. There are up to `GOMAXPROCS` Ps
available for parallel Go execution. An M needs a P to run Go code, but
can exist without one while blocked in a syscall.

**How.** A runnable G can be chosen from a P's local run queue or the
global queue. Idle Ps can steal runnable work from other Ps. When a G
blocks on a channel, mutex, timer or network readiness, the runtime parks
it and schedules another G on that P. When a thread blocks in a syscall,
the runtime can detach its P and let another M run Go code; when the
syscall returns, the M must reacquire a P or put the G back in a run
queue.

```text
P0 run queue: G1 G2 G3 ── M0 executes G1
G1 blocks on netpoll → parked; M0/P0 run G2
socket ready → netpoll wakes G1 → runnable queue → some P runs G1
```

Network I/O on supported platforms is integrated with a runtime poller,
so a goroutine waiting on a socket usually parks without tying up one OS
thread per connection. CPU-bound goroutines still require Ps and CPU
time. `GOMAXPROCS` limits parallel Go execution; it does not cap total
goroutine count. The runtime's preemption and syscall mechanics are
version/architecture dependent.

**Advantages.** Many mostly-blocked tasks share a much smaller number
of OS threads. The runtime can coordinate network readiness, timers,
blocking syscalls and goroutine scheduling in one process.

**Disadvantages.** A goroutine leak consumes stack and scheduler
metadata even while blocked. A hot lock, long non-preemptible operation,
cgo call or CPU-heavy loop can still delay unrelated work. Thread
affinity and foreign code complicate the scheduler's freedom.

### How goroutines park and wake

Channels, mutexes and the network poller connect blocking APIs to the
scheduler. A channel has runtime state for its element type, optional
buffer and waiting senders/receivers. An unbuffered send can hand a value
directly to a waiting receiver; a buffered channel uses a ring buffer
until full/empty. If an operation cannot proceed, the runtime parks the
G with a wait record instead of spinning an OS thread. A matching send,
receive or close makes waiting goroutines runnable. `select` registers
the current goroutine with its cases and parks once if none are ready.

`sync.Mutex` has a cheap uncontended atomic path and a contended parking
path integrated with runtime semaphores. The exact queueing/starvation
policy is an implementation detail, not a FIFO guarantee. Similarly,
the network poller waits for file-descriptor readiness using the host
OS's mechanism (such as epoll on Linux) and wakes the goroutine whose
socket operation can now progress.

```text
channel send with no receiver → G waits on channel queue → P runs other G
receiver arrives              → value handoff / buffer update → sender G runnable
net read returns EAGAIN       → G registered with netpoller → fd readable → G runnable
```

This is why a blocked goroutine is usually cheap compared with a blocked
thread—but an unbounded number of blocked goroutines still retains stacks,
request state and references that keep heap objects alive. See the
[`concurrency` guide](../concurrency/README.md) for the application-level
patterns that control ownership, cancellation and queue bounds.

---

## 3. Stacks and allocation: from a local value to a heap object

**Why.** Allocation cost is not just a call to `new`: a value may live
in a stack frame, a size-class span or a large-object allocation, and
those choices determine copying, GC scan work and lifetime.

**What.** Each goroutine has a small growable stack. Escape analysis
decides whether a value can remain on that stack or must outlive the
frame and move to the heap. Heap allocation is managed in size classes:
per-P `mcache` gives a fast local path; `mcentral` manages spans shared
by Ps; `mheap` manages spans and backing pages. Large allocations take a
less size-class-oriented path. Exact thresholds/layouts are implementation
details.

**How.** A small allocation commonly obtains a free slot from the
current P's `mcache` without a global lock. If the span is empty, the
runtime refills from a central list; if no suitable span exists, the
heap allocator obtains pages. Pointer-containing objects need GC scan
metadata and later tracing; pointer-free byte buffers are cheaper for
the collector to scan. The compiler may keep temporary slices/objects on
the stack when their addresses do not escape.

The runtime also reserves virtual address space in heap arenas and maps
physical memory as needed. Those internal heap arenas are address-space
management; they are not a user-facing region allocator. A virtual
reservation is not equivalent to resident memory: compare `HeapSys`,
`HeapAlloc`, RSS and page faults rather than treating one memory number
as “the heap.”

Useful compiler/runtime inspection:

```bash
go build -gcflags='-m=2' ./...    # escape and inlining decisions
go test -bench=. -benchmem ./...  # allocations/op, bytes/op
go test -gcflags='all=-d=checkptr=2' ./...  # extra unsafe-pointer checks
```

**Advantages.** The common small-object path is cheap and per-P
allocation reduces contention. Stack allocation avoids heap tracing and
becomes dead automatically when its frame returns.

**Disadvantages.** Returning a pointer, capturing a value, storing it in
a long-lived interface/map or handing it across a lifetime boundary may
force heap escape. Pointer-rich object graphs add scan work. A large
virtual heap reservation can look alarming in `VIRT` without occupying
that much physical RAM.

---

## 4. Garbage collection: classic concurrent mark/sweep and Green Tea

**Why.** Automatic memory management frees unreachable objects, but the
collector consumes CPU and can affect memory headroom and tail latency.
“Old GC versus new GC” needs care: the recent change improves the marking
implementation; it does not replace Go's tracing model with a different
ownership or generational model.

**What.** The classic Go collector is a concurrent, non-generational,
non-moving mark-and-sweep collector. It discovers roots, marks reachable
objects, and reclaims unmarked objects into allocator spans. Green Tea,
enabled by default in Go 1.26, changes how marking/scanning work is
organized—particularly for small-object spans—to improve memory locality
and CPU scalability. It remains tracing mark/sweep with the same
reachability semantics.

| Aspect | Earlier marker (“classic” path) | Green Tea (Go 1.26 default) |
|---|---|---|
| Collection model | Concurrent tracing mark/sweep | Same concurrent tracing mark/sweep |
| Work granularity | Objects/pointers placed on marking work buffers | Batches scan work by span/page for small objects |
| Main optimization | Concurrent work distribution, write barrier and pacer | Better locality: defer scans, collect marked objects in a span, scan together |
| Generational? | No | No |
| Moving/compacting? | No | No |
| User-visible object lifetime | Reachability | Reachability; same language semantics |

The old marker's object-by-object work can jump among unrelated cache
lines while tracing a pointer-rich graph. Green Tea groups pending small
objects by span: mark metadata identifies live objects, a span is queued,
and the marker later scans the pending objects together. It improves
locality and may use vector instructions on supported newer amd64 CPUs.
Large-object handling and other paths do not all become this new
span-oriented path.

**How.** The high-level cycle still has brief stop-the-world coordination
around concurrent work: roots/stacks are prepared, concurrent marking
traces the graph while the program runs, write barriers preserve
reachability invariants when mutator code changes pointers, mark
termination finishes outstanding work, and sweeping makes dead objects
available to the allocator. The pacer chooses when to start work based
on live heap growth and CPU/memory goals. Allocated-but-dead memory may
be reused by Go without immediately being returned to the OS.

```text
roots/stacks → concurrent mark (barrier active) → mark termination
             → sweep dead slots → reuse spans → scavenger may return pages
```

Go 1.25 exposed Green Tea as `GOEXPERIMENT=greenteagc`; Go 1.26 made it
the default and added vectorized scanning on supported CPUs. On the
repository's Go 1.26.4 toolchain, an A/B build can compare with
`GOEXPERIMENT=nogreenteagc`. Experiment flags are not stable API; check
the release notes and `go help build` for the exact compiler in use.
Go 1.27 keeps Green Tea as the default; the legacy opt-out may be
toolchain-specific and should only be used for a measured regression.

```bash
go build -o service-greentea .
GOEXPERIMENT=nogreenteagc go build -o service-classic .
go version -m service-greentea service-classic
```

Run the same load against both binaries with identical inputs, CPU
affinity, `GOGC`, memory limit and concurrency. Compare throughput,
latency, GC CPU, allocation rate, peak live heap and RSS; do not compare
only a single `gctrace` line.

The classic and Green Tea collectors are both non-generational. A
generational collector would treat recently allocated objects
differently from objects that survived previous cycles; Go's Green Tea
change is not that. The collector also does not compact the heap by
moving objects, so stable pointers remain a core implementation
constraint.

**Advantages.** Green Tea reduces pointer-scanning overhead for many
small-object-heavy workloads while preserving Go's existing allocation
and reachability semantics. The concurrent collector keeps most tracing
work off the application's stop-the-world path.

**Disadvantages.** Improvements depend on heap shape; pointer-free
buffers or workloads with little GC work may see little change. Both
collectors still pay for allocation volume, live pointer-rich graphs,
stack/root scanning and memory pressure. “New GC” is not a guarantee of
lower latency for every program.

---

## 5. Measure and tune GC work

**Why.** `runtime.GC()` in production is usually the wrong response to
memory usage. First determine whether the problem is allocation rate,
live heap, pointer scanning, high RSS, a memory limit or retained
references.

**What.** `GOGC` controls a heap-growth target, not a “GC frequency.” A
higher value usually trades more memory for less frequent collection;
a lower value usually spends more CPU to keep the heap smaller. The
runtime memory limit (`GOMEMLIMIT` or `debug.SetMemoryLimit`) is a soft
limit that makes the pacer work harder to stay within a process budget;
it cannot reclaim live objects or guarantee an exact RSS ceiling.

**How.** Capture several signals together:

```bash
GODEBUG=gctrace=1 ./service
go tool pprof http://localhost:6060/debug/pprof/heap
```

In code, `runtime/metrics` exposes counters/gauges such as GC cycle
counts, heap allocation/goal, live heap and GC CPU time. `runtime.ReadMemStats`
is useful for snapshots; `pprof` profiles show allocation sites. Compare
`alloc_space` (total allocated over time) with `inuse_space` (retained
objects now). Run an A/B benchmark with the same toolchain, input,
concurrency and memory limit before changing `GOGC` or collector
experiments.

Useful rules:

- High `alloc_space`, low `inuse_space`: reduce temporary allocation,
  batch work or reuse bounded buffers after profiling.
- High `inuse_space`: find retaining references, unbounded caches,
  goroutines or queues; lowering `GOGC` cannot free reachable objects.
- High GC CPU: inspect pointer-rich object graphs, allocation rate and
  heap goal; benchmark Green Tea against the default before opting out.
- High RSS with a modest live heap: inspect stacks, mapped files,
  allocator-retained pages and native/cgo memory as well as Go heap stats.

**Advantages.** Metrics separate churn from retention and collection
cost. A workload-specific memory limit can protect a container while
allowing the pacer to use spare memory when available.

**Disadvantages.** Lowering `GOGC` blindly can increase CPU use and
reduce throughput; setting an impossibly low memory limit can cause
near-continuous GC without fixing the actual retained live set. A single
`gctrace` cycle is not a benchmark.

---

## 6. Reflection: runtime type metadata with explicit safety checks

**Why.** Encoders, decoders, dependency injectors and generic
configuration tools often receive values whose concrete type is only
known at runtime. Reflection provides access to type/value metadata, but
it also moves some checks from compile time to runtime.

**What.** `reflect.Type` describes a type; `reflect.Value` describes a
particular value. `reflect.TypeOf` and `reflect.ValueOf` expose dynamic
information; `reflect.TypeFor[T]` obtains the type descriptor for a
static type parameter. A `Value` may be invalid, unaddressable,
non-settable or restricted because it came from an unexported field.
These are distinct states and should be checked before calling methods
that can panic.

**How.** The normal pattern is: inspect kind/type, verify validity and
assignability, verify `CanSet` for mutation, then perform the operation.
For dynamic values that should behave like a normal type assertion, Go
1.25 added `reflect.TypeAssert[T]`; Go 1.26 added iterator methods such
as `Type.Fields()` and `Value.Fields()`:

```go
type Config struct {
	Address string
	Port    int
}

typ := reflect.TypeFor[Config]()
for field := range typ.Fields() { // Go 1.26+
	fmt.Println(field.Name, field.Type)
}

value := reflect.ValueOf(&Config{}).Elem() // pointer makes it addressable
port := value.FieldByName("Port")
if port.IsValid() && port.CanSet() && port.Kind() == reflect.Int {
	port.SetInt(8443)
}

number, ok := reflect.TypeAssert[int](reflect.ValueOf(7))
```

The tested examples are in [`reflectx`](reflectx/). Reflection does not
automatically allocate: metadata lookups and `ValueOf` are not a blanket
heap-allocation rule. Repeated string-based lookup, `Value.Interface`,
dynamic `Value.Call`, conversions and escaping boxes can add cost; use
`-benchmem` and CPU/allocation profiles rather than folklore. Cache
immutable type metadata when profiling shows repeated inspection is hot.

**Advantages.** Reflection enables reusable serializers and adapters
without code generation for every type. Go 1.26 iterator APIs simplify
walking fields and methods, and `TypeAssert` makes dynamic assertions
more direct.

**Disadvantages.** Type mismatches, unexported fields and invalid values
can become runtime errors or panics. String-based field names weaken
refactoring safety; unrestricted reflection can make APIs harder to
understand than explicit interfaces or generics.

---

## 7. Arenas: internal heap arenas versus explicit regions

**Why.** Request-scoped workloads often allocate many objects that become
unreachable together. Region allocation can make bulk reclamation cheap,
but manually ending a region while a pointer escapes violates the
language's normal lifetime safety.

**What.** Go has internal heap arenas: runtime-managed virtual-address
regions used to organize the heap and its metadata. They are not an
application API. The proposal for a public `arena` package was an
experiment and is on indefinite hold; it is not a supported stable API
in Go 1.26.4. Do not copy old examples that require
`GOEXPERIMENT=arenas` into production code.

An explicit region/arena would allocate objects into a lifetime scope and
free that scope at once. Unlike ordinary Go heap objects, a pointer into
freed region memory would become invalid. The experiment required runtime
checks to fail safely, but the API was difficult to compose: arena
ownership would have to flow through many function signatures and every
escape past `Free` had to be prevented.

**How.** Prefer safe alternatives first:

| Need | Safer Go option | Lifetime semantics |
|---|---|---|
| Reuse byte buffers | Caller-owned `bytes.Buffer`; bounded `sync.Pool` for temporary buffers | Pool may discard values at any GC; callers reset before reuse |
| Avoid temporary heap objects | Let escape analysis keep values on stack | Compiler proves lifetime from program structure |
| Reuse per-request scratch | Pass an explicit reusable buffer/scratch object | Ownership and reset point are visible in API |
| Bulk lifetime | Keep request-local data behind one owner and drop the owner | Ordinary tracing GC reclaims when unreachable |
| Use unsafe/custom region allocator | Only in tightly bounded, measured low-level code | Must prove no reference outlives the region |

`sync.Pool` is not an arena and is not a cache: the runtime may discard
pooled items at any time. It is a best-effort reuse mechanism for
temporary objects. If evaluating any future region API, first verify
whether the `arena` package exists in the exact toolchain with `go list
arena`; never assume a GOEXPERIMENT from an old release still exists.

**Advantages.** Region allocation can reduce per-object bookkeeping and
free many objects together in workloads with strict, nested lifetimes.
The runtime's internal heap arenas help organize memory without exposing
manual frees to application code.

**Disadvantages.** Public arena APIs are not currently a stable Go
facility. Manual lifetime management can create use-after-free bugs and
forces ownership through APIs; pooled objects retain reset/aliasing
hazards. A lower GC percentage from an arena experiment is not worth
trading away safety unless end-to-end profiling proves the gain.

---

## 8. Runtime changes by release: what to re-measure

The runtime changes while the Go 1 language compatibility promise remains
stable. A few recent implementation changes matter when comparing old
benchmarks to a new compiler:

| Release | Runtime/toolchain change | What to check |
|---|---|---|
| Go 1.22 | Per-iteration variables for loop variables declared with `:=` | Closure-capture behavior by module language version |
| Go 1.24 | New built-in map implementation based on Swiss Tables | Map-heavy workloads, latency and memory |
| Go 1.25 | Green Tea collector available experimentally | A/B only with a controlled workload |
| Go 1.26 | Green Tea becomes default; vectorized scan paths on supported amd64 CPUs; new reflect iterators | GC CPU, small-object-heavy heaps and reflection utilities |
| Go 1.27 | Further small-object allocation specialization in the compiler/runtime | Allocation-heavy microbenchmarks and binary size |

Check the exact compiler selected by CI and production with
`go version`, inspect build metadata with `go version -m ./binary`, and
run benchmarks against both toolchains. Runtime changes should be
evaluated with representative heap shapes and workloads; “faster GC” is
not a substitute for measuring your own live set.

**Further reading:** [Go GC guide](https://go.dev/doc/gc-guide),
[Green Tea GC design](https://go.dev/blog/greenteagc),
[Go 1.26 release notes](https://go.dev/doc/go1.26),
[Go 1.27 release notes](https://go.dev/doc/go1.27), and the
[arena proposal status](https://github.com/golang/go/issues/51317).
