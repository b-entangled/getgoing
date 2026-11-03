# Container storage internals: images, OverlayFS, volumes and CSI

“Container storage” refers to several separate things: registry image
blobs, a local image/content store, unpacked root-filesystem snapshots, a
container's writable layer, node-local scratch and logs, mounted volumes,
and persistent storage exposed through an orchestrator. Their lifetimes,
accounting and performance differ. A file visible inside a container does
not tell you which layer owns it or whether it will survive a restart.

This guide follows content from an OCI registry to a process root, then
distinguishes Docker mounts from Kubernetes volumes and traces the CSI
provision/attach/mount lifecycle. The kernel's page cache, filesystem
durability and block layer are covered in [Linux](README.md); Kubernetes
control-plane behavior is in [KUBERNETES.md](KUBERNETES.md).

## 1. Image content: manifest, config and layers

An OCI image reference (such as `example/api:2.1`) is a name resolved by a
registry. The registry returns an image manifest or, for multi-platform
images, an image index. A platform-specific manifest references a config
blob and an ordered sequence of filesystem layer blobs by digest. Layers are
typically compressed tar changesets; a runtime store verifies and unpacks
them before constructing a root filesystem view.

```text
tag or digest
    │
    ▼
OCI image index (optional, maps platform → manifest digest)
    │
    ▼
image manifest ──► config JSON (entrypoint, command, env, user, rootfs diff IDs)
    │
    ├──► compressed filesystem layer 1 (digest)
    ├──► compressed filesystem layer 2 (digest)
    └──► compressed filesystem layer N (digest)
```

Tags can move; digests identify immutable content. An image ID and a registry
manifest digest are related but not necessarily the same digest: the image
config includes the rootfs layer diff IDs, while a repository digest
identifies a manifest/index object. Pin a digest when deployment
repeatability requires one exact artifact.

Image layers are immutable filesystem changesets, not each necessarily a
complete independent directory tree. A layer may include whiteouts that
hide paths from prior layers. At runtime, an implementation composes layers
into an ordered root filesystem view. Image history and Dockerfile
instructions are useful metadata, but layer boundaries do not correspond
one-to-one with logical application components.

## 2. Local image stores and snapshots

An image store keeps metadata and content-addressed blobs. A snapshotter
prepares a mountable root filesystem from the image layers, commonly using
OverlayFS on Linux. Starting a container generally creates a new writable
snapshot whose parent is the image's read-only snapshot. This lets many
containers share immutable content while each receives its own changes.

Current implementations differ. Docker Engine 29 and later defaults to the
containerd image store on fresh installs; classic installations may still
use Docker storage drivers such as `overlay2`. Containerd snapshotters are
pluggable, with overlayfs, block/device-mapper and remote/lazy-pull options
among possible implementations. Do not assume a particular on-disk directory
layout or estimate disk use only from `docker image ls`.

Containerd's image store may retain compressed registry content as well as
unpacked snapshot data. A lazy-pull snapshotter can expose a filesystem
before all data blocks are locally downloaded, fetching content on demand.
This can reduce startup time but changes cache, offline and push/save
behavior. Runtime state and image content stores may also live in different
paths or partitions.

## 3. OverlayFS and the writable layer

OverlayFS combines lower directories (usually read-only image layers) and
an upper directory (the container's writable layer) into a merged mount.
The upper copy takes precedence when the same path exists in both. Reads
from unchanged lower files need not copy their contents. A write to a lower
file can trigger `copy_up`, moving that file into the upper layer before
modifying it. Deletion of a lower-layer file uses a whiteout marker; a
directory can be made opaque so lower entries no longer appear under it.

```text
merged rootfs (the process view)
        │ lookup: upper first, then lower layers
        ├── upperdir: per-container writes and whiteouts
        ├── lowerdir N: image layer (read-only)
        ├── ...
        └── lowerdir 1: base image layer (read-only)
```

The writable layer belongs to a container instance, not to the image. It
normally disappears when that container is removed, though a stopped
container's layer remains until the container is deleted. A process can
therefore appear to “save” a file in its root filesystem while that file
will not be present in a replacement container.

Copy-up is file-level union behavior: changing one byte in a large lower
file may copy the whole file on first write, depending on the filesystem,
kernel features and snapshotter. OverlayFS is excellent for sharing
read-mostly application files. It is not a universal best store for
write-heavy database pages, high-churn logs or durable application state.
Use an explicit mounted volume for such data and benchmark the actual
filesystem/snapshotter/storage device combination.

## 4. Docker storage mounts

Docker separates rootfs/image storage from data mounts. Mounting a volume
over a directory in the image hides the directory's previous contents for
the lifetime of that mount; removing the mount later does not merge those
hidden files back automatically.

| Mount | Backing store | Survives container removal? | Typical use |
|---|---|---|---|
| Writable container layer | Snapshotter/union upper layer | No | Disposable runtime changes |
| Named volume | Docker-managed host/driver storage | Yes, until volume removal | Persistent app data, shared data |
| Bind mount | Specific host path | Yes, host-managed | Source tree/config/host integration |
| `tmpfs` | Host memory | No | Transient scratch/secrets on Linux |
| Image mount | Read-only content from another image | Image lifecycle | Tooling/assets without rebuilding |

Volumes are managed by Docker and are generally preferred for persistent
container data; the local volume driver stores data on that Docker host,
while plugins can provide other backends. A bind mount directly exposes a
host path, so it couples a container to the host directory layout and access
permissions. A read-only bind mount limits writes through that mount, but
does not change ownership of the source. `tmpfs` is temporary and consumes
memory charged to the container's memory cgroup; it is not free capacity
outside the memory budget.

Volume lifecycle is distinct from container lifecycle: removing a container
does not normally remove its named volume. Conversely, `docker volume
prune` can remove unused volumes and their data. Back up and restore data
using an application-consistent procedure; a filesystem copy of a live
database is not automatically a consistent database backup.

## 5. Kubernetes volume lifetimes

Kubernetes volumes are declared at Pod scope and mounted into selected
containers. The Pod lifetime—not an individual container process—is the
boundary for Pod-local ephemeral volume data.

| Kubernetes volume/data | Backing | Container restart | Pod replacement |
|---|---|---|---|
| Writable image/container layer | Node runtime snapshot | New container gets its own layer | Lost with old container |
| `emptyDir` (disk-backed) | Node-local ephemeral storage | Preserved | Deleted with Pod on node |
| `emptyDir.medium: Memory` | tmpfs | Preserved while Pod exists | Deleted; counted as memory |
| ConfigMap/Secret projected volume | Kubelet-managed projection | Remounted/reconciled | Recreated from API object |
| PVC-backed volume | Storage backend via PV/CSI | Reattached/remounted as needed | Can outlive Pod |
| `hostPath` | Arbitrary node path | Local path remains | Local path remains, node-specific |

`emptyDir` is created when the Pod is assigned to a node and removed when
that Pod leaves the node. A container crash alone does not delete the Pod,
so the scratch data survives that container restart. Disk-backed `emptyDir`
uses node ephemeral storage; memory-backed `emptyDir` uses tmpfs and is
charged as memory. Neither is a portable durable database volume.

Local ephemeral storage usage may include writable layers, container logs
and disk-backed `emptyDir`. Kubernetes can schedule against
`ephemeral-storage` requests and evict a Pod for its limit or node disk
pressure when supported measurement is enabled. Node filesystem layout
matters: image content, container writable layers, logs and kubelet data may
be on one or multiple filesystems. See
[Kubernetes disk internals](KUBERNETES.md#4-kubernetes-disk-and-ephemeral-storage-accounting).

## 6. PersistentVolume and claim model

Kubernetes separates storage supply from application consumption:

* A **PersistentVolume (PV)** represents storage available to the cluster,
  created statically by an administrator or dynamically by a provisioner.
* A **PersistentVolumeClaim (PVC)** is a namespaced request for capacity,
  access mode, volume mode and optionally a StorageClass.
* A **StorageClass** names a provisioning policy (driver and parameters).
* A **CSI driver** implements the protocol between Kubernetes storage
  components and a storage system.

Binding a PVC to a PV is an API-level claim match, not proof that a device
has already been attached, formatted or mounted. Access modes (RWO, RWX,
ROX, RWOP) describe backend capabilities and matching intent. In
particular, `ReadWriteOnce` means one node can mount read-write; multiple
Pods on that node may still use it. Access modes are not a general-purpose
filesystem lock or guarantee of write protection.

The reclaim policy determines what happens to backing storage after a claim
is released. `Retain` leaves it for operator recovery; `Delete` asks the
provisioner to delete it. Dynamically provisioned volumes commonly inherit
`Delete` from the StorageClass, so review this before deleting a PVC holding
important data. Topology-constrained block storage may require
`WaitForFirstConsumer` so scheduling and provisioning can agree on a node or
zone.

## 7. CSI: from claim to mount inside the container

CSI (Container Storage Interface) is a gRPC protocol for a storage driver.
Drivers may provide a controller service, a node service, or both. Kubernetes
CSI sidecars watch API objects and coordinate provisioning/attach operations;
the kubelet invokes node-side operations on the node that will run the Pod.
Exact calls depend on driver capabilities and volume type, but a common
block-volume lifecycle is:

1. A PVC requests storage. A StorageClass identifies a CSI provisioner.
2. The external provisioner calls `CreateVolume`; Kubernetes creates a PV
   referring to the driver and volume handle. The PV is bound to the claim.
3. The scheduler places a Pod, considering volume topology and node
   constraints.
4. If required, an external attacher calls `ControllerPublishVolume` to
   attach the backing device to the selected node.
5. On the node, kubelet/CSI node plugin stages the device (for example,
   formats and mounts it at a global path) when the driver supports staging.
6. `NodePublishVolume` exposes the volume at the Pod's target path, commonly
   using a bind mount.
7. The runtime mounts that path into the relevant container root filesystem.
8. On teardown, node unpublish/unmount runs, followed by controller detach
   when applicable. PVC deletion and reclaim policy determine whether the
   storage itself is deleted or retained.

```text
PVC → StorageClass/provisioner → PV + backend volume
                                  │ Pod scheduled to node
                                  ▼
                      controller attach (if needed)
                                  ▼
                     node stage (if supported)
                                  ▼
                     node publish to Pod path
                                  ▼
                       bind mount into container
```

Network filesystems and local volumes can have a different lifecycle; not
all drivers need controller attachment or node staging. CSI operations
should be idempotent because Kubernetes retries after errors or node
restarts. A PVC in `Bound` state only confirms claim binding; investigate
Pod events, `VolumeAttachment`, CSI controller/node logs and kubelet mount
state to find later failures.

## 8. Storage performance and failure modes

Storage latency and failure can originate at several layers:

| Symptom | Likely layer to inspect |
|---|---|
| Slow first write to image file | OverlayFS copy-up / snapshotter / backing filesystem |
| Pod restarts with data missing | Data was in container writable layer, not a volume |
| `ENOSPC` while `df` shows free bytes | Inode exhaustion, quota, overlay upperdir or wrong filesystem |
| Pod stuck mounting volume | CSI controller/node call, attach limit, topology, filesystem/mount options |
| `ReadWriteOnce` attach conflict | Volume still attached to another node, or access mode/topology mismatch |
| Node `DiskPressure` | Logs, `emptyDir`, writable layers, image cache or node filesystem layout |
| Storage full but host disk looks free | Containerd content store, separate image filesystem, project quota or reserved space |
| Database corruption after recovery | Application/filesystem/device durability and flush semantics |

Buffered writes can return before reaching physical storage. Volumes do not
automatically change filesystem durability semantics: application `fsync`,
filesystem journaling, storage-controller caches, cloud block-device
behavior and replication policy still matter. A persistent volume is a
lifecycle abstraction, not an automatic backup or consistency protocol.

## 9. Inspection tools

```sh
# Docker: identify store, mounts and per-container writable usage
docker info
docker system df -v
docker inspect <container>
docker volume inspect <volume>
findmnt -t overlay
df -hT
df -i

# Kubernetes: distinguish claim binding from node mount/startup
kubectl get pvc,pv,storageclass
kubectl describe pod <pod> -n <namespace>
kubectl describe pvc <claim> -n <namespace>
kubectl get volumeattachment
kubectl get events -A --sort-by=.lastTimestamp

# Node: local disk, inode, cgroup and runtime view
findmnt
df -hT
df -i
sudo crictl images
sudo crictl pods
sudo du -xhd1 /var/lib/kubelet /var/lib/containerd 2>/dev/null
```

The `du` command is only a rough view: overlay hardlinks, snapshots,
reflinks, delayed allocation and separate mounts can make directory totals
misleading. Prefer runtime/store accounting and filesystem-specific tools
before deleting files. Do not manually remove files beneath a runtime's data
root while it is operating; use runtime garbage collection and supported
volume/image lifecycle operations.

## Further reading

* [OCI Image Specification](https://github.com/opencontainers/image-spec)
  and [OCI Runtime Specification](https://github.com/opencontainers/runtime-spec)
* [Linux OverlayFS documentation](https://docs.kernel.org/filesystems/overlayfs.html)
* [Docker storage overview](https://docs.docker.com/engine/storage/),
  [containerd image store](https://docs.docker.com/engine/storage/containerd/),
  [volumes](https://docs.docker.com/engine/storage/volumes/),
  [bind mounts](https://docs.docker.com/engine/storage/bind-mounts/) and
  [tmpfs](https://docs.docker.com/engine/storage/tmpfs/)
* [Kubernetes volumes](https://kubernetes.io/docs/concepts/storage/volumes/),
  [PersistentVolumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/),
  [StorageClasses](https://kubernetes.io/docs/concepts/storage/storage-classes/)
  and [local ephemeral storage](https://kubernetes.io/docs/concepts/storage/ephemeral-storage/)
* [CSI specification](https://github.com/container-storage-interface/spec/blob/master/spec.md)
* [Kubernetes node-pressure eviction](https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/)
