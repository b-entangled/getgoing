# Linux designs (kernel paths end to end)

Five composed walkthroughs of the kernel paths every Go server walks —
process creation, page faults, packet delivery, readiness notification
and the write that must outlive the machine — each traced stage by
stage to the failure it produces when one stage misbehaves. The
concepts themselves live in the sibling [`README.md`](README.md).

---

## 1. A process is born: fork + execve

**Problem.** Trace the command line a shell (or `os/exec`) hands the
kernel into a running program: PATH lookup, `fork`, `execve`,
`waitpid` — and what each stage reports when it refuses, because "the
child ran" is a claim only the exit status can make.

**Path.**

```
shell:  command line → PATH lookup → fork (copy page tables, COW) → child
exec:   child execve → open binary → ELF segments mapped (text, rodata, data)
load:   PT_INTERP → ld.so mapped → stack + argv/envp/auxv → entry point
drops:  signal dispositions reset → CLOEXEC fds closed → new PID visible
wait:   parent waitpid → exit status reaped → table row freed
```

**Walkthrough.** One command line, ten kernel decisions:

1. The shell resolves the name against `PATH` itself and calls
   `execve` with an absolute path — libc's `execvp` searches for us.
   A shell built-in never reaches the kernel, which is why `cd` never
   appears in `ps`.

2. `fork()` duplicates the task: a new PID, page tables copied and
   marked read-only, a copied fd table with each `struct file`
   refcounted, credentials and cwd inherited. The frames are *not*
   copied — both sides share them until one writes, so forking 1 GiB
   costs page tables, not 1 GiB.

3. The child wakes holding the parent's code, so the next thing it
   must do is `execve`. In a multi-threaded parent only the calling
   thread survives the fork: a lock held by a dead thread deadlocks
   before the image changes.

4. `execve(path, argv, envp)` walks the path through the dentry cache
   and checks permissions. Missing binary → `ENOENT`, no execute bit
   → `EACCES`, and the parent learns of neither from `Start` — only
   from the status `waitpid` returns.

5. The kernel parses the ELF header and, for a dynamic binary,
   follows `PT_INTERP` to the loader and maps that too: the process
   starts at ld.so's entry, which maps libc, relocates and only then
   jumps toward `_start` and `main`.

6. Old mappings are torn down, new ones built from the `PT_LOAD`
   segments — text and rodata read-execute, data and bss read-write —
   then a fresh stack takes `argv`, `envp` and the auxiliary vector:
   `AT_PHDR`, `AT_ENTRY`, `AT_RANDOM`, `AT_HWCAP`.

7. Signal dispositions reset to default, because a handler cannot
   survive a new image (`SIG_IGN` stays ignored), pending signals are
   cleared, and the fd table carries over — except `O_CLOEXEC`
   entries, closed right here at exec.

8. The new PID is in the process table immediately, so `ps` can show
   a process that has not exec'd yet: "started" and "running the new
   program" are different facts, and a crash between them looks like
   a healthy start.

9. The parent waits in `waitpid` and reads the status word: the exit
   code, or the signal that killed the child. Skip the wait and you
   keep a zombie — a table row with a status and nothing else —
   harmless by the dozen, fatal in a respawn loop.

10. `vfork` and `posix_spawn` compress the sequence without changing
    it: `vfork` shares the address space and suspends the parent
    until exec, `posix_spawn` packages fork+exec in one call, built on
    `clone(CLONE_VM|CLONE_VFORK)`. The pipeline is still fork →
    exec → wait.

**Sketch.** The `os/exec` shape of the same path, wait included:

```go
cmd := exec.Command("/usr/local/bin/worker", "--queue", q)
// Setsid: a new session, so the child outlives our terminal's SIGHUP
cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
cmd.Stdout, cmd.Stderr = logFile, logFile // pipes exist only if you set them

if err := cmd.Start(); err != nil {
    return fmt.Errorf("spawn worker: %w", err) // ENOENT, EACCES, EMFILE
}

state, err := cmd.Wait() // the waitpid: skipping it leaks a zombie
if err != nil {
    if ee, ok := err.(*exec.ExitError); ok {
        return fmt.Errorf("worker exited %d", ee.ExitCode())
    }
    return err // never ran properly, or the wait itself failed
}
log.Printf("worker finished: %v", state)
```

**Failure story.** The parent's fd table is at `RLIMIT_NOFILE` and
`fork` fails with `EMFILE` while copying it, or `ENOMEM` when page
tables run out — nothing starts, and the caller sees one error; the
path is wrong or unreadable → `execve` answers `ENOENT`/`EACCES`,
which the parent learns only as exit status 127 or a `Wait` error; no
one calls `Wait` → zombies pile up and the pipes' read ends stay
open, so a chatty child blocks forever on a full stdout buffer while
`Start` reported success; the child exits before the parent writes →
the write to its stdin pipe returns `EPIPE` (Go turns SIGPIPE into an
error for you); the child dies of `SIGSEGV` → `Wait` reports "signal:
segmentation fault", the only place that fact exists.

**Watch-outs.** `O_CLOEXEC` is the leak that crosses process
boundaries: Go sets it on everything it opens (see
[`README.md`](README.md)), while a C helper that forgets it hands the
child your listening socket; zombies are cleared by `Wait` and by
nothing else, so count them in any respawn loop; PID space is finite —
`pid_max` is 32768 on many systems and a fork bomb hits `EAGAIN` on
`RLIMIT_NPROC` first; and "who reaps the orphan" needs an answer
before the supervisor exits, because PID 1 or a
`PR_SET_CHILD_SUBREAPER` will otherwise collect children nobody asked
it to take.

---

## 2. A missing page becomes bytes

**Problem.** Trace one load instruction from a virtual address the MMU
refuses to a byte the program can use: the walk, the fault
classification, the disk read when the page lives elsewhere — and the
two verdicts, SIGSEGV and the OOM kill, when no frame can be granted.

**Path.**

```
walk:   load → TLB miss → PML4 → PDPT → PD → PT → present bit clear → #PF
minor:  frame already in RAM → map it → retry the instruction → back to user
major:  page cache miss → filesystem read → I/O wait → map → retry
cow:    shared frame after fork → copy on write → PTE writable → retry
deny:   write to a read-only mapping → nothing to fix → SIGSEGV
oom:    no free frame → reclaim fails → OOM killer scores tasks → SIGKILL
```

**Walkthrough.** All eight happen inside one instruction's retry loop:

1. The instruction computes a virtual address and the MMU checks the
   TLB first — recent translations tagged by PCID or ASID, so a
   context switch does not flush them. A hit is unmeasurable; a miss
   falls through to the walk.

2. On x86-64 the walk reads CR3 and follows four arrays of 512
   eight-byte entries — PML4, PDPT, PD, PT — four 9-bit indexes plus
   a 12-bit offset. The PTE's present bit is clear, so the CPU raises
   `#PF` and the handler runs on this thread's kernel stack.

3. The handler classifies before it fixes: present or protection,
   read, write or fetch, user or kernel — and is the address inside a
   `vm_area_struct`? A user address with no VMA is not a late page,
   it is a bug: `SIGSEGV`.

4. Anonymous and never touched: take a *zeroed* frame, map it,
   return, retry the instruction. Zeroing is not politeness — hand
   back somebody else's bytes and the process reads them. A minor
   fault, about a microsecond.

5. File-backed: look in the page cache, keyed by inode and index.
   Hit → map → minor. Miss → read through the filesystem, sleep in
   I/O wait, map, retry — a major fault, hundreds of microseconds to
   milliseconds charged to one instruction.

6. Copy-on-write is the same trap with a different fix: after `fork`
   both tasks point at one read-only frame, the first writer faults,
   the handler copies 4 KiB and rewrites only that task's PTE. Fork
   is cheap *because* this is where the cost lands.

7. A write to a genuinely read-only mapping — a `PROT_READ` segment,
   a private page with nothing behind it — has no fix to apply, so
   the kernel delivers `SIGSEGV`: a panic in Go code, a fatal signal
   in cgo.

8. No frame to give: reclaim clean cache, swap anonymous pages, then
   give up — the OOM killer picks a task, roughly by memory use
   scaled by age and adjusted by `oom_score_adj`, and sends the one
   signal nothing handles. In a container the verdict is scoped to
   `memory.max`, and `memory.events` records the kill.

**Failure story.** The working set outgrows RAM plus swap and every
page touch becomes a disk read: the process stays *runnable* rather
than blocked, so nothing in the metrics says "disk" while p99 drifts
into seconds — the swap storm; a nil pointer or a bad index →
`SIGSEGV`, a panic in Go code and an unrecoverable crash in cgo; a
container whose `memory.max` is below the runtime's footprint → the
cgroup's own OOM killer fires and the process exits 137 (128 + the
SIGKILL the kernel sends), with no stack trace at all; a transparent
huge page collapsed under load → 512 page migrations in one stall, a
latency spike with no error attached; an overcommit allocation that
"succeeds" and dies on first touch.

**Watch-outs.** `vmstat 1` reports `pgmajfault` — major faults per
second is the number that matters — and `perf stat -e
minor-faults,major-faults` attributes them per process, if the perf
paranoid setting lets you; inside a cgroup, `memory.current` against
`memory.max` plus `memory.events` (`oom`, `oom_kill`, `max`) is the
only honest bookkeeping, and `memory.current` counts page cache too;
`vm.overcommit_memory` and `vm.overcommit_ratio` decide whether an
allocation that "worked" ever had to be real. From Go you can observe
none of it: `runtime.MemStats` reports heap, not faults.

---

## 3. A NIC packet becomes bytes in Read

**Problem.** Trace a frame arriving on the wire to a `Read` on a Go
`net.Conn` returning — DMA, softirq, IP, TCP, epoll, the runtime's
netpoller — and mark every point on the way where bytes can vanish
without anyone above noticing.

**Path.**

```
nic:    wire → NIC ring buffer → DMA into RAM → interrupt → NAPI poll
ip:     netif_receive_skb → IP → local? → defrag if needed → tcp_v4_rcv
tcp:    sequence match → receive queue → ACK generated → socket readable
wake:   epoll ready list → runtime netpoller wakes → goroutine unparked
read:   copy_to_user → your Go slice → Read returns
udp:    demux by port → queue the datagram → buffer full → drop, silently
```

**Walkthrough.** Nine hops from the wire to a slice:

1. The NIC DMAs the frame straight into a pre-posted ring buffer —
   descriptors tell the card where to write, so no CPU is involved
   until it has landed. One MSI-X interrupt per batch of
   completions, never one per packet.

2. The handler schedules NAPI, so the driver is polled in softirq
   with a budget of 64 packets: a flood becomes repeated polls, not
   an interrupt storm. The remainder goes to `ksoftirqd`, where a
   receive storm shows up as CPU.

3. `netif_receive_skb` hands the skb up, and GRO may already have
   merged adjacent TCP segments into one — so a single packet the
   stack sees can carry kilobytes of payload.

4. The IP layer decides: local or broadcast → continue, otherwise
   forward if this box is a router. Fragments reassemble first, and
   a datagram missing its last fragment expires after about 30
   seconds — dropped, silently.

5. TCP demultiplexes on the four-tuple to its `struct sock`:
   in-order bytes go to the receive queue, out-of-order bytes wait on
   a separate queue until the gap fills, and an ACK goes back —
   delayed up to about 40 ms, or immediate on the second segment.

6. The socket is now readable, so an epoll waiter is marked ready and
   woken. Level-triggered means "readable" holds until the queue is
   drained, which is why a short read is safe: epoll asks again.

7. In Go the waiter is the runtime netpoller — one epoll instance
   registered with `EPOLLIN|EPOLLOUT|EPOLLRDHUP|EPOLLET` — which
   unparks the goroutine on that fd and queues it for an M; only then
   does `read` run.

8. `copy_to_user` moves the bytes out of the skb into your Go slice
   and frees it; `Read` returns a count, and for TCP that count may be
   short — which is why `io.ReadFull` loops until the frame it wants
   is in hand.

9. UDP keeps none of the stream machinery: demux by port only, each
   datagram whole in the buffer, and when the buffer is full the
   packet is dropped at the socket — the sender is never told, the
   receiver just sees a gap.

**Sketch.** Deadlines first, then a read that does not trust one
`Read` to fill the buffer:

```go
if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
    return err
}
var hdr [4]byte // length prefix, big-endian
if _, err := io.ReadFull(conn, hdr[:]); err != nil {
    // deadline exceeded, EOF, or "connection reset by peer" (RST)
    return err
}
n := binary.BigEndian.Uint32(hdr[:])
payload := make([]byte, n)
if _, err := io.ReadFull(conn, payload); err != nil {
    return err // TCP may hand back fewer bytes than you asked for
}
```

**Failure story.** A receive storm outruns NAPI's budget → softirq
work piles onto `ksoftirqd`, packets sit in the ring and the NIC
drops them → TCP retransmits and the application sees latency with no
local error; the socket's receive queue fills because the application
reads slowly → the kernel drops silently, TCP recovers over the wire,
UDP loses the datagram forever; the peer resets the connection → the
next `Read` returns "connection reset by peer"; a peer that dies
without FIN or RST (host power cut) produces no EOF, so a `Read` with
no deadline parks the goroutine until TCP keepalive — two hours by
default — pinning fd and buffered bytes until then; a burst larger
than `SO_RCVBUF` → drops counted in `RcvbufErrors`, invisible from Go.

**Watch-outs.** Drop counters live in the kernel: `ss -ltnm` shows
socket memory, `nstat` holds `TcpExtListenOverflows`,
`TCPBacklogDrop` and `UdpRcvbufErrors`, and Go exposes none of them;
`SO_RCVBUF` is doubled when you set it and capped by `rmem_max`
unless you hold `CAP_NET_ADMIN`; softirq time is invisible from
inside the process — `mpstat`'s `%soft` or `perf` see it, and neither
runs in your goroutine. To recognise a timeout instead of
string-matching one, compare against `os.ErrDeadlineExceeded` —
errors are [`../concepts/README.md`](../concepts/README.md); the
wire-level story is [`../networking/README.md`](../networking/README.md).

---

## 4. Ten thousand connections on one reactor

**Problem.** Serve ten thousand connections without ten thousand
threads: build the interest list, wait for readiness, dispatch only
what is ready and drain it — the loop under epoll, under Go's
netpoller, and under every event-driven server you will meet.

**Path.**

```
arm:    listen fd → epoll_ctl(ADD) → N conn fds → interest list in kernel
wait:   epoll_wait → only ready fds come back → dispatch handlers → repeat
accept: one wakeup → accept4 until EAGAIN → epoll_ctl(ADD) → fd armed
drain:  EPOLLIN → read until EAGAIN → parse → EPOLLOUT → write until EAGAIN
herd:   thundering herd → accept mutex or SO_REUSEPORT → one acceptor wins
```

**Walkthrough.** The reactor, from registration to your handler:

1. Thread-per-conn dies before it is measured: each pthread reserves
   8 MiB of stack (`ulimit -s 8192`) and commits a 16 KiB kernel
   stack plus a scheduler entity, so 10 000 connections are 10 000
   switches of 1–5 µs thrashing TLB and caches — mostly for threads
   that sleep all day.

2. `select`/`poll` do not help: they rescan the whole set per call
   and glibc's `FD_SETSIZE` caps it at 1024. epoll (2.5.x, 2002)
   inverts the cost — `epoll_ctl` keeps an interest list of fds in
   the kernel, `epoll_wait` returns only what became ready: O(ready),
   not O(registered).

3. The loop is then small: wait, dispatch each ready fd, come back.
   Level-triggered, the default, keeps reporting a readable fd until
   you read it, so a short read is safe — you will be asked again.

4. Accepting adds one step: the listen fd fires, `accept4` runs until
   `EAGAIN` (one wakeup may cover several connections), then the new
   fd is registered and its interest armed.

5. Readable means read until `EAGAIN`, parse it all, queue a
   response. Writable means write until the buffer empties or
   `EAGAIN`; if bytes remain, keep `EPOLLOUT` armed and come back when
   the peer drains.

6. Edge-triggered (`EPOLLET`) notifies only on a change, so you *must*
   drain completely or the fd never signals again: data sits in the
   socket buffer with no future wakeup and the request hangs forever.
   The classic epoll bug — load-dependent, short-read-only.

7. A blocking call in a handler stalls every connection the loop
   owns: a synchronous disk read, an `fsync`, a naive `getaddrinfo`.
   Reactors push that work onto worker threads — in Go, onto another
   goroutine.

8. Thundering herd: a connection arrives on the shared listen socket
   and every acceptor wakes while all but one gets `EAGAIN`. An
   accept mutex serialises them; `SO_REUSEPORT` (3.9) gives each
   thread its own listen socket instead.

9. io_uring (5.1) is the next rung: submission and completion rings
   in shared memory, so a batch of reads, writes and accepts crosses
   the boundary once per batch rather than once per call.

10. Go's runtime netpoller *is* this reactor, which is why
    goroutine-per-conn works: one epoll instance, every listener and
    connection registered, the goroutine that would block parks and
    the ready ones are unparked for an M to run. The M/P/G layer
    above it is [`../internals/README.md`](../internals/README.md).

**Sketch.** The boring Go that scales — `ln` is a plain `net.Listen`,
and everything below is what you would write for ten connections:

```go
for {
    c, err := ln.Accept() // accept4 — parked until the fd is ready
    if err != nil {
        continue // transient EMFILE: back off, do not tear down
    }
    go serve(c) // one goroutine per conn; the reactor is the runtime's
}

func serve(c net.Conn) {
    defer c.Close()
    c.SetReadDeadline(time.Now().Add(30 * time.Second)) // idle conns time out
    for {
        if _, err := c.Read(buf); err != nil {
            return // EOF, reset or deadline — never a silent hang
        }
        respond(c, buf)
    }
}
```

**Failure story.** Edge-triggered without draining to `EAGAIN` → the
fd never signals again and one request hangs forever while its bytes
sit in the socket buffer; a connection storm faster than `accept` →
the listen backlog (`somaxconn`) fills and new SYNs are dropped, so
clients see connection timeouts while the process looks idle; the
*client* exhausts ephemeral ports — the default range spans about
28 000 ports and each `TIME_WAIT` holds one for 60 seconds — and
`connect` fails with `EADDRNOTAVAIL`; unbounded accepts → one
goroutine per idle connection until the GC never settles; a blocking
cgo call or an unpollable fd read parks the M that owns a P, and with
every P occupied the program stops running goroutines.

**Watch-outs.** Raise `ulimit -n` (`RLIMIT_NOFILE`, 1024 in a plain
shell) before anything else — the fd limit arrives before the CPU
does; keep-alives amortise the accept path, so `IdleTimeout` on
`http.Server` is a connection-churn knob; `GOMAXPROCS` should match
the cgroup's `cpu.max` quota, which the runtime has read for itself
since Go 1.25 (see [`../internals/README.md`](../internals/README.md));
watch `ListenOverflows` in `nstat` for backlog pressure; and when a
third-party library wants a thread of its own, you still need a
bounded pool beside the reactor.

---

## 5. write() until it survives a crash

**Problem.** Trace a `write` from the syscall to the platter and find
the crash windows: what the page cache promises, what writeback and
`fsync` actually add, and the four-step sequence that lets a replace
survive a power cut.

**Path.**

```
write:  app buffer → write syscall → VFS → page cache → page dirty → return
back:   dirty_expire ~30 s or dirty_ratio 20% → flushers → block layer → disk
sync:   fsync(fd) → writeback → journal commit → device FLUSH → durable
crash:  cut before writeback → data lost → cut during writeback → torn tail
safe:   write temp → fsync temp → rename → fsync dir → crash-safe replace
```

**Walkthrough.** Durability is a ladder, and the crash climbs it:

1. `write(fd, buf, n)` enters the VFS, finds the inode's
   `address_space` and copies your bytes into the page-cache page —
   a read-modify-write for a partial page. The page joins the dirty
   list and the call returns. No I/O happened: "written" means the
   *kernel* has the bytes.

2. The flushers age dirty pages out: `dirty_expire_centisecs` is
   3000, so a page may stay dirty 30 seconds;
   `dirty_background_ratio` (10% of RAM) wakes the flushers, and
   `dirty_ratio` (20%) makes the *writing task* block in
   `balance_dirty_pages`. That throttle is latency, not an error.

3. Writeback crosses three layers — filesystem, block layer, device.
   ext4's `data=ordered` default writes the data blocks first and
   only then the journal's descriptor and commit records, so a
   committed entry never points at blocks that were never written:
   you may lose a tail, never gain phantom data.

4. `fsync(fd)` is the explicit rung: flush data and metadata, wait
   for the journal commit, then issue a cache FLUSH to the device —
   the last hop, because a drive with a volatile cache can hold
   "durable" bytes in RAM. Only then is the data durable.

5. The *name* is a separate claim: creating or renaming mutates a
   directory inode, so the entry needs an `fsync` on a directory
   descriptor. `rename` within a filesystem is the only atomic
   primitive the API gives you — it happened, or it did not.

6. That yields the crash-safe replace: write a temp file in the same
   directory, `fsync` it, `rename` it over the target, `fsync` the
   directory. After step two some complete, valid version of the data
   is always reachable.

7. The crash windows in order: before writeback the bytes were only
   in RAM and are lost but the file stays consistent; during the
   write, a torn tail the journal rolls back; after the commit,
   metadata consistent and data valid; after the device FLUSH, on the
   platter — unless the drive lied about it.

8. `ENOSPC` need not arrive when you write: ext4's delayed allocation
   accepts the bytes and discovers there is no room during
   writeback — an error long after the caller was told "ok". If the
   journal cannot allocate, ext4 goes read-only rather than risk the
   tree.

9. The batch that pays for this is the group commit: one journal
   transaction covers many waiting fsyncs, which is why a database
   syncs on a timer, not per message. `fdatasync` and `O_DSYNC` are
   the narrower rungs — data plus what is needed to find it again.

**Sketch.** The atomic replace, every rung of the ladder:

```go
func AtomicReplace(dir, name string, data []byte) error {
    tmp, err := os.CreateTemp(dir, "."+name+".tmp") // same dir: no cross-fs
    if err != nil { return err }
    defer os.Remove(tmp.Name()) // harmless once rename has taken the name
    if _, err := tmp.Write(data); err != nil { return err }
    if err := tmp.Sync(); err != nil { return err } // bytes before the name
    if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
        return err // the one atomic primitive: old or new, never half
    }
    d, err := os.Open(dir)
    if err != nil { return err }
    defer d.Close()
    return d.Sync() // fsync the directory, or the new name can vanish
}
```

**Failure story.** `write` returns the count and the process
crashes → the bytes were in the page cache and are gone, while the
caller was told "ok"; a power cut during writeback → the tail is
torn and the journal rolls the *metadata* back, the file
consistent but shorter; a bulk load crosses `dirty_ratio` → every
writer blocks in `balance_dirty_pages` and p99 climbs to seconds
with nothing logged; `ENOSPC` surfaces during writeback after the
API said success, or the journal runs out and ext4 remounts
read-only; the drive acknowledges FLUSH CACHE without flushing →
`fsync` returns success with the data still in its own RAM; and in
Go, `bufio.Writer.Flush` is mistaken for durability — it empties a
userspace buffer into the page cache and nothing more.

**Watch-outs.** Group commit means your fsync latency depends on
everybody else's, so measure under load rather than alone; a
`rename` without a directory fsync leaves a name that evaporates on
reboot; `os.WriteFile` and a deferred `Close` never sync — which is
why the sketch exists; `dirty_ratio`, `dirty_expire_centisecs` and
tmpfs (RAM with a different name, and `/tmp` is not a backup) move
the latency curve; the filesystem concepts are the sibling
[`README.md`](README.md). You cannot verify from Go that a `Sync`
reached the platter — only a power cut tests it, and the ledger in
[`../distributed/DESIGNS.md`](../distributed/DESIGNS.md) leans on it.
