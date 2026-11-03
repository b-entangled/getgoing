# Linux (what the kernel does for your program)

A server process does not run on the machine — it runs on top of a kernel
that owns the CPU, the RAM, the disks and the network cards and lends them
out in small, audited parcels. Every request you serve is paid for in
processes being created, pages being mapped and faulted, descriptors being
read and written, goroutines being scheduled onto cores and woken by
readiness notifications. None of those mechanisms hide behind a Go
abstraction you can ignore: they decide the shape of your latency curve.

This document walks the five that matter for a long-running service —
processes, memory, files, scheduling and I/O — roughly in the order a
request meets them: how a call crosses into the kernel, how a `fork`
becomes a new program, how virtual memory and page faults decide what
"allocating" costs, how the page cache turns `write` into something that is
not yet on disk, how CFS and cgroups decide when your goroutines actually
run, and how epoll — and therefore Go's netpoller — turns ten thousand
connections into one thread's worth of work. Three entries then go a
layer deeper than the rest — the allocators and reclaim path under a
page fault, the block layer under the filesystem, the packet path under a
socket, and the interrupt/tracing paths below a wake-up — because that is
where a service's tail latency is manufactured while everything above it
reports success.

Every entry carries the same four labels: the need (**Why.**), the
mechanics (**How.**), the ways it fails in production (**Where it breaks.**)
and the honest stdlib and runtime touchpoints (**In Go.**) — `os/exec`,
`os/signal`, `os.File.Sync`, `syscall` and the netpoller — with pointers
into [`../internals/README.md`](../internals/README.md) wherever the
runtime's own layer takes over from the kernel.

The compiler-to-process handoff — ELF segments, `execve`, dynamic
libraries and hardening — is traced end to end in
[`execution`](../execution/README.md). Network-facing services and their
HTTP/TLS/gRPC boundaries are in [`webservices`](../webservices/README.md).

For the container layer built from these kernel primitives, read the
[Linux containers guide](CONTAINERS.md), then follow its companions on
[Kubernetes internals](KUBERNETES.md),
[container networking](CONTAINER_NETWORKING.md), and
[container storage](CONTAINER_STORAGE.md). Together they trace namespaces,
cgroups, OCI images/runtimes, Docker's lifecycle, Pod reconciliation, packet
paths and CSI-backed volumes.

---

## 1. User space, kernel space and system calls

**Why.** Your program may not touch a NIC, a disk or the clock: those
belong to the kernel, and one bad pointer must not be able to rewrite the
page tables. So every privileged action crosses a hardware-enforced
boundary through one narrow, audited door. The need is a call surface cheap
enough to use constantly and small enough for a human to review.

**How.** On x86-64 the kernel runs in ring 0 and the program in ring 3;
arm64 draws the same line as EL1 and EL0. A system call is a controlled
trap: the number goes in `rax`, the first six arguments in
`rdi, rsi, rdx, r10, r8, r9`, then `syscall` switches to a
kernel-programmed entry point with interrupts off and the stack swapped to
the thread's kernel stack. The result comes back in `rax` as a negative
errno and nothing else is shared.

That crossing is expensive next to a call: a call is a couple of
nanoseconds, a syscall is hundreds of nanoseconds to a microsecond once the
save, validation, work and return are counted. Programs ask for the same
handful of things constantly, so the kernel maps a small read-only page —
the vDSO — into every process and `clock_gettime` reads the clock from it:
`time.Now` costs about what a call costs and never traps.

The boundary is also where policy lives: `/proc/self/maps` lists the
mappings you may have, `/proc/self/status` the resource numbers,
`/proc/self/fd` a symlink per descriptor. `strace` attaches with ptrace and
stops the process at every call entry and exit — ideal for "what is it
actually doing", ruinous for timing, since each stop is a context switch.
seccomp-BPF closes the surface instead of watching it: a filter runs on
each entry and answers allow, `EPERM`, `TRAP` or `KILL`.

| Crossing | Typical cost | Paid when |
|---|---|---|
| function call | a few ns | same privilege, no shared resource |
| vDSO read | a few ns, no trap | clock and CPU-time queries |
| system call | ~100 ns – 1 µs | anything mutating kernel state |
| ptrace stop | µs per call, twice over | debugging, never in production |

**Where it breaks.** Hand-rolled syscall assembly puts the fourth argument
in `rcx` instead of `r10` — `syscall` clobbers `rcx` — and quietly passes
garbage. Returning a raw `-errno` as a success value turns a failure into a
bogus fd or a bogus byte count. `strace` in production hides the
load-dependent race you were chasing while inflating a 5 ms p99 into a
500 ms one. A seccomp profile that blocks `futex` or `epoll_ctl` kills the
runtime minutes after start-up rather than at boot, because those calls
only happen under load.

**In Go.** Prefer `os`, `net` and `os/exec`: they route through the runtime,
which folds blocking work into the scheduler and the netpoller. The
`syscall` package is the raw, frozen surface the runtime itself uses —
calling it directly skips that integration and depends on per-platform
call numbers, which is what `golang.org/x/sys/unix` exists to replace.

---

## 2. Processes: fork, exec and the process table

**Why.** A server must start a worker, a shell must run a command, a
supervisor must replace a crashed child. All three need the same
primitive: a fresh, isolated execution context, a different program loaded
into it, and a reliable answer about how it ended. Without that, one buggy
program takes the whole box with it.

**How.** A process is one row in the kernel's process table: a PID, an
`mm_struct` (its address space), a descriptor table, credentials, signal
state and a parent. `fork()` duplicates that row, but the address space is
not copied — pages start read-only and copy-on-write, so both processes
share frames until one writes, and the fault handler then copies for the
writer. Forking 1 GiB costs page tables, not 1 GiB.

`execve()` throws the image away: it loads a new binary, replaces the
mappings, resets the stack and starts at the entry point, while the
descriptor table, credentials, cwd and environment survive — unless a
descriptor carried `O_CLOEXEC`, which is what that flag exists for. The
pipeline is `fork`, `execve` in the child, `waitpid` in the parent, with a
pipe or two wiring stdin and stdout; `posix_spawn()` and `vfork()` only
compress that sequence, they never change it.

`wait` is not optional bookkeeping. An unreaped child stays in the table
as a zombie — no memory, no descriptors, just a status and a PID. Zombies
are harmless by the dozen and fatal by the hundred thousand: once every
PID is held nothing new can start, and a respawning supervisor makes it
worse. Orphans are adopted by PID 1 or by a registered subreaper, so the
crash of a mid-level supervisor still gets reaped — provided something in
the chain calls `wait`.

| Step | Call | What survives it |
|---|---|---|
| duplicate | `fork` | mm (COW), fds, creds — new PID |
| replace image | `execve` | fds and creds; the mm is new |
| reap | `waitpid` | status out, table row freed |
| shortcut | `posix_spawn` | same observable pipeline |

**Where it breaks.** Skipping `wait` in a respawn loop: the parent
accumulates zombies until PID allocation fails. Reading "started" as
"running" — the exit status is the only truth, and a crash-looping binary
looks healthy to anyone who never waits. Leaking a listening socket across
`exec` because `CLOEXEC` was never set, so the child holds the port after
the parent dies. Calling `execve` from a forked child while other threads
held a library lock, and deadlocking before the image is replaced.

**In Go.** `os/exec` is that pipeline: `Cmd.Start` forks and execs,
`Cmd.Wait` is the `waitpid` and must always be called — skipping it leaves
a zombie *and* keeps the pipes' read ends open, so a chatty child blocks
forever on a full stdout buffer. `SysProcAttr{Setsid: true}` starts the
child in a new session, free of the terminal's SIGHUP.

---

## 3. Threads and the kernel's view

**Why.** One process, several flows of control: a blocked read must not
stop the accept loop, and two cores should work on the same data without
paying a process boundary. The kernel needs a schedulable unit that shares
the process's resources — and user-space locks that cost close to nothing
when nobody else is competing for them.

**How.** To the kernel a thread is a `task_struct` built with different
`clone()` flags: `CLONE_VM` shares the address space, `CLONE_FILES` the
descriptor table, `CLONE_SIGHAND` the handlers, `CLONE_THREAD` the thread
group, `CLONE_SETTLS` the thread pointer. Threads are peers — there is no
main thread to the scheduler, only the one the user believes in. Since
NPTL (glibc 2.6, 2006) the model is strictly 1:1: one user thread, one
kernel-schedulable task, no library-scheduled switches left to debug.

Sharing is what makes locks interesting. A mutex is a word in shared
memory, so the fast path never enters the kernel: an atomic
compare-and-swap takes the word and the loser spins briefly. Only under
contention does it call `futex(FUTEX_WAIT)`, which queues it on that exact
word, and the holder calls `futex(FUTEX_WAKE)` to release one waiter. That
pair, added in 2002, underpins every pthread mutex, condvar and rwlock in
existence.

Each thread also carries fixed cost: a kernel stack (16 KiB on x86-64 by
default, plus a guard page under `CONFIG_VMAP_STACK`), saved state and a
scheduling entity. A switch between threads of one process runs about
1–5 µs — cheap, but it disturbs the TLB and the caches too, which is why
oversubscription costs more than the switch time suggests. TLS is one
register: `pthread_self()` reads a word relative to `fs`, not a lookup.

| Contention | Path | Cost |
|---|---|---|
| none | atomic on a shared word | nanoseconds |
| some | spin briefly, then `futex` wait | one syscall each way |
| switch | kernel scheduler | ~1–5 µs plus TLB/cache |
| new thread | `clone` plus kernel stack | µs and 16 KiB of kernel memory |

**Where it breaks.** Threads in one process share everything, so a wild
write in one is a wild write in all and there is no per-thread protection.
Forking from a multi-threaded process leaves the child with a single thread
still holding locks that their dead owners will never release. Blocking in
a library call — `getaddrinfo` in the wrong resolver — pins an OS thread
that cannot schedule anything else. Oversubscribe and the process's threads
collide with its cgroup quota while every one of them looks runnable.

**In Go.** Go lays M goroutine schedulers over N OS threads: `GOMAXPROCS`
caps how many threads may execute Go code, `runtime.LockOSThread` pins one
goroutine to one thread for code that needs thread-local state, and the
mapping itself — Ms, Ps, Gs, hand-off, run queues — is documented in
[`../internals/README.md`](../internals/README.md).

---

## 4. Virtual memory

**Why.** Every process needs its own view of memory: the pointer
`0x7f00...` must mean different physical bytes in different programs, a
bug in one must not scribble on another, and a few GiB of RAM must back
far more *mappings* than there is silicon. The abstraction that delivers
all three is a private address space per process, translated in hardware.

**How.** On x86-64 a process sees 48 bits of virtual address space and 4 KiB
pages. The page table has four levels — PML4, PDPT, PD and PT — each an
array of 512 eight-byte entries, so the address splits into four 9-bit
indexes plus a 12-bit page offset. The MMU walks that on every access,
which is why the TLB exists: it caches recent translations, and PCID (Intel)
or ASID (arm) tags them so a context switch does not flush everything —
switching processes then costs a tagged lookup, not a cold TLB.

Each entry carries permission bits: present, read/write, user, and a
separate execute bit (NX) that stops a stack buffer from becoming
shellcode. The cost of all that indirection is TLB reach: one 4 KiB page
needs one entry, and TLBs hold a few thousand, so large pages matter. A
2 MiB huge page covers 512× the range with one entry and a 1 GiB page
covers 512× that, which is why databases ask for huge pages — and why
transparent huge pages help on a quiet machine while occasionally costing a
collapse stall of 512 page migrations on a busy one.

Guard pages are the other half: glibc puts a `PROT_NONE` page below every
pthread stack and the kernel does the same under its own vmalloc stacks, so
an overflow faults instead of silently corrupting a neighbour. Go's
goroutine stacks skip the fault and compare a `stackguard` word in the
prologue instead — same intent, caught a few instructions earlier.

```
hit: virtual address → TLB lookup → frame plus offset → byte serviced
walk: TLB miss → PML4 → PDPT → PD → PT → fill the TLB → retry
huge: 2 MiB page → walk stops at the PD → one TLB entry → 512× reach
deny: write to a read-only page → fault → kernel fixes the PTE → resume
```

**Where it breaks.** Reserving address space and forgetting to touch it:
the allocation succeeds and the process dies on first use, long after the
"free memory" check that made you feel safe. `MAP_FIXED` quietly
overwriting an existing mapping. Page tables themselves consume memory —
about 2 MiB per GiB of 4 KiB-mapped space — so a huge sparse address space
is not free. Assuming a virtual address means anything across a fork, an
exec or an ASLR re-run.

**In Go.** The runtime never calls `malloc`: it takes address space with
`mmap` and manages arenas itself, so `runtime.MemStats.Sys` is the honest
"bytes from the OS" figure. The prologue's `stackguard` comparison is what
catches a runaway stack before it walks off the mapping, and
`debug.SetMemoryLimit` keeps heap growth inside a cgroup's `memory.max`.

---

## 5. Page faults

**Why.** Memory is the resource programs over-commit: a server may map a
100 GiB index while using 2 GiB, and a freshly created process should not
pay for pages it never touches. Faults are how the kernel defers the work
to the moment it is needed — and how it tells an honest bug (a wild write)
apart from an honest cost (a page arriving from disk).

**How.** A new mapping starts with the present bit clear in its PTE, so the
first load or store traps into the kernel. A *minor* fault finds the frame
already in memory — a cached file page, or a page the allocator already
has — maps it and returns, costing about a microsecond. A *major* fault has
to read from disk or swap first: hundreds of microseconds or milliseconds,
on the critical path of whatever instruction faulted.

Copy-on-write is a fault too. After `fork`, both processes share frames
marked read-only; the first write in either traps, the handler copies the
page and marks that process's entry writable. That is why `fork` is cheap
and why a write-heavy child pays for its memory one page at a time.
Protection faults are the same machinery with a different verdict: a write
to a read-only mapping or a fetch from a non-executable page is fatal to
the faulting instruction, and the kernel delivers `SIGSEGV` instead of
fixing anything.

When there is no frame to give, the kernel first reclaims clean cache
pages, then swaps anonymous pages out, then gives up: the OOM killer picks
a task — roughly by memory use, adjusted by `oom_score_adj` — and sends
`SIGKILL`. Inside a container the same decision is scoped: exceeding
`memory.max` kills a process in *that* cgroup and bumps
`memory.events: oom_kill`, leaving the host alone.

```
touch: load or store → MMU walks the PTE → present bit clear → fault
minor: frame already in RAM → map it → return → retry the instruction
major: page is on disk → issue the read → wait for I/O → map → retry
kill: memory exhausted → OOM killer scores tasks → SIGKILL → gone
```

**Where it breaks.** Swap-in latency hiding inside a p99: the process is
runnable, not blocked, so nothing in your metrics says "disk". The OOM
killer choosing your database because it is the biggest, not the guiltiest.
A container whose `memory.max` is smaller than the runtime's startup
footprint, so it dies under load and looks like a crash loop. Treating an
overcommit allocation as memory you have. Writing through a read-only
mapping and getting `SIGSEGV` where a bounds check would have been cheaper.

**In Go.** A nil dereference in Go code becomes a recoverable
`panic: runtime error: invalid memory address or nil pointer
dereference`; the same fault in cgo, or running out of memory, is fatal
with no panic value to recover. Guard the line with `runtime.MemStats`
and `debug.SetMemoryLimit`, not with `defer recover`.

---

## 6. The page cache, mmap and how writes actually work

**Why.** Disks are orders of magnitude slower than RAM, every program
re-reads the same files, and "the data is on disk" is a claim that must be
made true deliberately rather than accidentally. The kernel therefore
buffers everything and batches the expensive part — but that means your
`write` returning says far less than you think.

**How.** Reads go through the page cache: `read` copies between a cached
file page and your buffer, so the second read of the same bytes is a memory
copy instead of an I/O. Writes go the other way — `write` copies your bytes
into a page, marks it dirty and returns. No disk is involved, which is why
a write can be fast and still be entirely lost.

Nothing becomes durable until writeback runs: kernel flusher threads age
dirty pages out (`dirty_expire_centisecs`, 3000 = 30 s) and push back at
writers who get ahead — once dirty data passes `dirty_ratio` (20% of RAM by
default) the *writing task* blocks in `balance_dirty_pages` until writeback
catches up. That stall arrives as latency, never as an error.

Durability is a ladder, and each rung is a separate syscall:

| Rung | Call | What survives |
|---|---|---|
| buffered | `write` | a process crash, not a machine crash |
| file | `fsync(fd)` | that file's data and metadata |
| name | `fsync(dirfd)` | the entry created by `rename` |

`mmap` removes the copy instead of the flush: `MAP_SHARED` writes land in
the file's own dirty pages, `MAP_PRIVATE` writes land in private COW copies
that never do. malloc splits the same way — `brk` grows the heap for small
blocks and `mmap` takes over above glibc's 128 KiB threshold. The payoff of
all this batching is the group commit: one flush covering N writers, which
is what a log-structured design exploits and an fsync-per-message workload
throws away.

**Where it breaks.** `write` returning is not `fsync` succeeding — a power
cut after a successful write loses it, and a drive with a volatile cache
can lose it after an `fsync` that was never really flushed. Renaming into
place without fsyncing the directory, so the new name evaporates on the
next crash. Hitting `dirty_ratio` during a bulk load and watching latency
climb to seconds with zero errors logged. An `mmap`-edited file whose
dirty pages are also counted against a container's `memory.max`, so a
buffered writer gets OOM-killed while the host reports free memory.

**In Go.** `os.File.Sync` is the `fsync`, and it is the only call in the
stdlib that means "on disk"; `Close` is not it. The durable-write recipe is
write, `Sync`, `os.Rename`, then `Sync` on the directory opened with
`os.Open(".")` — exactly the pattern a write-ahead log needs, and exactly
what `os.WriteFile` does not do: it opens, writes, closes and never syncs.

---

## 7. Memory internals: buddy, slab and reclaim

**Why.** `make([]byte, 1<<30)` returned without an error, and the kernel
killed the process ten seconds later. Between the call that asks for
memory and the frame the MMU eventually hands back sit three allocators,
a set of watermarks and a reclaim path that can run *inside* your own
allocation — which is why "free", "available" and `memory.max` are three
different numbers and only one of them is a promise.

**How.** Physical memory is divided into 4 KiB frames and handed out by
the *buddy allocator*: one free list per order, order 0 a single frame
and order 10 a 4 MiB block on x86-64. A request whose list is empty
splits the next order up in half — the halves are address-adjacent
*buddies* — and a free merges back into its buddy when that one is also
free, so `/proc/buddyinfo` is a live map of the machine's fragmentation.

Frames arrive in zones: `DMA` (the first 16 MiB, for devices that cannot
address past it), `DMA32` (below 4 GiB) and `Normal`, which is nearly
everything on a 64-bit box. Each zone carries three watermarks. Above
`high`, allocation is pure bookkeeping. Below `low`, kswapd is woken to
reclaim in the background. Below `min`, the *allocating task* must
reclaim for itself before it can return — or fail outright if it cannot
sleep, as an interrupt-context allocation cannot. That crossing is where
the latency hides: a page fault, an `mmap` or a `fork` that needs a
frame runs a shrinker before the caller ever gets control back.

Reclaim walks the LRU lists by way of the reverse mappings: a page's
referenced bit is aged by scanning the page tables that point at it, and
what is collected first is cheapest to lose — clean file pages, dropped
with no I/O at all; then dirty file pages, after writeback; then
anonymous pages, which cost a swap write first. Compaction runs beside
it, migrating movable pages to free a contiguous order-N run, which is
exactly what a transparent huge page needs and exactly what a fragmented
machine can no longer produce.

Smaller than a page, everything comes from SLUB — the default slab
allocator since 2.6.23 — where fixed size classes pack objects into
slabs of one or more pages, a per-CPU freelist keeps the hot path
lock-free, and partial lists are reused rather than grown. `kmalloc(65)`
lands in the 96-byte class and pays for the rounding; `/proc/slabinfo`
accounts for every cache in the kernel. `vmalloc` is the other
direction: physically scattered frames made virtually contiguous for
module space and — under `CONFIG_VMAP_STACK` — the kernel stacks, at the
price of a deeper walk every time they are touched.

On top of all of it sits the cgroup accountant. Every page is charged to
the cgroup that first touched it, counted in `memory.current` (page
cache included), reclaimed from *that* cgroup's lists first, and killed
by *that* cgroup's own OOM when `memory.max` is passed — which is why
the host can look idle while a container dies.

| Layer | Unit | Serves | When it runs out |
|---|---|---|---|
| buddy | order-N frames | cache, stacks, slabs | watermark: reclaim, or fail |
| SLUB | size-class objects | kernel structs, dentries | grow a slab, maybe compact |
| vmalloc | virtually contiguous | modules, kernel stacks | reclaim, then fail |
| memcg | whole pages | one cgroup's charge | its own reclaim, then its OOM |

```
alloc:  order-N request → empty list → split a buddy → check the watermark
low:    below low → wake kswapd → background shrink of the LRU lists
deep:   below min → direct reclaim in this task → latency charged to you
frag:   frames free but none contiguous → migrate → or ENOMEM
oom:    nothing reclaimable → score tasks → SIGKILL, or the memcg's own
```

**Where it breaks.** Direct reclaim inside the allocation path is a p99
spike with no error attached: the task pays for shrinking pages somebody
else dirtied, and the only evidence is `pgscan_direct` in
`/proc/vmstat` and the `full` line of `/proc/pressure/memory`.
Fragmentation looks like plenty of free memory — hundreds of thousands
of free pages, none of them an order-4 run — so the huge page fails and
`allocstall` climbs while `free` reports gigabytes. Kernel memory is
invisible from the process: a socket-per-connection server grows its
`TCP`, dentry and inode slabs until a cgroup OOM fires with Go's heap
perfectly flat, and `memory.stat`'s `kernel`, `sock` and `pagetables`
rows are where it actually went. The OOM killer takes the biggest task,
not the guiltiest, so a cache that legitimately holds most of the RAM
dies for a leak in a sibling. And Go returns memory to the OS lazily, so
RSS sits at its high-water mark after the burst — a shape that trips a
`memory.max` sized from an idle reading.

**In Go.** The runtime is a second allocator on top of this one — size
classes, mcache, mcentral and the arena are
[`../internals/README.md`](../internals/README.md) — so `MemStats.Sys`
is the honest "bytes the OS gave us", and `HeapReleased` the part given
back, whether by the scavenger's `madvise(DONTNEED)` or explicitly by
`debug.FreeOSMemory`. `debug.SetMemoryLimit` / `GOMEMLIMIT` is a GC
*target*, not a reservation: set it below the cgroup's `memory.max` and
the collector runs harder instead of letting the kernel kill you — but
runtime overheads, stacks, span metadata and cgo all live outside it.
Nothing in the stdlib reads `memory.current` or PSI, and cgo allocations
never appear in `MemStats` at all: they are visible only in RSS.

---

## 8. The scheduler

**Why.** There are always more runnable tasks than cores, and every one of
them believes it should be running now. The scheduler's job is to decide,
continuously, whose turn it is — and to do it without starving the
interactive work while keeping the batch work busy. Fairness, latency and
priority all pull in different directions.

**How.** CFS (2007, the workhorse until 6.6) gave each CPU a runqueue
holding a red-black tree keyed by *virtual runtime*: the weighted time a
task has run. The leftmost task is picked; every tick adds elapsed time
divided by the task's weight, so a heavier-weighted task accrues vruntime
more slowly and gets the CPU more often. Nice values `-20..19` select
those weights — nice 0 is weight 1024 and each step multiplies by about
1.25, which is why "one nice step ≈ 10% more CPU" is the classic advice.

EEVDF, merged in 6.6 (October 2023), is the current direction: the tree is
still ordered by virtual time, but the picked task is the one with the
earliest *virtual deadline* among those currently *eligible*. A task that
has lagged behind accrues eligibility, so it gets served promptly instead
of waiting behind whoever happens to sit leftmost — the fix for CFS's
long-standing latency problem under mixed loads.

Real-time classes sit above all of it: `SCHED_FIFO` runs until it blocks
or yields, `SCHED_RR` adds a quantum, `SCHED_DEADLINE` reserves
wall-clock budget — each can starve every normal task on the box. Below
them the tick (`HZ` = 250 or 1000) decides when `SCHED_NORMAL` tasks are
preempted, and `taskset` pins tasks to cores at the price of balance.
Containers add a ceiling: cgroup v2 `cpu.max` reads `"$MAX $PERIOD"`
microseconds, so `50000 100000` grants 50 ms per 100 ms.

| Class | Policy | Starves others? |
|---|---|---|
| `SCHED_NORMAL` | CFS/EEVDF, nice -20..19 | no — shares by weight |
| `SCHED_FIFO` | runs until it blocks or yields | yes |
| `SCHED_RR` | FIFO with a time quantum | yes |
| `SCHED_DEADLINE` | deadline, runtime, period | highest priority |

**Where it breaks.** The cgroup throttle tail-latency story: a burst burns
the whole 50 ms quota in the first half of the period and then the
runnable task is frozen for the remaining 50 ms, so a service sized for
"50% CPU" shows p99 spikes at the period boundary with no queue and no
error. A rogue `SCHED_FIFO` thread spinning at priority 99 freezes the
machine. Pinning every worker to one core while eleven idle. Nice-ing the
wrong process — the value applies to the process and is inherited, not to
the hot path you meant. Short sleeps rounding up under timer slack.

**In Go.** The kernel schedules OS threads; the runtime schedules
goroutines *onto* them, so one schedulable unit exists per `GOMAXPROCS`,
not per goroutine; `LockOSThread` pins one that must stay put. Since
Go 1.25 the default is cgroup-aware, read from `cpu.max` at start-up.
See [`../internals/README.md`](../internals/README.md).

---

## 9. Signals

**Why.** Some things a process must be told rather than ask: the terminal
closed, the operator wants it to stop, a child died, a pointer was null.
Signals are the asynchronous per-process control channel — one bit of
information delivered at a point the kernel considers safe, not a queued
message with a payload.

**How.** Delivery happens when the kernel is about to return to user mode,
or inside a blocking call, which then fails with `EINTR` unless the handler
was registered with `SA_RESTART` — the kernel then restarts it, which is
why most blocking calls never appear to fail. A signal stays pending until
some thread unblocks it; the handler's mask blocks signals while it runs.

`sigaction` is the registration point: handler, mask and flags.
`SA_RESTART` restarts interrupted calls, `SA_RESETHAND` restores the
default after one delivery, `SA_ONSTACK` runs the handler on an alternate
stack — how a `SIGSEGV` handler survives a blown stack. Standard signals
are *not* queued: ten SIGTERMs arrive as one, so what you learn is "it
happened", never "how many". Real-time signals (`SIGRTMIN` up) are queued.

The hard rule is that a handler runs asynchronously, possibly in the middle
of `malloc` holding its lock. Only async-signal-safe calls are legal there:
`write`, `_exit`, `sigaction`, `socketpair`. `printf`, `malloc`, `free` and
your mutex are not — set a flag or write a byte to a self-pipe and let the
main loop do the work.

| Signal | Nr | Default | Meaning |
|---|---|---|---|
| `SIGHUP` | 1 | terminate | terminal hangup; daemons re-read config |
| `SIGINT` | 2 | terminate | Ctrl-C from the terminal |
| `SIGKILL` | 9 | terminate | uncatchable and unblockable — last resort |
| `SIGSEGV` | 11 | core | invalid memory access |
| `SIGPIPE` | 13 | terminate | wrote to a pipe with no reader |
| `SIGCHLD` | 17 | ignore | a child changed state |
| `SIGTERM` | 15 | terminate | polite "please stop" |
| `SIGSTOP` | 19 | stop | uncatchable pause |

**Where it breaks.** Calling `printf` from a handler and deadlocking on the
stdio buffer lock at exactly the wrong moment. Installing no `SIGTERM`
handler, so the process dies on the first deploy with no drain window.
Ignoring `SIGCHLD` and reaping lazily while a spawn loop outruns you.
Assuming `SIGKILL` cleans up — it cannot be trapped, so nothing you
registered runs and no lock is released. Dropping `EINTR` handling in a
program with its own handlers, or globally ignoring `SIGPIPE` and changing
every subsequent `write` into an `EPIPE` return.

**In Go.** `signal.Notify(ch, syscall.SIGTERM, ...)` is the registration:
the runtime already installed handlers with `SA_RESTART`, so Go code rarely
sees `EINTR`. SIGPIPE is special — a write to fd 1 or 2 that yields `EPIPE`
still kills the process, while any other descriptor returns `EPIPE`, so a
reader gone from a `log.Fatal`'d stdout can take the binary down.

---

## 10. File descriptors and the VFS

**Why.** One handle for everything: files, sockets, pipes, timers and
event sources all want the same `read`/`write`/`close` shape, and a
supervisor needs a single finite table it can count, inherit and close.
Without a uniform handle, every resource type needs its own API and its
own leak.

**How.** A descriptor is a small integer indexing the process's fd table —
0, 1 and 2 being stdin, stdout and stderr — and each entry points at a
kernel `struct file`: offset, flags and a vector of operations. A regular
file, a socket, a pipe end, an `eventfd`, a `timerfd` and an epoll
instance are all that same object with different operations, which is why
`select` can wait on all of them at once. `fork` inherits the table;
`O_CLOEXEC` (set by default on everything Go opens) drops entries at
`exec`.

Limits are finite and the error is famous: `RLIMIT_NOFILE` caps the soft
count — 1024 in a plain shell, higher under systemd — and going past it is
`EMFILE` for your own table or `ENFILE` for the system's. The classic leak
is a server that accepts, hits an error path and forgets `Close`, then
dies hours later while its descriptor count climbs monotonically.

The VFS is the indirection layer underneath: `open("/a/b")` walks a path
through the dentry cache (name → dentry → inode), the inode holds mode and
size and belongs to a superblock, and the superblock is mounted by a driver
that maps inode and block to disk addresses. Everything above the driver is
uniform, so `read` works on ext4, NFS and procfs alike. A hard link is a
second name for the *same* inode; a symlink is a tiny inode whose contents
are a path the walk follows, up to 40 hops before `ELOOP`.

| Handle | Backed by | Note |
|---|---|---|
| file | inode on a filesystem | offset shared by `dup` |
| socket | protocol plus inode | bound address, no directory entry |
| pipe | ring buffer, two ends | 64 KiB capacity by default |
| `/proc/self/fd/N` | symlink to a real file | `readlink` prints a path |

**Where it breaks.** An accept loop that hits an error path and forgets
`Close` dies hours later, its descriptor count climbing the whole time:
```text
$ ls /proc/self/fd | wc -l    # forty minutes after deploy
1021
```
`close` does not flush — durability needs `fsync`. Closing an fd while
another goroutine still reads it: numbers are recycled, so the "wrong fd"
bug is silent. Leaving `CLOEXEC` unset in C, or treating a `read` of 0
as an error rather than EOF.

**In Go.** Everything the runtime opens is `O_CLOEXEC`, and
`Cmd.ExtraFiles` is the only way to pass a descriptor to a child. A
never-closed `os.File` is reclaimed by its finalizer — a backstop, not a
pattern. `Close` on a nil file returns `ErrInvalid` rather than panicking,
but check it: that is [`../mistakes/README.md`](../mistakes/README.md).

---

## 11. Filesystems and crash consistency

**Why.** A power cut arrives at an arbitrary instruction, and the
filesystem must still be a tree afterwards. Without a story for partial
writes, a crash mid-update leaves bitmaps pointing at blocks that are in
use, or inodes pointing at blocks that are not — corruption that only
fsck can see, and only sometimes fix.

**How.** ext4 divides the device into 4 KiB blocks grouped around an
inode table (mode, uid, size, extent-tree root) and free-space bitmaps.
Large files use *extents* — start block, length, a small tree — instead of
indirect blocks. The journal (jbd2) writes each metadata update as an
intent, commits it behind a descriptor block, then checkpoints it home.

Without a journal — or with `data=writeback` — a crash mid-update leaves
torn writes: half an inode, half a bitmap, a block list that disagrees
with reality. `data=ordered`, the ext4 default, writes data blocks before
the commit record, so you may lose the tail of a new file but never see a
file claiming data that was never written.

| Crash point | data=ordered | no journal |
|---|---|---|
| after `write`, before `fsync` | data may be lost | data lost |
| after `fsync(file)` | the file is durable | usually durable, maybe torn |
| after `rename`, no dir `fsync` | the name may vanish | the name may vanish |
| mid-metadata update | journal replays → consistent | torn → fsck |

The API's one atomic primitive is `rename` within a filesystem, so the
crash-safe update is write, `fsync`, `rename`, then `fsync` the directory.
Even that needs a truthful drive: one with a volatile write cache that
ignores FLUSH loses data *after* a successful `fsync`.

`tmpfs` is the opposite bet: pages live in RAM, are swappable and vanish
at reboot — hence `/run` and container scratch. overlayfs stacks a
read-only `lower` on an `upper` where writes land, and the first write to
a lower file copies the whole file up before your first byte lands. The
full write path is traced in `sdesign/DESIGNS.md`.

**Where it breaks.** `rename` without the directory fsync, so the new name
survives in memory and not on the platter. Equating `Close` with durable.
A database on hardware whose flush is a lie, and a backup that restores
zeros. overlayfs copy-up making the first modification of a large file take
seconds. Running out of inodes (`df -i`) on a directory full of tiny files
while `df` reports space free, and the journal hitting `ENOSPC` — ext4 then
goes read-only rather than risk the tree.

**In Go.** `os.File.Sync` is `fsync`, `os.Rename` is the atomic swap, and
neither `os.WriteFile` nor a deferred `Close` performs either. The pattern
that survives a crash is small and entirely in the stdlib: write, `Sync`,
`Rename`, `Sync` on the opened directory — and check every one of those
errors, because a `Sync` that failed is a write that never happened.

---

## 12. The block layer: schedulers, queues and honest drives

**Why.** The filesystem above promises semantics; the device below
delivers *requests* — sorted, batched, split and acknowledged by a
machine that may acknowledge the bytes before they exist anywhere
durable. Between those two facts sit the queue depth, the scheduler,
the drive's own cache and, inside a virtual machine, the hypervisor's
cache. None of that is visible to `write`, and all of it decides the
shape of your tail latency.

**How.** Below the filesystem every operation becomes a `struct bio`: a
vector of pages, a sector range, an opcode. Since 3.13 that bio enters
*blk-mq*, where each hardware queue is a tag set with a lock-free
per-CPU submission side and a completion side, and an elevator sits in
front of it to decide the order:

| Scheduler | Default for | What it buys |
|---|---|---|
| `none` | NVMe | no reordering benefit to gain, so no overhead |
| `mq-deadline` | SATA / SCSI | reads meet a deadline before writes starve them |
| `kyber` | mixed loads | self-tuning latency targets |
| `bfq` | desktops, HDDs | per-process fairness, costs throughput on fast media |

NVMe already keeps many queues in parallel, so reordering buys nothing
and `none` is the driver's default; one SATA queue still wants an
elevator that keeps the device busy while reordering toward the read
deadline. The live choice is one file:
`/sys/block/*/queue/scheduler`.

Then the device. SATA NCQ holds 32 commands; an NVMe queue holds a
thousand or more and there are many of them, so throughput is depth
times service rate — a device with one request in flight is idle no
matter what the dashboard says. Behind the queue sits a cache, and the
cache is the durability question: a volatile write cache acknowledges
`fsync` before the bytes are on the medium, while power-loss-protected
drives hold acknowledged data in DRAM behind capacitors. The kernel's
fsync issues a FLUSH/FUA for exactly this reason, and a drive that
answers FLUSH without flushing turns the top rung of the durability
ladder into a lie.

Sector geometry still matters underneath: 512-byte logical sectors over
4 KiB physical ones (512e) make an unaligned access a
read-modify-write, and `O_DIRECT` must be aligned to the logical block
size or the kernel answers `EINVAL`.

And the stack is often deeper than one device. LVM and device-mapper
put a target — linear, thin, crypt — between filesystem and hardware,
md RAID puts parity and its write hole there, and inside a VM the
"disk" is virtio-blk over a hypervisor whose cache mode decides whether
a *guest* fsync ever reached the host.

```
write:  ext4 → bio → mq tag → elevator → driver → device cache → media
read:   bio → elevator → device → page cache → copy → your buffer
direct: O_DIRECT → bio → elevator → device → your buffer (aligned, uncached)
flush:  fsync → journal commit → FLUSH/FUA → device cache → medium
```

**Where it breaks.** `%util` in `iostat -x` reads 100% while half the
device is idle: it counts intervals with *at least one* request in
flight, so a multi-queue SSD pins the column at 100% with most of its
queues empty — `aqu-sz`, `r_await` and `w_await` are the numbers that
mean
something. Putting `bfq` on NVMe adds queueing the hardware never had,
buying tail latency with no throughput to show for it. A shared device
fills its queue with a neighbour's writes and your `fsync` waits behind
them, so the latency arrives in your p99 while every one of your own
metrics stays green. An unaligned `O_DIRECT` buffer is a plain
`EINVAL`; an unaligned filesystem on 512e media is a silent
read-modify-write tax on every access. And a database on a VM whose
host runs `cache=writeback` has a durability story that stops at the
guest: the guest fsyncs, the host dies, the bytes were never anywhere.

**In Go.** `os.File.Sync` is the fsync whose tail you just measured, and
`bufio` buys syscall amortisation rather than any of it. On Linux
`io.Copy(conn, file)` reaches `sendfile` for an `*os.File` source, so
page-cache pages travel to the socket without a user-space copy — the
page-cache entry above is precisely what it is bypassing. `O_DIRECT`
needs `golang.org/x/sys/unix` and your own alignment, io_uring has no
stdlib API, and the device's own counters live outside the process
entirely: `/proc/diskstats` or `iostat`, because `runtime.MemStats`
cannot see a queue.

---

## 13. IPC: pipes, sockets and shared memory

**Why.** Almost no real system is one process: a proxy talks to a worker,
a supervisor talks to its children, a database talks to a cache. The
mechanism has to carry data between address spaces that share nothing,
and each option trades copying against synchronisation, against
authentication, against portability.

**How.** A pipe is the simplest: one direction, two ends, a kernel ring
buffer that blocks the writer when it is full and the reader when it is
empty — 64 KiB by default, with writes of up to `PIPE_BUF` (4096) bytes
guaranteed atomic. `socketpair` makes the same thing bidirectional over a
unix domain socket. Named unix sockets add a filesystem path and
`SO_PEERCRED`, which hands you the peer's pid, uid and gid at connect time:
authentication without inventing a protocol.

TCP and UDP run the same API over the network stack — loopback included,
so even a local round trip costs context switches and copies. Shared
memory is the fast path: no copy at all, at the price of building your own
synchronisation across two address spaces, where a false-sharing cache line
between two cores can cost more than the copy you avoided. Files give
durability and no notification; message queues give bounded, prioritised
messages that few programs use; signals give one bit that coalesces.

| Mechanism | Shape | Use it for |
|---|---|---|
| `pipe` | unidirectional stream | parent/child stdout, 64 KiB buffer |
| `socketpair` | bidirectional stream | two processes you spawned |
| unix socket | named stream | local RPC with `SO_PEERCRED` identity |
| TCP / UDP | network stream or datagrams | anything crossing a machine |
| `mmap` shared | zero-copy pages | the fastest path, your own locking |
| file | durable, poll-free | results that must outlive the writer |
| mqueue / signal | messages / one bit | control events, rarely new code |

**Where it breaks.** Reading a pipe to EOF while some other process still
holds the write end: the read blocks forever, which is exactly what happens
when `os/exec` forgets to close its copies of a child's stdout. A 64 KiB
buffer filling because the reader is slower, turning a "send" into a
blocking call with no timeout. A unix socket path longer than the 108-byte
`sun_path`, or a stale socket file left behind by a crash. Assuming
`SO_PEERCRED` works on a socket that was not connected — it does not. Two
processes hammering one cache line of shared memory and paying a bounce per
handoff.

**In Go.** `os.Pipe` gives two `*os.File`s and both ends need closing.
`net.Pipe` is an in-memory, unbuffered `net.Conn` pair with exact
backpressure and no copy; `net.Listen("unix", path)` is the named form.
Channels are *not* IPC — they die with the process, as
[`../concepts/README.md`](../concepts/README.md) explains.

---

## 14. Kernel networking internals: skb, softirq and socket queues

**Why.** `conn.Write` handed the bytes to a kernel that must turn them
into DMA descriptors, interrupts, per-CPU queues and a wake-up on the
other machine — while a million other flows need exactly the same thing
at the same instant and your goroutine must not be involved. The need is
a path that is asleep almost always, wakes in batches, and drops on
purpose the moment a peer stops keeping up.

**How.** One structure is the currency: the `sk_buff`, a small linear
head for headers with paged fragments behind it, so a 64 KiB TSO segment
is one skb tagged with a `gso_size` instead of 64 copies of a 1500-byte
packet. skbs come from per-CPU pools and page fragments, because one
flow's traffic should not cross a cache line twice.

Receiving starts in hardware: the NIC DMAs into a pre-posted descriptor
ring, raises one MSI-X interrupt per batch of completions, and NAPI
turns that into a bounded poll — `netdev_budget`, 300 packets a round —
so a flood becomes repeated polls rather than an interrupt storm. GRO
merges adjacent segments before the stack sees them, receive-side
scaling (RSS) hashes the 4-tuple across hardware queues so different
flows land on different
CPUs, and what the driver leaves over queues on the per-CPU softnet
backlog (`netdev_max_backlog`, 1000). Overflow there is a silent drop
counted only in `/proc/net/softnet_stat`.

TCP does the stateful half: the 4-tuple looks its `struct sock` up in
the established hash — or the listener in the listen table — in-order
bytes join the receive queue, out-of-order bytes wait beside it for the
gap, an ACK goes out (delayed), and `sk_data_ready` wakes the epoll
waiter that has your goroutine parked on it. Space is `sk_rmem_alloc`
against the socket's receive buffer, and autotuning slides that window
between `tcp_rmem`'s 4 KiB and 6 MiB defaults until *you* pin it:
setting the buffer by hand disables the autotuning you were trying to
improve.

Sending mirrors it. `tcp_write_xmit` hands skbs to the qdisc —
`fq_codel` by default since 4.5, a real queue with real delay, which is
where `tc` shapes traffic and where bufferbloat lives — and the driver
then segments the GSO skb into the TX ring. Netfilter crosses both
directions at five hook points (PREROUTING, INPUT, FORWARD, OUTPUT,
POSTROUTING) where nftables rules run, with conntrack keeping a 5-tuple
state machine (NEW, ESTABLISHED, RELATED) that NAT then rewrites. And
XDP runs even earlier than all of it: a driver hook before an skb
exists at all, so a filter or a load balancer can drop or redirect at
line rate.

| Stage | Structure | Its bound |
|---|---|---|
| NIC | descriptor ring | `ethtool -G`, ring size |
| softirq | per-CPU backlog | `netdev_max_backlog` (1000) |
| socket | receive queue | `tcp_rmem` max, socket memory pressure |
| egress | qdisc | `default_qdisc`, `txqueuelen` |
| flows | conntrack table | `nf_conntrack_max` |

**Where it breaks.** A burst that outruns the backlog drops packets the
application never learns about: TCP retransmits and your p99 grows
while every local error counter still says zero. "Tuning" the receive
buffer with `SetReadBuffer` shrinks the window by turning autotuning
off, and the symptom is a small TCP window on a fat pipe. A full
conntrack table is worse — new connections are dropped with one kernel
log line ("nf_conntrack: table full"), no socket error and nothing at
all visible from Go. A deep qdisc queue inflates RTT until your own
timeouts fire first: the classic bufferbloat, and the reason
`fq_codel` became the default. Global socket memory pressure makes TCP
shrink every window on the box at once, so one memory-hungry connection
drags down a service that looks healthy. And one flow is one hash: a
single connection is processed on one CPU's softirq, so receive-side
scaling gives you nothing until a second flow exists.

**In Go.** The netpoller is the last stage of that path: one epoll
instance armed with `EPOLLIN|EPOLLOUT|EPOLLRDHUP|EPOLLET`, where a read
that would block parks the goroutine rather than the thread.
`SetReadBuffer` and `SetWriteBuffer` are `SO_RCVBUF`/`SO_SNDBUF` — the
kernel doubles what you ask for and caps it at `rmem_max`/`wmem_max` —
and `Dialer.Control` / `ListenConfig.Control` are where `SO_REUSEPORT`,
`IP_TOS` and friends go before bind. Drop counters live in `ss`, `nstat`
and `/proc/net/softnet_stat`, reachable only by shelling out or through
`golang.org/x/sys/unix`; the wire-level story is
[`../networking/README.md`](../networking/README.md) and the readiness
loop is the next entry.

---

## 15. I/O models and epoll ★

**Why.** A server with 10 000 connections must not pay for 10 000 threads,
and it must not poll 10 000 descriptors to discover that three are ready.
The I/O model is the decision that sets both the memory bill and the
latency floor, and every higher-level framework is a bet on one of these
rungs.

**How.** The ladder climbs from blocking — one thread per connection, simple
and correct, but a stack and a switch each, so it tops out around hundreds
— to non-blocking descriptors scanned by `select`/`poll`, where
`FD_SETSIZE` caps the set at 1024 on glibc and every call rescans it all.
epoll (Linux 2.5.x, 2002) inverts the cost: an *interest list* lives in the
kernel under `epoll_ctl`, and `epoll_wait` returns only ready descriptors —
O(ready) instead of O(registered).

Level-triggered (the default) keeps reporting a readable descriptor until
you read it, so a short read is fine — you will be asked again.
Edge-triggered (`EPOLLET`) notifies only on *change*, so you must read and
write until `EAGAIN` or the event is spent: one short read and the
connection stalls forever with data already arrived. That discipline is
the single most common epoll bug.

The reactor wraps this in a loop: wait for readiness, dispatch handlers
that may not block, come back — a blocking call in a handler stalls every
connection the loop owns, so DNS and `fsync` move to worker threads. Accept
thundering herd (many threads in `accept`, one connection, all but one
`EAGAIN`) is fixed by an accept mutex or by `SO_REUSEPORT` (3.9). io_uring
(5.1) skips the trap with submission and completion rings in shared memory.

```
loop: epoll_wait → ready list → dispatch handlers → back to epoll_wait
read: EPOLLIN fires → read until EAGAIN → parse → queue a response
write: write returns EAGAIN → wait for EPOLLOUT → drain → re-arm read
accept: one wakeup → accept until EAGAIN → register the fd → reuseport
```

**Where it breaks.** Edge-triggered without draining to `EAGAIN`: the
fd is never reported again and the request hangs with bytes already
arrived — a bug that reproduces only under load and only on short reads.
A blocking call left inside the reactor — `getaddrinfo`, a logging fsync —
and every connection behind it stops. Deregistering an fd while a handler
still holds it, then getting a wakeup for a stale `pollDesc`. Forking
without `EPOLL_CLOEXEC` and leaking the interest list into a child.
Thundering herd on accept when every worker waits on the same socket.

**In Go.** Go's netpoller *is* epoll: `netpollopen` registers
`EPOLLIN|EPOLLOUT|EPOLLRDHUP|EPOLLET`, and a read that would block parks
the goroutine instead — so a connection costs kilobytes, not a kernel
stack, and goroutine-per-conn scales where thread-per-conn does not. Raw
`epoll_ctl` is `golang.org/x/sys/unix` territory.

---

## 16. Namespaces, cgroups and capabilities

**Why.** One kernel, many tenants: a build job, a database and an untrusted
script all want the machine to themselves, and none of them may see, starve
or break the others. Isolation needs three separate answers — what you can
*see* (namespaces), how much you can *consume* (cgroups) and what you may
*do* (capabilities).

**How.** Namespaces cut the global lists into per-process views, one kind
per axis:

| Namespace | Isolates |
|---|---|
| `pid` | process IDs — you are PID 1 inside, children die to you |
| `mount` | the mount table — a private `/` to `pivot_root` into |
| `net` | interfaces, ports, routing — `:80` inside is not `:80` outside |
| `user` | uid mapping — root inside can be uid 100000 outside |
| `uts` | hostname and domain name |
| `ipc` | SysV shm, semaphores, message queues |
| `cgroup` | which cgroup tree the process can see |

cgroups v2 answer the second question from one unified hierarchy:
`cpu.max` as `"$MAX $PERIOD"` microseconds, `memory.max` as a hard cap whose
breach shows up as `oom_kill` in `memory.events`, `pids.max` against fork
bombs, `io.max` for device bandwidth — and `memory.current`, which counts
page cache too, so a "buffered write" program is charged for the cache it
created. Capabilities answer the third: instead of "uid 0 may do
everything", the kernel checks `capable()` per privilege — `CAP_NET_RAW`
for raw sockets and ping, `CAP_NET_BIND_SERVICE` for ports below 1024,
`CAP_SYS_ADMIN` for the huge mount-and-namespace surface that root used to
inherit by default. File capabilities (`setcap ...+ep`) grant that one
privilege to a binary without setuid, and seccomp-BPF trims the syscall
list on top. So `docker run` is: `unshare` the namespaces, `pivot_root`
into the image, attach a cgroup and set its limits, install the seccomp
allow-list, drop every capability the image did not ask for, then setuid to
the image's user.

**Where it breaks.** `memory.max` smaller than the page cache the app
generates, so the OOM killer fires inside the container while the host
reports plenty free. The `cpu.max` throttle from the scheduler entry showing
up as periodic tail latency in a service nobody thinks is containerised.
Copy-up latency the first time a container writes into a large file of its
own image. `CAP_SYS_ADMIN` or `--privileged` handed out for convenience,
which is the usual route out of the sandbox. A PID namespace whose entry
point does not reap `SIGCHLD`, so zombies pile up with no init to take
them. `pids.max` set too low and `clone` failing with a plain "try again".

**In Go.** The stdlib has no namespace, cgroup or capability API:
`os/exec`'s `SysProcAttr` stops at credentials and setsid, so container
plumbing means `setns`/`unshare` via `golang.org/x/sys/unix`. A service can
read `cpu.max` itself — and since Go 1.25 the runtime reads it once at
start-up to clamp the default `GOMAXPROCS` (`GODEBUG=containermaxprocs=0`).

---

## 17. Users, permissions and privilege

**Why.** A machine is shared, and the difference between "your process"
and "every process" is enforced entirely by the kernel at the point of
each syscall. Least privilege is not a virtue here — it is the only way a
compromised service stays a compromised service instead of a root shell.

**How.** Identity is numeric: the kernel knows uids and gids, never names,
so `/etc/passwd` merely maps name to uid to home to shell and
`/etc/shadow` holds the password hash, readable by root alone. Every file
carries an owner, a group and nine mode bits — the `rwx` triads for user,
group and other, compared against the *effective* identity, and `umask`
(default `022`) masks bits off at creation, so a `0666` request lands as
`0644`.

The same letters mean different things on a directory, and that is where
permission bugs live:

| Bit | On a file | On a directory |
|---|---|---|
| `r` | read the bytes | list the entry names |
| `w` | write the bytes | create or delete entries *inside* |
| `x` | execute it | traverse: `cd`, `stat`, open anything below |
| `4` / `2` / `1` | numeric weight of each letter | summed the same way |

So code directories are `0755` — everyone may list and traverse, only the
owner writes — and `/tmp` is `1777`: anyone may create, but the sticky bit
says only the owner or root may delete somebody else's entry. The setuid
bit (mode `4755`) makes a program run as *its owner*, which is what `sudo`
and `passwd` are; capabilities exist so that granting one right (bind
below 1024) no longer requires granting all of root. ACLs (`setfacl`,
shown by a `+` after the mode) extend the three triads with per-user
entries the kernel evaluates after them.

**Where it breaks.** A missing umask leaving a world-writable config that
holds a password. Running everything as root because permissions were
"taking too long" — every bug then becomes the machine's bug. Checking
access and then opening, a window an attacker fills by swapping the target
in between (TOCTOU: let the kernel check on the `open` itself). A shared
`/tmp` without the sticky bit, where strangers delete or symlink-squat your
files. Assuming uid 0 inside a user namespace is host root — it is exactly
the mapping, and that confusion is the basis of a dozen container escapes.

**In Go.** `os.Getuid` is the real identity and `os.Geteuid` the effective
one: a setuid helper reads `1000/0`, a dropped service `1000/1000`. With
`os.Chmod` and `os.Chown` that covers the bits, while
`SysProcAttr.Credential{Uid, Gid, Groups}` starts a child as another user —
a privilege you must hold before you can drop it.

---

## 18. Interrupts, softirqs, workqueues and kernel execution contexts

**Why.** A device can finish work at any instant, but the kernel cannot
run arbitrary driver code in the interrupted process or hold an interrupt
off while it drains a large queue. Linux therefore divides event handling
into contexts with different latency and blocking rules. Many “kernel
hangs,” lost packets and lockups are violations of those rules.

**How.** The short interrupt handler acknowledges or masks the device,
records the event and schedules deferred work. Deferred work runs later
in a context that matches its requirements:

```text
device event → hard IRQ top half → schedule deferred work
                                  ├─ softirq / NAPI: bounded, cannot sleep
                                  ├─ threaded IRQ: schedulable handler
                                  └─ workqueue: process context, may sleep
                                      ↓
                              wake wait queue / complete request
                                      ↓
                              scheduler runs waiting task
```

Hard IRQ context must stay short: it cannot take a sleeping mutex or
perform an operation that waits for memory or I/O. NAPI polling and
network softirqs process packets in batches; the budget yields CPU back
to other work, and excess work can be continued by `ksoftirqd/N`. A
threaded interrupt lets the handler run as a schedulable kernel thread
where supported/configured. Workqueues are appropriate when work must
sleep, allocate with blocking semantics or perform longer operations.
They are not “free background threads”: a blocked worker can stall other
items sharing that pool.

The context determines the lock. A process-context path can use a mutex;
interrupt-shared data needs a spin-based protocol with interrupt/bottom-
half protection appropriate to every caller. RCU takes another route:
readers traverse a published pointer cheaply, an updater removes or
replaces it, then waits for a grace period before reclaiming the old
object. Exact `spinlock_t` semantics differ on PREEMPT_RT, so low-level
code uses the primitive documented for its context rather than assuming
all spinlocks are hard spinning locks.

| Context | May sleep? | Typical work | Typical synchronization |
|---|---:|---|---|
| Process/task | Yes | Syscalls, filesystem, worker threads | Mutexes, rwsems, completions |
| Workqueue | Yes | Deferred device or cleanup work | Mutexes; worker-pool constraints still apply |
| Softirq/NAPI | No | Network RX/TX polling, timer work | Short spin/RCU critical sections |
| Hard IRQ | No | Acknowledge device and schedule follow-up | Minimal per-device IRQ-safe synchronization |
| NMI | No, highly restricted | Performance counter or watchdog events | NMI-safe primitives only |

**Where it breaks.** Calling a sleeping allocator or mutex from hard IRQ
context produces “sleeping function called from invalid context” or a
deadlock. Taking a lock in process context with interrupts enabled and
then taking it in that CPU's interrupt handler deadlocks when the handler
interrupts the holder. An unbounded softirq/NAPI poll starves user tasks;
work queued onto a single-worker queue can deadlock when a worker waits
for work queued behind itself. Reclaiming an RCU object before a grace
period turns a fast read path into a use-after-free.

**In practice.** Inspect interrupt distribution with
`cat /proc/interrupts`, deferred work with `cat /proc/softirqs`, and
softnet packet backlog/drop counters with `/proc/net/softnet_stat`.
`ps -eLo pid,tid,comm,psr,stat | grep -E 'irq|ksoftirqd|kworker'`
shows relevant kernel threads. For interrupt latency or starvation,
measure before tuning: `perf sched timehist`, ftrace latency tracers and
the kernel's IRQ/softirq tracepoints expose wakeup-to-run and handler
duration. Networking's NAPI path is described in
[`networking`](../networking/README.md#15-the-wire-to-socket-packet-path).

---

## 19. Tracing the kernel without guessing

**Why.** User-space profiles show where a thread spends its time, but a
thread blocked in the kernel can look idle while it waits for a lock,
page, device completion or runnable CPU. Kernel tracing connects those
waits to named events and execution paths.

**How.** Use the least invasive instrument that answers the question:

| Tool | What it observes | Good first question |
|---|---|---|
| `strace` | Syscall entry/exit and errno | Is the process blocked in `read`, `futex`, `connect` or `fsync`? |
| `perf stat` | Hardware/software counters | Did context switches, faults or CPU cycles change? |
| `perf sched` | Wakeups, runnable delay and scheduling | Was the task runnable but not scheduled? |
| ftrace/trace-cmd | Kernel tracepoints and function/latency tracers | Which IRQ, scheduler or block event preceded the delay? |
| eBPF/bpftrace | Filtered probes and aggregations in kernel context | Which process/stack is generating a particular event? |
| `perf record` | Sampled user and kernel stacks | Where are CPU cycles spent across the boundary? |

Tracepoints are named hooks maintained by kernel subsystems; kprobes
attach to implementation functions and can be more brittle across kernel
versions. BPF programs run in a verifier-constrained environment, usually
aggregate or filter data before it leaves the kernel. All tracing has a
cost: event volume, buffer size, symbol availability, permissions and
probe overhead matter. Start with a narrow PID, CPU, event and time
window.

```bash
# Count scheduling and context-switch activity while one process runs.
sudo perf stat -e context-switches,cpu-migrations,page-faults -p "$PID" sleep 10

# Record scheduler wakeups/switches and inspect runnable delay.
sudo perf sched record -- sleep 5
sudo perf sched timehist

# Discover the tracepoints actually present on this kernel.
sudo sh -c 'cat /sys/kernel/tracing/available_events | sort | head -40'
```

The last command is a sample of available event names, not a stable ABI
list; use the complete list and the target kernel's tracepoint format
files before writing a production tracer. On systems where tracefs is
mounted elsewhere, locate it with `findmnt -t tracefs`.

**Where it breaks.** `strace` ptrace-stops every call and distorts
latency-sensitive workloads. A high-rate tracepoint can fill per-CPU
rings faster than userspace drains them, losing events exactly during a
burst. Kprobes on inlined or changing functions may miss or break after a
kernel upgrade. A broad BPF program can add CPU cost, hit verifier limits
or export sensitive arguments. Kernel symbols may be unavailable due to
KASLR, stripped symbols or lockdown policy.

**In Go.** Start at the runtime boundary: `go tool trace` shows goroutine
blocking, GC and scheduler delay; `pprof` identifies user-space CPU and
allocation work. If a goroutine is blocked in a syscall or netpoll, pair
that view with `strace -f -ttT -p PID`, `perf sched`, or a narrowly
filtered kernel trace. One trace explains one layer; correlate timestamps
before concluding that the syscall or kernel event caused the user-visible
latency.
