# Linux containers: namespaces, cgroups, OCI and Docker

A container is an ordinary process launched with a deliberately constructed
view of the machine and a set of resource controls. There is no container
object in the Linux kernel that contains an application. A runtime combines
kernel primitives—namespaces, cgroups, mounts, capabilities and security
filters—with a root filesystem and a process configuration to create the
container experience.

This guide follows that construction from Linux system calls to Docker's
high-level command line. It covers the namespace and cgroup boundary, the OCI
formats that make runtimes interoperable, image layers, the `docker run`
path, and the security limits of process isolation. The accompanying guides
trace [Kubernetes control and node internals](KUBERNETES.md),
[container networking](CONTAINER_NETWORKING.md), and
[container storage](CONTAINER_STORAGE.md). The broader kernel mechanisms are
in [Linux](README.md).

## 1. The container model

**Why.** Programs need repeatable environments without booting a separate
kernel for each application. A process-level boundary is cheaper than a VM,
while still allowing an application to see its own process tree, hostname,
mount table, network interfaces and resource budget.

**How.** A container runtime sets up a process's execution environment before
`execve` starts the application. A typical Linux container has:

| Concern | Kernel mechanism | What it controls |
|---|---|---|
| Process IDs | PID namespace | PID numbering and visibility |
| Hostname/domain name | UTS namespace | `uname` hostname and domain name |
| Network stack | Network namespace | Interfaces, routes, ports, firewall state, network sysctls |
| Mount tree | Mount namespace | Mount points and filesystem view |
| IPC objects | IPC namespace | System V IPC and POSIX message queues |
| User and group IDs | User namespace | UID/GID mappings and capability scope |
| Cgroup path view | Cgroup namespace | How cgroup paths appear in `/proc` |
| Resource accounting and limits | cgroups | CPU, memory, PIDs, I/O and other controllers |
| Root directory | `pivot_root`/mounts | The process's filesystem root |
| Kernel attack surface | capabilities, seccomp, LSMs | Permitted privileged actions and syscalls |

The process still uses the host kernel. Namespaces virtualize selected global
resources; cgroups account for and constrain resource use. Neither one alone
is a complete security boundary. A container is therefore not a small virtual
machine: it has no guest kernel, firmware, or independent kernel upgrade.

**Where it breaks.** A process in a new mount namespace may still see host
files if the runtime bind-mounts them into its root. A network namespace can
have a distinct network stack but no route out. A memory limit can constrain
anonymous memory but not provide a disk quota. A privileged process can have
access to host devices or kernel interfaces that bypass the intended view.
“It is in a container” does not identify which resources are isolated or
which remain shared.

## 2. Linux namespaces

Namespace objects are kernel-managed contexts associated with processes. The
same task can be in one mount namespace, another network namespace and a
third user namespace; namespaces are independent building blocks, not a
single all-or-nothing container switch. The main creation and entry APIs are
`clone(2)`/`clone3(2)`, `unshare(2)` and `setns(2)`. Their command-line
counterparts include `unshare(1)` and `nsenter(1)`.

| Namespace | What becomes private | Practical observation |
|---|---|---|
| PID | Process ID numbering and process visibility | Container init is PID 1 inside; it has a different host PID |
| Mount | Mount-point tree | `/proc/self/mountinfo`, `findmnt` |
| Network | Interfaces, addresses, routes, sockets, firewall state | `ip netns`, `ip addr`, `ss -lnt` |
| UTS | Hostname and NIS domain name | `hostname`, `uname -n` |
| IPC | System V IPC and POSIX message queues | `ipcs`, `/dev/mqueue` |
| User | UID/GID mappings and capability ownership | `id`, `/proc/self/uid_map`, `capsh` |
| Cgroup | Cgroup-path view, not cgroup membership | `/proc/self/cgroup` |
| Time | Selected monotonic and boot-time clock offsets | `clock_gettime`, `/proc/self/timens_offsets` |

The namespace handles appear under `/proc/<pid>/ns/`. Entries are magic
links such as `net:[4026531993]`; matching identifiers indicate that two
processes refer to the same namespace instance. Holding a namespace file
descriptor or bind-mounting the namespace handle can keep it alive after its
original processes exit.

### PID and mount namespace details

A PID namespace is hierarchical. A process has a PID in its own namespace
and in each visible ancestor namespace, so the same task may be PID 1 in a
container and PID 28471 on the host. The first process created in a new PID
namespace becomes PID 1 there. It adopts orphaned descendants and receives
signals with special init semantics; if it exits, the kernel terminates the
remaining processes in that PID namespace. A container init should reap
children and forward termination signals, or use a small init such as
`tini`.

Creating a new PID namespace with `unshare` affects subsequently created
children, not the caller's existing PID identity. The runtime normally
creates namespaces for the container process and mounts a matching `/proc`
inside the new mount namespace; otherwise process tools can display a
misleading or host-wide view.

A mount namespace starts as a copy of the caller's mount tree. Mount
propagation controls whether later mount and unmount events cross namespace
boundaries. Container setup commonly makes the tree recursively private or
slave before mounting the root filesystem and injected volumes. This is
important: a shared mount can propagate host-side changes into the container
or vice versa even though the mount table itself is namespaced.

### User namespaces: “root” has a scope

A user namespace maps IDs in the namespace to IDs in its parent. For example,
inside UID 0 might map to an unprivileged host UID, and container UIDs 1–65535
to a delegated subordinate range. `/proc/<pid>/uid_map` and `gid_map` show
the mappings. The first process in a new user namespace receives capabilities
there, but those capabilities are scoped to that user namespace and do not
make the process host root.

This distinction is the basis of rootless containers and Kubernetes Pod user
namespaces. It is not magic privilege removal: file ownership, bind mounts,
devices, filesystem support, subordinate ID configuration and kernel
capability checks all affect what works. A user namespace also does not
automatically isolate network, mount, PID or other namespaces; the runtime
must create those separately.

**Where it breaks.** “Root in the container” is ambiguous without the UID
mapping. A bind-mounted host directory can expose host-owned files even when
the container uses another UID map. Mount propagation surprises are common
in nested runtimes and system containers. PID 1 that ignores `SIGTERM` or
never calls `wait` leaves shutdowns slow and children unreaped. Namespace
handles left mounted can keep otherwise-unused network namespaces around.

**Inspect.** These commands are read-only and work on a running container
whose host PID is known:

```sh
readlink /proc/$PID/ns/{pid,mnt,net,user,cgroup}
cat /proc/$PID/uid_map /proc/$PID/gid_map
cat /proc/$PID/cgroup
nsenter -t "$PID" -m -u -i -n -p -- sh
```

`nsenter` enters selected namespaces; `-p` affects children started by the
`nsenter` process, so combining it with a forked shell is the useful form.
Entering a namespace does not move a task to a different cgroup; a cgroup
namespace changes the view, while cgroup membership is managed separately.

## 3. Cgroups: resource control, not a view

**Why.** Isolation without budgets only separates what a process can see. A
runaway container could still consume every CPU cycle, exhaust RAM, create
unbounded processes, or dominate a shared block device. Control groups
organize tasks into a hierarchy and account for or constrain their use of
resources.

**How.** On modern Linux, cgroup v2 exposes one unified hierarchy, commonly
mounted at `/sys/fs/cgroup`. Directories represent cgroups; interface files
such as `cpu.max`, `memory.max`, `pids.max` and `io.max` are the control API.
The kernel associates each task with a cgroup. Children inherit membership
when they are created, and runtimes place container processes into their
container or Pod cgroup before starting the workload.

| v2 interface | Meaning |
|---|---|
| `cpu.weight` | Relative CPU share under contention |
| `cpu.max` | CPU bandwidth ceiling as quota and period |
| `cpu.stat` | Usage and throttling counters |
| `memory.current` | Current charged memory |
| `memory.high` | Reclaim/throttling pressure boundary |
| `memory.max` | Hard memory limit; can trigger cgroup OOM |
| `memory.events` | High/max/OOM event counters |
| `memory.stat` | Breakdown including anonymous memory, file cache and kernel memory |
| `pids.max` | Maximum number of tasks in the cgroup subtree |
| `io.max` | Per-device I/O bandwidth/IOPS limits where supported |
| `*.pressure` | Per-cgroup pressure-stall metrics on supported kernels |

For example, `cpu.max` set to `50000 100000` gives a cgroup 50 ms of CPU
time in each 100 ms period: an aggregate ceiling of half a CPU, not a promise
that one dedicated core is available. `cpu.weight` affects relative share
when tasks compete; it does not reserve capacity. `memory.max` is not the
same as an application heap limit: page cache, stacks, kernel allocations
charged to the cgroup and other memory contribute. Memory pressure can end
in a cgroup-scoped OOM kill even while the host has reclaimable memory.

The hierarchy matters. A child cannot spend resources beyond constraints
imposed by its ancestors. In v2, controllers are enabled down the hierarchy
and delegated deliberately. A cgroup namespace can make `/proc/self/cgroup`
look rooted at the container, but it does not itself impose a resource limit.

**Where it breaks.** A low CPU quota can create periodic latency spikes:
threads run until the period's quota is exhausted, then are throttled until
the next period. A memory limit sized only from the managed heap omits native
allocations, file cache and kernel memory. PID exhaustion can break process
creation while memory and CPU look healthy. Disk I/O limits can be ineffective
or have different behavior on devices and storage stacks that do not support
the requested controller operation. A cgroup path visible inside the
container may be virtualized and is not necessarily the host path.

**Observe.** Start at the process and its cgroup, then read that cgroup's
files on the host. `/proc/$PID/cgroup` maps the process to its host hierarchy;
`/proc/pressure/{cpu,memory,io}` and cgroup-local `*.pressure` files reveal
time lost to contention. `docker stats`, `kubectl describe pod`, and runtime
metrics summarize some of this data but do not replace the kernel counters.

## 4. OCI: the formats between build and run

OCI (Open Container Initiative) separates image distribution from the
runtime-specific process setup. “OCI image” and “OCI runtime” are different
specifications:

1. **Image specification:** an image manifest (or multi-platform image
   index), an image configuration, and ordered filesystem layers identified
   by content digests.
2. **Runtime specification:** a bundle containing a root filesystem and a
   `config.json` describing the process, mounts, namespaces, cgroups,
   capabilities, hooks and other platform settings.

An image is portable content plus defaults, not a running container. A
runtime translates image configuration and user-supplied options into an
OCI runtime bundle, then asks an OCI runtime to create and start the process.
The bundle contains host-specific mount paths and Linux settings, so a raw
bundle is not always portable between machines.

Tags are convenient mutable names; a digest identifies content. A multi-
platform tag commonly resolves first to an image index, then to a
platform-specific manifest, image config and layer blobs. The config records
process defaults such as `Entrypoint`, `Cmd`, environment variables, working
directory and user. Runtime flags and orchestrator Pod fields can override
some of those defaults.

## 5. What `docker run` asks the machine to do

Docker Engine is a client/server system. The CLI sends API requests to
`dockerd` over a Unix socket or configured remote endpoint. On a typical
Linux Engine installation, the daemon uses containerd for image and
container lifecycle work; containerd uses a runtime shim and usually `runc`
to implement the OCI runtime setup. The exact process arrangement and image
store vary by Engine version and configuration.

```text
docker CLI
    │ Engine API
    ▼
dockerd ── image/network/volume orchestration
    │ containerd API
    ▼
containerd ── content store + snapshotter + lifecycle
    │ shim API
    ▼
containerd-shim-runc-v2 ── monitors task, preserves I/O/exit reporting
    │ invokes
    ▼
runc ── applies OCI config through Linux syscalls ──► kernel
```

At a high level, `docker run image command` does the following:

1. The daemon resolves the image reference and pulls missing manifests,
   configs and layers from a registry.
2. The image store verifies content digests and makes the root filesystem
   available through its configured snapshotter/storage backend.
3. The Engine creates container metadata and a writable container snapshot,
   configures requested mounts and network attachment, and derives an OCI
   runtime configuration.
4. The low-level runtime creates namespaces, mounts and cgroup placement,
   configures credentials and security policy, then executes the process.
5. A shim/runtime monitor reports exit status and I/O to containerd; the
   daemon exposes state and logs through the Engine API.

`runc` is commonly a short-lived setup process. The shim or a different
runtime's monitor can outlive it and remain responsible for the running task.
Do not infer the current process tree from a diagram for an older Docker or
containerd release. Docker Engine 29 and later uses the containerd image
store by default on fresh installations; upgraded installations can retain
the classic storage-driver store. Docker's current storage documentation
distinguishes these paths.

**Where it breaks.** The Docker socket is a privileged control API: access
to a rootful daemon commonly implies practical host-root control. `docker
run` succeeding means the runtime created the requested environment, not
that the application is healthy. Image pull, snapshot, namespace, mount,
seccomp and application startup errors happen at distinct layers and should
be diagnosed from daemon/runtime events and logs rather than treated as one
generic “container error.”

## 6. Image layers and the container root filesystem

OCI image layers are ordered filesystem changesets. A local store can reuse
identical content across images, while a snapshotter or union filesystem
provides a merged root filesystem to a container. With OverlayFS, read-only
lower layers provide the image and a per-container upper layer records
changes; the merged mount is what the process sees. Docker's legacy `overlay2`
storage driver used this model. Containerd's overlayfs snapshotter uses
OverlayFS too, but it is a separate storage integration and is not identical
to every legacy Docker on-disk layout.

An OverlayFS lookup checks the upper layer and then lower layers. A write to
a lower-layer file may trigger **copy-up**: the file is copied to the upper
layer, then modified there. Deleting a lower-layer file creates a whiteout
marker in an upper layer so it stays hidden in the merged view. A new file is
written directly to the upper layer. These are file-level union semantics,
not a block-level snapshot of a virtual disk.

This gives cheap image reuse and fast container creation, but it has a cost:
the writable layer is container-local and usually removed with that
container. First-write copy-up can add latency and space use, especially for
large files. For databases and other write-heavy state, use an explicit
volume or orchestrator-managed persistent storage rather than relying on the
image layer or writable container layer. See [storage internals](CONTAINER_STORAGE.md).

## 7. Isolation is a stack, not a single switch

Namespaces and cgroups are complemented by controls that restrict what the
process may do:

* **Capabilities** split traditional root powers into individual privileges.
  Containers normally receive a reduced set; remove capabilities not needed
  by the application.
* **Seccomp** filters system calls (and, where configured, their arguments).
  The profile is inherited by descendants and reduces reachable kernel
  interfaces; it is not a filesystem or network policy.
* **AppArmor and SELinux** apply mandatory access-control policy to files,
  processes and other kernel objects.
* **Read-only mounts/root filesystems**, device cgroups and mount masks
  restrict specific resources.
* **User namespaces** map container credentials away from host IDs. Rootless
  Docker runs both daemon and containers without host-root privileges;
  `userns-remap` maps container IDs while the daemon itself remains root.

These mechanisms are layered. A restricted capability set does not replace
seccomp; a seccomp profile does not replace a mount namespace; a namespace
does not stop a kernel vulnerability. A privileged container, host PID/network
namespace sharing, a writable host bind mount or broad device access changes
the boundary substantially.

## 8. A small, safe namespace exercise

On a Linux host with util-linux, compare the current network/hostname view
with a short-lived UTS + network namespace. `unshare` may be restricted by
the host's security policy; do not add `sudo` just to force this test.

```sh
unshare --uts --net sh -c '
  hostname container-lab
  printf "hostname: "; hostname
  printf "interfaces: "; ip -brief link
  printf "namespace: "; readlink /proc/self/ns/net
'
```

The new network namespace normally starts with loopback down and no external
interface, so `lo` is all that the process sees. The exercise creates no
host bridge, veth pair, route, mount, or persistent container. Docker and
Kubernetes runtimes perform the additional setup needed for connectivity.

## Further reading

### Linux kernel and man pages

* [`namespaces(7)`](https://man7.org/linux/man-pages/man7/namespaces.7.html),
  [`user_namespaces(7)`](https://man7.org/linux/man-pages/man7/user_namespaces.7.html),
  [`clone(2)`](https://man7.org/linux/man-pages/man2/clone.2.html),
  [`unshare(2)`](https://man7.org/linux/man-pages/man2/unshare.2.html),
  [`setns(2)`](https://man7.org/linux/man-pages/man2/setns.2.html)
* [Control Group v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)
  and [Pressure Stall Information](https://docs.kernel.org/accounting/psi.html)
* [Overlay filesystem](https://docs.kernel.org/filesystems/overlayfs.html)

### Specifications and runtime documentation

* [OCI Runtime Specification](https://github.com/opencontainers/runtime-spec)
  and [OCI Image Specification](https://github.com/opencontainers/image-spec)
* [containerd Runtime v2](https://containerd.io/docs/2.4/runtime-v2/)
  and [containerd architecture](https://containerd.io/docs/2.4/)

### Docker and Kubernetes

* [Docker Engine security](https://docs.docker.com/engine/security/),
  [rootless mode](https://docs.docker.com/engine/security/rootless/),
  [storage](https://docs.docker.com/engine/storage/) and
  [networking](https://docs.docker.com/engine/network/)
* [Kubernetes container runtime interface](https://kubernetes.io/docs/concepts/containers/cri/)
  and [Linux kernel security constraints](https://kubernetes.io/docs/concepts/security/linux-kernel-security-constraints/)
