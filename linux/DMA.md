# Linux DMA internals: NICs, storage and Go userspace

Direct Memory Access (DMA) lets a device transfer data to or from system
memory without the CPU copying every byte. The CPU still allocates buffers,
sets up descriptors, grants ownership, handles completions and processes
the resulting data. DMA is a hardware-and-kernel protocol—not a Go API and
not a synonym for zero-copy.

This guide follows a DMA address from a Linux driver to a device, then
explains descriptor rings, cache ownership, IOMMU protection, VFIO/UIO and
what Go applications can and cannot safely control. See the [networking
guide](NETWORKING.md) for NIC queue paths and [disk management](DISK.md) for
the storage stack.

## 1. One buffer has several addresses

A CPU normally accesses memory through a **virtual address**. The MMU
translates it through page tables to physical RAM. A device issues DMA using
a **DMA address** (often called an I/O virtual address or IOVA). The DMA
address can differ from both the CPU virtual and physical address:

```text
CPU pointer X ── CPU page tables ──► physical RAM Y
                                       ▲
                                       │ IOMMU mapping (optional)
device DMA address Z ──────────────────┘
```

The IOMMU translates device requests and can limit a device to specific
memory ranges. Some simple systems expose physical addresses directly;
others use IOMMUs, bus bridges, bounce buffers or combinations of these.
Devices also have addressing limits (a DMA mask): older hardware might only
address a limited range. A driver must not assume that a CPU pointer, a
physical address and a DMA address are interchangeable.

Linux drivers use `dma_addr_t` and the kernel DMA API (`dma_map_*`,
`dma_alloc_coherent`, `dma_sync_*`, `dma_unmap_*`). The API chooses the
platform-specific mapping, handles IOMMU and bounce-buffer details, checks
the device's addressing limits, and returns a device-valid address. The
driver passes that address to hardware in a descriptor; the driver releases
the mapping when the transfer or buffer lifetime ends.

**Rule:** a kernel driver should never hand a device an arbitrary kernel
virtual address or guess a physical address. A userspace Go program has no
portable operation that replaces the DMA API.

## 2. Coherent and streaming mappings

The DMA API distinguishes two common buffer lifecycles:

| Mapping | Typical lifetime | Use | CPU/device ownership |
|---|---|---|---|
| Coherent | Device/driver lifetime | Descriptor rings, small control structures | Both sides can observe updates without explicit cache sync; ordering barriers may still be required |
| Streaming | One transfer or a reusable data buffer | Packet payloads, storage I/O buffers | Map/unmap or sync when ownership changes; direction is explicit |

“Coherent” does not mean “no ordering rules.” The CPU may reorder ordinary
stores to a coherent descriptor. A driver must publish descriptor fields in
the order required by the hardware protocol, using the kernel's memory
barriers before setting a valid/ownership bit or ringing a doorbell.

For a streaming receive mapping, the device owns the buffer while it writes.
The CPU may inspect it only after the driver observes completion and performs
the necessary sync-for-CPU on a non-coherent platform. Before returning that
buffer to hardware, the driver syncs for device or remaps it. The DMA API
specifies whether an operation can fail; drivers check `dma_mapping_error`
and handle failure rather than queueing an invalid DMA address.

## 3. Descriptor rings and ownership

NICs and NVMe controllers commonly communicate through submission and
completion queues in host memory. A ring is not just an array: producer and
consumer indices, phase bits, descriptor ownership and memory barriers
coordinate two agents that can run concurrently.

### Network receive (RX)

1. Driver allocates RX buffers and maps them for device writes.
2. Driver places DMA addresses and buffer sizes in free RX descriptors.
3. Driver makes descriptors visible and notifies the NIC (queue tail or
   doorbell).
4. NIC receives a frame, DMA-writes bytes into a buffer and marks its
   descriptor complete.
5. Interrupt/NAPI processing reclaims completed descriptors, syncs for CPU,
   passes packet data up the stack and replenishes buffers.

### Network transmit (TX)

The driver takes packet fragments, maps them for device reads, fills TX
descriptors, publishes them and notifies the NIC. On completion it unmaps
and releases the packet references. Scatter/gather lets descriptors refer
to multiple memory regions; checksum, segmentation and receive-coalescing
offloads move work between the CPU and NIC.

### Storage

The block layer converts file-system requests into bios and block requests;
the device driver translates those into controller commands referencing DMA
buffers or scatter/gather lists. NVMe uses submission queues for commands and
completion queues for results; the controller DMAs data to/from mapped host
buffers and notifies the host when commands complete. The file-system and
block-layer contracts remain above this mechanism, including ordering,
flushes and error reporting.

```text
file write → page cache → filesystem → bio/request → blk-mq
           → NVMe submission entry → DMA transfer → completion queue
```

## 4. Why “zero-copy” is not one property

DMA means the device moves bytes without the CPU copying each byte. A
zero-copy path is a claim about copies between particular software layers.
The CPU may still copy data before DMA, and a device driver may use a bounce
buffer even though the controller performs DMA. IOMMU mapping, pinning,
scatter/gather setup, cache maintenance, queue synchronization and completion
processing all have costs.

Examples:

* Buffered file I/O usually copies between the process buffer and page cache;
  the block device then DMAs between RAM and the device.
* `O_DIRECT` attempts to avoid the page cache for that I/O. It does not make
  a disk DMA directly into an arbitrary Go slice without kernel setup.
* `sendfile` can avoid a userspace copy from a cached file page to a socket.
  The NIC driver still manages its DMA mappings and TX ring.
* AF_XDP can expose UMEM and rings to userspace; zero-copy mode is optional
  and depends on NIC/driver support. Its XDP program and userspace ring logic
  still manage packet ownership.
* VFIO can give a userspace driver controlled device access with IOMMU
  mappings. It transfers responsibility for device programming, DMA lifetime,
  interrupts, reset and error handling to that userspace driver.

Optimize the measured copy and queue costs, not the label “zero-copy.” A
copy can be cheaper than pinning pages or passing a tiny buffer through an
elaborate asynchronous completion path.

## 5. DMA APIs exposed to userspace

### VFIO: full device assignment

VFIO exposes device regions and interrupts through file descriptors and
ioctls, with IOMMU-backed DMA mappings. The device is normally detached from
its host driver and assigned as part of an IOMMU group; group isolation and
hardware topology determine the safe ownership unit. Newer systems also use
IOMMUFD for managing I/O address spaces. VFIO is the typical Linux framework
for virtual-machine device assignment and is also used by specialized
userspace drivers.

VFIO does **not** make a device self-describing. A userspace driver still
needs the hardware specification, has to set up queues and interrupts, honor
memory ordering and reset/error rules, and cleanly release the device. An
unsafe no-IOMMU path removes a key isolation property and is not a routine
performance setting.

### UIO: a narrow userspace driver boundary

UIO is suitable for some simple devices whose control logic can live in
userspace while a small kernel module exposes memory regions and
interrupt-notification support. It is not a universal driver interface and
does not automatically provide the full IOMMU isolation and DMA management
model VFIO offers. Some UIO drivers, such as `uio_dmem_genirq`, integrate
with the kernel DMA mapping API to expose suitable allocated memory; this
depends on the kernel driver and platform.

### DMA-BUF: share a buffer between kernel subsystems

DMA-BUF is a kernel framework for sharing buffers between devices and
subsystems such as camera, graphics, display and video. Userspace passes a
DMA-BUF file descriptor between APIs; exporters/importers coordinate mapping
and synchronization. DMA-BUF heaps can allocate certain kinds of buffers
through a userspace interface. It is not a generic way to hand an arbitrary
Go allocation to any NIC or disk.

## 6. Go: normal network and storage I/O

For ordinary Go programs, `net.Conn`, `net.PacketConn`, `os.File` and
`io.Reader`/`io.Writer` are the right abstraction. The kernel's network and
block drivers do the DMA. A Go `[]byte` passed to `Read` or `Write` is a
userspace buffer participating in a system call; it is not itself a DMA
descriptor.

```go
conn, err := net.DialTimeout("tcp", address, 3*time.Second)
if err != nil {
    return err
}
defer conn.Close()

// The Go runtime and kernel handle socket readiness and the NIC driver
// handles descriptor rings, DMA mappings, interrupts and completions.
if _, err := conn.Write(payload); err != nil {
    return err
}
```

Go's runtime may move goroutines between OS threads, and Go values are
managed by the garbage collector. Keeping a Go slice reachable or using
`runtime.Pinner` does not create a device DMA mapping, does not return an
IOVA, and does not perform device-specific cache synchronization. Pinning
can be one prerequisite for some foreign-library APIs, but is not a DMA
protocol. Never program a NIC/NVMe descriptor with `uintptr(unsafe.Pointer(&b[0]))`
and assume the device can safely DMA to it.

`runtime.Pinner` is for supported Go-to-C pointer lifetimes. It does not
replace a kernel DMA map, IOMMU authorization, device ownership protocol or
required cache maintenance. Consult the specific device/runtime API before
pinning and pass Go pointers only according to cgo's pointer rules.

## 7. Go userspace drivers: what is feasible

Go can act as the control and data-plane language for a userspace driver if
the kernel exposes a supported ABI:

* Open a VFIO/UIO/DMA-BUF file descriptor using `os.OpenFile` or
  `golang.org/x/sys/unix`.
* Use `mmap` to map a kernel-authorized device region or shared buffer.
* Use the documented ioctls to negotiate region size, offsets, interrupt
  notification, DMA mappings and device lifecycle.
* Use `poll`, `epoll`, `eventfd` or a library to wait for completion.

The API is device-specific and much of its ABI is expressed as C structs,
unions, packed register layouts and ioctl numbers. In Go, a robust project
usually uses a small C shim or carefully reviewed architecture-specific
bindings for those definitions, then places lifecycle and application logic
in Go. A Go `struct` must match the C ABI exactly; implicit assumptions about
padding, endianness, atomic widths or volatile MMIO reads/writes can corrupt
device state. Kernel-style memory barriers are not automatically supplied by
plain Go field accesses to an mmap'd register page.

Before using a userspace driver, check whether an existing kernel driver,
`net`/`os` API, RDMA library or vendor-supported SDK already owns the
hardware interaction. Direct device access can unbind the device from normal
Linux services and requires careful reset, permissions, IOMMU group,
shutdown and recovery design.

## 8. Measure the path

For NIC DMA, inspect interface/driver counters, queue counts, RSS mapping,
interrupt affinity and CPU/NUMA placement. For storage DMA, inspect block
queue depth, request latency, device error counters, filesystem and page
cache behavior. `perf`, ftrace/tracepoints, BPF tooling and driver-specific
telemetry can locate the handoff that is stalled; a benchmark from a Go
process alone cannot distinguish a slow device from a slow queue or CPU.

```sh
# Network queue, interrupt, NAPI and driver view
ethtool -l eth0
ethtool -x eth0
ethtool -S eth0
grep -i eth0 /proc/interrupts
cat /proc/net/softnet_stat

# Storage queue and device view
cat /sys/block/nvme0n1/queue/scheduler
cat /sys/block/nvme0n1/queue/nr_requests
iostat -x 1
nvme smart-log /dev/nvme0
```

## Further reading

* [Linux DMA API HOWTO](https://docs.kernel.org/core-api/dma-api-howto.html)
  and [DMA API reference](https://docs.kernel.org/core-api/dma-api.html)
* [VFIO](https://docs.kernel.org/driver-api/vfio.html),
  [UIO HOWTO](https://docs.kernel.org/driver-api/uio-howto.html) and
  [DMA-BUF heaps](https://docs.kernel.org/userspace-api/dma-buf-heaps.html)
* [Network scaling](https://docs.kernel.org/networking/scaling.html),
  [NAPI](https://docs.kernel.org/networking/napi.html) and
  [AF_XDP](https://docs.kernel.org/networking/af_xdp.html)
* [Go `net`](https://pkg.go.dev/net),
  [Go `os.File`](https://pkg.go.dev/os#File),
  [`runtime.Pinner`](https://pkg.go.dev/runtime#Pinner), and
  [`golang.org/x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix)
