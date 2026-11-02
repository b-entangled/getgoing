# Database internals (how hardware and the OS shape your queries)

There are no Go packages in this directory. What follows is prose about
what a database does between SQL and silicon — the CPU cycles a plan
burns, the RAM the buffer pool fights over, the OS page cache sitting
quietly behind it, the fsync that turns a commit into a promise, and the
platters or flash cells that finally hold the bytes. Every tuning knob
you will ever be asked to turn — buffer pool size, autovacuum
thresholds, `vm.swappiness`, queue depth — is really CPU, RAM, the page
cache or the disk underneath wearing a different name, and a knob whose
underlying hardware you cannot name is a knob you are turning blind.
The last seven entries leave the running system behind and cover how you
get it back: backups, the WAL archive that turns them into point-in-time
recovery, and what to do when the pages themselves are damaged.

Each entry has the same four parts: the need a mechanism fills, the
mechanics that fill it, the ways it fails, and the knobs and metrics
that govern it. Read them top to bottom the first time — the ladder in
entry 1 is the map the other twenty-one stand on — and after that they
work as reference. There is no test suite to run here: the subject is
the machine you already have, and the tables in it are the
specification.

Language features live over in [concepts](../concepts/README.md) and
[runtime behaviour](../internals/README.md); the kernel you share the
box with is [linux](../linux/README.md), with crash-safety notes in
[linux/DESIGNS](../linux/DESIGNS.md); the network under a replica
stream is [networking](../networking/README.md); single-node building
blocks including a working write-ahead log are [sdesign](../sdesign/README.md);
systems assembled from all of it are [distributed](../distributed/DESIGNS.md);
and the ways people get this wrong are [mistakes](../mistakes/README.md).
Seven entries below carry a ★ — page cache, mmap, WAL, fsync, B-trees,
LSM-trees and consistent backups — because they fail quietly, in
production, at the worst possible moment.

---

## 1. The storage hierarchy every query walks

**Why.** A query is a sequence of fetches, and every fetch lands
somewhere on a ladder whose rungs are a thousand times apart. Without
one shared picture of that ladder, "the database is slow" cannot be
decomposed into "slow at which level", and every argument about adding
RAM or buying a faster disk is guesswork.

**How.** Ask each tier one question: for a cold, random 8-byte lookup,
how long does the answer take, and how many can one query afford inside
a one millisecond budget?

| Tier | Latency | Lookups in 1 ms |
|---|---|---|
| L1 data cache | ~1 ns | ~1,000,000 |
| L3 cache | ~4-10 ns | ~100,000-250,000 |
| Main memory (RAM) | ~60-100 ns | ~10,000-16,000 |
| NVMe SSD | ~10-25 µs | ~40-100 |
| SATA SSD | ~50-100 µs | ~10-20 |
| HDD random read | ~5-10 ms | ~0.1-0.2 |
| Same-rack network | ~100-500 µs | ~2-10 round trips |

The steps between rungs are the whole argument. L1 to RAM is about a
hundred times, RAM to an SSD another hundred or two, and an SSD to a
spinning disk about a thousand — so one random HDD read, at five to ten
milliseconds, burns roughly 15-30 million CPU cycles waiting for a head
to move. Cache lines add the second axis: a transfer is 64 bytes on
x86-64 and most ARM parts, so touching one 8-byte column pulls in its
neighbours, and a "random" access pattern is really a statement about
which cache lines you never revisit.

Databases are mostly an exercise in never touching the bottom rows. A
buffer pool keeps hot pages in RAM (entry 3), B-tree height is chosen
so a lookup descends pinned nodes and pays at most one cold leaf (entry
9), the write-ahead log turns many random page writes into one
sequential flush (entry 7), and the OS page cache doubles as a second
chance to avoid the device entirely (entry 4).

**Where it breaks.** The working set outgrows RAM and throughput does
not degrade gracefully — it falls off a cliff toward the raw IOPS of the
device underneath. Median latency stays acceptable while p99 is
dominated by the rare fetches that reach the bottom of the ladder, so
an average tells you nothing. False sharing puts two hot counters on
one cache line and both cores pay for every write. Cross a NUMA node
and the "RAM" row silently becomes 1.3-2× slower (entry 2).

**In practice.** Watch hit rates first: buffer pool hits vs disk reads,
then the device with `iostat -x` — `r/s`, `await` and `aqu-sz` say how
often and how long you are descending. `perf stat -e
cache-misses,LLC-load-misses` locates the cost inside the CPU. Size RAM
against the working set, not the data file: a 500 GB table with a hot
10 GB slice needs the 10 GB. In Go you cannot fix a ladder from code,
but you can bound the wait — `context.WithTimeout` caps how long a
request may stand at the bottom rung, and pooling (entry 15) stops a
burst from queueing above it.

---

## 2. CPU: where a query's cycles actually go

**Why.** The plan says "index scan, 400 rows" and the wall clock says
400 ms. Until that cost is attributed — parsing, planning, executing,
latching, or simply not running — every optimisation is a guess, and a
machine can sit at 100% CPU doing almost no useful work.

**How.** A query spends its cycles in three phases. Parsing lexes and
builds a parse tree; planning rewrites it, reads statistics and picks an
access path; execution walks the plan. Parsing and planning are usually
microseconds — unless an application builds SQL by string concatenation,
in which case no plan is ever reused and the planner becomes a
per-request tax.

Execution divides again. The classical Volcano iterator calls `Next`
once per row: one interface dispatch and one branch per row — cheap to
write, and at millions of rows it dominates. Vectorized engines pull
batches per call instead, amortising dispatch into a straight-line loop
the branch predictor and SIMD units can exploit. Between the phases sit
the shared structures — buffer pool latches, catalog locks, lock-manager
slots — won spin-then-sleep: spinning avoids a context switch of a few
microseconds, so engines spin briefly, then yield.

Scheduling is the multiplier. One thread or process per running query
means the run queue, not the query, decides latency, and cgroups turn
that into a hard cliff: a `cpu.max` of `100000 50000` grants 50 ms of
CPU per 100 ms period, so a throttled commit thread can sit still for
up to 50 ms — a stall that appears nowhere in the plan and everywhere
in p99. Locality matters as much: a 16 KB B-tree node is 256 cache
lines, so a descent streams it through L1 and lives in L2; remote NUMA
memory costs 1.3-2× a local access while huge pages shrink the TLB
working set by 512× per entry.

| Cost centre | Symptom | Where to see it |
|---|---|---|
| Parse and plan | tiny queries, repeated SQL text | plan time, statement stats |
| Execute | CPU grows with rows scanned | `perf top` in executor code |
| Latch contention | `%sys` rises with concurrency | wait events, `perf lock` |
| Run-queue wait | throughput flat while CPU looks busy | `vmstat` r, run delay |
| Cache misses | busy but unproductive | `perf stat` misses, IPC |
| NUMA remote | throughput halves at a socket boundary | `numastat -p`, `perf c2c` |
| Throttling | multi-ms stalls, no I/O to blame | cgroup `cpu.stat` |

**Where it breaks.** Latch convoys: hundreds of sessions pile onto one
hot page and the queue drains at the speed of the slowest holder. A
spinning thread that gets preempted is the worst case — it holds
nothing and burns a slot. Oversubscription makes context switches the
largest consumer on the box while queries crawl. In the cloud a noisy
neighbour shows up as `%steal`, cgroup throttling as a latency floor
nobody can explain from database metrics, and NUMA-blind placement puts
half the buffer pool behind the interconnect.

**In practice.** `mpstat -P ALL 1` splits user, sys, idle, iowait and
steal; `vmstat 1` gives the run queue in one screen; `perf top` names
the function holding the CPU. Read cgroup `cpu.stat` — `nr_throttled`
and `throttled_usec` are the smoking gun. From the database: wait
events by type, and per-statement CPU time. In Go, goroutines multiplex
onto `GOMAXPROCS` threads so a blocked driver call costs a goroutine
rather than an OS thread — but the server still spends a thread on it,
and a `context` deadline does not interrupt work already running there.

---

## 3. RAM: the buffer pool is the database's own cache

**Why.** The kernel already caches files, so a database could trust it.
It does not, because a cache tuned for "recently touched file blocks"
has no idea a query is about to sequential-scan a cold table and wipe
out the hot set. The need is a cache whose eviction policy, pinning
rules and dirty-page accounting answer to the planner.

**How.** A buffer pool is a fixed array of frames plus a hash from
(read file id, page number) to frame. A hit returns from memory; a miss
reads the page into a free frame and, if none is free, evicts something
chosen by the replacement policy. Writes mark the frame dirty and leave
it there — write-back happens later under checkpoint or memory
pressure, because the log already makes the change recoverable (entry
7).

Three properties come from doing this in user space. Scan resistance: a
plain LRU treats every page of a table scan as freshly used and evicts
the entire hot set, so engines insert mid-stream — InnoDB keeps young
and old sublists and lets scanned pages age slowly. Pinning: the root
and the path to a leaf stay resident while a traversal holds them.
Read-ahead: the engine knows the next leaf is adjacent and asks for the
next 64 pages in order rather than leaving the kernel to guess.

Sizing follows ownership of the box: 60-80% of RAM to the pool on a
dedicated machine, the rest for backend stacks, per-query workspaces
and what the kernel needs. PostgreSQL's `shared_buffers` stays modest —
128 MB by default — because it leans on the OS page cache (entry 4),
while InnoDB's pool is expected to hold the working set itself. And the
multiplier: 200 connections × 8 sort or hash nodes × 4 MB of `work_mem`
is another 6 GB that never appears in "buffer pool size".

| Policy | Idea | Fixes |
|---|---|---|
| LRU | evict least recently used | baseline; destroyed by scans |
| CLOCK | one reference bit, circular sweep | LRU at a fraction of the cost |
| 2Q | separate recency and frequency queues | single-touch pages never age in |
| ARC | adapt between recency and frequency | varying workloads, self-tuning |
| LIRS | stack distance, inter-reference recency | scans and looping references |

The one metric that matters is the hit rate: `1 - (reads from disk /
logical reads)`. Above 99% the pool is doing its job; at 95%, a system
taking 10,000 logical reads per second issues 500 real device reads per
second, and on an HDD those alone consume the whole query budget.

**Where it breaks.** The failure is a phase change, not a slope: once
the working set exceeds the pool, hit rate collapses and throughput
drops to the device's IOPS. One sequential report can evict the hot set
and leave it cold for minutes. An aggregate hit rate hides a per-table
disaster — the popular table at 60% while the average says 99.9%.
Undersized pools hide behind the page cache until the day that cache is
cold, and oversized pools push the box into swap or the OOM killer.

**In practice.** Knobs: pool size and its sharding
(`innodb_buffer_pool_instances` spreads latch contention), a realistic
`effective_cache_size`, and `work_mem` sized as *nodes × connections ×
workers*. Metrics: InnoDB's hit rate, PostgreSQL's block hit ratios in
`pg_statio_user_tables`, and `n_dead_tup`, because bloat inflates the
"logical reads" denominator (entry 12). In Go, iterate `sql.Rows`
rather than slicing a million rows into memory, and always `Close()`.

---

## 4. The OS page cache and the double-buffering problem ★

**Why.** The operating system caches file pages whether or not the
database asked it to. Ignore that and the same 16 KB of data sits in
two caches with two eviction policies that cannot see each other —
wasted RAM, confusing accounting, and two halves of the machine
disagreeing about what is hot.

**How.** Without `O_DIRECT`, every `read()` of a data file is satisfied
from the page cache when possible. A database with its own pool
therefore holds the page in the page cache *and* a private copy: the
memory is paid twice, and the two policies can fight — the pool evicts
a page the kernel still keeps, so the next "pool miss" becomes a cheap
page-cache hit and distorts what you think a miss costs.

```
write:   UPDATE → buffer pool frame marked dirty → WAL fsync → ack
kernel:  the same 16 KB file page also lives in the page cache
flush:   checkpoint → write() → page cache → writeback → device
cost:    16 GB pool + 16 GB cache for the same 16 GB → 32 GB of RAM
```

| Engine | Primary cache | File I/O | The governing knob |
|---|---|---|---|
| PostgreSQL | OS cache + `shared_buffers` | buffered reads/writes | `effective_cache_size` (advisory) |
| MySQL/InnoDB | its own buffer pool | `O_DIRECT` skips the cache | `innodb_flush_method` |
| MongoDB/WiredTiger | own cache + journal | buffered, journal fsynced | `wiredTigerCacheSizeGB` |
| Redis | none for data files | `write()` + policy fsync | `appendfsync everysec` |

PostgreSQL is the case worth studying: core does not use `O_DIRECT` at
all, so it keeps `shared_buffers` small (128 MB by default) and leans on
the page cache, while `effective_cache_size` — 4 GB by default — tells
the planner how much cache to *assume* when costing a read, a hint
rather than a reservation. InnoDB goes the other way: with
`innodb_flush_method=O_DIRECT` aligned I/O skips the page cache, so
double buffering — which wastes RAM but buys simplicity — is avoided.

The accounting confusion is real. `free -h` reports cache as "used"
until you subtract it, so a healthy box reads as 95% memory while the
truth is reclaimable file cache. And the asymmetry under pressure: page
cache pages are dropped first, cheaply, but a buffer pool is anonymous
memory — if it was sized on an optimistic reading of "available", the
kernel escalates to swap or the OOM killer.

**Where it breaks.** Over-allocation in both directions: a pool sized
as if page cache did not exist, plus a cache the kernel happily fills,
equals swap. Monitoring that conflates RSS, cached and buffer pool size
sends people chasing a leak that is a cache. `O_DIRECT` on a misaligned
buffer or unsupported filesystem fails with `EINVAL` at startup. A data
directory on `tmpfs` or an overlay means the "disk" is memory that
vanishes on reboot; network filesystems may fail where local ones work.

**In practice.** Read the real picture: `/proc/meminfo` for `Cached`,
`Buffers`, `Dirty` and `Writeback`, `vmstat` for cache and swap, and
`free -h` with the buffers/cache line subtracted by hand. Set
`vm.swappiness` with intent — the kernel default of 60 suits a general
box, not one where the database owns the memory. Choose
`innodb_flush_method` explicitly, and know which copy of each page your
hit-rate metrics are counting. Go does not expose any of this, but it
competes for it: a backup job streaming data files with plain `os.Open`
floods the page cache with pages the server will never re-read — and
`runtime.ReadMemStats` will not show you that.

---

## 5. mmap: the tempting shortcut ★

**Why.** Mapping a data file and dereferencing it removes the whole
read path: no buffers, no read loop, no copy, and every process mapping
the file shares one physical copy. It is the shortest distance between
"the data is on disk" and "the data is in a pointer" — and every part of
that sentence has a hazard attached.

**How.** `mmap` maps a file into the address space. The first touch of
an unmapped byte traps: a *minor* fault if the page is in memory but not
yet mapped into this process, a *major* fault if the disk must be
consulted, with the kernel's readahead heuristics running on the way.
That is correct for a file viewer and wrong for a query engine that
knows the next B-tree leaf before the hardware does.

The durability problem is the serious one. A writable shared mapping
holds dirty pages the kernel may write back at *any* moment, governed by
`dirty_background_ratio` and the flusher threads, not by your commit
logic — which breaks the one rule databases live by: WAL before data
(entry 7). The only escapes are to force ordering yourself (`msync` the
log first, where `msync(MS_SYNC)` is a full `fsync` by another name) or
to keep the durable write path out of mapped memory entirely.

| Property | mmap | pread-style I/O |
|---|---|---|
| Read-side code | none — faults do it | explicit buffer management |
| Who chooses readahead | kernel heuristics | the planner, which knows more |
| Dirty writeback timing | kernel, whenever | engine, ordered and logged |
| Failure surface | `SIGBUS` mid-read | `errno` from a call |
| Sharing across processes | free, coherent | separate caches per process |
| Huge mappings | page-table memory, TLB pressure | bounded, pinned buffers |

Who uses it: LMDB is *built* on a pure mmap of its B-tree, with
copy-on-write pages and no buffer pool; WiredTiger historically mapped
file ranges for reads; RocksDB maps some read paths. Who refuses:
InnoDB and PostgreSQL, both of which issue `pread`-style calls into
buffers they own — because they need ordering, timeout control and a
failure mode that is an error return rather than a signal. The rule of
thumb: mmap for reads you control and can afford to retry; never for
the durability-critical write path.

**Where it breaks.** A truncated or unwritable file turns into `SIGBUS`
in code that was only "reading": an `ENOSPC` during writeback of a
shared mapping surfaces at fault time, not at `write` time, and a signal
handler is a poor error path. Page tables for a mapping cost about 0.2%
of its size in resident memory — a 100 GB map needs on the order of 200
MB before a single data byte is read. Under pressure the pages evict and
the next access re-faults, a double miss if the pool dropped its copy
too. Fault latency is booked as page-fault time, so slowness shows up in
`majflt` counters rather than in any I/O metric you were watching.

**In practice.** Measure before blaming: `perf stat -e
minor-faults,major-faults`, `/proc/<pid>/status` for `VmPTE`, and
`pidstat -r` for per-process fault rates. In Go the stdlib offers
nothing: mmap lives in `golang.org/x/sys/unix` as `unix.Mmap` with
`unix.Madvise` hints, and it is unmanaged — no finalizer, so `Munmap` is
your job, and a `SIGBUS` on a truncated mapping kills the process
instead of returning an error. Your driver hides all of this, but the
fault costs land in query latency, which is what a `context` deadline is
there to bound.

---

## 6. Disk: HDD, SSD, NVMe and what each one rewards

**Why.** Three device families with cost functions a thousand times
apart sit behind the same `write()` call. An index layout, a page size
or a checkpoint strategy tuned for one punishes the other, and the same
SQL can be fast or hopeless depending on which row of the table it
falls into.

**How.** Compare what each device charges for the two operations a
database issues — a random small read and a sustained sequential write:

| Device | Random read | Random IOPS | Sequential | Queueing model |
|---|---|---|---|---|
| HDD (7.2-15k rpm) | 5-10 ms seek | ~100-200 | 100-200 MB/s | one command, one head |
| SATA SSD | ~50-100 µs | 10,000+ | 500-550 MB/s | one queue (AHCI, depth 31) |
| NVMe SSD | ~10-25 µs | 100,000+ | several GB/s | many queues, one per core |

The sequential-versus-random gap is enormous on a disk — the seek *is*
the cost, so a 16 KB random write costs nearly what a 1 MB sequential
one does — and small on an SSD, where the internal geometry still
rewards large contiguous writes. AHCI was designed for one queue behind
a lock; NVMe exposes up to 64k submission queues with per-core MSI-X
interrupts, so a deep queue no longer serialises every core through one
spinlock.

Durability has its own axis. Consumer drives carry volatile write
caches and may acknowledge a write before it reaches media unless the
`FLUSH CACHE` command is honoured and completed honestly. Data-centre
drives add power-loss protection — capacitors that finish in-flight
writes when the rail dies — and answer flushes in microseconds to a few
milliseconds. A drive that fakes the flush turns every `fsync` contract
above it into theatre (entry 8).

Flash has geometry too: writes land in pages of roughly 4-16 KB, but
erase happens in blocks of 256 KB-4 MB, so an in-place 16 KB update
forces the controller to read the old block, erase it and rewrite the
live remainder — write amplification. Over-provisioning gives the
controller slack, TRIM tells it which pages are dead, and endurance is
rated in TBW: a 600 TBW drive at 100 GB written per day lasts about
sixteen years, which is why write volume is a hardware budget. It is
also why log-structured designs (entries 7 and 10) fit flash so well —
they turn random overwrites into sequential appends.

**Where it breaks.** Benchmarks lie in three specific ways: without
`fsync` they measure the volatile cache; a burst test on a fresh drive
measures the SLC buffer and not steady state; a test that never
triggers garbage collection hides the cliff that arrives months later.
On hardware: an HDD array rebuilt with random I/O takes days, an NVMe
card thermally throttles and halves its throughput, and queue depth 1
on a device built for thousands of outstanding requests spends none of
the latency budget while `%util` still reads 100.

**In practice.** `iostat -x 1` is the instrument: `r/s` and `w/s` for
volume, `r_await`/`w_await` for latency, `aqu-sz` for queue depth, and
`%util` read with care — on a multi-queued NVMe it saturates long before
the device does. Benchmark with `fio` at queue depth 1 (latency) and at
realistic depth (throughput), with `fsync` on and after the drive is
steady. In Go, `os.File.Sync()` is a real `fsync`, so a tool writing
WAL-like files must call it before acknowledging, and neither a
deadline nor a cancellation can un-issue a flush already in flight.

---

## 7. WAL: write-ahead logging turns random I/O into one sequential fsync ★

**Why.** Durability the naive way means flushing every modified page
before acknowledging — N random writes, N flushes, N device latencies
per transaction, and a commit rate capped at the inverse of a disk seek.
The need is one rule that converts all of that into a single sequential
append plus one flush shared by many commits.

**How.** The rule is one sentence: before a page is written to its home
location, the log record describing that change must already be on
stable storage. Records are *physiological* — page identity and offset
physical, operation described logically — so a record for a 16 KB page
is tens of bytes, except after a checkpoint forces full-page images.

```
commit:     UPDATE → latch page → append WAL record → fsync tail → ack
group:      waiter 1 begins flush → waiters 2..N join → one fsync → N acks
checkpoint: dirty page table saved → redo starts here → replay stays bounded
```

Group commit is the central commit-throughput trick and it falls out of
the same arithmetic: if a flush costs 1 ms and ten commits arrive within
that millisecond, they share one `fsync` and each pays 0.1 ms. Engines
make it deliberate — `commit_delay` waits for company — so commit rate
becomes "flushes per second × group size".

Recovery is what makes the rest affordable. ARIES-style managers run
*steal* (dirty pages may be written before commit, safe because the log
has them) and *no-force* (commit writes no data pages), then replay:
**analysis** rebuilds the dirty-page table from the last checkpoint,
**redo** repeats every logged change idempotently by page LSN, **undo**
rolls back transactions that never committed. Data-page writes happen
at checkpoints, never in the commit path; checkpoints bound replay.

| Mechanism | What it buys | What it costs |
|---|---|---|
| Log before page | one flush instead of N | a log write on every change |
| Group commit | flush cost split across waiters | a little added latency each |
| Steal / no-force | lazy data writes, small pools | full ARIES recovery machinery |
| Checkpoint | bounded recovery time | periodic burst of page writes |
| Physiological records | tiny log entries | replay must track slots |

This repo has a working sketch of that framing in
[sdesign](../sdesign/README.md): a length-prefixed, CRC-checked append
file whose torn tail is truncated on recovery.

**Where it breaks.** A full log buffer blocks writers outright:
`WALWrite` wait events mean the log writer, not the query, is the
bottleneck, and putting the log on the same spindle as the data destroys
the sequential advantage it exists for. Checkpoints arrive as an I/O
burst that stalls everything. Durability settings get weakened to buy
throughput and never revisited — `innodb_flush_log_at_trx_commit=2` and
`synchronous_commit=off` both lose the last second on a crash. A
standby that cannot apply WAL fast enough turns log volume into
replication lag.

**In practice.** Knobs: `wal_buffers`, `max_wal_size`,
`checkpoint_completion_target`, `commit_delay`/`commit_siblings`,
`synchronous_commit`, and InnoDB's `innodb_flush_log_at_trx_commit=1`,
the honest setting. Metrics: WAL bytes/s, commit p99, log-wait time.
In Go, `sql.Tx.Commit()` returns only after the flush completes, so
its duration *is* your durability cost — batch statements into one
commit to run your own group commit, and a cancelled `context` leaves
you checking the error: the commit may already have landed.

---

## 8. fsync and the durability contract ★

**Why.** `write()` returning means the kernel accepted the bytes, not
that they survived a power cut. Applications, replication and backups
all assume an acknowledged commit cannot be unmade, so the need is a
call whose completion means "on stable storage" — and an honest account
of everything that must happen first.

**How.** An `fsync` crosses three layers: the VFS hands the inode to
the filesystem, which commits its own journal so the *mapping* of the
data is durable; then the block layer issues a flush to the device,
which must drain its volatile write cache to media; only the completion
of that device flush makes the call meaningful. Miss any layer and you
have a durable-looking write that a crash can still eat.

```
fsync:   syscall → VFS → journal commit → device FLUSH → return
write:   write() returns → bytes sit in page cache → power cut → gone
replace: write temp → fsync file → rename → fsync directory → crash-safe
```

| Media | fsync latency | Why it costs that |
|---|---|---|
| Data-centre SSD with PLP | ~0.1-1 ms | capacitors finish the drain in µs |
| Consumer NVMe, flush honoured | ~1-10 ms and up | volatile cache drained to NAND |
| Consumer drive, flush ignored | ~0 ms — a lie | data still sitting in RAM |
| HDD with FLUSH CACHE | ~5-20 ms | platter rotation plus cache drain |
| Network filesystem | tens to hundreds of ms | round trip and a remote flush |

Because one flush is that expensive, engines batch: a group of waiters
shares a single device flush (entry 7), and `commit_delay` waits for
company before paying. Filesystem policy is the other half. On ext4 the
default `data=ordered` mode forces data blocks to disk before the
metadata transaction that references them commits, so a file never
points at garbage; `data=writeback` drops that for speed. Delayed
allocation means `ENOSPC` surfaces *later*, during writeback or the
final `fsync` — and it is why the crash-safe replace ends with an
`fsync` of the file **and then of the directory**: create, write, sync,
rename, sync the directory, or a crash leaves the old file (or none).

**Where it breaks.** The silent one: a drive whose flush is a no-op, so
the filesystem believes data is on media while it is in the drive's RAM
— every layer is correct and the data is lost anyway. The ordering one:
a rename acknowledged without the directory fsync, so the crash-safe
replace is neither. The late one: `ENOSPC` at writeback after `write()`
returned success. Common too: `fsync`ing every small file in a
directory during backups (metadata-bound and slow), assuming
`synchronous_commit=off` is durable, and trusting a filesystem or FUSE
layer that does not implement fsync faithfully.

**In practice.** The only test that matters is the power-cut test:
pull the plug mid-write and read the result — `dm-flakey` and
file-level crash tests approximate it, simulation does not. Measure with
`strace -T -e fsync,fdatasync` for per-call time and `blktrace` to see
whether FLUSH and FUA commands are actually issued; keep ext4's
`data=ordered` unless you have a reason. In Go the pattern is concrete:
write the file, `f.Sync()`, `f.Close()`, `os.Rename`, then open the
directory and `Sync()` it — four calls that together are the contract —
and a `context` deadline firing does not cancel an `fsync` already in
flight.

---

## 9. B-trees: the structure that matches the hardware ★

**Why.** Billions of rows, point lookups and range scans, updates in
place — all in a handful of memory references and at most one trip to
storage. No other structure balances those constraints against
page-sized I/O, latching and cache residency, which is why it is still
the default forty years on.

**How.** A node *is* a page: 16 KB in InnoDB, 8 KB in PostgreSQL. The
size is chosen so one node is one useful unit of I/O (16 KB is exactly
four 4 KB OS pages, fetched in a single read) yet small enough to latch,
copy and evict at page granularity. Fanout follows: a 16 KB node with
short keys holds on the order of a thousand child pointers, so the tree
gets very wide and very shallow.

| Height (root→leaf) | Rows at fanout ~1000 | Nodes above the leaves | 16 KB node memory |
|---|---|---|---|
| 2 | ~10^6 | ~1,000 | ~16 MB |
| 3 | ~10^9 | ~10^6 | ~16 GB |
| 4 | ~10^12 | ~10^9 | ~16 TB |

Read that with the hot set in mind: the root and its ~1000 children are
about 16 MB and never leave memory, so a lookup descends pinned nodes in
RAM and pays, at worst, one cold leaf read — while heights of three and
four comfortably reach billions and trillions of rows. The read path is
*latch crabbing*: pin the root, binary-search the node, take the child's
latch before releasing the parent's, and drop ancestor latches as soon
as the path is safe.

The write path is where the design pays off. Find the page, latch it,
append the WAL record, modify the frame in the buffer pool, release —
no disk write at commit, because no-force (entry 7) makes the log
sufficient. Splits happen when a page fills: two half-full pages and a
separator pushed into the parent, sometimes cascading to the root.
Sequential keys split rarely; random UUID keys split constantly and
leave pages half empty. B+ leaves are linked so a range scan walks
siblings instead of re-descending.

Clustered versus secondary decides how many I/Os a lookup costs. InnoDB
stores whole rows in the primary key tree, so the PK read *is* the row
read; its secondary indexes carry the primary key, so an uncovered
lookup costs two tree walks. On PostgreSQL every index is secondary:
index entry → heap pointer → heap page, two reads unless it covers.

**Where it breaks.** A cold cache turns every point read into a device
read — and a secondary lookup into two, which is why a "simple lookup"
on an HDD costs 10-20 ms. The rightmost leaf is a latch hot spot under
concurrent inserts, showing up as buffer-page wait events rather than
as CPU. Random insert keys cause split storms and leave the tree
fragmented at half fill. Torn pages corrupt a node silently unless
checksums are on, and both InnoDB's and PostgreSQL's must be *enabled*.
Secondary indexes that silently include a heap fetch make "indexed"
queries look inexplicably expensive.

**In practice.** Knobs: `fillfactor` on PostgreSQL and
`innodb_fill_factor` on InnoDB to leave split room; checksums on; pool
size so the upper levels and hot leaves stay resident; a monotonic
component in generated identifiers to avoid right-edge contention.
Metrics: buffer pool hit rate, latch/page-wait time, index fill ratio
and fragmentation (`pgstattuple`), and `pg_stat_user_indexes.idx_scan`
to find indexes earning nothing. In Go, `EXPLAIN` belongs in review for
any hot query, and a `sql.Tx` should never hold row locks across an
HTTP call (entry 12).

---

## 10. LSM-trees: trading read speed for write speed ★

**Why.** An insert into a B-tree lands wherever the key says — a seek,
a page latch, a split, a possibly-random write. Under sustained ingest
that is the wrong shape for flash. The need is a write path that is
purely sequential, with the cost paid where the design can control it.

**How.** Writes go to an in-memory sorted structure — a skip list
*memtable* — after being appended to the WAL for durability, so the
acknowledged write touched nothing random at all. When the memtable
fills it is frozen and flushed to disk as a sorted, immutable SSTable
carrying a sparse block index and a bloom filter. Reads consult the
memtable, then the overlapping files in level 0, then one level at a
time downward, using the index to skip to a block and the bloom filter
to avoid touching most files at all.

```
write:  PUT → WAL append → memtable → ack, no random I/O
flush:  memtable full → sorted SSTable → L0 → compaction rolls it down
read:   GET → memtable → L0 blooms → L1 bloom + index → L2 → Ln
```

Compaction is the engine room: background merges pull runs down through
levels where each level is roughly an order of magnitude larger than
the one above and its files do not overlap, so a point read touches at
most one file per level. Leveled compaction (RocksDB's default) buys
fast reads and modest space at the price of rewriting data as it
descends; tiered compaction (Cassandra's traditional choice) rewrites
far less but keeps overlapping runs, so reads must consult more files.

| Amplification | Cause | What reduces it |
|---|---|---|
| Read | memtable + L0..Ln + false positives | bloom filters (~10 bits/key ≈ 1%), block cache |
| Space | tombstones and stale versions | prompt compaction, TTLs, partitioning |
| Write | every byte rewritten per level traversed | tiered runs, rate limits, fewer levels |

**Where it breaks.** Compaction debt is the production failure: ingest
outpaces background merging, L0 file count climbs, reads slow because
more files must be probed, and the engine eventually throttles or
stalls writes outright — a cliff that arrives months after launch when
data volume finally outruns provisioned compaction throughput. Space
amplification can fill the disk, and then compaction cannot run because
the disk is full, and then writes stop: a deadlock with no operator
move but deleting data. Tombstones compacted away too early let deleted
rows resurface from older runs. Even in steady state a cold point read
may probe several levels, so tail latency is noisier than a B-tree's.

**In practice.** Watch the debt, not the latency: RocksDB's
`rocksdb.num-files-at-level0`, `pending-compaction-bytes` and stall
counters; Cassandra's pending compactions; each engine's reported read
and space amplification. Tune bloom bits per key, block cache size (the
LSM's answer to the buffer pool), level count and target file size, and
rate-limit compaction so it cannot starve foreground I/O. In Go the
engines are real and reachable: Badger and Pebble expose these exact
levers — `WriteOptions.Sync`, batched writes, compaction triggers, a
metrics snapshot — so *your* batch size and sync policy decide both
durability and compaction load.

---

## 11. Transactions: WAL for atomicity, MVCC for readers

**Why.** Many sessions read and write at once, and "it looked
consistent when I read it" has to be a guarantee rather than a hope. The
need is an isolation level defined as a *set of anomalies* — these are
prevented, those are permitted — plus a durability term meaning exactly
what the fsync contract says and nothing more.

**How.** Take ACID literally. **Atomicity** comes from the WAL: undo
records let recovery roll back losers (entry 7). **Consistency** is
yours — the engine enforces declared constraints and nothing about your
invariants. **Isolation** is a menu of anomalies, not a vibe.
**Durability** means an acknowledged commit survives a crash, which is
precisely the storage's fsync contract (entry 8) — no stronger.

Concurrency itself has two classical answers. Strict two-phase locking
holds write locks to commit and takes read locks too, so readers block
writers and writers block readers. MVCC instead keeps old versions: a
row carries the transaction ids that created and deleted it (`xmin`/
`xmax` on PostgreSQL, `trx_id` plus an undo pointer on InnoDB), and a
reader's *snapshot* — the in-flight transactions, or a low-water mark
from one — decides which version is visible. The cost is housekeeping:
somebody must eventually remove the old versions (entry 12).

```
begin:  BEGIN → snapshot {xmin, xip} → reads see only versions visible then
write:  UPDATE → new version recorded → row lock held until COMMIT
commit: COMMIT → WAL record → fsync → advance xmin → release locks
```

| Level | Dirty read | Non-repeatable | Phantom | Write skew |
|---|---|---|---|---|
| Read uncommitted | possible | possible | possible | possible |
| Read committed | forbidden | possible | possible | possible |
| Repeatable read | forbidden | forbidden | prevented | possible |
| Serializable | forbidden | forbidden | forbidden | prevented |

The fine print matters. PostgreSQL's repeatable read is snapshot
isolation: no phantoms appear inside the snapshot, but write skew — two
transactions reading disjoint rows and writing conflicting ones — slips
through, which is why real serializability needs SSI, which tracks
read-write conflicts and aborts one side. MySQL's repeatable read
closes the phantom hole with gap and next-key locks, a classic source
of deadlocks. And locks do not vanish: deadlocks are found by building
a wait-for graph, spotting the cycle and picking the youngest victim.

**Where it breaks.** Lost updates: read-modify-write under read
committed without `SELECT FOR UPDATE` lets two sessions compute from
the same snapshot. Deadlock storms when two paths take locks in
opposite order, made worse if the application retries on top. A
repeatable-read transaction running for minutes reads a world that no
longer exists — correct, and surprising. And long transactions block
purge and vacuum (entry 12): the classic killer is a session that
begins, makes an HTTP call, and lets the snapshot age behind it.

**In practice.** Know your level and write for its anomaly set, use
explicit locking for read-modify-write, and keep transactions short.
Metrics: deadlock counts, lock wait time, and the age of the oldest
snapshot. In Go, `BeginTx(ctx, &sql.TxOptions{...})` sets isolation
declaratively instead of racing a `SET` statement — and **a cancelled
`context` does not roll the transaction back**: it stops waiting while
the Tx and its connection stay held until you `defer tx.Rollback()`
right after `Begin`.

---

## 12. MVCC housekeeping: why databases bloat

**Why.** Old versions must survive as long as some open snapshot can
see them — and must die the moment no one can, because a table whose
visible content is flat while its file size doubles is not growth, it
is garbage. The need is a reaper that runs continuously, keeps up with
the write rate, and is watched like any other capacity limit.

**How.** Each engine names the same job differently: PostgreSQL's
**vacuum** marks dead tuple slots reusable and updates the visibility
and free-space maps; **autovacuum** runs it in background workers,
triggered after 50 dead rows *plus* 20% of the table — too late for
big tables. InnoDB's **purge** reclaims undo history; an LSM's
**compaction** removes stale versions as it merges: the same chore.

The snapshot is what keeps old rows alive. A transaction that began
last Tuesday pins the visibility horizon: everything deleted since then
must be retained so its snapshot still reads coherently, and bloat
grows as *write rate × age of the oldest snapshot*. That is why one
idle-in-transaction connection — a session-pooled pooler, a forgotten
`BEGIN` — is the most common way a healthy database quietly doubles.

The damage then propagates in a chain worth internalising: bloat forces
sequential scans over garbage, which touches more pages than the query
needs, which displaces the hot set from the pool, which lowers the hit
rate, which churns the page cache and burns CPU in `%sys` copying
tuples nobody will ever read. One pinned snapshot degrades the machine.

PostgreSQL's transaction ids are 32-bit, which adds a hard floor:
roughly 2.1 billion transactions later the counter wraps and old rows
would look like they are from the future. `VACUUM FREEZE` rewrites rows
so they belong to no epoch at all; autovacuum starts freezing
aggressively at `autovacuum_freeze_max_age` (200 million by default),
and if it cannot keep up the database refuses writes rather than lose
data — a designed failure that still takes down production.

| Symptom | Metric | Source |
|---|---|---|
| Dead tuples piling up | `n_dead_tup`, `pg_stat_user_tables` | PostgreSQL |
| Vacuum losing the race | autovacuum workers busy, xact age | PostgreSQL |
| Snapshot pinned | `pg_stat_activity.xact_start`, oldest xmin | PostgreSQL |
| Undo growth | InnoDB history list length | InnoDB |
| Actual bloat | `pgstattuple` dead-tuple fraction | PostgreSQL |
| Wraparound risk | age of `datfrozenxid` per database | PostgreSQL |

**Where it breaks.** The idle-in-transaction session, at the top of the
list every time. Autovacuum switched off "for performance", or starved
because the box has no spare CPU or I/O headroom — it competes with
your queries for the same cgroup. A vacuum that cannot match the write
rate, so the table grows while the work grows with it. Index bloat,
which ordinary vacuum does not fix. And the endgame: wraparound-driven
refusal of writes with data loss warnings on the screen.

**In practice.** Tune the trigger for your table sizes — a lower
`autovacuum_vacuum_scale_factor` (0.01 on a large table) plus a raised
`autovacuum_vacuum_cost_limit` keeps it ahead. On InnoDB:
`innodb_purge_threads`, `innodb_max_purge_delay`, and history list
length. Put a pooler in *transaction* mode (entry 15) so
idle-in-transaction cannot exist. Alert on oldest-snapshot age,
dead-tuple counts and frozen-xid age rather than on disk usage, which
lags. In Go: never hold a `sql.Tx` across an HTTP call, `defer
tx.Rollback()` the instant after `Begin`, `Close()` every `sql.Rows`,
and set `ConnMaxIdleTime` so idle backends do not accumulate.

---

## 13. Indexes: paid on every write, spent on reads

**Why.** Scanning a billion rows to find forty is not a query, it is a
batch job. But an index is a second copy of a column in sorted form,
and every write must maintain every copy — so the need is indexes that
earn their maintenance cost on the reads they actually serve.

**How.** Each index is another B-tree: one more search, one more latch,
one more page modification and one more WAL record per write, plus
occasional splits. A table with five indexes turns a single `INSERT` in
six page modifications and roughly six times the log volume — and each
index occupies buffer pool and page cache the table itself wanted
(entry 4), so "add an index" spends memory as well as write bandwidth.

Column order is a tree-order question: entries sort lexicographically,
so a predicate can *seek* only as deep as its constrained prefix allows.

| Predicate | Index on (a, b, c) | Why |
|---|---|---|
| `a = ?` | seeks | leading column constrains the tree |
| `a = ? AND b = ?` | seeks deeper | order preserved down the prefix |
| `a = ? AND b > ?` | seeks then scans | range starts at (a, b) |
| `b = ?` | cannot seek | not the leading column |
| `a IN (?, ?)` | two seeks | each value is an independent seek |

That is equality-before-range in one table: `=` columns first, then the
column used with a range, because the range kills ordering for
everything after it. A *covering* index — containing every column the
query needs — skips the heap fetch entirely: one random read instead of
two. MVCC adds a wrinkle: an index-only scan on PostgreSQL must still
visit the heap to check visibility unless the page is marked
all-visible in the visibility map, so the first scan after a heavy
write wave pays for heap fetches and later ones do not.

Variants exist for narrower cases: hash indexes answer equality only
and have no ordering — and on PostgreSQL they were **not WAL-logged
before version 10**, meaning a crash could require `REINDEX`; since
version 10 they are crash-safe. Partial and expression indexes cover
narrower predicates, smaller and cheaper to maintain. Finally the
planner decides: it compares the index path (random page cost) against
a sequential scan, crossing over around 10-30% of the table.

**Where it breaks.** The write-heavy table carrying five indexes that
none of its queries use — every insert pays, nobody benefits, and WAL
volume rises with it. The wrong column order, which looks right in the
schema and cannot seek. A function or implicit cast on the indexed
column (`WHERE lower(email) = $1`) silently defeats the index. Stale
statistics send the planner the other way, and index bloat — invisible
to table vacuum — makes index scans slower while the plan insists they
are cheap.

**In practice.** Find indexes that earn nothing:
`pg_stat_user_indexes.idx_scan` of zero, or `sys.schema_unused_indexes`
on MySQL; check `EXPLAIN (ANALYZE, BUFFERS)` for estimated-vs-actual
row counts; watch index size and `pgstattuple` for bloat. Keep
statistics fresh with `ANALYZE`, and lower `random_page_cost` (default
4 against `seq_page_cost` of 1) on flash. In Go, build predicates with
bound parameters — `db.QueryContext` with `$1`/`?` placeholders lets
the plan be parsed once and reused, while concatenated SQL replans
every time.

---

## 14. Query execution: memory budgets and spilling

**Why.** Every operator in a plan is a memory claim, and no single
operator knows how many of its siblings are running on other
connections. Without a per-operation budget and a defined behaviour when
it is exceeded, twenty concurrent joins turn a database into the OOM
killer's favourite target.

**How.** The classical executor is the Volcano iterator: each operator
implements `Open`, `Next`, `Close`, and the parent pulls one row at a
time — one dispatch and one branch per row, a measurable share of CPU
at tens of millions of rows. Vectorized engines answer with batches: a
thousand rows per `Next`, straight-line loops SIMD can act on.

| Operator | Memory used | When it exceeds the budget |
|---|---|---|
| Sort | `work_mem` (or a top-N heap) | external merge sort via temp files |
| Hash join (build) | `work_mem` for the smaller side | partitions spill to temp files |
| Hash aggregate | `work_mem` for the hash table | spills similarly |
| Nested loop | ~nothing | time explodes instead of memory |
| Window function | `work_mem` over its partition | sorts, then spills like ORDER BY |

The trap is in the units. PostgreSQL's `work_mem` is per sort *or* hash
node *per execution*, and one query can contain many nodes: eight nodes
× 32 concurrent connections × 256 MB is 64 GB of theoretical demand
from a setting that sounds small. MySQL's `sort_buffer_size`,
`join_buffer_size` and `read_buffer_size` are allocated per use for the
same reason. A sort that fits in memory is quick; one that spills
becomes run files written and read back, merged k-way at the end.

```
hash join: build side → work_mem cap → overflow partitions → temp files
sort:      input rows → top-N heap? → overflow → run files → k-way merge
```

Spilling has consequences beyond the query. Temp files are ordinary
writes, so they land in the OS page cache (entry 4) and evict file
pages the database was relying on; their I/O shares the device with the
WAL and checkpoints, so a report that spills competes with every commit
for the same queue. Parallel query then adds workers, a shared tuple
queue and coordination on top (entry 2).

**Where it breaks.** The OOM case: many connections, each with several
spilling operators, each inside `work_mem` × nodes — the sum exceeds
RAM and the kernel picks a victim. The latency case: a spill lands
behind a checkpoint burst, so a report's tail latency appears in
*commit* p99 and nobody connects the two. The plan case: stale
statistics produce tiny estimates, a hash join builds on what turns out
to be the huge side, and the "cheap" plan spills three times. And the
nested-loop accident: an inner side with no index turns 10,000 × 10,000
into 100 million probes at 100% CPU.

**In practice.** Measure rather than guess: `pg_stat_database.temp_bytes`
and `pg_stat_statements.temp_blks_written` say who is spilling;
`EXPLAIN (ANALYZE, BUFFERS, SETTINGS)` shows per-node actual rows
against estimates; `iostat` shows `iowait` while `perf` shows where CPU
went — that split tells IO-bound from CPU-bound. Size `work_mem` as
*nodes × connections × workers* and cap concurrency with
`SetMaxOpenConns` so only N queries can spill at once. In Go, stream
with `rows.Next()` into a bounded slice instead of accumulating a
million structs — a spill you cause in your own heap — and put a
`context` deadline on the query so a runaway plan returns.

---

## 15. Connections, pooling and the scheduler

**Why.** A connection is not a socket: it is a backend process or
thread, a stack, memory contexts, a scheduler entry and a snapshot. Set
the limit by gut feel and you either starve a four-core box with five
hundred backends or refuse clients while the CPU idles — the need is a
bound chosen from memory, cores and queueing theory together.

**How.** PostgreSQL forks a process per connection and MySQL spawns a
thread, each carrying private memory in megabytes plus buffers
allocated on use. Five hundred connections on a four-core machine is
125 runnable entities per core: context switches and cache/TLB
pollution eat the cores, the run queue becomes a waiting line, and
throughput falls while CPU looks busier.

The kernel has queues in front of all of it. The accept backlog is
bounded by `net.core.somaxconn` (128 on older kernels, 4096+ on modern
ones), the SYN queue by `tcp_max_syn_backlog`; overflow drops the third
handshake and the client sees a timeout. Every restart produces a
connection storm into a server not yet ready, so bounded pools with
jittered backoff matter as much as the database's own limit.

Pooling then happens in tiers, because what a pooled connection may
carry depends on when it is handed back:

| Tier | Server connection held for | Named prepared statements | Session state (`SET`) |
|---|---|---|---|
| Session | the whole client session | safe | safe |
| Transaction | until COMMIT/ROLLBACK | not safe | not safe |
| Statement | a single statement | not safe | not safe |

Transaction pooling is what lets 1,000 clients share 50 server
connections, and it is why prepared statements interact with it: a
*named* prepared statement lives on one specific server connection, so
the next checkout may not have it — drivers prepare unnamed or track it,
and a naive client gets "prepared statement does not exist". Session
state like `SET work_mem` leaks for the same reason.

Queueing theory supplies the one-liner that governs all of it:
response time scales as `1 / (1 − ρ)` in units of service time, so
at 50% utilisation you wait once the service time, at 90% nine.
Latency runs hyperbolic before saturation — extra conns only queue.

**Where it breaks.** The connection storm after a deploy, with the
backlog dropping SYNs and clients retrying in lockstep.
`max_connections` exhausted while the box is idle, because 500 backends
each hold a snapshot and a stack rather than doing work — and the
mirror failure, too few connections, so cores sit idle while requests
queue. Inside the application, pool exhaustion looks like a hang:
`database/sql` blocks until your `context` expires, and the timeout you
added masks the diagnosis. Transaction pooling plus named prepared
statements produces intermittent "statement does not exist".

**In practice.** Bound from both ends: `max_connections` to what memory
supports, the application's pool smaller than the pooler's, and the
pooler's smaller than the server's. Read `pg_stat_activity` for session
counts and `SHOW POOLS` on the pooler for waiting clients. In Go the
defaults are the trap: `SetMaxOpenConns(0)` means **unlimited open
connections**, while `SetMaxIdleConns` defaults to just **2** — set open
conns to roughly cores × 2, raise idle conns, add `SetConnMaxIdleTime`,
and watch `db.Stats()`, where a non-zero `WaitCount` means the pool,
not the database, is the queue. cgroup `cpu.max` and `io.max` decide
what the server may actually spend.

---

## 16. Backup taxonomy: full, incremental, differential — and the WAL archive

**Why.** Backups fail during restores, and most restore failures are
taxonomy failures: a chain of incrementals treated as if each link
were independent. The need is vocabulary that fixes dependency, space
cost and recovery time on paper before the incident, not during it.

**How.** Three kinds, told apart only by what they depend on. A **full**
is a complete copy of everything needed to stand the database up. An
**incremental** holds the changes since *any* prior backup, so a
restore needs the whole chain back to a full — every link, in order. A
**differential** holds the changes since the last **full**, so chain
depth is always two: full plus one differential however old it is.

| Kind | Depends on | Restore needs | Shape over a week |
|---|---|---|---|
| Full | nothing | itself | one big copy per run |
| Incremental | the previous backup | every link to a full | tiny, deepening chain |
| Differential | the last full | full + newest diff | grows until the next full |

The arithmetic picks the winner. At a 1 TB base with 5% daily change,
seven daily fulls cost 7 TB of storage and seven incrementals about
350 GB, while differentials cost the same total for two files to
restore. Incrementals minimise space and maximise risk; fulls buy the
shortest restore at the largest bill.

Then the insight that made nightly snapshots worth replacing: the WAL
is already append-only, sequential and written anyway (entry 7), so
copying each segment as it is switched out costs almost nothing — and
it converts a nightly snapshot into **point-in-time recovery**. A base
backup plus every archived segment since it restores to any moment
inside the archive, not just to 03:00.

Archiving happens on switch, not on schedule: when a 16 MB segment
fills, `archive_command` runs once and the copy must leave the machine
— same filesystem, same failure. Segment names carry time, timeline
and position (`00000002000000000000000A`), which is how a restore
finds the order of things.

**Where it breaks.** The broken chain: segment
`000000020000000000000004` was pruned by a tidy-up script, and the
restore stops at `...0003` with an unrecoverable hole. The silent gap:
replay halts at the first missing segment, so the PITR window ends
*there* and a target beyond it is quietly unreachable — nobody finds
out until someone asks for last Thursday. The full disk:
`archive_command` failing for days while `pg_wal` absorbs it, until
the server throttles and then refuses writes. And the mixed timeline:
a recovery that made a new timeline with the old one's segments still
in the directory.

**In practice.** Watch the archiver, not free space: `pg_stat_archiver`
reports `last_archived_time`, `last_failed_time` and `archived_count`
— alert when the gap from now to the last archived time passes a few
minutes, and on any climb in failures. `pg_archivecleanup` on a
scratch copy proves the chain is contiguous; `pg_controldata` after a
restore confirms system identifier, timeline and redo address. Make
`archive_command` exit non-zero on any doubt — a truncated copy that
"succeeds" is the gap you find at restore time.

---

## 17. Physical vs logical backups

**Why.** "Back up the database" hides a fork: one kind answers how
fast the same instance comes back, the other answers how these rows
get somewhere else entirely. The need is to choose by question —
restore speed, portability, scope — instead of by whichever tool sat
on the runbook when it was written.

**How.** A physical backup copies the files; a logical backup
re-derives the data as statements. They part company on every axis
that matters:

| Axis | Physical | Logical |
|---|---|---|
| What it is | file-level copy of the data directory | rows and schema as SQL |
| Tools | `pg_basebackup`, FS snapshot, hot copy | `pg_dump`, SQL export, `mysqldump` |
| Restore | copy back, replay WAL — minutes | parse and run DML — hours |
| Portability | that engine version, that build | across versions, OSes, engines |
| Scope | the whole instance | chosen schemas, tables, rows |
| Consistency | WAL bracket: start-LSN to end-LSN | one MVCC/export snapshot |

The consistency models differ in kind. A physical copy watches files
change under it, so it must be bracketed by log positions — the whole
story of entry 18. A logical export needs one instant for every table,
so it runs inside a consistent snapshot rather than trusting the
clock.

Same-version disaster recovery is physical's win: the fastest path
back to the same database is its own files. Version upgrades, moves to
another OS or engine, a subset for a reporting copy or a developer's
laptop — all logical, because a dump speaks SQL and SQL travels.

The hybrid is what most teams land on: a physical base for speed plus
a logical dump as insurance against physical-format lock-in. A file
format only one engine version can read has exactly one supplier, and
the dump is how you break the tie when that supplier changes terms.

**Where it breaks.** A logical dump taken mid-transaction with no
snapshot yields a dataset that does not add up — accounts balanced
against ledger rows from two different instants — and the job reported
success, so the fault surfaces downstream. A physical restore onto a
newer major version or an unfamiliar platform is unsupported: the
files are the engine's own format, version-bound by design, and they
look fine until they do not. A logical dump of a large instance that
outlives its window, then stalls restore while it takes locks.

**In practice.** Take both, on different schedules and different
storage: physical for the RTO, logical as the insurance policy.
Logical: `pg_dump -Fc -j 4`, `--section=pre-data` for a schema-only
rehearsal, `mysqldump --single-transaction --master-data=2` on InnoDB
to pin one snapshot. Physical: `pg_basebackup -X stream` so the WAL
travels with the copy. Rehearse into a side directory, then check row
counts and a checksum aggregate before calling a dump good.

---

## 18. Consistent backups of a running database ★

**Why.** Copying files out of a running database is a fuzzy copy: pages
change under the copier, so the image is a torn mix of before and
after states — a database-shaped object no transaction ever produced.
The need is a method that makes a moving target come back restorable
without stopping the world to get there.

**How.** There are five ways to freeze a moment, each trading
availability, I/O and precision against the others:

```
cold:    stop the engine → copy the files → start again
hot:     record start LSN → copy files → record end LSN → replay WAL to end
snap:    filesystem snapshot → copy → replay WAL from the snapshot's LSN
mvcc:    begin repeatable read → export snapshot → dump → commit
redo:    engine flushes buffers → marks redo position → backup ends
```

A cold copy is the only one that needs no reconciliation, and it bills
availability. A hot copy brackets the run with log positions: record
the start-LSN before the first byte is read, the end-LSN after the
last, and the restore is the copy plus WAL replayed through the end
LSN — redo is idempotent by page LSN (entry 7), so reconciliation
repairs the torn image.

Filesystem snapshots (ZFS, LVM, Btrfs) copy-on-write a moment for the
price of a metadata change; you still replay WAL from the snapshot's
LSN, or take the snapshot AT a checkpoint so redo work is minimal. An
MVCC export (`pg_export_snapshot`, a repeatable-read transaction)
freezes logical content instead of files. Engine-level tools do the
flush themselves: write buffers out, mark the redo position, stop.

The unifying rule: **a physical backup is only complete with the WAL
from before it started through after it ended.** Without the
before-part the copy misses pages written just before it began;
without the after-part the torn image is never reconciled. The PITR
path this builds is worked end to end in [DESIGNS](DESIGNS.md), and
the write path the WAL rides is [linux/DESIGNS](../linux/DESIGNS.md).

**Where it breaks.** Copying only data files and no WAL: the image
looks complete, then restores into an engine that refuses it or
corrupts silently. A snapshot taken between checkpoint and page flush
still needs redo — it holds the pages, not the intent. Torn pages
during the copy are expected; replay heals them only if the WAL
covered the window. An LVM thin snapshot that outgrows its
copy-on-write space and breaks halfway through. Replaying from too
late a start, so the image never reconciles — detected, if at all, by
a constraint that fires months later.

**In practice.** Let the tool bracket the copy: on PostgreSQL
`pg_basebackup -X stream --checkpoint=fast` streams the WAL along
with the files; on ZFS `zfs snapshot data@base` plus `zfs send`; on
LVM `lvcreate --size 10G --snapshot /dev/vg/data`. Take the base from
a replica so the primary pays no extra I/O, and take one often enough
that replay stays bounded — every 6-24 hours is a common range.
Verify each backup with `pg_checksums --validate` on the restored
copy plus a few smoke queries, and log the start/end LSN and timeline.

---

## 19. Recovery objectives: RPO, RTO and the point-in-time restore

**Why.** "We have backups" is not a promise, and two different
questions hide inside it: how much data the business can lose, and
how long it can stay down. The need is two numbers, written per
service before the incident, plus a strategy whose measured cost
actually reaches them.

**How.** **RPO** is data loss measured backwards in time — how much you
can afford to lose. A nightly full has an RPO of up to 24 hours; a
continuous archive has one of minutes. **RTO** is elapsed time from
the incident to serving again. The two pull against each other:
buying one usually spends the other.

| Strategy | RPO | RTO | Cost |
|---|---|---|---|
| Nightly full, no archive | up to 24 h | hours | cheapest |
| WAL continuous archive | one segment: minutes | restore + replay | archive space |
| Async replica failover | seconds unshipped | seconds | second node |
| Sync replica or cluster | ~0 | seconds | most expensive |

The arithmetic of RTO: pull the backup, replay WAL for N hours,
validate, cut over. Replay grows linearly with archive depth — twelve
hours of segments takes about twice the replay of six — which is why
frequent base backups, or a base taken from a replica, shorten
recovery more than a faster disk does. Backup age sets replay time,
and replay time is most of your RTO.

A point-in-time restore also needs a target, named three ways: by time
(wall clock — beware time zones and a clock stepped backwards by
NTP), by log position or byte offset (precise, engine-native), and by
transaction or named savepoint (the business event: everything
committed before the bad migration). Pick the precision you can
defend.

**Where it breaks.** A target that lands mid-transaction: the engine
stops at the last complete transaction before it — correct, and worth
documenting so nobody expects half of a batch. RTO blown because the
archive lives in cold storage: the restore starts on schedule and then
waits in a retrieval queue nobody measured. Restoring straight onto
the live data directory — it destroys the evidence and the rollback
in one move. A "15 minute RPO" that counts the backup job but not the
archive behind it.

**In practice.** Write both numbers per service, then time a real
restore and compare. Know the target knobs before you need them:
`recovery_target_time`, `recovery_target_lsn`, `recovery_target_name`
(PostgreSQL) or a position in the relay log (MySQL); async failover
semantics are [distributed](../distributed/README.md)'s territory.
Keep recent WAL on fast storage and age it to cold only after the
base it follows is durable, so an incident's first hour is not a
retrieval request. Track measured restore duration against RTO as a
metric: replay time, not backup time, sets the clock.

---

## 20. Replication is not a backup

**Why.** A replica feels like a backup because it is a second copy, and
it fails as one for a single reason: it shares the primary's mistakes
and the primary's fate. The need is a definition that separates a copy
which moves forward with production from an artifact you can go back
in time with.

**How.** Replication reproduces every mistake at wire speed: the
`DELETE` without `WHERE` commits, ships and lands on the standby in
seconds. Corruption, operator error, ransomware and an accidental
`DROP TABLE` reach a replica exactly as they reach the primary,
because a replica's job is to apply what it is given.

A backup differs in kind: an *independent artifact*, under retention
you control, that restores a state from before the mistake. A replica
cannot roll back a committed change at all — it can only be pointed at
a snapshot someone took, which is a backup wearing a replica's name.

```
backup:  artifact outside the blast radius → restore → any point, backwards
replica: live copy inside the blast radius → promote → seconds, same mistakes
archive: WAL since the base backup → replay → point in time, minutes or hours
```

Three properties separate the real thing: **independent** — a separate
failure domain *and* separate credentials; **restorable** — a restore
has actually been performed, on a schedule; **verified** — checksums
read back and data spot-checked, not just a green job. Anything short
of all three is a copy with an untested story attached.

Replication carries its own recovery failure modes: lag is a hidden
RPO — the standby is a time machine stuck on last minute — and
failover promotes whatever the primary had, including the corruption
you were hoping to outlive.

**Where it breaks.** The classic story: the primary is deleted,
replication faithfully mirrors the deletion, the "backup" was the
replica, and yesterday does not exist anywhere. A failover that
promotes a lagging standby, quietly losing the last seconds of
commits. Shared credentials, so one compromise reaches both copies. A
restore never run, turning "we replicate offsite" into a hypothesis.
And the layering error: buying one of the three and assuming the
other jobs are covered, when backup, replica and archive do three
different jobs.

**In practice.** Run the three-property test against every system you
call backed up: where is the copy physically, whose credentials read
it, when was the last restore, what did the spot-check return. Treat
lag as the RPO it is — `pg_stat_replication.replay_lag`,
`Seconds_Behind_Source` — and alert past your stated number. Keep a
WAL archive even with three replicas: replicas give failover, the
archive gives time travel. This failure is catalogued in
[mistakes](../mistakes/README.md); the stream itself is
[networking](../networking/README.md).

---

## 21. Corruption, checksums and the restore decision

**Why.** Storage returns wrong bytes rather than no bytes: memory
errors, decaying flash cells, firmware bugs and torn writes all yield
a page that reads cleanly and means nothing. The need is detection at
read time plus a decision procedure for what to do next, because
guessing at corruption turns a local problem into a full restore.

**How.** Page checksums store a fingerprint in every page, computed on
write and verified on read, so corruption surfaces as an error naming
the relation and block number instead of a wrong answer. Whether they
are on is not something to assume: `data_checksums` is not always set
at init, so read `pg_controldata` rather than trust a default; InnoDB
computes a checksum on every page write regardless.

Torn-page defence works alongside. A doublewrite buffer writes pages
to a known-good area first and reconciles on recovery, so a page torn
mid-write is repaired instead of discovered — the reason a crash does
not leave half-written pages behind (entry 8 covers the flush path
underneath).

A scrub job reads every page in the background and verifies it, moving
discovery from a user's query — which returns garbage or fails — to a
monitoring surface that catches rot while it is still one page and
one replica-copy away.

| Finding | Likely source | Move |
|---|---|---|
| One page, checksum failure | bit rot | PITR to before it, or page from a replica |
| Many pages failing | bad media | full base restore; trust nothing between |
| Checksums off, garbage | silent old rot | restore, then enable and re-verify |
| Archive SHA-256 mismatch | rot in archive | earlier base; re-verify whole chain |

The source changes the fix: bit rot, a logic bug and a dying disk are
three different problems, and restoring onto the same failing hardware
recreates the fault inside a day. The archive needs checksums too — a
SHA-256 manifest per segment, checked when that segment is restored —
because a corrupt archive discovered *at* restore time is the worst
possible moment to learn about it.

**Where it breaks.** Checksums never enabled, so a corrupt page
returns garbage silently for months and the truth finally surfaces in
a downstream system that did arithmetic on it. Bit rot in the WAL
segment exactly at the replay boundary: the base is good, the chain is
good, and recovery stops one file short. A scrub scheduled into the
same window as the backup, so both saturate the device. Copying a
corrupt page from a replica that shipped from the same primary, so
both copies carry the same damage.

**In practice.** Enable checksums at init (`initdb --data-checksums`)
or migrate with `pg_checksums --enable`, then `--validate` on a copy
on a schedule; run device scrub monthly and alert on its result — the
block layer is [linux](../linux/README.md)'s story. Hash each archived
segment into a manifest (`sha256sum`) at archive time and verify at
restore — cheap to write, priceless at 03:00. Alert on the server
log's checksum-failure string instead of waiting for a user.

---

## 22. Verifying, retaining and encrypting backups

**Why.** A backup that has never been restored is a hope with a
storage bill, and every layer — the copy, the chain, the key, the
media — can be the one that fails when it is finally opened. The need
is proof on a schedule: something that restores, inside the promised
time, with bytes that read back correctly.

**How.** The restore drill is the only test that exercises the real
path: pull a backup, restore it into a sandbox, run the smoke queries,
time the whole thing. Automate it — a cheap check daily, a full
restore weekly — and track measured duration against the stated RTO,
because the number in the document and the number on the clock drift
apart silently.

```
drill:   pick a backup → restore into a sandbox → smoke queries → time vs RTO
retain:  3 copies on 2 media, 1 offsite → immutable → deletion becomes a no-op
protect: encrypt at rest + TLS in transit → key in KMS → rotation beats clock
```

Retention is a policy, not a habit: a daily/weekly/monthly ladder (7
dailies, 4 weeklies, 12 monthlies is one common shape), legal holds
that override deletion, and the pair people miss — PITR window versus
archive retention, which must match. Promising 30 days of
point-in-time recovery while keeping 14 days of WAL is a lie the
restore will tell.

Immutability closes the gap retention alone leaves: object-lock or
write-once storage means last week's base cannot be deleted by
whoever holds the production credentials — which are exactly the
credentials a ransomware run asks for first.

Encryption needs an honest answer about keys: at rest with AES-GCM
via the storage layer or the engine, in transit over TLS whenever WAL
leaves the host, and key management sized against the recovery
window. An encrypted backup with a lost or rotated-out key is not a
backup, it is a checksum. And backups hold every credential that was
ever in the data, so read access to them is production access.

**Where it breaks.** The drill that fails at hour two of a four-hour
window, discovering the RTO was fiction and the runbook never covered
restoring extensions. A key rotated after retention started covering
encrypted bases, so last month's base decrypts into nothing. The
silent monitoring gap: the job logs "success" every night while
producing no files, because nobody alerts on backup *age*. Two
"offsite" copies on the same array. Retention shorter than the
promised PITR window — a promise nobody checks until the day it is
needed.

**In practice.** Monitor four numbers: age of the last full, archive
lag (segments pending), days since the last successful restore drill,
and measured restore duration against RTO. Put keys in a KMS with
rotation slower than your longest recovery, and list who may read
them. The drill is an ordinary job — `database/sql` against the
sandbox DSN, a few counts and a checksum aggregate, `crypto/sha256`
over the segment manifest — and it pages someone at production
severity when it fails.
