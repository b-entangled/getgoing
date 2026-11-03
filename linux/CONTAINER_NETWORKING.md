# Container networking internals: namespaces, bridges, CNI and Services

Container networking is the combination of a private Linux network stack,
links between that stack and the host, address allocation, routing, firewall
rules, NAT and (in Kubernetes) service discovery and policy. “The container
has an IP” is just one state in this system. The packet still has to find a
route, cross host and possibly node boundaries, pass policy, reach a socket,
and return over a usable path.

This guide traces Docker's common Linux bridge path and Kubernetes' Pod and
Service paths. General kernel packet processing is in
[Linux networking internals](README.md#14-kernel-networking-internals-skb-softirq-and-socket-queues),
and higher-level network protocols are in
[`../networking/README.md`](../networking/README.md).

## 1. Network namespace: a separate stack, not a cable

A Linux network namespace isolates network devices, IP addresses, routes,
neighbor tables, sockets/port space, network sysctls, and network filtering
state. A new network namespace normally starts with only a loopback device,
which is down until configured. It has no implicit Ethernet or Internet
connection.

Processes in one network namespace share its TCP/UDP port space. Two
containers in distinct network namespaces can both bind `:8080`; two
processes sharing one Pod network namespace cannot bind the same address and
port unless the socket options/application permit it. `hostNetwork: true`
or Docker host networking deliberately shares the host network namespace,
removing this isolation and the container/Pod's independent IP identity.

### veth pair and bridge

A veth pair is a virtual Ethernet cable with two endpoints. Packets written
to one endpoint arrive at the other. The host side can be attached to a Linux
bridge; the peer can be moved into a container's network namespace.

```text
container netns                                  host netns
+---------------------------+                  +---------------------------+
| eth0 10.88.0.2/16         |<── veth pair ──>| veth... → br0 10.88.0.1  |
| default via 10.88.0.1     |                  | routes + firewall + NAT    |
+---------------------------+                  +-------------+-------------+
                                                            │
                                                         host NIC
```

The bridge forwards Ethernet frames between attached ports. The host's IP
stack routes packets between the container subnet and external networks.
For outbound IPv4 traffic, source NAT/masquerading commonly rewrites the
container address to a host address so replies return to that host. IPv6
can use routed addressing without NAT, depending on the configuration.

## 2. Docker bridge networking and published ports

On a typical Linux Engine installation, Docker creates bridge networks and
uses host networking/firewall mechanisms to connect containers. User-defined
bridge networks provide an isolated L2 segment on that daemon host and
Docker-managed name resolution between attached containers. The default
bridge has different discovery behavior and is less configurable.

With a user-defined bridge, containers can generally reach each other at
their container IPs and through Docker's network-scoped DNS names. An
unpublished port is reachable by peers on that same bridge, but is not
automatically published to external hosts. `-p HOST:CONTAINER` asks Docker to
install a forwarding path from a host address/port to a container address/
port. The exact path may use netfilter DNAT, a userland proxy, or a configured
direct-routing mode.

### Packet walk: outside client to `-p 8080:80`

For the common bridge + NAT implementation, the inbound path is:

1. The client sends to host-IP:8080; the NIC receives the frame into a host
   RX queue.
2. The host network stack processes the packet through routing and netfilter
   hooks. Docker's published-port rules match the destination and DNAT it to
   the container-IP:80. A userland proxy may handle some configurations.
3. The host routes the translated packet toward the bridge. The bridge
   forwards it to the veth port associated with that container.
4. The container's network namespace receives the packet on its `eth0`;
   TCP demultiplexes it to the listening socket.
5. The reply traverses the reverse path. Conntrack uses the connection state
   to reverse NAT and emit a packet sourced from the host address/port.

```text
client → host NIC → PREROUTING/DNAT host:8080 → route → bridge → veth
       → container eth0 → TCP socket
reply: socket → veth → bridge → host routing/conntrack reverse-NAT → client
```

Outgoing container traffic typically follows `container eth0 → veth → bridge
→ host routing/FORWARD → POSTROUTING masquerade → host NIC`. Firewall policy,
forwarding sysctls, conntrack capacity, MTU, routing and host firewalls can
change or interrupt either path. Docker's nftables backend is supported in
newer Engine versions with version/configuration constraints; do not assume
that every host uses iptables or inspect only one rules backend.

Docker also provides host, none, overlay, macvlan and ipvlan drivers. They
have different topologies and security/port behavior. Host networking shares
the host stack; none creates no external connectivity; overlay links Docker
daemons for Swarm; macvlan/ipvlan integrate containers with external L2/L3
networks. On Docker Desktop, the Engine runs inside a VM, so the Linux host
bridge picture is not the same as the desktop host network.

## 3. Kubernetes network model

Kubernetes defines connectivity semantics, but most packet plumbing is
provided by the node runtime and a network implementation. A conforming
cluster network aims for:

* Each Pod gets a unique cluster-reachable IP address.
* Containers in the same Pod share a network namespace and communicate over
  `localhost`.
* Pods can communicate with other Pods without mandatory NAT, including
  across nodes (unless policy intentionally blocks them).
* Nodes can communicate with Pods on their nodes.
* Services provide a stable virtual address/name while their backing Pods
  change.

On Linux, a runtime commonly invokes CNI plugins to attach a Pod sandbox to
the network. CNI is an interface for adding and removing a container's
network connectivity; it does not prescribe a particular overlay, routing
protocol, firewall, or policy engine. A plugin may use a veth + bridge,
routed host interfaces, an overlay tunnel, eBPF, cloud VPC interfaces, or
another data plane.

During Pod setup, the runtime creates a sandbox and network namespace. The
network plugin allocates an address and configures interface(s), routes,
gateway and host/node connectivity. On teardown it releases the address and
removes corresponding links/rules. Repeated setup/teardown must be
idempotent enough to handle retries and node/runtime restarts.

```text
kubelet ─CRI─> runtime: RunPodSandbox
                    │ create sandbox/netns
                    └─ invoke network implementation (often CNI)
                        ├─ allocate Pod IP
                        ├─ create/connect interfaces
                        ├─ install routes and policy
                        └─ return addresses/routes to runtime
```

Kubernetes does not require a specific CNI plugin. It also does not
guarantee that a `NetworkPolicy` object is effective: the selected network
implementation must enforce that API. Service proxying can be provided by
`kube-proxy` or an integrated implementation in the network plugin.

## 4. Service IPs: a virtual destination

A normal `ClusterIP` is a virtual IP allocated for a Service, not usually an
address assigned to a physical interface. A Service selector identifies
Pods, and Kubernetes maintains EndpointSlices listing the backends. A
service-proxy implementation observes Service and EndpointSlice changes and
programs a node data plane to translate or route Service traffic to an
endpoint.

On Linux, kube-proxy modes include iptables and nftables (and IPVS on
versions/configurations that support it). These modes install kernel rules;
they do not normally proxy every request through a user-space process. The
first packet to a ClusterIP is matched to a backend, often using DNAT and
conntrack state; subsequent packets follow connection tracking. CNI plugins
may implement equivalent service translation themselves.

```text
client Pod → Service ClusterIP:port → node service rules → PodIP:targetPort
                     EndpointSlice backends change as Pods become eligible
```

Endpoint readiness matters. A running but unready Pod is generally excluded
from normal Service traffic. `NodePort`, `LoadBalancer`, Ingress and Gateway
API are distinct exposure layers: Service proxying is not the same as an
HTTP reverse proxy or cloud load balancer. Ingress is stable but frozen;
Kubernetes documentation points new feature work toward Gateway API.

Kubernetes DNS is provided by a cluster DNS implementation (commonly
CoreDNS), which watches Services/Pods and answers names. DNS resolution
returns names and addresses; it does not itself forward packets or balance
connections. Clients often retain resolved IPs and long-lived TCP connections,
so endpoint updates do not migrate established connections.

## 5. Network policy and security boundaries

Kubernetes NetworkPolicy selects Pods and describes allowed L3/L4 traffic.
The actual enforcement is a responsibility of the network implementation.
Creating a policy without an enforcing implementation leaves it as API data
with no traffic effect. Policy propagation may be asynchronous; established
connections and non-TCP/UDP/SCTP protocol handling can vary by plugin.

Network namespaces are not host firewalls. The host still routes container
traffic, applies host firewall rules, performs conntrack and can expose
services on host interfaces. Published ports and NodePorts are intentional
holes through isolation. `hostNetwork` removes the Pod's separate network
stack and changes how policy and port collisions work. For multi-tenant
clusters, reason about CNI policy, node firewall, service rules, cloud ACLs,
and ingress separately.

## 6. Trace and diagnose a dropped connection

Start from the endpoints and walk outward. Identify whether the failure is
name resolution, connection establishment, transport, policy, or application
readiness.

```sh
# Kubernetes objects and endpoint state
kubectl get pod -A -o wide
kubectl get svc,endpointslice -n <namespace>
kubectl describe pod <pod> -n <namespace>

# On the node, in the relevant network namespace
ip -br link
ip addr
ip route
ss -lntup
nft list ruleset                # if nftables is the active backend
iptables-save                   # if the iptables backend is active
conntrack -S                    # conntrack counters, when installed

# Enter the Pod network namespace using a sandbox/container host PID
nsenter -t "$PID" -n -- ip addr
nsenter -t "$PID" -n -- ip route
nsenter -t "$PID" -n -- ss -lnt
```

Useful questions:

1. Did DNS resolve the intended Service name and address?
2. Does the Service have ready EndpointSlice addresses and matching ports?
3. Can the source Pod route to that destination, and can the destination
   route replies back?
4. Which namespace owns the listening socket? Is it bound to `127.0.0.1`
   instead of the Pod interface (`0.0.0.0` / Pod IP)?
5. Is the traffic subject to a NetworkPolicy, host firewall, security group,
   Service DNAT or external load balancer?
6. Are interface MTU, conntrack capacity, routes, IPAM allocation and
   packet-drop counters healthy?

## Further reading

* [Linux network namespace manual](https://man7.org/linux/man-pages/man7/network_namespaces.7.html)
* [Linux networking stack scaling](https://docs.kernel.org/networking/scaling.html)
* [Docker networking overview](https://docs.docker.com/engine/network/),
  [bridge driver](https://docs.docker.com/engine/network/drivers/bridge/) and
  [port publishing](https://docs.docker.com/engine/network/port-publishing/)
* [CNI specification](https://github.com/containernetworking/cni/blob/main/SPEC.md)
* [Kubernetes networking model](https://kubernetes.io/docs/concepts/services-networking/)
* [Cluster networking](https://kubernetes.io/docs/concepts/cluster-administration/networking/)
* [Virtual IPs and Service Proxies](https://kubernetes.io/docs/reference/networking/virtual-ips/)
* [NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
* [DNS for Services and Pods](https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/)
* [Ingress](https://kubernetes.io/docs/concepts/services-networking/ingress/) and
  [Gateway API](https://gateway-api.sigs.k8s.io/)
