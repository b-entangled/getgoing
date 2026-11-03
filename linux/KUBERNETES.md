# Kubernetes internals: control plane, node, Pod and resources

Kubernetes is a declarative control system. Users submit desired objects to
an API; controllers continually compare desired state with observed state
and ask other components to move the cluster toward it. The API objects are
not commands executed once, and Kubernetes itself is not the container
runtime. On Linux nodes, kubelet speaks CRI to a runtime such as containerd
or CRI-O, which in turn configures the kernel and starts processes.

Terminology note: a Kubernetes **Namespace** (`kubectl get namespaces`) is
an API-level grouping used for names, access control and policy. A Linux
**namespace** is a kernel object that virtualizes a process's view of a
resource such as PIDs, mounts or networking. Kubernetes API namespaces do
not create Linux namespaces by themselves.

This guide follows an application from an API write to a process on a node,
then traces restart, resource control and storage/network setup. Begin with
[Linux container primitives](CONTAINERS.md), then use the specialized guides
for [networking](CONTAINER_NETWORKING.md) and
[storage](CONTAINER_STORAGE.md).

## 1. Architecture: desired state and reconciliation

**Why.** A cluster contains changing machines, processes, addresses and
failures. The operator needs to declare what should run and let the system
continually repair the difference between that target and reality.

**How.** A typical cluster has a control plane and worker nodes:

```text
kubectl / controller clients
             │ Kubernetes API (HTTP)
             ▼
      kube-apiserver ───── etcd (durable API state)
        ▲      ▲
        │      └── scheduler: choose node for unscheduled Pods
        └───────── controllers: reconcile API objects

  node: kubelet ── CRI gRPC ── containerd or CRI-O ── OCI runtime ── kernel
         │                         │
         ├── volumes (CSI)         └── images / container lifecycle
         └── Pod networking (CNI or runtime/plugin integration)
```

* **kube-apiserver** is the front end and validation/admission boundary for
  Kubernetes API objects. Other control-plane components communicate with
  the API rather than sharing application state directly.
* **etcd** is the key-value backing store for the API server's cluster state.
  Back it up and operate it as a latency-sensitive, strongly consistent
  control-plane dependency.
* **Controllers** watch API objects and submit updates to converge toward
  them. A Deployment controller, for example, creates or adjusts
  ReplicaSets; the ReplicaSet controller creates or removes Pods.
* **kube-scheduler** watches Pods with no assigned node, filters nodes that
  cannot satisfy the constraints, scores feasible nodes, then binds a Pod
  to a selected node through the API.
* **kubelet** runs on each node and is responsible for the Pods assigned to
  that node. It observes desired Pod specs, configures them, and reports
  status back to the API.
* **container runtime** creates Pod sandboxes and containers and handles
  image operations. The runtime boundary is standardized by CRI, a gRPC API.

The state is eventually convergent, not one global transaction. The API
server may accept a desired Pod before it is scheduled, before its image is
pulled, or before the application becomes ready. Each phase has separate
status and events. Controllers also work from caches and watches; they
reconcile after events and periodic resync rather than acting as a single
imperative script.

**Where it breaks.** An API object existing does not mean a process is
running. A Pod may be Pending because it has no feasible node, a node may
have the Pod but be unable to mount a volume or pull an image, or containers
may be running while readiness is false. A failed controller can leave
objects present but unreconciled. Loss of etcd quorum can prevent control-
plane state changes even while already-running containers continue on nodes.

## 2. From a Pod manifest to a running process

**Why.** A Pod is Kubernetes' scheduling and execution unit. Its containers
need a shared lifecycle context, an IP identity, optional shared storage,
resource budgets and independently restartable container processes.

**How.** Consider a Deployment requesting three replicas. A representative
sequence is:

1. `kubectl apply` sends the desired object to the API server. The server
   authenticates and authorizes the request, runs admission and schema
   validation, then persists the object.
2. The Deployment controller notices the object and ensures a matching
   ReplicaSet. That controller creates Pod objects; it does not start
   containers itself.
3. The scheduler sees each Pod without `spec.nodeName`, checks constraints
   and resources, scores candidate nodes and binds each Pod to one node.
4. The kubelet on that node observes the assigned Pod and reconciles the
   requested state against runtime state. It prepares resources, mounts
   volumes, and calls CRI to create a Pod sandbox.
5. The runtime configures a sandbox context (commonly a pause/infra process
   on Linux), requests Pod networking, pulls images if necessary, creates
   each container and starts its configured process.
6. The kubelet observes container state, runs probes, updates Pod status
   and reports conditions. A Service controller/data plane separately
   learns which endpoints are eligible to receive traffic.

```text
Deployment → ReplicaSet → Pod object → scheduler binding → kubelet
  → CRI RunPodSandbox → network setup → image/rootfs → CreateContainer
  → StartContainer → probes/status → Service endpoint eligibility
```

CRI separates Kubernetes from a particular runtime implementation. The
kubelet acts as a gRPC client for runtime and image services. Docker Engine
is not itself a CRI runtime; the old in-tree dockershim was removed from
Kubernetes in v1.24. Docker-built OCI images continue to run with CRI
runtimes because image and runtime formats are standardized. See
[OCI and Docker](CONTAINERS.md#4-oci-the-formats-between-build-and-run).

### Pod sandbox and containers

On Linux, containers in one Pod share a Pod network namespace, so they use
the same IP address, routes and port space and can reach each other over
`localhost`. They do not automatically share a PID namespace; process
namespace sharing is configurable. The runtime may use a small pause/infra
process to anchor namespaces while application containers are added or
restarted. This is a common implementation model, not a Kubernetes mandate
that every runtime use a particular executable or process tree.

Each application container still has its own container lifecycle, image,
root filesystem layer, environment, security context and resource settings.
Pod volumes are mounted into selected containers. A container restart
therefore need not replace the Pod's IP or shared storage, while replacing
the Pod usually creates a new UID and may create a new network identity.

### Kubelet reconciliation on the node

The kubelet's sync loop aggregates work from API watches, local sources such
as static Pod manifests, and runtime/container state. Pod workers attempt to
reconcile each assigned Pod. The kubelet calls CRI to create the sandbox and
containers; it also tracks lifecycle events, probes, mounts, resource
accounting and status. Runtime events and API status can be briefly out of
sync because they cross asynchronous boundaries.

On termination, kubelet asks the runtime to stop containers, ordinarily
sending `SIGTERM` and waiting for the grace period before forceful `SIGKILL`.
It then cleans up runtime/network resources and reports terminal state.
Container restart policy operates inside a Pod; higher-level workload
controllers replace or scale Pod objects.

**Where it breaks.** A container process can be running but not ready, so
it is not selected as a normal Service endpoint. A sandbox can fail before
any application container exists, so looking only for container logs misses
the error. A process can exit and restart repeatedly, with exponential
backoff producing a `CrashLoopBackOff` state. The Pod object may be deleted
from the API before a force-deleted process is confirmed dead on an
unresponsive node; a replacement with the same name does not prove the old
process has stopped.

## 3. Requests, limits and cgroup enforcement

Kubernetes resource settings have two distinct jobs:

* **Requests** primarily inform scheduling and relative sharing. The
  scheduler accounts for requests against node allocatable resources; CPU
  requests also generally influence cgroup CPU weight under contention.
* **Limits** are ceilings enforced by the kubelet/runtime through kernel
  mechanisms. Linux CPU limits are implemented as CPU bandwidth throttling;
  memory limits can lead to cgroup OOM kills.

On Linux, the runtime creates cgroups for containers (and often a Pod-level
cgroup) and configures the relevant controllers. With cgroup v2, inspect
`cpu.max`, `cpu.weight`, `memory.max`, `memory.current`, `memory.events`,
`pids.max` and `io.max` in the host hierarchy. A cgroup-v2 namespace can
hide the host path but does not change which ancestor limit applies.

```text
Pod resource request → scheduler's fit calculation
                    → node placement
Container CPU request → relative cgroup CPU weight
Container CPU limit   → quota/period → throttling under sustained use
Container memory limit→ memory.max → reclaim → cgroup OOM if unrecoverable
```

CPU limits are a ceiling, not a dedicated core. A latency-sensitive service
can experience throttling within a quota period even if the host appears to
have spare average capacity. Memory includes more than the application's
language heap: native allocations, stacks, charged page cache and some
kernel memory may count. A memory-backed `emptyDir` consumes memory, not
free disk. A Pod can be evicted for node pressure even before its own memory
limit is hit.

Kubernetes supports cgroup v1 and v2 depending on host/runtime configuration.
For v2, the kubelet and runtime should use a compatible cgroup driver;
systemd is the recommended alignment on systemd hosts. Mismatched cgroup
drivers can produce conflicting hierarchies and unreliable resource
management.

**Diagnose.** Check Pod requests and limits, QoS class, node allocatable,
restart/termination reason, cgroup counters and pressure metrics together.
`kubectl describe pod` provides events and last termination details;
`kubectl top` reports sampled usage but not all kernel memory or throttling
history. The node's cgroup files and PSI counters expose the enforcement
path more directly.

## 4. Kubernetes disk and ephemeral-storage accounting

Node-local storage is shared among image content, writable container layers,
container logs, `emptyDir` and kubelet state. `ephemeral-storage` requests
and limits let Kubernetes schedule and (where measurement is supported)
evict based on local usage. They are not a filesystem quota applied
uniformly to every path; actual enforcement depends on kubelet measurement
and the node's supported filesystem layout.

The kubelet tracks local ephemeral storage for writable layers, node-level
container logs, and disk-backed `emptyDir`. A memory-backed `emptyDir` is
accounted as memory instead. When a Pod exceeds its configured storage limit,
or node filesystems reach eviction thresholds, the kubelet may evict Pods.
Node-pressure signals distinguish filesystems commonly called `nodefs`,
`imagefs` and, in supported layouts, `containerfs`. Image garbage collection
removes unused images under its configured thresholds; it cannot reclaim
data still referenced by running containers or persistent volumes.

Do not estimate free space from an image's compressed download alone. Local
stores may hold compressed content plus unpacked snapshots; container logs,
copy-up files, writable layers, build cache and volumes add separate use.
Inode exhaustion can prevent file creation while byte capacity remains.
See [container and Pod storage](CONTAINER_STORAGE.md) for layer, volume and
CSI details.

## 5. Security boundaries in the Pod API

Kubernetes fields such as `hostPID`, `hostNetwork`, `hostIPC`, privileged
mode, Linux capabilities, `runAsUser`, `allowPrivilegeEscalation`,
`readOnlyRootFilesystem` and seccomp profile are requests that the runtime
translates to host kernel controls. The API server validates configuration,
but node kernel and runtime behavior enforce it.

Pod Security Standards define policy levels for these settings. A
NetworkPolicy is enforced only if the installed network implementation
supports it. A Pod user namespace can map container root to an unprivileged
host identity, subject to Linux/filesystem/runtime support and its feature
limitations. None of these options changes the shared-kernel model.

The effective boundary should be reasoned about as a combination of:

1. Namespace membership and mount exposure.
2. UID/GID mappings, capabilities and privilege escalation controls.
3. Seccomp and the node's AppArmor/SELinux policy.
4. Device access, host namespaces, host paths and runtime configuration.
5. Cgroup resource boundaries and network/storage plugin behavior.

## 6. Useful node-side inspection

These commands help connect API state to host processes and kernel
configuration. They require suitable node access and runtime tools.

```sh
# Locate containers and sandboxes through CRI rather than Docker's API.
sudo crictl pods
sudo crictl ps -a
sudo crictl inspect <container-id>
sudo crictl inspectp <pod-sandbox-id>

# After finding a host PID in inspect output:
sudo readlink /proc/<pid>/ns/{pid,mnt,net,user,cgroup}
sudo cat /proc/<pid>/cgroup
sudo cat /proc/<pid>/status

# Node and API-side view:
kubectl describe pod <pod> -n <namespace>
kubectl get events -n <namespace> --sort-by=.lastTimestamp
```

`crictl` is a CRI client and can inspect runtime state without asking the
Kubernetes API. On Kubernetes-managed nodes, avoid manually deleting
runtime-owned containers or editing cgroups: kubelet will reconcile against
the Pod spec and may recreate them.

## Further reading

* [Kubernetes cluster architecture](https://kubernetes.io/docs/concepts/architecture/)
* [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/)
* [Kubernetes scheduler](https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/)
* [Kubelet sync loop](https://kubernetes.io/docs/reference/node/kubelet-sync-loop/)
* [Pod lifecycle](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/)
* [Container Runtime Interface](https://kubernetes.io/docs/concepts/containers/cri/)
* [cgroup v2 in Kubernetes](https://kubernetes.io/docs/concepts/architecture/cgroups/)
* [Resource management](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)
* [Node-pressure eviction](https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/)
* [Garbage collection](https://kubernetes.io/docs/concepts/architecture/garbage-collection/)
* [Linux kernel security constraints](https://kubernetes.io/docs/concepts/security/linux-kernel-security-constraints/)
