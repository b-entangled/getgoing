# GPU internals (NVIDIA hardware, the CPU↔GPU contract, custom CUDA kernels)

A GPU is not a faster CPU — it is a second machine sitting on a PCIe
card with its own instruction set, its own memory and its own
scheduler, and your process has to cross to it and back for every byte
it wants computed. That crossing is where the surprises live: a launch
that returns in microseconds and means nothing yet, a copy that quietly
serialises because the buffer was pageable, a kernel that runs but
waits on memory for 90% of its life. Every knob the documentation hands
you — block size, stream count, pinned versus managed memory,
occupancy — is one of the machine's parts wearing a different name, and
a knob whose part you cannot name is a knob you are turning blind.

Each entry has the same four parts: the need a mechanism fills
(**Why.**), the mechanics that fill it (**How.**), the ways it fails in
production (**Where it breaks.**) and the knobs, commands and metrics
that govern it (**In practice.**). Read them top to bottom the first
time — entry 1 is the map the other thirteen stand on — and after that
they work as reference. There is no test suite to run here: the subject
is hardware you already have, and the tables in it are the
specification.

Language features live over in [concepts](../concepts/README.md) and
[runtime behaviour](../internals/README.md); the kernel your process
runs under is [linux](../linux/README.md), with composed kernel-path
walkthroughs in [linux/DESIGNS](../linux/DESIGNS.md); the bytes a GPU
eats arrive over the network described by
[networking](../networking/README.md) or from disk warmed by the page
cache in [database](../database/README.md); the machines around this
one are [distributed](../distributed/README.md); the building blocks
you can copy are [sdesign](../sdesign/README.md); and the ways people
get this wrong are [mistakes](../mistakes/README.md).

---

## 1. Why a GPU is a different machine: SIMT and the streaming multiprocessor

**Why.** A CPU hides memory latency by predicting and caching; a GPU
hides it by sheer numbers — thousands of threads parked in flight so
that whenever one stalls, another is ready. Without that idea read
backwards, "16,896 cores" invites exactly the wrong optimisation:
per-thread latency thinking on a machine that only ever reports
throughput.

**How.** The CPU is a latency machine. Out-of-order execution, branch
prediction and a 384 MB L3 exist so that one thread finishes fast, and
the enemy is memory: a ~80 ns DRAM hit against a ~1 ns L1 hit is a
hundred or more cycles of nothing, so caches are the bet that you will
not pay it twice.

The GPU is a throughput machine. It gives up most of that machinery and
instead keeps 64 warps of 32 threads resident on every SM; when one
warp waits 400-600 cycles for HBM, the scheduler issues from another,
and the memory latency is drowned in other people's work. The warp is
the unit of everything: one instruction counter, one schedule and one
branch decision for every 32 threads, so divergence (entry 8) and
coalescing (entry 9) are both consequences of this one choice.

Each streaming multiprocessor is a small chip in its own right: four
warp schedulers, separate FP32 and INT32 pipelines, tensor cores, a
64K-entry register file (256 KB), 228 KB of shared memory plus L1, an
L2 slice and a path to HBM. An H100 SXM carries 132 of them — 16,896
FP32 lanes, a ~1.98 GHz boost clock and 50 MB of L2 shared by the
whole device.

| Property | Server CPU (EPYC 9654) | GPU (H100 SXM) |
|---|---|---|
| Cores / threads | 96 / 192 (SMT) | 132 SMs / 64 warps resident each |
| Clock | ~3.7 GHz boost | ~1.98 GHz boost |
| Hides latency with | caches, OoO, prediction | 2,048 threads resident/SM |
| On-chip cache | 384 MB L3 | 50 MB L2 + 228 KB shared/L1 per SM |
| Device memory | ~50-90 GB/s DDR5 | 3.35 TB/s HBM3, 80 GB |
| Optimises for | one thread finishing fast | every thread finishing together |

Occupancy is that idea made countable, so it belongs up front: the
threads resident per SM — bounded by registers, by shared memory and by
the 2,048-thread hardware cap — are the currency of latency hiding,
just as cache depth and reorder-buffer size are on the CPU. The
difference is that the GPU makes the budget explicit in the launch
configuration, and a profiler reads it back instead of you inferring
it.

```
cpu:   fetch → out-of-order issue → cache hit → one thread retires fast
gpu:   issue warp → stall on HBM → switch to a ready warp → 32 lanes busy
sm:    warp schedulers → FP32/INT pipes → tensor cores → shared + L1
```

**Where it breaks.** Porting CPU intuitions: adding threads to a loop
and expecting serial-shaped gains. Launching one thread per element for
a 3 µs kernel when the launch itself costs 5-10 µs, so the CPU spends
its time queueing. Quoting clocks — "2 GHz versus 3.7 GHz" — when the
comparison is lanes multiplied by clocks. Assuming 132 SMs after a MIG
partition, when a 1g instance gets one seventh of the chip. Treating
SIMT as lockstep across the device: warps are independent, so
device-side wall-clock measurements disagree. Comparing a gaming card's
FP64 rate (1/64 of FP32) against a server part's (1/2).

**In practice.** Start with
`nvidia-smi --query-gpu=name,compute_cap,clocks.sm --format=csv`,
then `nvidia-smi topo -m` for the fabric underneath (entry 3). Read
achieved occupancy and warp-stall reasons before touching arithmetic:
a kernel at 12% occupancy that is 90% memory-stalled has a data problem,
not a core problem. Choose the GPU by memory bandwidth if your kernel
touches memory per FLOP — most do. In Go, `runtime.NumCPU` and
`GOMAXPROCS` describe the host only: device parallelism comes from the
launch configuration you pass, never from the Go scheduler.

---

## 2. The launch: how the CPU starts work on the GPU

**Why.** The device cannot schedule itself, and the host cannot execute
on it, so one side of the machine must submit work it will not wait
for. The need is a submission path cheap enough to call thousands of
times a second and asynchronous enough that the CPU keeps configuring
while the GPU keeps computing.

**How.** The stack has three layers. Your binary links the CUDA runtime
(`libcudart`), which calls the CUDA driver (`libcuda.so`), which talks
to the `nvidia` kernel module that owns the device. The runtime gives
you `cudaMalloc`, streams and events; the driver gives you contexts,
module loading and the launch call itself. You almost always stay in
the runtime layer.

The launch syntax is a bracket list bolted onto a C call:

```cuda
kernel<<<grid, block, shmem, stream>>>(args);
```

`grid` and `block` are `dim3` triples of blocks and threads-per-block,
`shmem` is dynamic shared memory in bytes (0 unless requested), and
`stream` selects the queue (NULL for the legacy default). The launch
descriptor the CPU builds carries those dimensions, the parameter
block holding your kernel arguments (copied into device-accessible
parameter space), the stream, and the entry point's code address. The
CPU hands it to the driver and returns — typically 2-10 µs later,
before a single thread has run.

That asynchrony is a contract in both directions. Unified virtual
address space (UVA) means host pointers, device pointers and managed
pointers all live in one 64-bit map, so the same `void *` can be passed
to `cudaMemcpy` and to a kernel without translation, and
`cudaPointerGetAttributes` tells you which kind of memory it names.
Error delivery is asynchronous too: a kernel that faults latches an
error, and the next CUDA call — or the next synchronisation — is where
you find it.

```
launch: cpu builds descriptor → driver queues → sm executes → completion event
errors:   kernel faults → error latched in context → next cuda call returns it
overlap:  cpu configures kernel N+1 → sm runs kernel N → both busy
```

**Where it breaks.** Checking the return of the launch line only: it
reports that the queue accepted the work, not that it succeeded. Every
real fault surfaces later, at a `cudaDeviceSynchronize` or the next API
call, and blames the wrong line unless you check everywhere. Calling
`cudaDeviceSynchronize` inside the hot loop throws the asynchrony away
and serialises the two machines. Launching a grid with zero blocks (a
0-length input) or with block dimensions over 1,024 threads returns
`cudaErrorInvalidConfiguration` — a CPU crash loop expressed as a bad
ceil-divide. Assuming a launch waits for previous streams: only the
legacy default stream does that.

**In practice.** Wrap every call: `CUDA_CHECK(cudaGetLastError())`
immediately after the launch bracket and `CUDA_CHECK(
cudaDeviceSynchronize())` at each checkpoint — the pair localises a
fault to one kernel. Time on the device with `cudaEventRecord` pairs
(host clocks measure queue latency, not work). Batch small kernels
until launch overhead is under 5% of kernel time; if you cannot, reach
for CUDA Graphs (entry 11). See the whole pipeline with
`nsys profile -o run ./app`, and in Go keep launches inside one worker
process so a single context serves them all.

---

## 3. The interconnect: PCIe, NVLink and GPUDirect

**Why.** The GPU owns memory the CPU cannot address, so every byte of
input, weight and result crosses a wire. The need is a per-hop cost you
can name in advance, because a pipeline of five hops at the wrong
bandwidth silently caps everything above it.

**How.** The default wire is PCIe. A 4.0 x16 link moves 16 GT/s over 16
lanes with 128b/130b encoding — 31.5 GB/s each direction in theory,
24-26 GB/s for a real pinned `cudaMemcpy` once framing, headers and
retries are counted. PCIe 5.0 doubles the line rate to 63 GB/s and
delivers roughly 45-50 GB/s copied. Many cards are still wired x8 or
negotiate down to 8 GT/s, halving both figures; `lspci -vv` shows what
you actually got.

Inside one server, multi-GPU traffic leaves PCIe for NVLink. An H100
SXM has 18 NVLink 4 links at 25 GB/s per direction each — 900 GB/s
bidirectional — and NVSwitch lets all eight GPUs in a DGX/HGX box talk
at that full rate instead of daisy-chaining through a neighbour. GPU
memory is then just another address on the fabric.

GPUDirect removes the host from the path entirely. GPUDirect RDMA lets
a NIC write incoming RDMA transfers straight into GPU memory across
PCIe peer-to-peer, skipping host DRAM and a copy — the GPU end of the
zero-copy idea whose transport side is
[networking](../networking/README.md). GPUDirect Storage does the same
for NVMe, so an `O_DIRECT` read lands in HBM without staging. Both are
worthless if the devices sit behind different root complexes; they need
a shared PCIe switch.

| Hop | Theoretical | Realistic copy | Notes |
|---|---|---|---|
| PCIe 4.0 x16 | 31.5 GB/s per direction | 24-26 GB/s pinned | most cards today |
| PCIe 5.0 x16 | 63 GB/s per direction | 45-50 GB/s pinned | newer hosts |
| PCIe 4.0 x8 | 15.7 GB/s per direction | 12 GB/s pinned | a silent 2× penalty |
| NVLink 4 (H100) | 900 GB/s bidirectional | ~700 GB/s measured | 18 links, SXM |
| Host DDR5 socket | ~300-400 GB/s | ~150 GB/s stream | bounce buffer home |

Every hop through host DRAM costs twice: once in bandwidth (the copy
consumes the memory channel you thought was for the CPU) and once in
latency (PCIe round trip plus copy engine scheduling, hundreds of
nanoseconds to microseconds). A NIC→DRAM→GPU path pays both; a
NIC→GPU path pays neither.

**Where it breaks.** Copy-bound workloads: 800 µs of kernel work
paired with 3 ms of H2D traffic per batch, so the GPU idles two thirds
of the time and no kernel change helps. Cards that negotiated x8 or
Gen3 — a 2-4× cut you will chase in the wrong place for a week. Two
GPUs behind one shared PCIe switch whose x16 uplink is oversubscribed:
each gets half exactly when both are busy. Access Control Services on
the root port forcing every peer-to-peer transaction through the IOMMU,
which turns GPUDirect RDMA into a slow path — the classic fix is
disabling ACS in BIOS or clearing the capability. NIC and GPU on
different NUMA sockets, so traffic crosses the inter-socket link too.

**In practice.** `nvidia-smi topo -m` first: `PIX` (same switch) and
`NODE` (same NUMA) are what you want, `SYS` (cross-socket) and `PHB`
(host bridge) are the slow ones. Check link health with
`nvidia-smi nvlink -s` and `nvidia-smi nvlink -e` (replay errors).
Measure the wire with the `bandwidthTest` sample rather than trusting
the spec sheet, and watch copy time against kernel time in Nsight
Systems (entry 13) — if copies dominate, fix the topology before the
maths. In Go, size your input batches so one PCIe crossing carries at
least tens of megabytes, amortising the per-transfer overhead.

---

## 4. GPU memory hierarchy in depth

**Why.** 132 SMs cannot be fed from one channel, and the CPU's memory
hierarchy answers a different question. The need is a ladder big enough
to sustain terabytes per second, with the fast rungs small and private
and the choice of who manages each rung made explicit — because on the
GPU the middle of the ladder is yours to program.

**How.** Start at the bottom of the ladder and walk up. Each thread owns
its registers — a 64K-entry, 32-bit file per SM, 256 KB, one read per
clock per ALU — and the compiler decides how many your kernel uses (entry
8). Next comes shared memory: 228 KB per SM on H100 (150-192 KB on older
parts), partitioned into 32 banks, written and read by the block's threads
in tens of cycles. It shares its physical storage with L1, so you choose
the split at compile time or per kernel.

Then L2 — 50 MB shared by every SM on the H100, 40 MB on the A100 — as
the last line of defence before HBM: 80 GB of HBM3 on the package,
3.35 TB/s on an H100 SXM, ~2 TB/s on an A100 80 GB. That final number is
the one that matters: host DDR5 gives you 50-100 GB/s per channel pair,
so HBM is 10-40× faster, and it exists purely to keep 16,896 FP32 lanes
from starving. A machine with 67 TFLOPS of FP32 and 3.35 TB/s of memory
needs 20 FLOP per byte to be compute-bound; most real kernels are not.

| Level | Managed by | Capacity (H100) | Typical latency |
|---|---|---|---|
| Registers | the compiler, per thread | 256 KB per SM (64K × 32-bit) | ~1 cycle |
| Shared memory | you, per block | up to 228 KB per SM | ~20-30 cycles |
| L1 | hardware | shares the 256 KB with shared | ~30-40 cycles |
| L2 | hardware | 50 MB across all SMs | ~200 cycles |
| HBM3 | `cudaMalloc` | 80 GB at 3.35 TB/s | ~400-600 cycles |
| Host DDR5 | OS and `malloc` | 1 TB and up, via PCIe | plus a PCIe hop |

Shared memory is a software-managed cache, and that is the whole
architectural argument: you decide what is reused because you choose the
tiling (entry 10), instead of hoping a 64-byte cache line happens to
carry neighbours you wanted. The CPU hides this choice inside its cache
policy; the GPU hands it to you as `__shared__` arrays and
`__syncthreads()`. The price is budget arithmetic — a block asking for
100 KB of shared memory can only fit two per SM, and a kernel using 96
registers per thread gets only 672 threads resident — 21 warps —
capping occupancy before anything else is considered.

**Where it breaks.** Static `__shared__` larger than the per-arch limit
(48 KB without opt-in) fails the launch outright with
`cudaErrorInvalidValue` at the bracket. Register pressure does not fail —
it spills to local memory, which is global memory in disguise: your
kernel suddenly has bandwidth traffic you never wrote. Asking for more
than 228 KB of dynamic shared memory needs an explicit
`cudaFuncSetAttribute` opt-in, or the launch returns
`cudaErrorInvalidValue` for a request that "should" fit. Capacity OOM
arrives mid-stream when activations plus weights plus the staging buffer
exceed 80 GB. Treating L2 as free: two kernels streaming disjoint arrays
of the same size fight over it and thrash.

**In practice.** `nvcc -Xptxas -v` prints registers per thread, spilled
bytes and static shared memory per kernel — read it before profiling.
Knobs: `__launch_bounds__` to cap registers, tile sizes to trade shared
memory against occupancy, `cudaFuncSetAttribute` for large dynamic
tiles. Metrics: achieved occupancy, local-memory traffic (should be
zero), L2 hit rate (streaming kernels sit near 0% and that is fine).
`nvidia-smi --query-gpu=memory.total,memory.used --format=csv` tells you
the capacity budget; the bandwidth budget you learn from Nsight Compute's
roofline (entry 13).

---

## 5. Unified memory: the tempting shortcut

**Why.** Two heaps mean every data structure has a host copy and a device
copy, an explicit `cudaMemcpy` per hand-off, and bugs where the two drift
apart. The need is one pointer that is valid on both sides so that code can
be made correct first and made fast afterwards.

**How.** `cudaMallocManaged(&p, n)` returns a single address the CPU and
the GPU may both touch. Nothing migrates at allocation time: the first
access on either side takes a page fault, the driver moves a whole page —
on the order of 100 KB, not the 4 bytes you asked for — and the mapping
updates. It is demand paging with the OS and the CUDA driver cooperating,
exactly the trick the page cache plays for files in
[database](../database/README.md) and virtual memory plays in
[linux](../linux/README.md), except the second consumer is a device that
cannot take a fault handler itself.

Three APIs steer it. `cudaMemPrefetchAsync(p, n, dev, stream)` moves the
range ahead of the kernel that will read it, turning faults into a single
bulk DMA. `cudaMemAdvise(p, n, cudaMemAdviseSetPreferredLocation, dev)`
pins residency, and `cudaMemAdviseSetReadMostly` makes replicated read
data stay put. `cudaMemAdviseSetAccessedBy` skips the mapping work when
you have already prefetched. Use them and managed memory behaves like
explicit copies with better ergonomics; ignore them and it behaves like
NUMA first-touch with worse ones.

The third regime is oversubscription: allocate 120 GB of managed memory
on an 80 GB card and pages live in system RAM, then fault back and forth
across PCIe. Push further and the host's own memory pressure pushes pages
out through the swap path, so an access costs a fault, a network of one
hop, and possibly a disk read — a three-order-of-magnitude swing from the
HBM hit you wrote the kernel to exploit.

**Where it breaks.** Fine-grained interleaving: a kernel touching managed
memory element by element in a pattern you did not prefetch faults
thousands of times per launch, and each fault is microseconds. Faults
measured as "the first kernel after a resize is 50 ms slower" get
attributed to compilation or clocks. Oversubscription looks fine in
benchmarks until the working set grows past HBM and throughput falls off
a cliff into thrash — the same cliff databases fall off when the buffer
pool outgrows RAM. Host threads that touch a buffer during its kernel
fault on the CPU side and stall the GPU's copy engine. Prefetch issued on
the wrong stream, so the migration races the read instead of preceding it.

**In practice.** Managed memory is right for porting (get the code
running before rewriting the copies), for control structures and small
metadata, and for datasets comfortably under HBM with coarse-grained
access. For performance, the honest rule stands: pinned buffers plus
explicit `cudaMemcpyAsync`, prefetched on the same stream as the kernel,
wins — it is fewer moving parts and the driver can DMA it in bulk.
Instrument `cudaMemRangeGetAttribute` to see where pages actually live,
and treat any fault count above a handful per kernel as a bug. In Go,
keep managed memory out of the cgo boundary entirely: a Go slice header
is not a CUDA pointer, so the worker process owns both.

---

## 6. Getting data in and out: disk → CPU → GPU

**Why.** Every model, log batch and table starts on a device the GPU
cannot see, and the pipeline that carries it there is a chain of five
hops with five different speeds. The need is to know each hop's number
before launch day, because a chain runs at its slowest link and no
kernel optimisation moves that number.

**How.** A sequential NVMe read delivers ~7 GB/s on a Gen4 x4 drive and
~14 GB/s on Gen5 — close to what the CPU can pull from DRAM, which is why
the page cache absorbs so much of this path. From DRAM the bytes are
copied into a pinned (page-locked) staging buffer: only pinned memory
lets the driver program a real DMA engine that runs without the CPU
touching the bytes. PCIe carries it at 24-26 GB/s on Gen4, and HBM
receives it at up to 3.35 TB/s — the last hop is the fastest, which is
the opposite of a CPU-side pipeline and the reason the front of the chain
is where the time goes.

Pinned versus pageable is not a micro-optimisation. `cudaMemcpy` from
pageable memory has the driver copy your bytes into an internal pinned
buffer first, so the transfer is really `memcpy` plus DMA and the call
does not return until both finish. `cudaMemcpyAsync` from pinned memory
issues DMA and returns immediately, which is what makes overlap (entry
11) possible at all. One is a synchronous function with an async name;
the other is the async function.

```
disk:    nvme read (~7 GB/s) → page cache in DRAM → memcpy → pinned staging
host:    pinned ring buffer → cudaMemcpyAsync → PCIe 4.0 x16 (25 GB/s) → HBM
direct:  nvme → gpudirect storage (p2p over pci e) → GPU memory, no bounce
slowest: disk 7 GB/s → staging 25 GB/s → hbm 3.35 TB/s → disk wins the hour
```

GPUDirect Storage removes the middle: an `O_DIRECT` read from NVMe lands
in GPU memory over the same peer-to-peer PCIe fabric as GPUDirect RDMA,
saves one full DRAM round trip and a `memcpy`, and works well when the
dataset is far larger than memory. The bounce-buffer path (disk → DRAM →
HBM) is simpler, easier to cache and page-cache friendly, so it is the
default unless profiling says otherwise.

**Where it breaks.** A pageable `cudaMemcpyAsync` that silently
serialises: the copy blocks, the overlap you designed never happens, and
the profiler shows the copy engine idle. Disk reads stalling the queue —
one cold 4 KB random read at 100 µs while the GPU waits starves all 132
SMs, so prefetch depth matters more than queue depth. GPU OOM mid-stream
when the staging ring, the model and the batch all fit on paper. Double
copies nobody counted: `read()` into a Go slice, then a copy into a pinned
buffer, then DMA — three moves per batch. Pinned memory allocated and
freed per iteration, which is slow and fragments host RAM that the page
cache now cannot use.

**In practice.** Keep a ring of two or three pinned buffers sized to one
batch and recycle them: fill buffer *i* from disk while buffer *i−1*
copies and buffer *i−2* computes. Allocate with
`cudaHostAlloc(&p, n, cudaHostAllocDefault)` once, at startup. Measure
each stage separately — `fio` for disk, a `memcpy` loop for DRAM,
`bandwidthTest` for PCIe — then Nsight Systems for the assembled
pipeline. Prefetch in page-cache terms: read ahead at least one batch.
In Go, the host-side stages are ordinary `bufio` and `os.File` work; the
pinned buffers belong to the C worker, because the Go allocator will
never give you page-locked memory.

---

## 7. What a kernel is

**Why.** Everything above is plumbing for one object: the function you
actually wrote that runs on the device. The need is a precise definition
of what the host launches, what the device executes, and how a million
threads are assigned to it — because every later entry (divergence,
coalescing, tiling) is a property of that assignment.

**How.** A kernel is a `__global__` function: called from the host with
the `<<<>>>` bracket, executed on the device by every thread in the grid,
returning `void` — the caller is asynchronous, so there is no return slot
to wait on. Three qualifiers partition every function in a CUDA file.

| Qualifier | Callable from | Runs on | Typical use |
|---|---|---|---|
| `__host__` (default) | host | CPU | setup, allocation, validation |
| `__device__` | device | GPU | helpers called by kernels |
| `__global__` | host | GPU | the kernel itself, returns void |
| `__host__ __device__` | both | either | small maths shared by both sides |

The grid is a 3-D array of blocks, each a 3-D array of threads, and built-in
variables expose the coordinates: `blockIdx` and `threadIdx` locate the
thread, `blockDim` gives its block's shape and `gridDim` the grid's.
Total threads is blocks times threads-per-block — 256 blocks of 256
threads is 65,536 — and the whole skill of kernel writing is assigning
useful work to each one without recomputing or racing.

```cuda
__global__ void add(const float *a, const float *b, float *c, int n) {
  int i = blockIdx.x * blockDim.x + threadIdx.x;
  if (i < n) c[i] = a[i] + b[i];          // guard: grid may overshoot n
}

int main() {
  int n = 1 << 24;
  CUDA_CHECK(cudaMalloc(&d_a, n * sizeof(float)));
  CUDA_CHECK(cudaMemcpy(d_a, h_a, n * sizeof(float), cudaMemcpyHostToDevice));
  add<<<(n + 255) / 256, 256>>>(d_a, d_b, d_c, n);
  CUDA_CHECK(cudaGetLastError());          // launch itself
  CUDA_CHECK(cudaDeviceSynchronize());     // and the execution
}
```

When one thread cannot own every element, it loops: the grid-stride
pattern starts at `blockIdx.x * blockDim.x + threadIdx.x`, steps forward
by `gridDim.x * blockDim.x` each iteration until it passes `n`, and visits
every element exactly once. The grid can then be sized for what the
hardware likes instead of for the data, and the kernel handles any `n`.

```
grid:    block 0 0 → block 1 0 → block 0 1 → block 1 1
block:   warp 0 → warp 1 → ... → warp 31 (1024 threads)
warp:    thread 0 → thread 1 → ... → thread 31 (one instruction)
launch:  4 blocks × 256 threads = 1024 threads → one grid, one kernel
```

**Where it breaks.** Off-by-one in the ceil-divide, so the last block
reads past the end of an array — the `if (i < n)` guard above is not
optional, and without it you get an illegal access three kernels later.
A grid computed as `n / 256` dropping the remainder, silently computing
most of the answer. Forgetting `CUDA_CHECK` on the launch line, so a
`cudaErrorInvalidDeviceFunction` ("no kernel image is available") surfaces
at an unrelated sync. Host-only calls inside a kernel — `malloc`, not
`printf` — earn error messages that never mention which side you are on.
Indexing with `threadIdx` alone in a multi-block grid, so only block 0
does the work and every other block rewrites the same cells.

**In practice.** Compile with `nvcc -O3 -arch=sm_90 add.cu -o add`, or
`-arch=native` to match the installed card — omitting either is how "no
kernel image is available" happens. Size grids as
`(n + threads - 1) / threads` and keep blocks at 128-512 threads until a
measurement says otherwise. `cudaGetLastError()` right after the bracket
keeps the traceback honest, and `printf` inside a kernel — buffered until
synchronisation — makes a sync plus a print the cheapest debugger. In Go,
run the binary as a worker and take its exit status as the error channel.

---

## 8. Execution model: warps, divergence and occupancy

**Why.** The device will run your kernel either way — badly or well —
and the difference is set by two facts about how warps execute and one
budget about how many fit. The need is to predict performance from the
source before the profile comes back, and to recognise a "slow kernel"
that is really a scheduling problem.

**How.** A warp has a single program counter: its 32 lanes take the same
instruction each cycle, so scalar-looking code is implicitly vectorised.
When a branch splits the warp, both paths run serially — the hardware
keeps an active-lane mask per path, executes one, then the other, then
reconverges — and short if-statements are predicated instead: one
instruction carrying per-lane predicates, no split at all. The bill is
per divergent warp, not per thread: a warp whose 32 lanes take 32
different paths performs 32 serial executions of a body meant to run
once, and the lanes that wanted the common path wait behind all of it.

Occupancy is the budget. Resident threads per SM is the minimum of
three limits: registers (65,536 per SM divided by your per-thread
count), shared memory (228 KB divided by what each block claims), and
the hardware cap — 2,048 threads and a few dozen blocks per SM. Whichever
binds first sets the number: a 64-register kernel gets 1,024 threads,
50% of the cap; a 96-register kernel gets 672 (21 warps) and 33%.

100% is not the goal, and this is where the CPU analogy misleads. You
need enough resident warps that a stalled one is replaced — 32-64 per
SM suits a bandwidth-bound kernel — and past that, extra residency only
steals registers and shared memory from code that would use them. A
compute-bound kernel at 25% occupancy with more registers per thread can
beat the 100% version outright. `__launch_bounds__(maxThreads,
minBlocksPerSM)` tells the compiler the shape you intend, so it budgets
registers toward that instead of assuming the maximum.

**Where it breaks.** Tree traversal, hash probes and early-exit search
routines: branch efficiency falls to 20-40% and each warp does several
lanes' worth of serial work for a problem that looked embarrassingly
parallel on paper. Register spilling — too many live values at once push
locals into memory, which is invisible until `-Xptxas -v` prints "spill
stores" and the kernel suddenly has global traffic. Shared memory
crowding (entry 4) launches successfully and runs at one eighth
occupancy, which the profiler reports as memory latency. Chasing 100%
occupancy: tuning block size up to 1,024 threads for a kernel whose
working set then no longer fits, measuring once and hard-coding the
number forever. `__syncthreads()` reached by only some threads of a
block is undefined behaviour, not a slow path.

**In practice.** Run `nvcc -Xptxas -v kernel.cu` first — registers per
thread, spill counts and static shared memory are three lines that
decide more than any later tuning. In Nsight Compute read the warp stall
breakdown (`stall_barrier`, `stall_short_scoreboard`, `stall_wait`,
`stall_math_pipe_throttle`) before the throughput numbers: they name the
resource each warp is waiting for. Knobs: tile size (trades shared memory
against registers), `__launch_bounds__`, and the blunt
`--maxrregcount` per translation unit. In Go there is nothing to turn —
the shape lives in the kernel — but your batch size decides whether the
grid is big enough to fill the machine.

---

## 9. Memory access in depth: coalescing and bank conflicts

**Why.** The 3.35 TB/s in the datasheet is reachable only if one warp's
32 accesses collapse into a few transactions; scatter them and the same
kernel runs an order of magnitude slower with no extra arithmetic. The
need is a rule you can apply by reading the index expressions.

**How.** Global memory is serviced in 32-byte sectors. When a warp issues
32 four-byte loads the memory system merges whatever it can: contiguous
and 128-byte aligned means 128 useful bytes in 4 sectors, which is the
peak. Every element of stride doubles the sector count — stride 2 gives 8
sectors, and a stride of one full cache line per lane gives 32 sectors
for 128 bytes of useful data, an 8× bandwidth tax paid before any maths.
L1 does not rescue streaming data, because there is no reuse to cache.

Shared memory has its own version of the same problem. It is 32 banks,
four bytes wide, one per lane per cycle: lane 0 to bank 0, lane 1 to
bank 1, ..., lane 31 to bank 31, all in one access. Two lanes landing in
one bank cannot be served together, so the access serialises — a conflict
of *k* lanes costs *k* cycles instead of one. The standard fix is padding:
declaring `float tile[33][32]` shifts each row's start bank by one, and
the conflict disappears.

Whether any of it matters is decided by arithmetic intensity — FLOPs per
byte of HBM traffic. An H100 delivers 67 TFLOPS of FP32 against 3.35
TB/s, so its ridge point is ~20 FLOP/byte; an A100's is ~9.6. Below the
ridge your kernel is memory-bound and wins come from moving fewer bytes
(entry 6, entry 10), above it from computing faster.

```
coalesced: lane 0 → lane 1 → lane 2 → ... → lane 31 → 4 sectors, 128 B
strided:   lane 0 → lane 8 → lane 16 → ... → lane 31 → 32 sectors
shared:    lane 0 bank 0 → lane 1 bank 1 → ... → lane 1 bank 0 → conflict
```

**Where it breaks.** Array-of-structs layouts: a 64-byte `struct {float
x, y; int id; char pad[48];}` makes every field read a separate
strided pass. Reading a column of a row-major matrix — the exact access
the naive matrix multiply performs — turns every load into its own
transaction. Transposes, and anything built on `A[j * n + i]`. A base
pointer misaligned by four bytes, which doubles sectors for the whole
kernel and shows up as a mysterious 2× gap against your bandwidth
estimate. Padding removed during a refactor because "the tile size
didn't change" — but the row stride did. Assuming the L2 will smooth over
a pattern with no temporal reuse.

**In practice.** Profile with Nsight Compute's Memory Workload Analysis
section: global sectors per request should sit near 1 for contiguous
access, and the shared-memory bank-conflict counter (`l1tex__data_bank_`
`conflicts_pipe_lsu_mem_shared_op_ld`) should read 0. Fixes in order:
struct-of-arrays instead of array-of-structs, vectorised 16-byte loads
(`float4`, aligned), padded shared tiles, and double buffering so the
next tile loads while the current one computes. Put the kernel on the
roofline: if it sits on the memory slope, no arithmetic optimisation will
move it, and if it sits on the compute slope, stop counting sectors.

---

## 10. Writing a custom kernel, end to end: tiled matrix multiply

**Why.** Every other entry is theory until you have written one kernel
that beats its naive version for a reason you can name in advance.
Matrix multiply is the specimen: memory-bound in its naive form, one
idea away from fast (reuse through shared memory), and its speedup is
predictable from arithmetic before the profiler confirms it.

**How.** The naive kernel is six lines and wrong in exactly one way. Each
of the n² outputs reads a whole row of A and a whole column of B, so the
device moves 2n³ floats — 8n³ bytes — to perform n³ fused multiply-adds.
At n = 4096 that is 550 GB against 3.35 TB/s of HBM, a 164 µs floor, and
the column reads are strided by n floats so they are not even coalesced
(entry 9): real time lands several times higher.

```cuda
__global__ void matmul_naive(const float *A, const float *B, float *C,
                             int n) {
  int row = blockIdx.y * blockDim.y + threadIdx.y;
  int col = blockIdx.x * blockDim.x + threadIdx.x;
  if (row >= n || col >= n) return;
  float acc = 0.0f;
  for (int k = 0; k < n; ++k)
    acc += A[row * n + k] * B[k * n + col];   // column read, stride n
  C[row * n + col] = acc;
}
```

The tiled kernel fixes the traffic and leaves the arithmetic alone. A
block loads square tiles of A and B into `__shared__` memory, calls
`__syncthreads()`, computes from the shared copies, and repeats for the
next tile. Every element is then fetched n/BS times instead of n — a
factor of BS, 32× at BS = 32 — and each of those fetches is contiguous,
so both the sector count and the traffic collapse together.

```cuda
#define BS 32                     /* 1,024 threads; BS 16 gives 256 */

__global__ void matmul_tiled(const float *A, const float *B, float *C,
                             int n) {
  __shared__ float As[BS][BS];
  __shared__ float Bs[BS][BS];
  int row = blockIdx.y * BS + threadIdx.y;
  int col = blockIdx.x * BS + threadIdx.x;
  float acc = 0.0f;
  int tiles = (n + BS - 1) / BS;
  for (int t = 0; t < tiles; ++t) {
    int aCol = t * BS + threadIdx.x;
    int bRow = t * BS + threadIdx.y;
    As[threadIdx.y][threadIdx.x] =
        (row < n && aCol < n) ? A[row * n + aCol] : 0.0f;
    Bs[threadIdx.y][threadIdx.x] =
        (bRow < n && col < n) ? B[bRow * n + col] : 0.0f;
    __syncthreads();
    for (int k = 0; k < BS; ++k)
      acc += As[threadIdx.y][k] * Bs[k][threadIdx.x];
    __syncthreads();
  }
  if (row < n && col < n) C[row * n + col] = acc;
}
```

The host side needs the same discipline. One macro turns every CUDA call
into a checked call, and the launch is checked twice — once for
acceptance, once for execution:

```c
#define CUDA_CHECK(call)                                              \
  do {                                                                \
    cudaError_t err_ = (call);                                        \
    if (err_ != cudaSuccess) {                                        \
      fprintf(stderr, "%s:%d: %s\n", __FILE__, __LINE__,              \
              cudaGetErrorString(err_));                              \
      exit(1);                                                        \
    }                                                                 \
  } while (0)

dim3 block(BS, BS);
dim3 grid((n + BS - 1) / BS, (n + BS - 1) / BS);
CUDA_CHECK(cudaMalloc(&dA, (size_t)n * n * sizeof(float)));
CUDA_CHECK(cudaMemcpy(dA, hA, (size_t)n * n * sizeof(float),
                      cudaMemcpyHostToDevice));
matmul_tiled<<<grid, block>>>(dA, dB, dC, n);
CUDA_CHECK(cudaGetLastError());
CUDA_CHECK(cudaDeviceSynchronize());
```

The walkthrough, in order:

1. Pick the tile and block shape: `dim3 block(BS, BS)` gives BS² threads,
   and `dim3 grid(ceil(n/BS), ceil(n/BS))` covers the matrix exactly once.
2. Each thread owns one output cell, identified by `blockIdx * BS +
   threadIdx`, and bounds-checks every global load — tiles at the edges
   are partial, and out-of-range positions load 0.0f rather than fault.
3. Cooperatively fill `As` and `Bs` (coalesced: `threadIdx.x` walks
   contiguous columns), then `__syncthreads()` so all 1,024 threads see
   the tile.
4. Run the inner loop over BS — this is the FMA work, entirely from
   shared memory at ~20-30 cycles instead of 400-600 — then
   `__syncthreads()` again before anyone overwrites the tile for `t+1`.
5. Compile with the commands below, keeping a `-g -lineinfo` build for
   the debugger and the profiler.
6. Validate: run both kernels on the same input and compare with a
   relative tolerance (`|a-b| <= 1e-3 * |b|` for float at n ≥ 1024) —
   never `==`, because float summation order differs between versions.

```bash
nvcc -O3 -arch=sm_90 matmul.cu -o matmul
nvcc -O3 -g -lineinfo -arch=native matmul.cu -o matmul_dbg
```

**Where it breaks.** A missing second `__syncthreads()`, so fast threads
reload `As` while slow ones still multiply from it — wrong answers that
appear only sometimes and only at certain sizes. Bounds checks written
for `row` but not `aCol`, so the last tile reads one row past the end and
the failure surfaces kernels later as an illegal address. Choosing BS =
32 with a fatter inner loop than this one: 1,024 threads × 64 registers
exceeds the per-SM register file, occupancy halves, and the spill shows
up as local-memory traffic. Comparing against a double-precision CPU
reference with exact equality — float accumulation over 4,096 terms
drifts. Launching with `grid` computed from `n/BS` instead of the
ceil-divide, which silently computes only the aligned sub-matrix.

**In practice.** Block size: start at 128-256 threads (BS = 16), then
measure 256, 512 and 1,024 — the right answer depends on register use and
the rest of your pipeline, and 1,024 is not automatically better. Expect
the tiled kernel to beat naive by 10-30× at n = 4096, and expect cuBLAS
`sgemm` to beat your tiled kernel by another 2-5×: it uses tensor cores,
double buffering and hand-tuned shapes. Metrics that prove the mechanism
— global load sectors per request falling from ~8 to ~1, L2 hit rate
rising, and achieved bandwidth moving from a few hundred GB/s toward
2 TB/s. Build with `nvcc -O3 -arch=native matmul.cu -o matmul`, time with
events rather than the host clock, and in Go submit matrices in batches
big enough that launch overhead stays under 5% of the run.

---

## 11. Streams and events: overlapping copy and compute

**Why.** The default path is strictly serial: host-to-device copy, then
kernel, then device-to-host copy, each waiting for the last, so the copy
engines idle while the SMs work and the SMs idle while the copies run.
The need is a way to say "these are independent" that the hardware can
act on without asking the CPU.

**How.** A stream is a FIFO queue on the device. Work in one stream runs
in submission order; work in different streams has no enforced order, and
the H2D/D2H copy engines are physically separate from the SMs, so a copy
in stream A can genuinely run beside a kernel in stream B. The legacy
default stream (NULL) is the trap: it synchronises with every other
blocking stream, so one `cudaMemcpy` on it both waits for and blocks your
carefully pipelined work.

The shape that falls out is double or triple buffering. With two pinned
buffers you submit `copy[i]` into buffer *i* on stream `s_copy`, launch
the kernel reading buffer *i−1* on stream `s_run`, and refill *i−1* while
the current one computes — each stage always has someone else's work to
hide behind.

```cuda
cudaStreamCreate(&s_copy);
cudaStreamCreate(&s_run);
cudaEventRecord(&ready[i], s_copy);            // buffer i is filled
cudaStreamWaitEvent(s_run, &ready[i], 0);      // s_run waits for it
cudaMemcpyAsync(d_in, h[i], bytes, cudaMemcpyHostToDevice, s_copy);
kernel<<<grid, block, 0, s_run>>>(d_out, d_in, n);
```

Events are both the ordering and the timing primitive inside that.
`cudaEventRecord` marks a point in a stream, `cudaStreamWaitEvent` makes
another stream wait on it without involving the host, and
`cudaEventElapsedTime` between two records gives device-side timing at
roughly 0.5 µs resolution — host clocks measure queueing, not compute
(entry 2).

For pipelines of many small kernels there is one more layer: CUDA Graphs.
Capture the whole sequence once — begin capture on a stream, submit the
launches, end capture — instantiate it, and replay it per iteration. One
launch call replaces hundreds, so the 5 µs of per-launch overhead stops
dominating kernels that run 3 µs each.

```
serial:  copy 0 → kernel 0 → copy 1 → kernel 1 → copy 2 → engines idle
overlap: copy i+1 on engine → kernel i on sms → copy i+2 on engine
graph:   capture 400 launches → instantiate → replay → one call
```

**Where it breaks.** `cudaMemcpyAsync` from pageable memory returns only
after the copy, so the overlap you designed never happens and the
timeline shows one long serial strip (entry 6). A missing event between
streams: the kernel reads a half-written buffer, which shows up as rare
wrong answers at some sizes rather than any error. A library call that
uses the default stream behind your back, serialising everything.
Creating and destroying an event per iteration — the churn costs more
than the dependency — or reusing one event across stages that overlap.
Graph capture failing outright because `cudaMalloc` or a synchronising
call happened inside the capture window, and errors inside a graph node
surfacing at the launch, not at the node. Timing a copy and a kernel with
events recorded in the wrong order, then believing a negative elapsed
time is a hardware fault.

**In practice.** Two or three streams and a ring of pinned buffers is the
whole recipe; more streams buy nothing once the copy engine is saturated.
One reusable event per dependency edge, `cudaEventDisableTiming` for
pure ordering. Judge overlap directly: `nsys profile ./app`, then check
whether the copy strip and the kernel strip in the timeline overlap — if
copy plus kernel time equals the serial sum, something is pageable or on
the default stream. Reach for CUDA Graphs when a step is 20+ launches
under 10 µs each. In Go, drive the whole pipeline from one worker loop so
each iteration is one submit, not one syscall per stage.

---

## 12. Choosing the tool: CUDA vs libraries vs higher layers

**Why.** Most kernels should never be written: the common operations are
already implemented at near-peak bandwidth, and a hand-written version
usually loses. The need is a decision rule and an honest map of what
each layer costs in portability, control and time.

**How.** Start with the toolchain, because it explains the portability
rules. `nvcc` splits a `.cu` file: host code goes to the ordinary C++
compiler, device code is lowered to PTX — a documented virtual ISA, a
register machine independent of any particular chip — and `ptxas`
assembles PTX into SASS, the actual machine code of an `sm_XX`
architecture. `-arch=sm_90` targets Hopper directly;
`-arch=compute_90` embeds PTX, which the driver JIT-compiles the first
time it meets that code on unknown hardware. That is the forward
compatibility rule: PTX keeps running on newer GPUs (paying a JIT pause
once), SASS does not — an `sm_70` cubin contains no instructions for
`sm_90`. Ship several `--gencode` slices, or rely on embedded PTX.

Then libraries, and they come first almost every time. cuBLAS and
cuSOLVER for dense linear algebra, cuFFT for transforms, cuDNN and
cuSPARSE for the operations neural networks and sparse work are built
from, NCCL for collectives across GPUs — each tuned for years against
the roofline of several architectures.

Hand-writing is justified by three situations: fusion (pointwise,
activation and normalisation merged into one pass so you pay one memory
round trip instead of three), operations libraries deliberately do not
expose (custom quantisation, per-tensor scaling, novel layers), and
memory-bound kernels where the arithmetic is trivial but the traffic is
not — exactly the class libraries leave on the table. Above the raw API
sit higher layers: Triton (a Python DSL that thinks in blocks and lets
the compiler pick tile shapes), CUTLASS/CuTe (C++ templates for GEMM-class
kernels at peak), and HIP or SYCL when portability to other vendors is the
actual requirement.

| Route | Effort | Portability | Reach for it when |
|---|---|---|---|
| cuBLAS / cuFFT / cuDNN / NCCL | zero kernel lines | NVIDIA only | the op exists and is hot |
| Hand-written CUDA | days to weeks | NVIDIA only | fused, novel or memory-bound |
| CUTLASS / CuTe | steep templates | NVIDIA, tunable | GEMM-shaped and needs peak |
| Triton | moderate, Python | NVIDIA and AMD | fusions libraries will not fuse |
| HIP / SYCL | a rewrite | vendor-neutral | avoiding a single vendor |

**Where it breaks.** Writing your own GEMM and discovering cuBLAS is 10×
faster — the most common first kernel, and a good lesson in why the
order above is an order. Calling five library ops per layer, each
launching its own kernel, so launch overhead and five separate memory
round trips dominate a fused pass that would do one. Compiling only
`sm_70` SASS and meeting a newer card: "no kernel image is available for
execution on the device" at first launch. Relying on JIT'd PTX in a
latency-sensitive startup path and eating a multi-second compile on cold
start. Assuming portability means performance portability — a HIP port
that runs at half the bandwidth is portable and slow. A library version
pinned to a CUDA release that the container image does not have.

**In practice.** Decision order: profile the roofline, check the library,
check Triton or CUTLASS, and only then open a `.cu` file. Build with
`nvcc -O3 -arch=native` (CUDA 12+ detects the installed card), list what
a binary actually contains with `cuobjdump --list-elf` and
`cuobjdump --list-ptx`, and keep at least one `compute_XX` PTX slice so
the binary survives the next hardware. Fuse when profiling shows kernels
under ~50 µs doing memory-bound work. In Go, all three routes live inside
the worker process from entry 14 — the Go side submits data and reads
results.

---

## 13. Debugging and profiling kernels

**Why.** A GPU fault is reported asynchronously, in a code that names a
context rather than a line, and the two common outcomes are a crash three
calls later and a kernel that runs perfectly while computing the wrong
answer. The need is a checking discipline plus two tools that
disambiguate before you start guessing.

**How.** The discipline is one macro (entry 10) around every CUDA call,
and explicit `cudaDeviceSynchronize` checkpoints that turn "the error
surfaced somewhere" into "the error surfaced after kernel *k*". Without
the checkpoints you are debugging the API's error queue, not your code.

Two sanitizers then run the code instrumented. `compute-sanitizer
--tool memcheck` finds out-of-bounds and misaligned global accesses with
the offending instruction; `--tool racecheck` finds shared-memory races
between threads of a block; `--tool initcheck` reports reads of
uninitialized device memory (the silent-wrong-answer class); `--tool
synccheck` catches barrier misuse such as `__syncthreads()` in
divergent control flow. Each is slow — minutes for a second of compute —
so run them on the smallest reproducer.

Nsight Compute answers "why is this kernel slow", per kernel: a roofline
plot placing you on the memory or compute slope, achieved occupancy, and
the warp stall breakdown — `stall_barrier` (waiting on other threads),
`stall_short_scoreboard` (waiting on a memory load), `stall_memory_`
`throttle` (the memory system is saturated). Nsight Systems answers "what
was the machine doing", across kernels, copies, streams and host threads
in one timeline — the tool you reach for when the GPU is idle and the
question is on the CPU side.

| Symptom | Likely cause | First tool |
|---|---|---|
| `cudaErrorIllegalAddress` | out-of-bounds indexing | `compute-sanitizer --tool memcheck` |
| `cudaErrorMemoryAllocation` | OOM plus context overhead | `nvidia-smi`, smaller batch |
| device-side assert / bad result | illegal index in device code | rebuild `-G`, printf, sanitizer |
| Xid 31/43/79 in `dmesg` | device fell off the bus, VRAM ECC | [linux/DESIGNS](../linux/DESIGNS.md) |
| no error, wrong answer | uninitialised memory, race | `initcheck`, `racecheck` |
| "no kernel image is available" | missing or wrong `-arch` | rebuild with `-arch=native` |

**Where it breaks.** Checking only the launch line and then blaming the
kernel for an error that surfaced at the next sync (entry 2) — every
suspicious line needs its own check, or the traceback is fiction.
Assuming `nvidia-smi` is a health check: its `Utilization [%]` is a
sample over a window, so a stream of 3 µs kernels reads as 100% when the
machine is mostly idle and 0% when it is not — and it says nothing about
ECC errors or a kernel that computes garbage. Profiling the wrong thing:
Nsight Compute on a launch-bound workload when Nsight Systems was needed,
or a single kernel profiled without the copies that precede it. Debug
builds (`-G`) disable optimisation and change register allocation, so a
race that reproduces there may vanish in `-O3` and vice versa.
`cuda-memcheck` from older toolkits silently missing Ampere-era
instructions — use `compute-sanitizer`.

**In practice.** Order of operations: check macro everywhere, one
synchronisation checkpoint per kernel in a suspect sequence, sanitizer
run on the small case, then Nsight Compute for the hot kernel and Nsight
Systems for the pipeline. Keep `-lineinfo` in release builds so
Nsight can map SASS back to source. Watch three counters daily in
whatever dashboard you have: device memory used, ECC single-bit
corrected error rate, and Xid events — and in Go, make the health check
run a tiny real kernel (add two vectors, verify the sum) rather than
calling `nvidia-smi`, which only proves the driver answers.

---

## 14. GPUs in production: containers, partitioning and Go

**Why.** The machine is expensive, so it is shared; the device is
stateful, so it is leased; and your service is written in Go, which does
not speak CUDA. The need is isolation with known boundaries and a process
model where a GPU fault is one worker's problem, not the fleet's.

**How.** Containers get the device from the NVIDIA Container Toolkit:
the `nvidia` runtime injects the driver libraries and device nodes, CDI
describes them declaratively, and `--gpus all` (or
`NVIDIA_VISIBLE_DEVICES=0,1`) selects which ones a container may open.
The cgroup limits CPU and host RAM; the GPU is not partitioned by any of
that — without extra machinery, a container sees whole GPUs and may fill
them. Turn on persistence mode (`nvidia-smi -pm 1`) so the driver stays
loaded: otherwise every process's first context pays a seconds-long
re-init.

When one whole GPU is more than one tenant needs, there are two answers.
MIG (on A100 and H100) partitions the device into isolated instances —
an A100 splits into up to seven instances (1g.5gb, 2g.10gb, 3g.20gb,
7g.40gb), each with its own SMs, memory controller and L2, so a runaway
in one slice cannot touch another's memory or timing, and each appears
to the driver as a separate GPU. MPS instead lets several processes
share the whole GPU concurrently, time-slicing their warps: higher
utilisation, no isolation beyond fairness. The same
sharing-versus-isolation tension at node scale is what
[distributed](../distributed/README.md) works on; here it fits in one
die. Without either, the default stays one process per GPU, because
a CUDA context is expensive — tens to hundreds of megabytes of device
memory and a setup cost paid per process.

For Go there are three realistic routes, in the order most teams should
try them. First, run a Python or C++ worker process and talk to it over
stdin/JSON or gRPC: no cgo, a crash or a wedged context kills only the
worker, and you can restart it with a backoff. Second, call the CUDA
runtime through cgo when you need the control — the cost is real: a
blocking cgo call parks its P and holding a lock across it stalls the
whole scheduler, as [internals](../internals/README.md) and the cgo
notes in [linux/DESIGNS](../linux/DESIGNS.md) describe. Third, a Go
library with CUDA bindings such as gorgonia, for training-shaped code
where its graph abstraction fits — do not expect the kernels you would
have written by hand (entry 12).

**Where it breaks.** Kubernetes with no GPU scheduler: pods are placed by
CPU and memory alone, so two jobs land on one card, both see 80 GB in
`nvidia-smi` and one dies with OOM at hour three. Time-slicing without
MIG advertised as isolation: the processes still share one context pool
and one fault domain. A forgotten process holding a context — a leaked
worker keeps hundreds of megabytes and its stream alive, and the card
looks healthy while a quarter of it is unusable. Health checks that run
`nvidia-smi` and nothing else: they miss Xid events, ECC escalation and
the far more common case of a kernel that runs and returns garbage.
Driver/toolkit skew in images — the container ships newer CUDA than the
host driver supports and fails at first launch with "driver version is
insufficient for CUDA runtime version". OOM caused by several MPS clients
whose combined allocations exceed the card, reported as one client's
allocation failure.

**In practice.** Inventory first: `nvidia-smi -L`, `nvidia-smi topo -m`,
`nvidia-smi -pm 1`, and for shared boxes `nvidia-cuda-mps-control -d`.
In Kubernetes, use the device plugin with either MIG profiles or
time-slicing config, and a scheduler that actually places on
`nvidia.com/gpu` — the default scheduler knows nothing about them. Set
`CUDA_VISIBLE_DEVICES` explicitly per worker so a bug cannot reach card
2. Export DCGM fields (device memory, ECC, Xid, SM occupancy) instead of
scraping `nvidia-smi` per second. In Go, the working pattern is one
supervised worker process per card: JSON over stdin for jobs, a
readiness probe that runs a small real kernel, and a restart counter
that pages when it climbs — the GPU is then just another resource with a
lease, a quota and a failure mode you have already rehearsed.
