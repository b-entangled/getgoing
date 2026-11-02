# Database designs (hardware paths end to end)

Five walkthroughs tracing real database operations down to CPU, RAM, the
page cache and the platter — a commit, a point read, a crash, an UPDATE
against write amplification, and a connection storm. Each ends where
durability or the scheduler decides the outcome; concepts in the
sibling [`README.md`](README.md).

---

## 1. A commit: from UPDATE to durable truth ★

**Problem.** Trace one `UPDATE` from SQL text to the durable truth a
client is allowed to believe: parse, plan, latch, log, group-commit —
and name the single fsync whose return is what "committed" means.

**Path.**

```
execute: UPDATE → parse/plan → latch row → find page → hit? else read
log:     WAL record (page id + change) → log buffer → LSN assigned
commit:  COMMIT → group commit leader → batch waiters → fsync tail → ack
flush:   no-force: dirty BP page waits → background flusher → data file
checkpt: checkpoint records low LSN → recovery window bounded
```

**Walkthrough.**

1. Parse turns the text into a parse tree, rewrites it (views, rules)
   and plans it: for this UPDATE the access path is an index scan on
   the primary key. A prepared statement skips all three only while its
   cached plan is still valid.

2. Execution reaches the target row and takes its tuple lock, held
   until commit. A conflicting holder means you queue, and when two
   transactions each hold what the other needs, one is picked as the
   deadlock victim and gets an error instead of waiting.

3. The page is looked up in the buffer pool by (tablespace, page id)
   hash. A hit pins the frame and latches it; a miss posts the read
   and frees a frame — dirty victims are flushed before reuse.

4. The change is logged before the page is touched: record type, page
   id, offset and the changed bytes go to the in-memory log buffer
   with an LSN. The pool page is now dirty — the file is untouched.

5. COMMIT ends the transaction: its final LSN is queued for the log
   writer. Nothing is durable yet in any sense the client can rely
   on; the statement has only reached RAM.

6. Group commit: one woken thread becomes leader, collects every
   waiter whose end-LSN sits at or below the log tail, and issues one
   fsync — an fdatasync over the log file — covering the batch.
   Followers return as that same flush completes: N commits, 1 fsync.

7. The ack leaves the server only after that fsync returns. From here
   down the commit latency *is* the storage tier's latency: an NVMe
   flushing in tens of microseconds, a disk behind a write barrier or
   a lying RAID cache in milliseconds — or never.

8. No-force: the dirty data page may stay in RAM for hours. A
   background flusher writes it at leisure, at checkpoint time, or
   when memory pressure demands the frame back.

9. Steal is safe: the flusher may write a page whose transaction has
   not committed, because recovery can redo everything from the log.
   The invariant is ordering — the log record is durable before the
   page it describes reaches the disk.

10. A checkpoint records a low LSN, plus the dirty page table for a
    fuzzy checkpoint, bounding where recovery starts — without it a
    restart replays the log since the file was first created.

**Sketch.**

```go
db, err := sql.Open("postgres", dsn) // lazy: first use dials
if err != nil { return err }
db.SetMaxOpenConns(20) // stay under max_connections: queue here, not there
db.SetMaxIdleConns(10)
db.SetConnMaxIdleTime(5 * time.Minute)
ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
defer cancel() // one budget over begin, exec and the fsync behind Commit
tx, err := db.BeginTx(ctx, nil)
if err != nil {
    return err
}
defer tx.Rollback() // no-op after Commit, required on every error path
q := `UPDATE accounts SET balance = balance - $1 WHERE id = $2`
if _, err := tx.ExecContext(ctx, q, amt, id); err != nil {
    return err // 40P01 deadlock victim: retry the whole transaction
}
return tx.Commit() // returns only after the server's fsync of the log tail
```

**Failure story.** Crash before the fsync: no ack left the server, so
the client retries — at-least-once, which makes idempotency the
client's contract, not the database's
([idempotency](../distributed/README.md)). Crash after the fsync: the
log tail is on disk, recovery redoes the record, and the data file need
never have been written. An fsync latency spike lands as commit p99 —
below the fsync, tuning the query plan changes nothing. A log buffer
filling faster than it drains stalls every committer waiting for
space: one slow disk becomes a queue of blocked transactions, and a
deadlock victim gets an error where it wanted an ack.

**Watch-outs.**

- Commit rate against fsync rate: group commit size is commits per
  flush, and batch windows (`commit_delay`) trade p99 for throughput.
- WAL growth and checkpoint cadence: frequent checkpoints cost
  background writes, rare ones cost recovery time and disk space.
- `synchronous_commit=off` buys latency by moving the durability
  window into the failure story above — know its size in seconds.
- Watch commit p99 and group-commit wait time, never averages: the
  mean hides exactly the flush barrier you will be paged for.
- Honest gap: nothing in application metrics proves the device honored
  the flush; a write cache that lies voids the whole design.

---

## 2. A point read: buffer pool hit vs miss

**Problem.** Trace one `SELECT` by primary key: the B-tree descent from
a pinned root, the latch-and-visibility work on a hit, and the entire
miss path — victim choice, `pread`, the OS page cache, the retry.

**Path.**

```
descend: parse/plan → pin root → latch → child ptr → … → leaf (3-4 deep)
hit:     leaf in buffer pool → latch → MVCC check → row → µs, no syscall
miss:    post read → pick CLOCK victim → pread 16 KB → install → retry
dual:    no O_DIRECT → OS page cache first → 16 KB in RAM twice → then BP
scan:    seq scan → ring / low-water window → pool not polluted
```

**Walkthrough.**

1. The plan for a primary-key equality is an index scan, and a prepared
   statement usually has it cached, so the interesting work begins at
   the root page rather than at the parser.

2. Descent: pin the root — protocol keeps upper levels resident —
   shared-latch it, read the child pointer, unpin, repeat. Millions
   of rows are 3-4 levels deep; the top two never really leave.

3. Hit: the leaf frame is found by page id, latched, and the row read
   through the page's slot directory. The latch is the concurrency
   gate — short, exclusive, and never held across I/O.

4. MVCC decides what the row *is* to this snapshot: xmin committed and
   visible, xmax uncommitted or from the future. A row updated
   concurrently yields the previous version from its update chain, or
   nothing at all.

5. Total cost on this path: one hash lookup, a couple of latches and a
   visibility check — microseconds in RAM, no syscalls, and the device
   never learns the query happened.

6. Miss: the buffer manager posts the read and first frees a frame; a
   CLOCK/LRU-variant scan skips pinned buffers to pick the victim. A
   dirty victim must be written first, so a read can wait on fsync.

7. The read itself is `pread(fd, buf, 16384, off)`. Without O_DIRECT
   it passes through the OS page cache, so the page now lives in RAM
   twice — pool frame plus cache page ([linux](../linux/DESIGNS.md)) —
   wasting memory but giving every other reader a second chance.

8. With O_DIRECT the cache is bypassed: NVMe in roughly 10-25 µs, a
   disk in milliseconds. The frame is installed, the descent resumes
   at the level that stalled, and the leaf latch is taken again.

9. A scan over a table larger than the pool would evict what it
   touches, so PostgreSQL uses a ring of buffers and InnoDB a
   low-water mark: a moving window that leaves the hot set alone.

**Sketch.**

```go
db, err := sql.Open("postgres", dsn)
if err != nil {
    return err
}
// Old Postgres heuristic: cores × 2 + spindles. The pool bounds how
// many point reads fly at once; beyond it requests queue in process.
db.SetMaxOpenConns(16)
db.SetMaxIdleConns(8) // idle conns are warm: fewer cold-leaf surprises
db.SetConnMaxLifetime(30 * time.Minute) // recycle before the server's cull
ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
defer cancel() // queueing + parse + latch + read share one budget
var name string
err = db.QueryRowContext(ctx, `SELECT name FROM users WHERE id = $1`, id).
    Scan(&name)
if err != nil {
    return err // ErrNoRows is absence; a timeout is contention or a miss
}
```

**Failure story.** Restart with a cold cache: plans may be warm but
every leaf is on disk, so the first minutes show a p99 spike while the
pool refills — prewarming is the runbook, and until it finishes "fast"
queries are device-bound. Random UUID keys defeat locality: each read
lands on a different leaf and hit rate slides toward working set over
pool size. A double miss — the pool evicted the frame *and* the OS
dropped the cache page — turns a microsecond lookup into true device
latency, which `posix_fadvise(DONTNEED)` after bulk loads makes
routine. A hot leaf latch queues every writer behind one page: the
wait reports as latch contention, not I/O, so disk-oriented
dashboards stay green while the table is stuck.

**Watch-outs.**

- Point reads should hold the buffer pool hit rate above 99%; below
  that, resize the pool or rethink the key before tuning anything.
- Logical (pool) reads against physical (device) reads: the ratio says
  whether the OS cache is quietly covering for you.
- Cold-start runbooks: prewarm hot indexes after restart or failover,
  or accept the first-queries spike as a priced-in cost.
- Page cache is RAM the database does not manage; O_DIRECT trades the
  second chance for a bounded, predictable footprint.
- You cannot see from the application whether a slow read was a latch
  wait, a pool miss or the device — those are server-side wait events.

---

## 3. A crash: recovery in the WAL's footsteps

**Problem.** Trace the boot after a crash: the three ARIES phases that
decide which transactions survived, why redo replays everyone's
changes, and how the checkpoint interval sets how long clients wait
outside.

**Path.**

```
boot:     read last checkpoint → ANALYSIS start → server not yet open
analysis: walk WAL to log end → dirty page table (recLSN) + loser list
redo:     from min recLSN → physiological replay → idempotent → torn fixed
undo:     losers walk prev-LSN chain → before-image → CLR → repeat-safe
open:     undo done → flush → clients admitted; wait = log since checkpt
```

**Walkthrough.**

1. Startup finds the last checkpoint record — or the start of the log
   when there is none — and reads what it captured: the dirty page
   table for a fuzzy checkpoint, an empty one for a sharp checkpoint,
   and the transaction table as of that LSN. Nothing is open yet.

2. ANALYSIS walks forward from that LSN to the end of the log,
   extending the dirty page table: for every page touched, keep the
   earliest LSN that dirtied it (its recLSN). It also collects losers
   — transactions with no commit record before the log ends.

3. REDO starts from the minimum recLSN and replays *every* record from
   there, committed or not. It must: steal means committed changes may
   be missing from the data file because their page was never
   flushed, and no-force means dirty pages may never have been
   written at all.

4. Redo is physiological: the record names page id, offset, length and
   the bytes, so applying it to a page that already carries them is a
   no-op — `page.lsn >= record.lsn` skips the write. That check also
   repairs a torn page: half-written bytes get their logged image back.

5. UNDO walks each loser backwards through its own chain — every
   record carries a prev-LSN — restoring before-images in reverse
   order of application, one transaction at a time.

6. Each undo step writes a compensation log record (CLR) naming what
   was undone and where to continue, so a second crash resumes the
   undo instead of repeating it: recovery is itself WAL'd.

7. Losers end marked rolled back, committed work from before the crash
   is intact because it was redone, uncommitted work is gone because
   it was undone — and only then does the database open.

8. Fuzzy against sharp: a sharp checkpoint waits for the pool to
   drain and stops the world; a fuzzy one records state while writers
   run, costing log records now to buy a shorter outage later.

9. Recovery time is roughly bytes since the last checkpoint divided by
   replay throughput. The checkpoint interval is therefore the knob
   trading background write overhead against time-to-open — and nobody
   tunes it until they sit through one long recovery.

**Sketch.**

```go
db, err := sql.Open("postgres", dsn) // Open is lazy: no socket yet
if err != nil {
    return err
}
// The first Ping pays for analysis, redo and undo: the server accepts
// no query until recovery finishes, bounded only by the last
// checkpoint. Size this for the worst interval, not a round trip.
ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
defer cancel()
if err := db.PingContext(ctx); err != nil {
    return fmt.Errorf("still recovering or unreachable: %w", err)
}
db.SetMaxOpenConns(4) // until recovery settles, connections only queue
```

**Failure story.** A torn page during redo is the ordinary case, not a
disaster: physiological redo re-applies the logged bytes and the LSN
check makes the repair idempotent. A crash *during* recovery hits the
CLRs written for every undo step, so the next boot resumes where the
undo stopped. A server that ran weeks without a checkpoint can take
minutes to recover while clients time out just trying to connect —
their retries hammer a port nobody is listening on yet. Transaction
age does not pause: wraparound pressure can force freezing, or shut
the server down again the moment it opens, turning one outage into a
loop. If the device lied about a flush during redo, the next crash
undoes the recovery itself.

**Watch-outs.**

- Checkpoint frequency and size are the recovery-time dial; measure
  time-to-open from a crash of a real log, never estimate it.
- The WAL is also your backup stream — archiving lives next door in
  [sdesign](../sdesign/README.md); an unshipped log dies with its host.
- Recovery-time objective belongs beside the RTO: minutes of analysis
  is an outage even when no data was lost.
- Data pages written by redo are flushed before clients are admitted;
  verify the device honors flushes, not just that code called them.
- Honest gap: during recovery you see "starting up" and nothing else —
  no percent, no LSN — so drill it before you need the number.

---

## 4. Write amplification: a B-tree page vs an LSM compaction

**Problem.** Count what one logical `UPDATE` really costs on disk: the
B-tree's 16 KB page writes and the flash's own rewrites against the
LSM's memtable-and-compaction stream — and where RAM and the OS
absorb or amplify each of them.

**Path.**

```
btree:  UPDATE → latch leaf → WAL → dirty 16 KB page → flush 16 KB later
btree2: + secondary index pages ×N → block erase → SSD garbage collection
lsm:    UPDATE → WAL + memtable (RAM) → flush SSTable → L0 → levels
lsm2:   compaction merges runs → WA typically 10×+ logical → sequential
ram:    BP vs memtable/block cache/blooms → compaction CPU + I/O compete
os:     page cache writeback (no O_DIRECT) → compaction storm → io.max
```

**Walkthrough.**

1. Path A, the B-tree: the UPDATE latches the leaf, appends its WAL
   record and edits the row in place — say 80 bytes inside a 16 KB
   page. The page turns dirty; nothing has reached the device yet.

2. The eventual flush writes the whole 16 KB: a 200:1 ratio for that
   one change, before counting the WAL bytes, the secondary index
   pages the same statement also dirtied, and any page split.

3. Flash amplifies underneath: an SSD must erase a whole block —
   hundreds of KB to a few MB — before it can rewrite a page, so its
   FTL garbage-collects live data to make room. Device write amp is
   your multiplier times the drive's own, worst on random writes.

4. Path B, the LSM: the same statement appends the same WAL record for
   durability, then edits a memtable — an ordered structure in RAM,
   typically a skip list — where the change costs no I/O at all.

5. A full memtable freezes and flushes as one sorted, sequential
   SSTable. Reads merge runs: overlapping files at L0, then sorted
   non-overlapping files down the levels, with bloom filters in RAM
   skipping whole files and a block cache holding hot blocks.

6. Compaction picks a file, merges it with the overlapping files of
   the next level and writes the result back, deleting the inputs.
   Bytes written track write amp times logical bytes — leveled
   designs are commonly quoted at 10×+ — but all of it is sequential.

7. The cost moves rather than vanishes: compaction burns CPU (merge,
   compression, checksums) and I/O bandwidth that competes with
   foreground reads, and unchecked debt trips L0's slowdown then stall
   thresholds, blocking writes until it clears.

8. RAM does different jobs per engine: a buffer pool plus dirty-page
   backlog and log buffer on one side; memtables, block cache, bloom
   filters and compaction input/output buffers on the other. All are
   eviction candidates, and compaction's reads are a polluter.

9. The OS sits under both paths: without O_DIRECT every write lands in
   the page cache and is written back later — WAL traffic is
   sequential and kind to it, while compaction's bulk reads fill the
   cache with one-hit data. A cgroup `io.max` throttles them together.

**Sketch.**

```go
// Batch size trades latency against amplification: bigger batches
// amortise the commit fsync and the index page latches.
const batch = 500
tx, err := db.BeginTx(ctx, nil)
if err != nil { return err }
defer tx.Rollback() // prepared statements live and die with the tx
stmt, err := tx.PrepareContext(ctx, `INSERT INTO events (id, body)
    VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET body = EXCLUDED.body`)
if err != nil {
    return err
}
for _, e := range events[:batch] {
    if _, err := stmt.ExecContext(ctx, e.ID, e.Body); err != nil {
        return err // one failure rolls back the whole batch
    }
}
return tx.Commit() // one fsync for every row above
```

**Failure story.** Compaction debt: the L0 file count crosses the
slowdown threshold and writes throttle, then the stall threshold and
foreground writes block — the client times out on an insert that never
touched a disk. Sustained write amplification eats the SSD's TBW budget
several times faster than application byte counts suggest, so the drive
dies "early" against every user-space estimate. Compaction's reads
stream through the page cache and evict hot rows: read miss rate climbs
exactly when the storm starts, and the team blames its own key design.
On the B-tree side, sustained random updates outrun the flusher: dirty
pages max out and background write lag grows until the engine stalls
writers to catch up. Under `io.max`, compaction shares the throttled
class with the WAL, so the commit fsync queues behind bulk I/O.

**Watch-outs.**

- Read, update and space form the RUM-style triangle: amplification is
  the update corner, and it shrinks only by paying elsewhere.
- Track compaction pending bytes, L0 file count, and WAL bytes per
  second against flush bytes per second — debt shows before stalls.
- Logical amp against device amp: SMART NAND-written divided by
  application bytes is the multiplier the FTL adds invisibly.
- Give compaction a lower I/O class than foreground writes — but
  throttling it too hard simply banks the debt for later.
- Honest gap: flash garbage collection is invisible from the host;
  only SMART counters and sudden device stalls hint it exists.

---

## 5. A connection storm: 1,000 clients reconnect at once

**Problem.** A thousand clients reconnect the moment the server
returns: trace the SYN queue to the accept backlog, the per-connection
setup that runs before any query exists, and the queueing math that
turns availability into a timeout.

**Path.**

```
kernel: SYN queue → handshake → accept backlog (somaxconn) → full → drop
accept: app accepts → thread/process per conn → auth + catalog → ready
sched:  thousands of runnable threads vs few cores → cpu.max throttles
ram:    per-conn structs + per-stmt workspaces → memory.max → OOM 137
queue:  utilization ρ → wait ~ ρ/(1-ρ) → p99 explodes before saturation
fix:    pgbouncer txn pooling → SetMaxOpenConns → backoff + jitter → steady
```

**Walkthrough.**

1. The server finishes recovery and returns to the accept loop. All
   thousand clients were retrying on timers set by the same outage,
   so their SYNs arrive in one burst rather than a stream.

2. The kernel completes each handshake into the accept queue — sized
   by the listen backlog, capped by `somaxconn`. When that queue
   fills, new SYNs are dropped (or absorbed with SYN cookies), and the
   client waits a full retransmission timeout before trying again.

3. `tcp_abort_on_overflow=1` turns the same condition into an RST, so
   clients see "connection refused" — an honest error. Silence is
   what makes clients retry at their maximum rate instead.

4. Each accepted connection costs setup before any SQL: a backend
   process or thread with its own stack, catalog lookups, SCRAM
   authentication, session parameters. The CPU dies here, and
   dashboards show connection counts, not query latency.

5. The scheduler sees thousands of runnable threads on a handful of
   cores: the run queue explodes, context switches grow, and inside a
   cgroup `cpu.max` begins throttling everyone — including the
   connections that would have been cheap ([linux](../linux/README.md)).

6. RAM multiplies too: per-connection state plus per-statement
   workspaces (sorts and hashes at `work_mem` each) — and past
   `memory.max` the OOM killer takes the server with exit 137,
   restarting the very storm it was suffering.

7. Queueing finishes the job: mean wait grows as ρ/(1-ρ) in service
   times — at 95% utilization every query waits around 20× its own
   work, at 99% around 100×, long before anything fails outright.

8. The fix path has three layers: a pooler in front (pgbouncer in
   transaction pooling — one server connection serves many clients,
   so `max_connections` stops being the ceiling), a bounded app pool
   (`SetMaxOpenConns`) so the queue forms where you can see it, and
   jittered backoff ([networking](../networking/README.md)).

9. The client host has limits of its own: an ephemeral range of
   roughly 28,000 ports burning in TIME_WAIT produces "cannot assign
   requested address" while every server-side metric stays green.

10. After recovery the herd replays the same hot statement: the run
    queue, the row latch and the WAL fsync all serialize on one row,
    one participant takes the deadlock error, and its retry rejoins
    the storm it came from.

**Sketch.**

```go
db, err := sql.Open("postgres", dsn)
if err != nil {
    return err
}
// The storm knob: excess requests queue in your process, bounded and
// visible, instead of arriving as a thousand server backends.
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(25) // idle reuse: re-dialing after restart IS the storm
db.SetConnMaxIdleTime(5 * time.Minute)
ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
defer cancel() // includes time spent waiting for a pool slot
if err := db.PingContext(ctx); err != nil {
    // Back off with jitter before retrying: synchronized retries turn
    // one restart into a thundering herd on the accept backlog.
    return err
}
```

**Failure story.** Backlog overflow on restart: SYNs dropped, client
timeouts, and every timeout schedules more attempts — the herd eats the
recovery window before a single query runs. CPU dies in SCRAM and
catalog setup while query charts stay flat: there are no queries yet.
Per-connection memory pushes past `memory.max` and the OOM killer
returns exit 137 — a crash loop whose trigger is the retry pattern.
When the server does come up, every session replays the same hot row:
one deadlock victim per round, its 40P01 error spawning a retry that
rejoins the herd. On the client, ephemeral ports run out first:
"cannot assign requested address" with a healthy server.

**Watch-outs.**

- Pool sizing heuristic: cores × 2 plus spindles, the old Postgres
  rule; a pool larger than the server just relocates the queue.
- pgbouncer modes: session keeps state but keeps the cost too,
  transaction is the default trade, statement forbids long txns.
- Full-jitter backoff beats fixed intervals — synchronized retries
  are the storm, and equal timeouts keep them synchronized.
- Watch connection-establishment rate against query rate: connects
  rising while queries stay flat is churn, not load.
- Honest gap: from inside one client you cannot see the other 999
  queues; server-side connection and wait metrics are the only view.

---

## 6. A point-in-time restore: from archive to open database

**Problem.** A bad `DELETE` committed at 14:32:07 today and shipped to
the archive with everything else. Restore the database to 14:32:00 on
a side host from the archived base plus WAL, open it there, validate
it, and cut over — inside the RTO, live data directory untouched.

**Path.**

```
fetch:    object storage → manifest → checksum verify → stage dir
replay:   base → place WAL segments → start → analysis/redo → target
→ 14:32:00 → pause (target action) → last complete txn → stop
validate: open read-only → row counts → smoke queries → cut over
→ repoint clients / re-attach replica → archive continues
→ fresh base → the next generation starts from this restore
```

**Walkthrough.**

1. Pick the base: the most recent *verified* base, not the newest one
   sitting in the bucket. The newest is only as good as the manifest
   that signed it — record its start LSN, timestamp and generation
   before anything moves.

2. Pull it to a side host, into a fresh staging directory. Never
   restore onto the live files: they are the only copy of the state
   you are trying to reproduce, and muscle memory has eaten more
   production than every other failure on this list.

3. Verify the base before trusting it — manifest signatures,
   per-file checksums, sizes, expected page count. A base failing
   that check is not a base, it is a landfill; drop a generation.

4. Assemble the WAL from the archive: every segment from the base's
   start LSN forward past 14:32:00, decompressed into the staging
   directory in sequence order ([sdesign](../sdesign/README.md)).

5. Run the gap check at the boundary. If a segment is missing the
   window ends there: restore to the gap and accept the loss, or fail
   loudly. Quietly replaying past the hole and calling it a
   point-in-time restore is how an incident gets a second act.

6. Set the recovery target and its action. A time needs an explicit
   zone (`2026-10-07 14:32:00+05:30`) — an LSN or xid works too — and
   the action is pause or promote. The intent is to stop *before*
   14:32:07: the last complete transaction before 14:32:00.

7. Run recovery: ANALYSIS rebuilds the dirty page table and loser
   list from the base forward, REDO replays every record — idempotent,
   so a fuzzy base copy heals under the LSN check — UNDO rolls back
   the losers, and the engine halts at the target.

8. Promote or open the restored instance. It is a real database now,
   independent of the archive that produced it; hold it read-only
   until validation passes, so a bad number costs another replay
   rather than an apology.

9. Validate against expectations captured before the incident: row
   counts, spot queries on exactly what the DELETE touched, and the
   application's invariants — sums, foreign keys, monotonic
   sequences — against the last good report, not against memory.

10. Cut over: repoint clients or re-attach the replica at the
    restored instance, then resume archive shipping *from its LSN*
    so the new chain begins where this one was stitched in — a gap
    in the new chain is a gap in next year's restore.

11. Take a fresh base soon after the cutover. The restored instance
    becomes the basis of the next generation, and the incident
    window's archive stays until the investigation closes.

**Sketch.**

```go
// Restore runs on a side host; the original data directory is never
// written. The whole path is I/O-bound: fetch, decompress, then replay.
ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
defer cancel() // the RTO is a deadline here, not a suggestion
pull := exec.CommandContext(ctx, "restore", "pull", "--base", baseID,
    "--to", stagingDir) // stage dir, never PGDATA: fetch + decompress
if err := stream(ctx, pull); err != nil { return err } // tee stdout to log
replay := exec.CommandContext(ctx, "restore", "replay",
    "--target-time", "2026-10-07 14:32:00+05:30", // the zone is mandatory
    "--target-action", "pause") // stop before the DELETE at 14:32:07
if err := stream(ctx, replay); err != nil { return err } // analysis → redo
db, err := sql.Open("postgres", sideDSN) // _ "github.com/lib/pq"
if err != nil { return err }
var n int // smoke check before anyone repoints traffic at this host
err = db.QueryRowContext(ctx, `SELECT count(*) FROM orders`).Scan(&n)
if err != nil { return err } // validate with data, not with logs
```

**Failure story.** The archive holds every segment but one, and it is
exactly the segment the replay boundary lands on: restore aborts, and
the fallback is to restore to the gap and accept the data loss it
represents — or escalate. A target inside an open transaction is not
an error: the engine stops at the last *complete* transaction, so the
caller sees rows committed after the target missing, by design. Pulling
from cold storage blows the RTO on its own — restore is I/O-bound on
fetch plus decompression, and replay adds linearly to the same budget.
ENOSPC mid-replay ([linux](../linux/DESIGNS.md)) leaves a staging directory
nobody should trust: replay wants scratch space nobody provisioned.
Restoring into the live data directory by muscle memory destroys the
only copy — the reason every step says "side host". Skip validation
and you cut over a consistent-but-wrong database: missing rows, not
corruption. "14:32" evaluated in the wrong zone lands hours away from
the incident, and the restore succeeds perfectly at the wrong time.

**Watch-outs.**

- Archive retention ≥ the promised PITR window plus margin; a log
  expiring early turns the window into a marketing claim.
- Automate the restore drill and measure it against the RTO — an
  untested restore plan is fiction with a runbook attached.
- Always stage to a side directory on a side host: the live data
  directory is a source, never a destination.
- Checksum archive segments at *archive* time, not only at restore
  time; a corrupt segment found during the incident is a second
  incident.
- A rehearsed runbook with exact commands beats documentation that
  only describes intent — copy-paste under stress is a feature.
- Pin the time zone in the target, in the runbook and in the alarm
  that wakes you; "14:32" alone means nothing.
- After cutover, archive continuity: resume shipping from the
  restored LSN so the new chain has no gap
  ([sdesign](../sdesign/README.md)).
- Keep the incident window's archive until the investigation closes,
  even after the fresh base lands.
- Honest gap: from inside a running client you cannot tell which side
  of the target you landed on — validate with data, not with logs.
