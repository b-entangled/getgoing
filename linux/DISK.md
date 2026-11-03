# Linux disk management internals: block devices, filesystems and Go

Linux presents storage through a stack of interfaces, not one “disk” API.
A physical NVMe device, a partition, an LVM logical volume, a dm-crypt
mapping and a filesystem mount can each be separate block devices or mount
points. Filesystem I/O enters the page cache and block layer; raw block I/O
starts lower in that stack and bypasses filesystem semantics. This guide
connects these layers, shows safe inspection and maintenance workflows, and
demonstrates what Go can do through files, block devices and Linux syscalls.

The kernel's VFS, page cache, filesystem crash consistency and block request
details are introduced in [Linux](README.md), entries 6, 11 and 12. This
companion concentrates on disk discovery/management, queue observability,
low-level Go I/O and the limitations of raw-device access.

## 1. The storage stack

```text
Go / application
      │ read, write, pread/pwrite, mmap, fsync
      ▼
VFS + filesystem (ext4, XFS, btrfs, NFS, ...)
      │ page cache / extents / journal / writeback
      ▼
block layer (bio → request → blk-mq software/hardware queues)
      │
      ├── device mapper: dm-crypt, LVM, thin provisioning, snapshots
      ├── md RAID / multipath / loop devices
      ▼
block driver (NVMe, SCSI, virtio-blk, MMC, ...)
      │ commands + DMA mappings + completion queues
      ▼
controller/cache/medium
```

`/dev/nvme0n1` and `/dev/sda` name whole block devices; names ending in a
partition number often name a partition. `/dev/mapper/*` usually names a
device-mapper target. `/dev/disk/by-id/*` and `/dev/disk/by-uuid/*` are udev
symlinks that are more stable than enumeration-order names. A mounted path
such as `/var/lib/data` is a filesystem view, not a device identity.

The layers are observable through sysfs and user tools:

```sh
lsblk -o NAME,TYPE,SIZE,FSTYPE,FSVER,MOUNTPOINTS,UUID,MODEL,SERIAL
findmnt --target /var/lib/data
ls -l /dev/disk/by-id/
readlink -f /dev/disk/by-id/<stable-device-id>
cat /proc/partitions
```

The kernel reports logical and physical sector sizes separately. A 512e
drive presents 512-byte logical sectors backed by 4 KiB physical sectors;
misaligned writes can force read-modify-write. NVMe namespaces and
multipath devices can add yet another identity layer. Use `lsblk`, sysfs
and device-specific tooling to establish which object an application is
actually using.

## 2. From file write to the device

### Buffered I/O: filesystem and page cache

For ordinary buffered writes, the kernel copies data into page-cache pages,
marks them dirty and can return before the storage device has written them
to nonvolatile media. Writeback later creates bios/requests and sends them
down the block stack. Reads can be served from page cache without device
I/O. This is often the fastest path and is what normal Go `os.File` I/O uses.

`File.Sync` requests file synchronization; it does not sync the parent
directory entry for a newly created or renamed file. A durable replace
therefore requires syncing the temporary file, renaming it, then syncing the
directory. The filesystem and block driver translate this request to the
required journal commit and device cache flush/FUA operations. A device or
virtualization layer that falsely acknowledges cache flushes can still break
the durability contract.

### Direct I/O: an opt-in path with alignment rules

`O_DIRECT` asks the filesystem to minimize page-cache use for that I/O. It
does **not** imply that the process talks directly to a disk controller or
that the data is durable on return. The kernel still validates the request,
maps pages for DMA, submits block requests and handles completion. Filesystem
support and constraints vary by kernel/filesystem; buffers, file offsets and
lengths may all need alignment. Since Linux 6.1, `statx` can report per-file
direct-I/O alignment via `STATX_DIOALIGN` where supported. An unsupported or
misaligned request may fail with `EINVAL` or, in some circumstances, fall
back to buffered I/O.

Do not mix buffered and direct I/O to overlapping regions without a clear
coherency design. An unaligned `O_DIRECT` buffer is not automatically fixed
by choosing a “sector size” guessed from the device. Query the actual file
alignment and handle filesystems that do not report it.

### Queueing and completion

The block layer supports multiple queues with blk-mq: per-CPU software
staging queues feed hardware dispatch queues and tags track in-flight
requests. A request scheduler may reorder requests within supported queues;
NVMe is already highly parallel, while other devices may benefit from a
different policy. Completion can occur out of order, so higher layers
preserve the required ordering and consistency.

Queue depth matters. A device with many hardware queues can be underused by
one-at-a-time synchronous I/O; too much outstanding I/O adds latency and
memory pressure. Measure queue depth and per-operation latency instead of
assuming that a 100% “utilization” figure means an SSD has no remaining
capacity.

## 3. Disk administration and maintenance

### Discovery and health

Start with the mapping from mount point to filesystem to block device:

```sh
lsblk -f
findmnt -T /srv/data
df -hT /srv/data
df -i /srv/data
```

`df -h` reports filesystem capacity; `df -i` reports inode capacity. A
filesystem may have free bytes but no inodes, or the application may be
writing to a different mounted filesystem than expected. `du -x` stays on
one filesystem when comparing directory use, though snapshots, reflinks and
reserved filesystem blocks can still make totals differ from `df`.

Inspect device health with the tool appropriate to the device and driver:

```sh
sudo smartctl -a /dev/sda            # SATA/SAS, when supported
sudo nvme list
sudo nvme smart-log /dev/nvme0       # NVMe controller health counters
cat /sys/block/nvme0n1/stat
```

SMART/NVMe health values are vendor/device telemetry, not a deterministic
prediction of remaining life. RAID controllers, USB bridges and virtual
disks may hide or transform physical-device telemetry.

### Partitions, filesystems and mounts

`fdisk`, `sfdisk` and `parted` edit partition tables. `mkfs.*` creates a
filesystem. `mount` attaches a filesystem to a directory; `findmnt` reports
the current mount graph. LVM, `mdadm` RAID, multipath and device mapper add
logical devices beneath the filesystem. Disk encryption tools such as
`cryptsetup` create mapped block devices whose filesystem is mounted above
them.

These are distinct lifecycle steps: creating a partition does not format
it, formatting does not mount it, and mounting an existing filesystem does
not make its data durable or backed up. Partition-table edits, filesystem
creation, encryption, RAID assembly, resizing, discard and mount changes
have device-specific preconditions and can make data inaccessible. Use
dedicated administrative tools and review their target device and mount
state before making changes. In production, prefer established Linux
storage tooling and an explicit recovery plan over hand-rolled ioctl
sequences.

### Discard/TRIM is not secure erase

Filesystem discard can notify a thin-provisioning layer or SSD that ranges
are no longer in use. `fstrim` issues discard for free filesystem ranges;
`blkdiscard` issues it against a block device range and warns that the
specified data is lost. Discard is not a guaranteed forensic erase: SSD
flash translation layers, remapping and device firmware determine what
happens to the physical cells. Encrypted-device discard may also reveal
allocation patterns. Treat discard, secure erase and cryptographic erase as
different operations.

## 4. Measure I/O instead of guessing

```sh
iostat -xz 1                         # device throughput, queue, await
pidstat -d 1                         # per-process I/O rates
cat /proc/diskstats                  # cumulative per-device counters
cat /sys/block/nvme0n1/queue/scheduler
cat /sys/fs/cgroup/<path>/io.stat    # cgroup v2 accounting, if enabled
cat /sys/fs/cgroup/<path>/io.pressure
```

`iostat`'s `await` combines queueing and service time. `aqu-sz` shows average
queue depth; `%util` can reach 100% on parallel NVMe devices without every
hardware queue being saturated. Use `blktrace`/`bpftrace`/tracepoints to
separate submit-to-issue queueing from issue-to-completion device time.
Correlate filesystem and device metrics with application latency; an
`fsync` wait, page-cache miss, cgroup I/O limit and saturated device are
different failure modes.

## 5. Go file I/O: the best starting point

For files and filesystems, Go's standard library provides robust buffered
and positional I/O. `ReadAt` and `WriteAt` avoid changing a shared file
offset; `Sync` requests synchronization; `Stat` reports metadata. These
interfaces work with regular files and, on Linux, block-device file nodes
when permissions and device policy allow it.

```go
func ReadFirstBlock(device string) ([]byte, error) {
	// Open read-only: never use a writable raw-device handle for discovery.
	f, err := os.Open(device)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}
```

This is a read-only raw block read **through normal kernel I/O**, not direct
controller access and not `O_DIRECT`; it may use page cache. Reading sector
zero can expose partition/filesystem metadata. Device paths should be
provided by the operator or resolved through stable `/dev/disk/by-id` links,
not guessed from `/dev/sdX` enumeration order.

Use `exec.CommandContext` with argument arrays to query `lsblk`, `findmnt`,
`nvme`, `smartctl` or `cryptsetup` from a Go maintenance tool. For structured
data, prefer machine-readable output such as `lsblk --json`; don't parse
localized human output if the tool has a JSON mode. Check exit status and
stderr, use a timeout, and keep inventory/reporting separate from destructive
operations.

## 6. Go `O_DIRECT` and aligned I/O

`os.OpenFile` does not expose Linux's `O_DIRECT` flag. A Linux-only Go tool
can use `golang.org/x/sys/unix` to open a file/device with `O_DIRECT`, then
must provide a buffer, offset and length that meet the file's reported
alignment. On Linux 6.1+, query `STATX_DIOALIGN` where supported. Do not
hard-code 512 or 4096 as universal direct-I/O alignment.

A `make([]byte, n)` allocation has no API guarantee of device/filesystem
direct-I/O alignment. `unix.Mmap` gives page-aligned virtual memory, which
may satisfy a device's requirement but is not universally sufficient; a
higher alignment can require an aligned allocator or a small native helper.
Keep the mapping alive until I/O completes, unmap it only after the kernel
has finished, and handle partial reads/writes and `EINTR` according to the
API used.

```go
// Linux-only sketch; add golang.org/x/sys/unix to the application module.
fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECT, 0)
if err != nil {
	return err
}
defer unix.Close(fd)

// alignment must come from statx(STATX_DIOALIGN) or a documented,
// device/filesystem-specific interface. The buffer must be aligned too.
buf, err := unix.Mmap(-1, 0, alignedLength, unix.PROT_READ|unix.PROT_WRITE,
	unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
if err != nil {
	return err
}
defer unix.Munmap(buf)

n, err := unix.Pread(fd, buf[:alignedLength], alignedOffset)
if err != nil {
	return err
}
_ = n // validate short I/O; successful calls need not fill the buffer.
```

This is intentionally a sketch: it does not query alignment, issue writes,
or promise durability. For durable writes, direct I/O does not replace
`fsync`/`fdatasync`; `O_DIRECT` and `O_SYNC` have different purposes.
Benchmark against buffered I/O: direct I/O can lose page-cache read-ahead,
writeback coalescing and cache sharing, and can complicate concurrent access.

## 7. Go and asynchronous disk I/O

`os.File.ReadAt`/`WriteAt` are positional synchronous APIs. They do not expose
the block hardware queue or guarantee that an operation runs asynchronously
at the device. On Linux, `io_uring` provides shared submission/completion
rings and operations such as read, write, fsync and fixed-buffer I/O. Go's
standard library does not expose a general `io_uring` API; projects use a
third-party Go package or cgo with `liburing` and must handle ring setup,
completion ownership, cancellation, runtime scheduling and kernel support.

Higher-level systems should first use Go's `os`/`io` APIs and benchmark. A
low-level async interface can reduce syscall or copy overhead for some
workloads, but it does not bypass the filesystem/block stack or turn every
completion into DMA directly into a Go object. Direct-I/O alignment, page
registration, buffer lifetime, short I/O, cancellation and durability remain
explicit responsibilities.

## 8. Go disk management boundaries

Go can read `/sys`, `/proc`, run utilities and issue selected ioctls. It
doesn't provide a standard library API for partition tables, filesystem
creation, device-mapper graphs, RAID assembly, SMART, NVMe administration
or mount policy. `golang.org/x/sys/unix` exposes low-level syscalls and many
ioctl constants, but ioctl payload layouts and semantics are device- or
kernel-interface-specific. Use documented UAPI definitions and established
tools rather than guessing ioctl numbers or binary layouts.

For example, a Go inventory service can invoke:

```go
cmd := exec.CommandContext(ctx, "lsblk", "--json", "--bytes",
	"--output", "NAME,PATH,TYPE,SIZE,FSTYPE,MOUNTPOINTS,UUID")
out, err := cmd.Output()
if err != nil {
	return fmt.Errorf("query block devices: %w", err)
}
// json.Unmarshal(out, &inventory)
```

For changes, tools such as `parted`, `mkfs.*`, `mount`, `cryptsetup`,
`lvm`, `mdadm`, `fstrim` and `nvme-cli` encode a great deal of device-specific
knowledge. If a Go service wraps them, pass separate arguments (never shell
concatenation), validate a stable device ID, capture output, time out,
serialize conflicting operations and surface every error. Raw-device writes
can overwrite partition tables or filesystems immediately; a path check
alone does not make the target safe.

## Further reading

* [Linux network scaling](https://docs.kernel.org/networking/scaling.html),
  [NAPI](https://docs.kernel.org/networking/napi.html),
  [AF_XDP](https://docs.kernel.org/networking/af_xdp.html), and
  [network statistics](https://docs.kernel.org/networking/statistics.html)
* [DMA API HOWTO](https://docs.kernel.org/core-api/dma-api-howto.html),
  [DMA API reference](https://docs.kernel.org/core-api/dma-api.html),
  [VFIO](https://docs.kernel.org/driver-api/vfio.html),
  [UIO](https://docs.kernel.org/driver-api/uio-howto.html) and
  [DMA-BUF heaps](https://docs.kernel.org/userspace-api/dma-buf-heaps.html)
* [blk-mq](https://docs.kernel.org/block/blk-mq.html),
  [write-cache flush/FUA](https://docs.kernel.org/block/writeback_cache_control.html)
  and [device mapper](https://docs.kernel.org/admin-guide/device-mapper/)
* [`open(2)` and `O_DIRECT`](https://man7.org/linux/man-pages/man2/open.2.html),
  [`statx(2)` and direct-I/O alignment](https://man7.org/linux/man-pages/man2/statx.2.html),
  [`fsync(2)`](https://man7.org/linux/man-pages/man2/fsync.2.html),
  [`io_uring(7)`](https://man7.org/linux/man-pages/man7/io_uring.7.html)
* [Go `os.File`](https://pkg.go.dev/os#File),
  [Go `net`](https://pkg.go.dev/net),
  [`runtime.Pinner`](https://pkg.go.dev/runtime#Pinner) and
  [`golang.org/x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix)
