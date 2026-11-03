# Linux networking internals: packet paths, queues and Go

This guide goes below TCP/IP concepts into the Linux host datapath: NIC
queues and DMA, interrupts and NAPI, `sk_buff`, routing and netfilter,
socket queues and the Go runtime's netpoller. It complements the protocol
and wire-level reference in [`../networking/README.md`](../networking/README.md),
the kernel's shorter packet-path chapter in [Linux](README.md#14-kernel-networking-internals-skb-softirq-and-socket-queues),
and the [container networking guide](CONTAINER_NETWORKING.md).

The important distinction is between **what the application observes** and
**what the host is doing**. A successful `Write` means the local socket
accepted bytes; it does not prove that a NIC transmitted them or a peer read
them. Similarly, a `Read` readiness event means the socket may make progress,
not that a complete application message is available.

## 1. The Linux network stack in one picture

Linux networking is a set of queues connected by ownership transfers:

```text
RX: wire → NIC RX descriptors → DMA into host memory → MSI-X interrupt
       → NAPI poll → optional XDP → skb / GRO → Ethernet → IP → netfilter
       → TCP/UDP socket queue → readiness wakeup → Go netpoller → goroutine

TX: goroutine → Go net.Conn → socket send queue → TCP/UDP → route/netfilter
       → qdisc → driver TX descriptors → DMA from host memory → NIC → wire
```

The queues are real and bounded: descriptor rings, the per-CPU network
backlog, TCP/UDP socket queues, qdiscs, and connection-tracking tables. A
packet can be delayed or dropped at each one. Counters are often owned by the
kernel or driver, so application metrics alone may show only a rising tail
latency or retransmit rate.

### The common packet representation: `sk_buff`

After a driver hands a received packet to most of the stack, it is represented
by an `sk_buff` (skb). The skb holds packet metadata and references packet
data, which may be in a linear head plus page fragments. The stack can carry
large offloaded/GRO packets rather than one skb per Ethernet frame. This is
why a host packet capture can show a packet larger than the interface MTU or
a checksum that looks unfinished: the capture may run before segmentation or
checksum offload.

An skb is not the wire frame and not necessarily a single allocation. XDP's
native driver hook can inspect a packet before the usual skb is built. XDP
can pass, drop, redirect or transmit; whether it is native, generic (skb
based), or hardware-offloaded depends on the driver and program attachment.

## 2. Receive: NIC descriptors, DMA, NAPI and socket delivery

### A. Driver posts receive buffers

The driver allocates or obtains buffers, maps them for the device using the
kernel DMA API, and writes DMA addresses into receive descriptors. The NIC
uses DMA to place packet bytes into those buffers without having the CPU copy
every byte. The driver and device agree on descriptor ownership and ordering
through device-specific rings, doorbells and memory barriers.

The address in a descriptor is a **DMA address**, not necessarily a CPU
physical or virtual address. An IOMMU may translate the device-visible I/O
virtual address to RAM and constrain which memory that device can access.
See [DMA internals](DMA.md) for the full address and ownership model.

### B. Interrupts schedule polling

Modern NICs usually have multiple receive/transmit queue pairs and MSI-X
interrupt vectors. Raising an interrupt for every packet would overwhelm the
CPU at high packet rates. Linux drivers use NAPI: an interrupt signals work,
then a poll routine drains a bounded batch of completions. Under sustained
load, polling amortizes interrupt overhead; under light load, interrupts
avoid burning a CPU continuously.

The NAPI poll budget bounds how much receive work one poll consumes before
yielding. Budget exhaustion can defer work to another poll or `ksoftirqd`.
The exact mapping between queues, interrupts, NAPI instances and CPUs is a
driver choice; it is commonly one queue pair per NAPI instance, but not a
kernel guarantee.

### C. Receive scaling chooses where work runs

Receive Side Scaling (RSS) is normally implemented in NIC hardware. A hash
of flow fields (often an IP/transport tuple) indexes an indirection table that
selects an RX queue. Queue-to-interrupt affinity then influences which CPU
starts processing that flow.

Linux can steer work at later stages too:

| Mechanism | Stage | Main tradeoff |
|---|---|---|
| RSS | NIC selects RX queue | Hardware parallelism; one flow usually stays on one queue |
| RPS | Kernel selects protocol-processing CPU | Works without RSS hardware; adds cross-CPU queueing/IPI |
| RFS | Kernel steers toward the CPU consuming the socket | Can improve cache locality; needs flow tracking |
| Accelerated RFS | NIC steers toward consumer CPU | Hardware/driver support required |
| XPS | Kernel selects TX queue | Aligns transmit work and queue to CPU/flow |

These mechanisms are complementary but are not “turn all of them on” knobs.
If RSS already maps queues well to CPUs, RPS can add unnecessary cross-core
traffic. A single elephant flow remains ordered and may saturate one queue;
adding more queues does not split that one flow arbitrarily.

### D. Stack traversal and socket wakeup

The driver passes the packet into the receive stack. Ethernet, VLAN and
protocol handlers process it; GRO may coalesce compatible TCP packets; IP
decides whether to deliver locally or forward; netfilter hooks and rules may
filter or translate; TCP/UDP demultiplexes to a socket. TCP verifies sequence
numbers and queues in-order bytes for the application. Out-of-order data
waits for gaps to fill. UDP preserves datagram boundaries and drops when its
receive resources are exhausted.

When data becomes available, the kernel marks the socket readable and wakes
its readiness waiters. Go's Linux runtime uses epoll for network pollable
descriptors: the goroutine blocked in `Read` is parked, while the OS thread
can run other goroutines. The runtime then unparks that goroutine when the
descriptor is ready. Readiness is not message framing: a TCP `Read` may
return fewer bytes than requested, and the application must loop or use
`io.ReadFull` when it needs an exact length.

## 3. Transmit: socket queue to NIC

The application writes into the socket. TCP copies or references data into
its send state, applies congestion and receive-window limits, schedules
segments and hands skbs to the qdisc. A qdisc can shape/rate-limit traffic,
provide fairness or add queueing delay. The driver maps data for DMA, fills
TX descriptors, rings a device doorbell and reclaims descriptors on completion.
GSO/TSO can keep a large logical segment in software or DMA it to a NIC that
does final segmentation.

Copy avoidance is not synonymous with DMA and not synonymous with zero-copy:

* `sendfile` can move file-backed page-cache data to a socket without a
  userspace read/write copy when the source and destination support it.
* `splice` can transfer between suitable file descriptors through kernel
  pipes.
* `MSG_ZEROCOPY` can avoid copying large socket-send buffers after enabling
  `SO_ZEROCOPY`; the kernel pins pages and returns completion notifications
  on the socket error queue. The application must not reuse a buffer until
  its completion notification; pinning/notification overhead means small
  writes often lose.
* AF_XDP exposes RX/TX rings and UMEM buffers to userspace, optionally using
  driver-supported zero-copy. It requires XDP setup, queue affinity, buffer
  ownership discipline and matching hardware/driver support; it does not turn
  an ordinary `net.Conn` into direct NIC access.

Measure the real workload before adopting a copy-avoidance path. The kernel
may fall back to copying, and fewer copies can trade against page pinning,
fragmentation, synchronization, completion processing and more complicated
failure handling.

## 4. TCP queues, backpressure and latency

A listening TCP server has at least two relevant queues: the incomplete
connection/SYN request state and the completed connection accept queue.
The application drains completed connections by calling `Accept`; kernel
backlog limits, `somaxconn`, SYN handling and syncookies influence pressure
before the application owns the connection. After accept, each socket has
send and receive memory, TCP state and timers. An application that stops
reading eventually applies backpressure to the peer through the receive
window. UDP has no equivalent reliable backpressure: a full socket receive
queue loses datagrams.

TCP receive/send autotuning grows buffers against the path and the process's
consumption. Setting `SO_RCVBUF` or `SO_SNDBUF` manually can cap or otherwise
change autotuning behavior; the kernel also doubles socket buffer values for
accounting and applies configured maxima. A huge socket buffer may reduce
visible packet loss but increase memory use and queueing delay. Monitor
buffer occupancy and RTT instead of blindly raising every limit.

On egress, a deep qdisc can turn a fast local `Write` into a packet that waits
in a long queue. This bufferbloat inflates latency while throughput may look
healthy. Congestion control, qdisc policy, socket queueing, NIC rings and
receiver window are separate controls and should be observed separately.

## 5. Using the Linux network through Go

For ordinary applications, Go's `net` package is the right interface. It
preserves the runtime's netpoller integration, deadline handling, platform
portability and resource management. `net.Conn` is a byte stream; it does
not expose descriptors, DMA mappings or packet boundaries. `net.PacketConn`
preserves datagrams, but still uses the kernel network stack.

### Configure a socket before connect/listen

The standard library exposes `Dialer.Control` and `ListenConfig.Control` for
socket options. Linux-specific options can be set with `golang.org/x/sys/unix`
(add that module to a project that uses this example):

```go
lc := net.ListenConfig{
    Control: func(network, address string, raw syscall.RawConn) error {
        var sockErr error
        if err := raw.Control(func(fd uintptr) {
            sockErr = unix.SetsockoptInt(
                int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, 1<<20,
            )
        }); err != nil {
            return err
        }
        return sockErr
    },
}

ln, err := lc.Listen(ctx, "tcp", ":8080")
if err != nil {
    return err
}
defer ln.Close()
```

This is an example of a socket option, not a general recommendation to pin
the receive buffer at 1 MiB. Check the kernel's effective value and socket
memory counters, and prefer defaults unless measurement shows a bottleneck.
Use `net.Dialer.Control` for outbound sockets. Use `SyscallConn` only when
there is no higher-level API; raw descriptor operations must respect Go's
runtime ownership and should run via `RawConn.Control`/`Read`/`Write` as
appropriate.

### Raw packets, routes and interfaces

Linux supports packet sockets (`AF_PACKET`), raw IP sockets, and netlink
families such as `NETLINK_ROUTE`. These are distinct from TCP/UDP sockets:
they let a process observe Layer 2 frames, raw IP packets, or ask the kernel
to inspect/configure links, addresses, neighbors, qdiscs and routes. They
require Linux-specific APIs and privileges (commonly `CAP_NET_RAW` for
packet/raw sockets and `CAP_NET_ADMIN` for network configuration).

For configuration tools in Go, a maintained rtnetlink library such as
[`github.com/vishvananda/netlink`](https://pkg.go.dev/github.com/vishvananda/netlink)
is usually safer than assembling netlink messages by hand. For read-only
operations, `ip -j`, `ethtool --json` where supported, and `/proc`/sysfs
provide practical interfaces. When calling tools from Go, pass each argument
to `exec.CommandContext` separately; do not build shell command strings.

```go
cmd := exec.CommandContext(ctx, "ip", "-j", "-s", "link", "show", "dev", ifname)
out, err := cmd.Output()
if err != nil {
    return fmt.Errorf("inspect interface %s: %w", ifname, err)
}
// Decode the returned JSON into the fields the application needs.
```

Raw packet and netlink code is valuable for packet capture, routing agents,
and network appliances. It is usually the wrong choice for an HTTP or RPC
service: it increases the privilege boundary and makes the application
responsible for behavior that `net` already implements.

## 6. Diagnose by queue boundary

Use counters from multiple layers and correlate them with a flow and time
window:

```sh
# Link and hardware queues
ip -s -s link show dev eth0
ethtool -l eth0                 # queue/channel counts
ethtool -x eth0                 # RSS hash and indirection table
ethtool -S eth0                 # driver-specific counters
cat /proc/interrupts

# Kernel and transport
cat /proc/net/softnet_stat
ss -tinm                        # TCP info and socket memory
nstat -az                       # protocol counters
tc -s qdisc show dev eth0       # qdisc backlog and drops
cat /proc/net/netstat

# Observe actual packets, accounting for offloads
sudo tcpdump -ni eth0 host 192.0.2.10
```

Start at the first counter that increments unexpectedly. NIC missed/no-buffer
counters suggest the hardware ring/driver cannot keep up. Softnet drops
point later, at per-CPU backlog processing. TCP retransmissions with no
local drops can be path loss, reordering or a peer issue. A rising qdisc
backlog with stable link errors suggests queueing rather than a bad cable.
Packet captures can change apparent packet sizes/checksums because of GRO,
GSO and checksum offload.

## Further reading

* [Linux networking scaling: RSS, RPS, RFS, XPS](https://docs.kernel.org/networking/scaling.html)
* [NAPI](https://docs.kernel.org/networking/napi.html),
  [AF_XDP](https://docs.kernel.org/networking/af_xdp.html), and
  [network interface statistics](https://docs.kernel.org/networking/statistics.html)
* [`socket(7)`](https://man7.org/linux/man-pages/man7/socket.7.html),
  [`packet(7)`](https://man7.org/linux/man-pages/man7/packet.7.html),
  [`rtnetlink(7)`](https://man7.org/linux/man-pages/man7/rtnetlink.7.html)
* [Go `net` package](https://pkg.go.dev/net),
  [Go `os.File`](https://pkg.go.dev/os#File), and
  [Go `x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix)
* [Linux `MSG_ZEROCOPY`](https://docs.kernel.org/networking/msg_zerocopy.html)
