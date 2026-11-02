# Memory from CPU to language: allocators, types and garbage collection

This guide connects the machine's memory hierarchy to the decisions made
by a compiler, language runtime and allocator. It then compares Go's
garbage-collected heap with Rust's ownership and deterministic `Drop`
model. The goal is to give you a useful mental model for questions such
as “did this allocate?”, “who owns these bytes?”, “why is RSS still high?”
and “what does the GC actually scan?”

For measured Go layouts and allocations, see the
[internals package guide](README.md). For deeper Go runtime details, see
[runtime internals](RUNTIME.md); for Rust language detail, continue to the
[Rust guide](../rust/README.md).

## 1. The whole path in one picture

```text
source expression
   ↓ type/layout/lifetime decisions by compiler
value in register, stack frame, static data, or allocated object
   ↓ address used by a load/store instruction
CPU cache → memory controller → DRAM
   ↓ virtual address translated by MMU/page tables
physical page supplied and mapped by the operating system
```

An allocator manages chunks of memory for a language runtime or library.
A garbage collector or ownership rule decides when an object is eligible
to be destroyed. The operating system maps and unmaps pages for a process.
Those are related jobs, but they are not the same job.

The CPU itself does not know that an address refers to a Go slice, Rust
`Vec`, C structure or garbage-collected object. Machine code loads and
stores bytes at addresses. Type layout, object lifetime and which words
are pointers come from the compiler, runtime metadata and program
conventions.

## 2. How the CPU reaches memory

### Registers, caches and DRAM

The CPU executes on values held in registers and fetches other values
through a hierarchy of increasingly large, generally slower storage:

| Level | What it is for | Typical effect on code |
|---|---|---|
| Registers | Operands the current instructions use directly | Fastest access; a value may never be stored in RAM |
| L1/L2 cache | Small caches close to a core | Repeated nearby accesses can be served without going to DRAM |
| Last-level cache | Larger cache shared by some cores | Helps when a working set is too large for private caches |
| DRAM | Main memory | Much larger, but a cache miss costs more time and energy |

Exact cache sizes and timings vary by processor; the important lesson is
that a pointer dereference can miss a cache, and a chain of unrelated
pointer dereferences can make the CPU wait repeatedly. Contiguous arrays
often use memory more efficiently than linked objects because one cache
line can contain several neighbouring elements. Fewer allocations do not
automatically mean faster code, but allocation count, object shape and
locality often interact.

Caches move data in cache-line-sized chunks, not one language variable at
a time. If a loop walks adjacent elements, the next elements may already
be present. If a loop jumps through a scattered graph, the CPU may stall
on each missing line. This is why reducing pointer chasing or using a
compact representation can outperform replacing one allocator with
another.

### Virtual addresses and physical pages

An ordinary process uses **virtual addresses**. The address in a pointer
is interpreted through the process's page tables; the Memory Management
Unit (MMU) translates it to a physical page. A translation-lookaside
buffer (TLB) caches recent translations. A cache miss in the TLB may
require a page-table walk; an unmapped or nonresident page may cause a
page fault that enters the operating system.

```text
program pointer (virtual address)
             ↓ MMU + TLB/page tables
physical address → cache hierarchy → DRAM
```

Virtual memory lets processes have separate address spaces and lets the
OS map, protect, share and lazily back memory. A process can reserve a
large virtual range without having the same amount resident in RAM.
Therefore **virtual size**, **committed memory**, **resident set size
(RSS)** and a language runtime's **live heap** are different measurements.
They answer different questions.

The CPU and MMU enforce page-level read/write/execute permissions. They do
not normally check language array bounds or whether a pointer still refers
to a live object; a language, runtime, sanitizer or hardware safety
feature must provide those checks.

## 3. Stack, heap, static data and “where values live”

A process image is commonly described with regions like these:

| Region | Contents (simplified) | Usual lifetime |
|---|---|---|
| Text | Executable instructions | Process lifetime |
| Read-only/static data | Constants and some global data | Process lifetime |
| Mutable globals | Package/module variables and other static storage | Process lifetime |
| Stack | Call frames, spills and temporary storage | Frame/task lifetime |
| Heap | Dynamically managed objects | Until explicitly freed, ownership ends, or GC reclaims |
| Memory mappings | Files, shared memory, allocator/runtime reservations | Mapping-specific |

This is a useful model, not a promise that every source variable has a
particular physical home. A compiler may keep a local only in a register,
remove it entirely, inline a value into its owner, place temporary storage
on a stack, or allocate it in a heap. “Stack versus heap” describes a
storage/lifetime strategy; it is not a pair of memory chips, and most
languages do not let source code demand a particular machine address.

Stacks are cheap to reuse because a call frame is typically reclaimed as
the call returns. A heap is needed when storage must outlive the current
frame, has a dynamically determined size, or is shared through references
with a longer lifetime. These are common reasons, not universal rules:
escape analysis, inlining and optimization can change the final placement.

### A pointer is not the object

A pointer/reference is a value that identifies or refers to storage. A
container often has a small fixed-size header next to a separately
allocated buffer:

```text
slice / vector header                 backing allocation
┌───────────┬─────────┬──────────┐    ┌────┬────┬────┬────┬────┐
│ data ptr  │ length  │ capacity │ →  │ 7  │ 8  │ 9  │ .. │ .. │
└───────────┴─────────┴──────────┘    └────┴────┴────┴────┴────┘
```

The header can live on a stack or inside another object while its backing
array lives elsewhere. Copying a slice header in Go copies its pointer,
length and capacity, not the elements. Rust's `Vec<T>` similarly owns a
dynamic buffer conceptually described by pointer, length and capacity;
its exact private field layout is not a stable representation contract.
For Go's actual slice/string headers, see the
[measured layout guide](README.md#slices-slice).

## 4. What an allocator does

An allocator answers requests such as “give me at least *N* bytes aligned
to *A*.” It has to find a free block, split or round it to a suitable
size, track the resulting allocation, and later reuse or return it.
General-purpose allocators often combine:

- **Size classes/slabs:** pools of same-sized objects make small common
  requests fast and reduce bookkeeping.
- **Thread-local or per-processor caches:** a fast path avoids a global
  lock for many allocations.
- **Free lists or bump-pointer regions:** quickly hand out an already
  reserved slot or the next aligned address in a region.
- **Large-object/page paths:** handle requests too large for small-object
  pools.
- **Coalescing and scavenging:** combine free blocks or return whole pages
  to the operating system when appropriate.

Allocators trade speed, fragmentation, locality and memory overhead.
**Internal fragmentation** is unused space inside a rounded allocation;
**external fragmentation** is free space split into blocks that do not
fit a larger request. Allocators frequently keep freed memory in pools so
the next allocation can reuse it; freeing an object therefore does not
guarantee an immediate drop in process RSS.

A common layering is:

```text
language operation (Box, make, malloc, object creation)
          ↓
language/runtime allocator (small object pools, spans, arenas)
          ↓ requests/maps pages
operating-system virtual-memory manager
          ↓ maps physical frames as needed
physical memory
```

“The allocator” may refer to any layer in that stack. The Go runtime's
small-object allocator, Rust's selected global allocator, libc `malloc`,
and the kernel's page allocator manage different granularities and
lifetimes.

## 5. Types guide layout and safety; types are not allocations

A type tells the compiler which values and operations are valid. It can
also determine a value's size, alignment, field offsets, calling
convention and pointer structure. Padding may be inserted so fields meet
alignment requirements. A collector needs to know which object fields
contain pointers so it can trace references rather than mistaking an
integer that happens to look like an address for a live edge.

Do not treat these as one axis:

- **Static/dynamic typing** asks when type errors are checked.
- **Value/reference semantics** asks whether an assignment copies a value
  or shares an object.
- **Ownership/lifetime** asks who is responsible for ending a value's
  lifetime.
- **Allocation placement** asks whether storage ends up in a register,
  frame, static region or heap.
- **Memory management** asks how storage is eventually reused.

Languages mix these choices. Go and Rust are both statically typed, but
their lifetime management differs. A Go value can be stack allocated and
still be statically typed. A Rust `Box<T>` has a static type and owns a
heap allocation. A type declaration alone does not say “heap.”

Go structs follow source field order with padding according to the target
ABI/compiler; `unsafe.Sizeof` and `unsafe.Offsetof` can inspect a specific
build. Rust's default representation is not generally a stable field
layout promise; use `#[repr(C)]` when a C-compatible layout is required.
In either language, exported serialization or FFI formats should be
defined explicitly rather than relying on incidental in-memory layout.

The Go collector uses compiler/runtime type metadata and pointer maps to
scan heap objects and goroutine stacks. Rust's ordinary ownership/drop
path uses the compiled type's destructor behavior; it does not trace every
heap pointer at runtime. The distinction is central to how the two
systems reclaim memory.

## 6. Memory management strategies in general

### Explicit allocation and release

In C, a program may request storage with `malloc` and release it with
`free`. This is flexible and has little automatic lifetime machinery,
but every path must agree on who frees the block and how often. Missed
frees leak memory; freeing too early creates a dangling pointer; freeing
twice corrupts allocator state. Ownership APIs and RAII automate some of
the bookkeeping while preserving explicit destruction.

### Reference counting

Each shared object has a count of strong references. Creating a strong
reference increments the count; dropping one decrements it; zero means
the object can be destroyed immediately. This gives prompt reclamation
for acyclic ownership, but adds count updates and does not collect cycles:
two objects that point strongly at one another can keep their counts
above zero even when nothing outside the cycle can reach them.

### Tracing garbage collection

A tracing collector starts from **roots**—such as global references,
active stacks/register roots and runtime-owned references—and follows
pointers to discover reachable objects. Reachable objects are live; the
remaining objects can be reclaimed. A cycle is reclaimed if the entire
cycle is unreachable from roots.

A classic mark/sweep cycle is:

```text
white (not visited) → gray (found; children pending) → black (scanned)
roots → trace references → finish marking → sweep white objects
```

The basic algorithms make different trade-offs:

| Strategy | Reclamation | Typical trade-off |
|---|---|---|
| Mark/sweep | Mark reachable, reuse unmarked blocks | Can fragment; objects need not move |
| Copying/compacting | Move live objects, reclaim source region | Improves locality/fragmentation, needs pointer updates/space |
| Generational | Collect young objects more often | Leverages short-lived objects; needs barriers/remembered sets |
| Concurrent/incremental tracing | Share marking work with application | Lower long pauses, but barriers and CPU overhead are required |
| Reference counting | Destroy when count reaches zero | Prompt destruction; cycles need special handling |
| Ownership/regions | Compiler or scoped region determines when storage is dead | Little tracing work; lifetime must fit the model or use explicit escape hatches |

Real collectors combine techniques. A collector's **write barrier** is
runtime/compiler instrumentation around pointer updates that preserves
the tracing invariant while application code mutates the graph during
concurrent marking. A generational collector uses barriers to remember
old-to-young references. A tracing collector can pause, consume CPU
concurrently, or do both; “automatic” does not mean “free.”

### Reachability is not intention

A GC frees objects that are unreachable, not objects the programmer no
longer considers useful. An accidental reference in a global cache,
closure, goroutine, map or slice can retain a large object graph. This is
a **logical memory leak** even though the collector is working correctly.
In reference-counted or ownership systems, a value retained by a strong
owner is likewise still live.

After an object is destroyed or swept, the language allocator may reuse
its slot and retain the backing page. Only later may the allocator or
runtime return pages to the OS. Thus live heap, allocator-reserved memory,
virtual size and RSS do not have to move together.

## 7. Go: escape analysis, allocator and tracing GC

### Allocation is decided by the compiler

In Go, `new(T)` creates a zero-initialized `*T`, and `make` initializes
slices, maps and channels. Neither is a promise that the result must
reside on the garbage-collected heap. The compiler may prove that storage
does not escape and keep it on a goroutine stack, scalarize it into
registers, or remove it. A value that must outlive its stack frame or be
visible through a longer-lived reference generally has to escape to the
heap.

```go
type Record struct{ ID uint64 }

var retained *Record

func localWork() uint64 {
	r := Record{ID: 7}
	return r.ID // may stay in registers/stack or be optimized away
}

func retainForProcessLifetime() {
	r := Record{ID: 42}
	retained = &r // the value must outlive this call, so it escapes
}
```

Escape outcomes depend on context and compiler version. Inspect them rather
than guessing:

```bash
go build -gcflags='-m=2' ./...       # escape/inlining diagnostics
go test -bench=. -benchmem ./...     # allocs/op and bytes/op
```

The Go specification does not require a particular stack/heap placement.
An optimization may change where a value is stored without changing Go
program behavior. The runtime's implementation details, including
per-goroutine stack growth and stack maps, are covered in
[runtime internals](RUNTIME.md).

### Runtime allocator

The standard Go runtime commonly serves small heap objects from
size-classed spans. A P (scheduler processor) has an `mcache` fast path;
span lists are refilled through `mcentral`; `mheap` manages spans/pages
and larger allocations. Pointer-containing and pointer-free objects are
tracked differently because only the former require pointer scanning.
These names and exact size classes are runtime implementation details,
not language guarantees. Current Go releases continue optimizing common
allocation paths, including stack placement for some slice backing
storage.

The useful performance question is not only “how many allocations?”
Also measure bytes allocated, objects retained, pointer density, cache
locality, and allocation rate. A single large pointer-free buffer can
have different GC cost from the same bytes spread across many
pointer-rich objects.

### Go's collector

The Go runtime collector is a tracing, concurrent mark-and-sweep
collector. It starts from roots, traces typed pointers, marks live
objects and sweeps unmarked slots for reuse. It is **not generational or
moving** in the standard Go runtime model described here; unreachable
cycles can be reclaimed because reachability is traced from roots.

The collector does most marking concurrently with application goroutines,
with brief coordination pauses and write barriers to preserve correctness
when pointers change during marking. The GC pacer schedules work based on
heap growth and goals. `GOGC` controls a heap-growth target; a higher value
usually trades more memory headroom for less frequent collection. The
soft `GOMEMLIMIT`/`runtime/debug.SetMemoryLimit` limit can make the
collector work harder to stay within a process budget, but it cannot
reclaim live objects or guarantee a precise RSS ceiling.

Go 1.26 made the **Green Tea** collector the default. Green Tea improves
locality and scalability while marking/scanning small objects; it changes
how GC work is organized, not Go's basic tracing/reachability semantics.
See the [Go 1.26 release notes](https://go.dev/doc/go1.26) and the
[Green Tea design overview](https://go.dev/blog/greenteagc) for
version-specific changes. Verify collector details against the toolchain
you deploy.

Common Go memory pitfalls:

- A short slice can keep its entire backing array reachable.
- A pointer stored in a map, closure, global or goroutine stack may retain
  a large object graph.
- A slice tail that still contains pointers can keep deleted elements
  alive; clear pointer slots when the API/algorithm requires it.
- A goroutine blocked forever can retain its stack and everything
  reachable through values in that stack.
- `runtime.GC()` is not a substitute for finding allocations or retained
  references, and freeing heap objects does not force RSS to fall at once.

For GC traces and profiles, see the
[runtime measurement section](RUNTIME.md#5-measure-and-tune-gc-work). The
official [Go GC guide](https://go.dev/doc/gc-guide) covers pacing,
memory limits, roots and cost models.

## 8. Rust: ownership, allocators and deterministic destruction

### The compiler checks who may use a value

Rust has no built-in tracing garbage collector for ordinary values. Its
type system tracks ownership and borrows:

- A non-`Copy` value has one owner; moving it transfers ownership.
- `&T` is a shared borrow; `&mut T` is an exclusive mutable borrow while
  that borrow is active.
- Lifetimes let the compiler verify that references do not outlive the
  values they refer to. Lifetimes are compile-time relationships, not a
  runtime GC table attached to every reference.
- `Drop` runs when an owned value is destroyed, releasing resources that
  its type owns.

The owner may be a stack variable, a field inside another value, or a
smart pointer. Ownership decides who is responsible for destruction; it
does not mean every value is heap allocated.

```rust
struct Record { id: u64 }

fn borrow(record: &Record) -> u64 {
    record.id // borrow: no new heap allocation is implied
}

fn main() {
    let local = Record { id: 7 };
    let owned_on_heap = Box::new(Record { id: 42 });
    println!("{} {}", borrow(&local), borrow(&owned_on_heap));
    drop(owned_on_heap); // drops Record and deallocates its Box allocation
}
```

`Box<T>` owns one heap allocation (for non-zero-sized `T`); `Vec<T>` and
`String` own dynamically sized buffers when they have capacity/data.
`&T`, `&[T]` and `&str` are borrowed views and do not allocate just because
they are references. A move usually transfers the value/owner without
cloning its heap buffer. `Clone` explicitly creates another value and may
allocate.

### Drop is deterministic; the allocator still exists

When the owner is dropped, its destructor runs; containers drop their
owned elements and release their backing allocation through the allocator.
This is deterministic program order, unlike “some future GC cycle.” But
deallocation returns storage to an allocator; the allocator may retain
pages for reuse rather than return them immediately to the OS.

Rust's standard library has a global allocator used by types such as
`Box` and `Vec`; the exact default is platform/toolchain dependent. An
application can install a global allocator with `#[global_allocator]`
when it has a concrete reason to select or instrument one. Safe ownership
code handles ordinary allocation/deallocation; `std::alloc` and raw
pointers are low-level APIs with stricter layout and safety contracts.

Rust's safety rules do not prove that a program never leaks memory. A
deliberately forgotten value (`mem::forget`), an unbounded collection, or
a strong `Rc`/`Arc` cycle can retain allocations indefinitely. `Rc<T>`
uses a non-atomic reference count within one thread; `Arc<T>` uses an
atomic count for shared ownership across threads. `Weak<T>` is useful for
back-edges in graphs so the reference count can reach zero. Reference
counting is not tracing GC: it does not discover unreachable cycles.

`unsafe` can implement custom allocators, containers and FFI, but it does
not disable Rust's rules by magic. The unsafe code must uphold pointer,
aliasing, initialization and deallocation invariants that safe callers
rely on.

## 9. Go and Rust side by side

| Question | Go | Rust |
|---|---|---|
| Who checks types? | Static compiler; runtime type data also supports interfaces/reflection/GC | Static compiler; ownership and borrow rules are type-checked |
| Does a type force heap placement? | No; escape analysis and optimization choose storage | No; `Box`, `Vec` and other owners request heap storage explicitly |
| How is a dynamic buffer represented? | Slice/string/map/channel values have runtime-defined descriptors and backing storage | `Vec`/`String` own a buffer; references borrow; exact private layout is not a stable ABI |
| Ordinary reclamation | Tracing GC reclaims unreachable heap objects | Owners run `Drop`; allocator deallocates owned heap buffers |
| Cycles | Unreachable pointer cycles are collectible | `Rc`/`Arc` strong cycles can leak; use `Weak` for non-owning edges |
| When does cleanup happen? | Collector timing is nondeterministic; explicit resource APIs are still required | `Drop` runs deterministically when an owner is destroyed, though memory may stay cached by allocator |
| Runtime cost | GC metadata, tracing and pacing; stack/escape optimizations avoid some heap work | Ownership checks mostly compile-time; heap allocator and reference-count operations still cost runtime work |
| Typical inspection | `-gcflags=-m`, `pprof`, `-benchmem`, `GODEBUG=gctrace=1` | `cargo build --release`, profiling/allocator tools, inspect `Box`/`Vec` growth and clones |

Neither model eliminates the need to understand the data structure. Go
code can avoid heap allocation through locality and escape-friendly APIs;
Rust code can allocate heavily with repeated `Box::new`, `Vec` growth or
`String::clone`. Conversely, a Go garbage-collected program can be
predictable if its live set and allocation rate are bounded, and Rust
code can have leaks if strong owners form cycles or containers grow
without limit.

## 10. A practical way to reason about an allocation

When a benchmark or production service has a memory problem, ask these
questions in order:

1. **What bytes exist?** Find the value's layout, backing buffers,
   capacities, pointer fields and padding.
2. **Where are they stored?** Check compiler escape decisions and the
   actual allocator path; do not infer heap placement from syntax alone.
3. **Who owns or references them?** Trace Go roots/references or Rust
   owners/`Rc`/`Arc` counts. Identify what keeps the data alive.
4. **What can end the lifetime?** Go needs the object to become
   unreachable; Rust needs its owner to be dropped and its allocation
   released.
5. **Which layer retains the pages?** Distinguish live objects, free
   allocator slots, runtime-reserved pages, virtual address space and
   resident physical memory.
6. **Where is the time going?** Measure allocation rate, GC work,
   reference-counting, cache misses, page faults and memory bandwidth
   separately. Optimize the measured cost, not the word “heap.”

Useful starting commands:

```bash
# Go: see allocations and retained objects
go test -bench=. -benchmem ./...
go build -gcflags='-m=2' ./...
go tool pprof -alloc_space ./service heap.prof
go tool pprof -inuse_space ./service heap.prof

# Rust: make the optimized binary and use your platform's profiler/allocator tools
cargo build --release
cargo tree
```

The `alloc_space` profile answers “where were bytes allocated over the
profile interval?”; `inuse_space` answers “what is still retained at the
profile snapshot?” A large difference often means short-lived allocation
pressure, while a growing retained set points toward ownership/reference
retention. RSS can still differ because of runtime and allocator page
caching.

## Further reading

- [Go language specification: allocation](https://go.dev/ref/spec#Allocation)
- [Go garbage collector guide](https://go.dev/doc/gc-guide)
- [Go 1.26 release notes](https://go.dev/doc/go1.26) and [Green Tea GC overview](https://go.dev/blog/greenteagc)
- [Go compiler stack-allocation optimizations](https://go.dev/blog/allocation-optimizations)
- [Rust Book: ownership](https://doc.rust-lang.org/book/ch04-01-what-is-ownership.html)
- [Rust standard allocation APIs](https://doc.rust-lang.org/std/alloc/index.html)
- [Rust Book: reference cycles and `Weak`](https://doc.rust-lang.org/book/ch15-06-reference-cycles.html)
- [Linux kernel: page tables, MMU and TLB](https://docs.kernel.org/mm/page_tables.html)
- [Go runtime deep dive](RUNTIME.md), [measured Go memory internals](README.md), and [Rust guide](../rust/README.md)
