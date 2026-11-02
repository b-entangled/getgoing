# Networking (how the internet works)

There are no Go packages in this directory. What follows is prose about the
machinery every networked program rides on whether it knows it or not: the
packet underneath an `http.Request`, the resolver behind a hostname, the
certificate chain a `tls.Dial` walks. None of it is optional — a thirty
second stall is usually one of these layers doing precisely what it was
designed to do, which is why knowing the design is the debugging skill.

Each entry has the same four parts: the need it fills, the mechanics that
fill it, the ways it fails, and the stdlib surface where your code meets
it. Read them top to bottom the first time; after that they work as
reference. There is no test suite to run here — the subject is the network
you already have, and the tables in it are the specification.

Language features live over in [concepts](../concepts/README.md) and
runtime behaviour in [internals](../internals/README.md); the systems
built on top of these mechanisms are in
[distributed](../distributed/DESIGNS.md) and [sdesign](../sdesign/DESIGNS.md).
Four entries below carry a ★ — routing, DNS, TCP and TLS — because they
fail loudly and every operator ends up learning them by hand.
For application-layer service boundaries — HTTP APIs, gRPC, TLS
termination, authentication and authorization — see
[`webservices`](../webservices/README.md).

---

## 1. The internet in one page

**Why.** Your program opens a socket to a host it has never met, in a
building it will never see, over fibre owned by three companies it has no
contract with. The need is one forwarding scheme that works between
strangers: no global scheduler assigning transmission slots, no handshake
before the first packet, and a path that may change while a flow is live.

**How.** The unit of forwarding is the datagram — a self-contained block of
bytes carrying its own destination address, handed to whichever router is
next door, which decides afresh where it goes after that. There is no
circuit and no reservation, and a router keeps no memory of where a
packet came from. Packets queue in small buffers and move
store-and-forward, so congestion appears first as queue growth and then
as loss. Loss is a normal operating condition rather than an error, and
everything above it must assume some fraction of what it sends will
simply vanish.

The strangers are grouped into autonomous systems — one policy, one
administrator, one number. An access network buys *transit* from an
upstream provider that carries its traffic everywhere; equals *peer*
instead, exchanging only their own and their customers' prefixes, usually
for free, at an internet exchange point — a shared layer-2 fabric where a
rack port buys adjacency with hundreds of networks. Tier-1 networks peer
with everybody and buy transit from nobody, so the global graph is a
hierarchy of bilateral contracts with no centre.

That arrangement has no board and no outage protocol. IANA hands large
blocks to five regional registries, they delegate to networks, and every
other relationship is a contract. What keeps it coherent is the
*end-to-end principle*: the network moves bytes and does not retry them,
authenticate them, encrypt them, or repair their order. Reliability
belongs to the two hosts, because only the hosts know what was sent.

| Relationship | What crosses | Who pays |
|---|---|---|
| Transit | a customer's traffic to the whole internet | the customer |
| Peering | each side's own and customers' prefixes | nothing, or a port fee |
| IXP fabric | whatever members choose to exchange | a flat monthly port |

**Where it breaks.** A transit provider cuts a fibre and a whole region
degrades while every local link stays up — the failure is a contract, not
a cable. Congestion is asymmetric, so uploads and downloads suffer
differently. A single per cent of random loss collapses a TCP flow to a
fraction of its path capacity, and queueing at a shallow router is felt
first as jitter, which real-time media cannot hide behind retransmission.

**In Go.** `net.UDPConn.Read` hands you one datagram per call — no
reassembly, no retransmission, no ordering — the best-effort contract made
visible. `net.Dialer.Control` runs on the socket before connect, where
marks and buffer sizes live. Everything else assumes the rest works.

---

## 2. The layer model

**Why.** A connection can be refused, timed out, reset or silently
blackholed, and each of those names points at a different box. Without
shared vocabulary for which layer does what, every incident report
degenerates into "the network is broken" while the real fault sits one
layer below the one being watched.

**How.** The TCP/IP model has four layers — link, internet, transport,
application. OSI has seven, splitting link into physical plus data link
and inserting session and presentation above transport. No packet on the
wire is organised by OSI; it survives as vocabulary, and the four-layer
model survives as the description of what is actually implemented.

Encapsulation is the mechanism: each layer prepends its own header on the
way down (the link layer also appends a frame check sequence), and the
receiving side strips headers off on the way up. The payload is opaque to
the layer carrying it, so the same bytes in the same buffer are an HTTP
message, a TCP segment, an IP datagram and an Ethernet frame depending
only on which header you are currently reading.

| Layer | Addressed by | Scope | Unit | Header |
|---|---|---|---|---|
| Link | 48-bit MAC | next hop only | frame | Ethernet 14 B |
| Internet | 32 or 128-bit IP | end to end | datagram | IPv4 20 B |
| Transport | 16-bit port | one process | segment | TCP 20 B |
| Application | host and URL | one program | message | HTTP/1.1 |

The nuance that keeps the model honest: BGP is the internet layer's
control plane, yet it runs over TCP port 179 and peers exchange their
routes as ordinary application data. Control planes commonly ride the
data plane they configure, which is exactly why a forwarding bug can take
down the protocol that would have repaired it.

**Where it breaks.** The layers fail independently and report into
different places: a host answers ping while the process is not listening,
so reachability checks pass and the service is still down. Firewalls are
opened for the wrong layer — port 80 reachable, port 443 filtered. A
tunnel with an MTU mismatch black-holes one size of packet while small
ones sail through, and application timeouts blame a healthy network.

**In Go.** `net.Conn` *is* the transport layer: a byte stream with
deadlines and no message boundaries. `net.UDPConn` skips it entirely.
`http.Client` sits one layer up, and `net.Dialer.Control` is where the
link and internet choices are still visible from user code.

---

## 3. IP addressing and CIDR

**Why.** A router must forward to billions of destinations from a table
holding thousands of entries, and it must do it in microseconds on every
packet. The need is aggregation: one entry that covers a contiguous block
of addresses, chosen correctly among the entries that overlap it.

**How.** IPv4 addresses are 32 bits, written as four octets. A prefix
length splits the bits into a network part and a host part — `192.0.2.0/24`
means the first 24 bits name the network and the last 8 name the host.
Route tables hold prefixes rather than hosts; classful A/B/C blocks gave
way to classless inter-domain routing in 1993 because a fixed /8 or /16
was too blunt to allocate or to aggregate.

Forwarding picks the *longest* match: if a packet for 192.0.2.7 matches
`192.0.0.0/16`, `192.0.2.0/24` and `192.0.2.0/28`, the /28 wins because
its mask covers the most bits. That single rule is why a table of a
million routes still resolves in a hardware trie, and why one stray
longer prefix in a leaked announcement black-holes traffic rather than
merely annoying someone.

| Block | What lives there |
|---|---|
| 127.0.0.0/8 | loopback — never leaves the host |
| 169.254.0.0/16 | link-local; cloud metadata sits at 169.254.169.254 |
| 10/8, 172.16/12, 192.168/16 | RFC 1918 private, not routable on the internet |
| 100.64.0.0/10 | carrier-grade NAT shared address space |
| 224.0.0.0/4 | multicast — one-to-many, no connection to have |

IANA handed out its last free /8 in 2011 and the registries followed,
which is the whole reason IPv6 exists: 128 bits, written as eight
hexadecimal groups, with a /64 given to every LAN by convention so the
host space is unbounded. A host derives its address from router
advertisements (SLAAC), neighbour discovery over ICMPv6 replaces ARP,
broadcast disappears in favour of multicast and anycast, and because
every host is globally routable there is no NAT to pierce.

**Where it breaks.** Two merged networks both using 10/8 overlap and
cannot reach each other without renumbering. A server bound to 127.0.0.1
is unreachable from outside the host while one bound to 0.0.0.0 is
reachable from everywhere, and the difference is a single line of
config. Any process that can make a request can usually reach
169.254.169.254 and read instance credentials — the classic SSRF prize.
Duplicate addresses appear when a static host collides with a DHCP pool.

**In Go.** `net/netip.Prefix` with `Contains` is the aggregation rule in
code, and `netip.Addr` carries `IsPrivate`, `IsLoopback`,
`IsLinkLocalUnicast` and `IsMulticast` for the special blocks above.
`net.ParseCIDR` is the older spelling. `Resolver.LookupNetIP` returns
`netip.Addr` values straight from the answer section.

---

## 4. Ethernet, ARP and the MTU

**Why.** Before IP can deliver anything, two devices on the same wire
have to address each other, and every wire has a size limit. Both facts
are invisible until they bite, and both are decided below the layer your
code can see.

**How.** An Ethernet frame is 6 bytes of destination MAC, 6 bytes of
source MAC, 2 bytes of EtherType, a payload of 46 to 1500 bytes, and a
4-byte frame check sequence. The 1500-byte payload ceiling is the MTU,
and every size calculation in the stack hangs from it: a TCP segment
carries 1460 bytes of data because 1500 minus a 20-byte IP header minus
a 20-byte TCP header is 1460. Jumbo frames raise the ceiling to 9000
inside data centres where every hop agrees.

| Link | MTU | Why it differs |
|---|---|---|
| Ethernet LAN | 1500 | the default everything derives from |
| PPPoE | 1492 | 8 bytes of PPPoE overhead eat into it |
| IPv6 minimum | 1280 | the floor every link must support |
| Jumbo frame | 9000 | data-centre links only, all hops must agree |

ARP maps an IPv4 address onto a MAC address. A host that wants
192.0.2.7 broadcasts "who has 192.0.2.7?" to `ff:ff:ff:ff:ff:ff`, the
owner answers with its own MAC, and the result sits in a cache for about
a minute. Only local addresses are ever resolved this way: to reach
anything off-subnet a host ARP's for its *default gateway*, not for the
destination, because frames only ever travel to the next hop. ARP has no
authentication at all, so an unsolicited reply can rewrite any entry.

Fragmentation splits a datagram that will not fit the next link. With
the don't-fragment bit clear the router does the split and tags each
piece with an offset, and only the destination host ever reassembles
them. With the bit set the router refuses and replies with ICMP
"fragmentation needed" (type 3, code 4) carrying the smaller MTU — which
is the feedback loop Path MTU Discovery rides: start optimistic, shrink
when told.

**Where it breaks.** Filter the ICMP reply or the DF-flagged probe and
you get the PMTU black hole: the handshake completes because a SYN is
tiny, then every full-size segment vanishes and the flow grinds through
doubling retransmission timeouts until it gives up. PPPoE and VPNs
produce the same symptom by negotiating 1492 under an application that
assumed 1500. ARP poisoning puts an attacker between a host and its
gateway with two spoofed replies, and bad FCS frames are dropped by the
NIC without ever reaching the stack.

**In Go.** The stdlib does not expose the MTU; `net.Dialer.Control` is
where you reach the fd for `IP_MTU_DISCOVER` and `TCP_MAXSEG` through
`syscall.SetsockoptInt`, with the constants in `golang.org/x/sys/unix`.
`TCPConn.SetReadBuffer` and `SetWriteBuffer` tune the socket queues.
Everything above that is the kernel's job.

---

## 5. ICMP and path diagnostics

**Why.** IP alone has no error channel: when a router drops a packet
there is nothing in the protocol that tells the sender. Applications
need a way to learn *where* a path failed, and networks need a way to
say "not this way" without opening a session or state.

**How.** ICMP rides inside IP as protocol number 1 and is addressed to a
host rather than to a port — it has no ports, no session and no
ordering. Every error message quotes the offending datagram's IP header
plus its first eight bytes, which is enough for a TCP or UDP receiver to
identify which connection went wrong, and that is why an ICMP reply
alone cannot tell your application anything.

| Type | Name | What it carries |
|---|---|---|
| 8 / 0 | echo request / reply | reachability and round-trip time |
| 3 code 4 | fragmentation needed | the next hop's MTU |
| 3 code 3 | port unreachable | "no listener" for a UDP probe |
| 11 | time exceeded | the TTL hit zero at this hop |

Traceroute exploits time exceeded. Send a probe with TTL 1 and the first
router replies type 11, then TTL 2 and the second replies, and so on
until the destination cannot decrement further and answers in its own
way — port unreachable in UDP mode, echo reply in ICMP mode. MTR runs
those probes side by side to attach a per-hop loss percentage to each
line of the path.

```text
 1   192.0.2.1      0.4 ms   0.3 ms   0.3 ms   type 11 — TTL 1 met a router
 2   198.51.100.1   8.2 ms   8.0 ms   8.1 ms   type 11 — TTL 2 met a router
 3   203.0.113.9    9.1 ms   9.0 ms   9.0 ms   type 3/3 — the destination itself
```

Kernels rate-limit ICMP hard — Linux answers at most one identical
message per second, with a global message-per-second cap — so under load
ICMP is the first traffic to disappear, exactly when a diagnostic would
have been most useful. It is a courtesy protocol, not a guaranteed one.

**Where it breaks.** Firewalls drop ICMP and traceroute ends in a column
of `*`, while PMTUD silently stops working and the connection hangs
instead of shrinking. Rate limiting masquerades as packet loss in
diagnostics. An echo reply proves only that something answered an echo:
a firewall can pass type 8 while the TCP port you need stays shut, so
reachability checks and connection attempts disagree. Spoofed redirects
and unreachables are a classic way to bend a host's route, which is why
hosts are told to ignore ICMP redirects.

**In Go.** The stdlib ships no ICMP package: `net.Dial("ip4:icmp", host)`
needs raw-socket privilege and hands back untyped bytes, so real code
reaches for `golang.org/x/net/icmp` with `x/net/ipv4`, or shells out to
`ping` and `traceroute` and parses the text. Neither path is in `net`.

---

## 6. Routing and BGP ★

**Why.** Every autonomous system must choose, from thousands of equally
valid paths to the same prefix, the one it will forward along — and it
must choose by policy expressed in money and contracts, not by hop
count. Two neighbours must also be able to exchange those choices
continuously, without a central directory of who reaches what.

**How.** An autonomous system is identified by an ASN, a 16- or 32-bit
number. eBGP connects different ASes; iBGP mirrors what an AS learned
from the outside to every router inside it, usually through route
reflectors so a full mesh is unnecessary. The protocol speaks over TCP
port 179 — it borrows retransmission and ordering from TCP rather than
reinventing them — and exchanges OPEN, UPDATE, KEEPALIVE and
NOTIFICATION messages for the lifetime of the session.

```
AS64500 peer: customer AS 64500 → eBGP over TCP/179 → provider AS 64501
update:      203.0.113.0/24 announced → AS_PATH [64500] → LOCAL_PREF 200
best path:   LOCAL_PREF → AS_PATH length → MED → eBGP over iBGP → IGP cost
export:      provider prepends its ASN → path [64501 64500] → peer sees 2 hops
hold timer:  keepalive every 60 s → silence at 180 s → session drops → all routes gone
```

UPDATEs carry the path vector: the AS_PATH list of every AS traversed,
appended on export and rejected on receipt if your own ASN already
appears — which is the loop check. A full table is about a million
prefixes today, and the decision among them runs LOCAL_PREF (your own
policy, higher wins) then shortest AS_PATH, then the neighbour's MED,
then eBGP over iBGP, then IGP cost to the exit. RFC 4271 deliberately
leaves the selection algorithm implementation-defined, so two vendors may
legitimately pick different exits for the same prefix.

Traffic follows contracts: transit customers' prefixes are carried
anywhere, peer prefixes only for each side's own customers, upstream
prefixes downstream — the valley-free pattern. Defending the routing
itself took decades. An origin hijack announces somebody else's prefix,
a route leak re-exports customer routes to a provider that should never
have seen them, and the defence is RPKI: a signed ROA states which ASN
may originate a prefix and up to what length, routers fetch it over RTR
and mark invalid announcements accordingly.

**Where it breaks.** A session flap withdraws every prefix it carried in
one instant, so a healthy site goes dark with every link still up.
Mis-set LOCAL_PREF quietly sends traffic over an expensive transit path
and the only symptom is the invoice. Without RPKI validation a hijacked
prefix is simply believed, and asymmetric return paths defeat stateful
firewalls that only see one direction. Reconvergence takes seconds to
minutes, which is longer than most application timeouts.

**In Go.** No BGP speaker is in the stdlib — implementations are
embedded or shelled out (FRR, bird, gobgp). What Go shows you is the
consequence: DNS still resolves while `net.Dial` to the withdrawn prefix
returns `i/o timeout`, which is how a routing event looks from
application code. `netip.Prefix` models the prefixes you filter.

---

## 7. DNS ★

**Why.** Humans use names and packets need numbers, and the mapping has
to survive every host being able to reach every other without a central
database sitting in the request path. The need is a distributed,
cacheable, delegated directory that can answer from anywhere in a few
milliseconds.

**How.** Resolution is delegation all the way down: the root zones
hand `.com` to a TLD server, which hands `example.com` to the servers
named in the zone's NS records, and only those know the answer. Three
roles play it out — the stub resolver in your program, which only ever
asks; the recursive resolver, which walks the delegation and caches it;
and the authoritative server, which answers for its own zones.

```
stub:      lookup www.example.com → OS resolver → recursive resolver on UDP 53
cache hit: recursive already holds a fresh A → answer → TTL counts down locally
miss:      recursive → root → referral to .com → referral to example.com's NS
auth:      example.com server → A 203.0.113.10 → cached → answer returned to stub
```

On the wire a name is a run of length-prefixed labels of at most 63
bytes, 255 for the whole name. Queries run over UDP port 53 under a
classical 512-byte ceiling; EDNS0 raises the advertised size, and a
reply that still will not fit sets the truncated bit so the client
retries over TCP, where zone transfers (AXFR) always run. Every answer
carries a TTL the cache must honour, and RFC 2308 caches negatives
too, so a typo costs no query; DoT (853) and DoH (443) encrypt it all.

| Type | Answers | Typical use |
|---|---|---|
| A / AAAA | IPv4 / IPv6 address | the lookup everything else wants |
| NS | authoritative server for a zone | delegation |
| CNAME | an alias to another name | CDNs, and apex-record caveats |
| MX | mail exchanger with priority | SMTP routing |
| TXT | arbitrary strings | SPF, DKIM, domain verification |
| PTR | the name behind an address | reverse lookup, mail reputation |
| SOA | zone parameters | serial, timers, negative-cache TTL |
| SRV | service host, port and priority | internal service discovery |
| CAA | which CA may issue for the zone | certificate policy |

**Where it breaks.** A resolver outage is total blindness: every lookup
fails while every host is up, and the failure looks like "everything is
down". The three failure modes mean quite different things — NXDOMAIN is
a definitive "no such name", SERVFAIL is resolver or authoritative
trouble or a DNSSEC validation failure, and a timeout is nothing
answered at all — yet most code collapses them into one string. Cache
poisoning is an off-path forgery of a response; TXID and source-port
randomization raise the bar (RFC 5452) and DNSSEC's RRSIG chain removes
trust from the transport entirely. Open resolvers also make fine
amplifiers: a small spoofed query can return a much larger answer to a
victim.

**In Go.** `net.Resolver` is the stub: `LookupIPAddr`, `LookupNetIP`,
`LookupCNAME`, `LookupSRV` and `LookupMX` all speak to whatever the host
is configured with. `Resolver.PreferGo` forces the pure-Go
implementation in `net` and `Resolver.StrictErrors` changes how partial
failures surface — useful for making resolver behaviour identical across
machines. The stdlib does no DNSSEC validation.

---

## 8. TCP ★

**Why.** IP will drop, duplicate, reorder and truncate freely, and an
application wants a pipe: bytes in one end come out the other, in order,
exactly once. The need is a reliable ordered stream plus a fair share of
a shared, unknowable path — reliability alone would let one sender
saturate every link it touches.

**How.** A connection is identified by the 4-tuple of protocol, source
address, source port, destination address and destination port, which is
why one listener on 443 can hold thousands of distinct sessions and why
two sockets colliding on the same tuple cannot coexist. Opening it costs
one round trip, and both sides agree their starting sequence numbers,
segment size, window scale and SACK support in the process.

```
open:    SYN seq 100 → SYN-ACK seq 500 ack 101 → ACK ack 501 (one RTT)
data:    segment seq 501 len 1460 → cumulative ACK → SACK blocks for the gaps
loss:    3 duplicate ACKs → fast retransmit without waiting → recovery, cwnd halved
timeout: RTO starts at 1 s → doubles per retry → Karn forbids sampling retransmits
```

Sequence numbers count *bytes*, and the acknowledgement number is the
next byte wanted, which makes a cumulative ACK both an acknowledgement
and a retransmission instruction. SACK reports the blocks received
beyond a gap so the sender retransmits only what is actually missing.
Two independent windows then decide how much may be sent: the receiver's
advertised window says "my buffer has this much room", while the
sender's congestion window says "the path probably has this much room",
and the effective window is the smaller of the two. Confusing the two
is the classic mistake — one is flow control, the other is fairness.

Congestion control starts by doubling the window every round trip (slow
start), switches to adding one segment per round trip once past the
threshold, and backs off on loss. CUBIC, Linux's default for many years,
grows the window as a cubic function of time since the last loss rather
than of acknowledgement count; BBR instead models bottleneck bandwidth
and minimum round-trip time and ignores loss as a signal altogether.
Window scaling lifts the 64 KB ceiling so a fat pipe can stay full, and
delayed ACK interacts badly with Nagle's small-packet coalescing — Go
sets `TCP_NODELAY` for you, which is why request headers go out in one
write.

**Where it breaks.** After our final ACK a connection lingers in
TIME_WAIT for 60 seconds so late duplicates cannot impersonate a new
session, and a client that opens and closes thousands of connections per
minute exhausts the roughly 28,000 ephemeral ports of a machine. SYN
flooding fills the half-open queue; SYN cookies answer the final ACK
with state encoded in the sequence number so no memory is held, at the
cost of TCP options. A zero window means the peer is stalled, not dead,
and only a window probe eventually revives it.

**In Go.** Deadlines are mandatory: `conn.SetDeadline`, `SetReadDeadline`
and `SetWriteDeadline` are the only cancellation a blocking socket has,
so a `context.Context` must be translated into them. Keep-alives come
from `net.Dialer.KeepAlive`. Goroutine-per-connection is the idiomatic
shape and holds up well into the tens of thousands, provided bodies are
drained and goroutines are not parked on sockets with no deadline; the
cost model lives in [internals](../internals/README.md).

---

## 9. UDP and QUIC

**Why.** Some applications need to emit one message now and hear an
answer or nothing — no handshake, no teardown, no ordering to repair.
And TCP's guarantees are paid for with a round trip and a queue, which
is a bad trade for a voice frame that is worthless 200 ms from now.

**How.** UDP is datagrams with a thin header: source and destination
ports, a length, and a checksum that is mandatory in IPv6 and optional
in IPv4. The kernel keeps no per-flow state beyond the socket table, so
a `sendto` is one datagram and a loss is simply a hole in the stream
that nobody notices unless the application does. There is no congestion
control, which is both its freedom and its hazard.

That freedom is why DNS asks and answers in single datagrams, why media
senders drop rather than retransmit, and why games can let stale state
expire in the next packet instead of blocking on an old one. Multicast
and broadcast also have no notion of a connection to establish, so
datagrams are the only shape available.

QUIC is what happens when you rebuild a modern transport in user space
on top of UDP (RFC 9000). Streams are independent, so a lost packet
blocks only the stream that carried it — TCP's head-of-line blocking
survives into HTTP/2, where one loss stalls every multiplexed request.
Connection IDs survive NAT rebinding and address changes, so a phone
moving from Wi-Fi to cellular keeps its session. TLS 1.3 is integrated
rather than layered, headers are authenticated, and 0-RTT resumption
lets a returning client send data in its first flight — which any
on-path attacker can replay, so it belongs to idempotent requests only.

| Mechanism | What it guarantees | Ordering | Setup |
|---|---|---|---|
| UDP | best effort, checksum | none | none |
| TCP | reliable byte stream | strict, global | 1 RTT handshake |
| QUIC | reliable streams | per stream | 1 RTT, or 0 RTT resumed |

**Where it breaks.** Bare UDP has no manners: a naive sender starves TCP
flows sharing the link until a fair queue or an operator punishes it.
Forged source addresses turn any query service into an amplifier, which
is how DNS reflection attacks reach tens of gigabits. Middleboxes and
carriers rate-limit or simply drop UDP, so QUIC can be blocked on a
network where TCP port 443 is always allowed, and a checksum collision
corrupts a datagram silently rather than triggering a retransmission
nobody was going to send.

**In Go.** `net.ListenUDP`, `net.DialUDP` and `net.UDPConn` are the whole
datagram API; `ReadFromUDP` returns the sender per packet because there
is no session to remember it, and deadlines apply per call. The stdlib
ships no QUIC stack, so there is no HTTP/3 in `net/http` either — that
is `quic-go` and friends, outside the standard library.

---

## 10. TLS 1.3 and the certificate PKI ★

**Why.** The network is hostile by assumption: anyone on the path can
read, drop, reorder or inject bytes, and a hostname is only a hostname
until someone claims it. The need is confidentiality and authenticity in
as few round trips as possible, plus a way for a client to decide that
the party holding the key really is the named host.

**How.** TLS 1.3 (RFC 8446) cut the handshake to one round trip: the
client offers a key share, the server answers with its own and finishes,
and both sides derive independent keys from an ephemeral Diffie-Hellman
exchange (X25519 by default) using HKDF. Because the shared secret is
fresh per session and never sent, capturing today's traffic and stealing
tomorrow's private key decrypts nothing — that is forward secrecy, and
it is why the RSA key-exchange suites are gone. Only AEAD ciphers
remain: AES-GCM, ChaCha20-Poly1305, no CBC, no compression.

```
first:   ClientHello with key share → ServerHello + key share → Finished both ways
resumed: PSK binder verified → early data accepted → 0-RTT, replayable on the path
derive:  X25519 shared secret → HKDF → handshake keys → application traffic keys
```

Certificates decide *who* the peer is. A chain runs leaf → intermediate
→ root, each link signed by the next, and trust comes from roots
preinstalled in the operating system or browser rather than from
anything in the handshake. The leaf's subjectAltName must list the
hostname being dialled — the common name is legacy — and CAA records in
the zone name which CAs are allowed to issue for it. OCSP stapling lets
the server include a fresh proof that its certificate is not revoked, so
the client skips both a round trip to the CA and the privacy leak it
would cause. Honest gap: OCSP responses go stale and CRLs are enormous,
so real revocation is approximate and browsers answer it with
short-lived certificates renewed every few days rather than with a live
check.

**Where it breaks.** Name mismatch (SNI and hostname not in the SAN) and
expired certificates are the two common ones, and clock skew on either
side is a frequent cause of the second. An untrusted root is routine in
a minimal container image that shipped without a CA bundle. A server
that omits intermediates works against browsers that fetch the missing
link and fails against clients that will not. Wildcards cover exactly
one label, so `*.example.com` never matches `a.b.example.com`.

**In Go.** Set `tls.Config.MinVersion` explicitly — with it unset Go
will not negotiate below TLS 1.2, but saying so documents intent — and
`InsecureSkipVerify: true` belongs to tests only, never to a client that
traverses a network. Client certificates for mTLS are `ClientAuth:
tls.RequireAndVerifyClientCert` plus a `x509.CertPool`; resumption uses
a `ClientSessionCache`. `tls.DialWithDialer` and `http.Client`'s
`TLSClientConfig` are the two entry points.

---

## 11. HTTP 1.1, 2 and 3

**Why.** The application protocol must survive proxies, caches, naive
load balancers and decades of clients while carrying objects, streams
and RPCs. Each version buys a different resource: 1.1 buys simplicity
and deployability, 2 buys multiplexing inside one connection, 3 buys
multiplexing that a single lost packet cannot stall.

**How.** HTTP/1.1 keeps connections alive by default, so one TCP
connection serves request after request, and frames each message with
Content-Length or chunked transfer encoding. Pipelining — sending the
next request before the previous response arrives — was specified and
never deployed: head-of-line blocking through intermediaries made
browsers switch it off, so clients open several connections per host.

| Code | Meaning | Code | Meaning |
|---|---|---|---|
| 200 | OK, body present | 201 | Created, Location set |
| 204 | No Content, zero bytes | 301 | Moved Permanently, change the URL |
| 302 | Found, temporary redirect | 304 | Not Modified, use your cache |
| 400 | Bad Request, parsing failed | 401 | Unauthorized, authenticate first |
| 403 | Forbidden, allowed to ask but refused | 404 | Not Found, no such resource |
| 408 | Request Timeout, client too slow | 429 | Too Many Requests, back off |
| 500 | Internal Server Error, your bug | 502 | Bad Gateway, upstream sent garbage |
| 503 | Service Unavailable, shed load | 504 | Gateway Timeout, upstream stayed silent |

Retries are only safe where the method is idempotent.

| Method | Idempotent | Blind retry after a timeout? |
|---|---|---|
| GET, HEAD | yes, and safe | yes — reads only |
| PUT, DELETE | yes, same end state | yes, once you have checked |
| POST | no | never — needs an idempotency key |

HTTP/2 multiplexes many streams over one TCP connection with framed,
binary messages and HPACK header compression, which solves idle
connections and creates head-of-line blocking: one lost segment pauses
*every* stream on it. HTTP/3 repeats the idea over QUIC with QPACK, so
a loss stalls only the stream that carried it, at the price of UDP.

Timeouts are the application's job: dial, TLS handshake, response
header and idle are four different failures hiding behind a single
total budget, and a shared `Transport` is what stops a process from
building a fresh pool per request.

**Where it breaks.** An idle keep-alive connection dies the moment a
load balancer's idle timer fires, so the next request pays a reset and a
retry; a body left unread pins the connection and quietly empties the
pool. A proxy that buffers chunked responses turns streaming into a
timeout. Load shedding shows up as 503 and 429, upstream trouble as 502
and 504, and an HTTP/1.1 client with six connections pinned behind one
slow response waits for all six.

**In Go.** `http.Client.Timeout` defaults to zero — no timeout at
all — while the `Transport` supplies the phases: `DialContext` (30 s),
`TLSHandshakeTimeout` (10 s), `ResponseHeaderTimeout` (unset) and
`IdleConnTimeout` (90 s). Raise `MaxIdleConnsPerHost` — the default is
**2** — or a burst to one host churns connections, and every body must
be closed and drained or it never returns to the pool.

---

## 12. NAT and why IPv6 exists

**Why.** IPv4 addresses ran out: IANA's free pool emptied in 2011 and
the regional registries followed, while a household of eight devices
almost certainly needs one routable address. The need is many-to-one
multiplication — a whole private network presenting itself to the
internet as a single public address.

**How.** RFC 1918 carves out private space (10/8, 172.16/12,
192.168/16) that must never be routed publicly, and a NAT box performs
port address translation: it keeps a table mapping (private address,
private port) to (public address, public port), rewrites the source of
every outbound packet, and uses the mapping to steer replies back to the
right host. Colliding source ports get rewritten, so an external port
number says nothing about which internal machine is talking.

The state now lives in the middle, and every packet of a flow must pass
through the same box. Protocols that carry addresses inside their
payload break — FTP's PORT command, SIP's Contact header — and are
patched by application-layer gateways that rewrite the payload too.
Inbound traffic has no mapping at all, so nothing can be reached from
the outside without an explicit port forward: end-to-end addressing is
gone, replaced by a broker that owns the state.

Carriers ran out too, so 100.64.0.0/10 (RFC 6598) adds a second layer
of translation — CGNAT, where two boxes must both hold the flow and both
agree on its timeout. NAT is not security: it hides addresses by
accident and refuses inbound traffic only because no mapping exists,
while a firewall does both deliberately and auditably. IPv6 restores the
original design — every host globally routable, no translation, state
back in the firewall where it belongs.

| Transition | Mechanism | What it costs |
|---|---|---|
| Dual stack | both protocols run at once | two stacks to operate |
| NAT64 + DNS64 | synthesised AAAA for IPv4-only targets | breaks DNSSEC validation |
| Happy Eyeballs | race A and AAAA, take what connects | one wasted attempt |
| CGNAT | second NAT inside the carrier | two states, no inbound at all |

**Where it breaks.** Mapping timers kill idle flows: a NAT that expires
UDP after 30 seconds will drop a silent VPN tunnel or a game session
that sends nothing for half a minute. Two hosts behind one address
cannot both be reached inbound, so peer-to-peer falls back to relays.
Two merged branches both using 10/8 cannot talk without renumbering.
CGNAT makes all of the above worse while presenting your application
with a "public" address that is not yours and cannot be reached.

**In Go.** The stdlib has no NAT awareness: `net.Dialer` binds, the
kernel forwards, and the translation is invisible except that
`conn.LocalAddr()` reports the private address, which is useless for
identifying a machine behind CGNAT. STUN, ICE and TURN are outside the
stdlib. The practical consequence is that idle-timeout tuning matters
more than address bookkeeping — assume the mapping dies first.

---

## 13. DHCP

**Why.** Every host needs an address, a mask, a gateway and a resolver
before it can speak to anything, and typing them into five hundred
machines does not scale. The need is zero-touch configuration with a
lease, so addresses can be reclaimed when machines leave.

**How.** DHCP runs four messages over UDP ports 67 (server) and 68
(client), starting from a client that has no address of its own: it
broadcasts DISCOVER, a server answers OFFER, the client broadcasts
REQUEST to accept one offer among possibly several, and the server
confirms with ACK. Both broadcast legs exist because neither side has a
usable address yet, and a relay agent on the router is what forwards
those broadcasts to a server on another subnet, so one server can serve
a whole estate.

| Step | Message | Direction |
|---|---|---|
| 1 | DISCOVER | client broadcast, 0.0.0.0 → 255.255.255.255 |
| 2 | OFFER | server → client, with a proposed address |
| 3 | REQUEST | client broadcast, naming the offer it took |
| 4 | ACK | server → client, the lease is now valid |

The lease carries a duration, and the client renews at T1 — half way —
by unicasting to the original server, then falls back to broadcasting a
rebind at T2 — seven eighths — and must abandon the address entirely at
expiry. What travels in the ACK is options: 1 is the subnet mask, 3 the
router, 6 the DNS servers, 15 the domain name, 51 the lease time, 12
the hostname. Before using an address a client probes it with ARP, so a
statically assigned duplicate is detected rather than discovered later.

DHCPv6 is a separate protocol on UDP 546 and 547 that uses multicast
instead of broadcast, while SLAAC lets a host configure itself from
router advertisements alone — prefix in, address out — leaving DNS to
RDNSS or DHCPv6. Neither helps if something answers that should not.

**Where it breaks.** A rogue DHCP server on the segment answers first
and hands out a gateway it controls, and every packet from that host is
then silently forwarded through the attacker — which is why untrusted
ports get port-based segregation. An exhausted scope leaves new machines
on 169.254 link-local addresses that route nowhere. A stale lease handed
to a second host produces a duplicate-address conflict, and a relay
agent missing its helper address means PXE boot and first boot simply
get nothing.

**In Go.** The stdlib has no DHCP client: leases are applied by the
kernel, `dhclient` or `systemd-networkd` before `main` runs, so your
only job is to observe the result. `net.Interfaces` and
`net.InterfaceAddrs` show the leased address, `os.Hostname` may come
from option 12, and `net.Resolver` reads `/etc/resolv.conf` — filled
from option 6 — whenever `PreferGo` is false.

---

## 14. Anycast, CDNs and load balancing

**Why.** One address must serve users on five continents within a few
tens of milliseconds, and no single machine can. The need is to put
copies everywhere and steer requests to the right one without ever
changing the address the client dials.

**How.** Anycast announces the same prefix from many locations at once,
and BGP's best path decides which one a given network reaches — hot
potato routing means each AS hands the traffic to whichever exit is
closest to it, so the choice follows topology rather than a lookup
table. The DNS root servers are the textbook case: thirteen lettered
identities, A through M, each served by hundreds of machines across many
countries, so the count of letters says nothing at all about the count
of machines.

```
client:    lookup edge.example.com → geo answer with TTL 30 s → nearest PoP
anycast:   192.0.2.10 announced by AS 64500 and AS 64501 → each AS exits locally
cache:     edge hit → serve → miss → origin → store under the cache key
balancer:  L7 → terminate TLS → route on path and host → backends, health-checked
```

DNS steering is the other lever: an authoritative server answers the
same name with different records per client resolver region, with TTLs
of a few seconds so a bad answer expires quickly. The trade-off is that
a low TTL multiplies query load and a resolver that ignores TTL pins
whatever it got first. Inside a CDN the edge is a cache in front of an
origin, keyed typically by host, path, query and a vary header; misses
fetch and store, expiry and explicit purge both propagate best effort,
and a load balancer then picks a backend — L4 forwarding whole TCP
connections and preserving the client address, L7 terminating TLS and
routing on the request itself, which costs a proxy protocol header or
`X-Forwarded-For` to keep the original client visible.

**Where it breaks.** A hot object expiring everywhere at once sends
every edge node to the origin simultaneously — the cache stampede —
unless requests are coalesced or stale content is served while
revalidating. Health checks that flap oscillate the backend set every
few seconds and cause more damage than the original fault. Withdrawing
an anycast announcement leaves a reconvergence window of seconds to
minutes during which traffic black-holes or takes a pathological path.
And DNS steering is slow to undo precisely because the answers are
cached where you cannot reach them.

**In Go.** Nothing in the stdlib knows an address is anycast or which
PoP answered; `net.Dialer`'s dual-stack race and `FallbackDelay` decide
which candidate connects first, and an `http.Client` timeout is what
turns a withdrawn PoP into a bounded error instead of a hang. Size the
`Transport` pool deliberately — `MaxIdleConns` and `MaxIdleConnsPerHost`
(2 by default) — or a burst spread across many edge hostnames will churn
connections exactly when the path is least stable.

---

## 15. The wire-to-socket packet path

**Why.** “The packet reached the machine” does not mean the application
can read it. The NIC, interrupt steering, kernel packet stack, firewall,
socket queues and process scheduler each have a queue and a drop point.
Locating a loss or latency spike requires knowing their order.

**How.** A simplified receive path on Linux is:

```text
wire → NIC RX ring (DMA) → MSI-X interrupt → NAPI poll
     → XDP (if attached, before skb) → skb/GRO → tc/netfilter/IP
     → TCP/UDP demultiplexing → socket receive queue
     → wakeup/readiness → epoll/netpoll → application read
```

The NIC DMA-writes packet data into buffers described by an RX ring.
Receive Side Scaling (RSS) hashes a flow tuple and selects a hardware
queue; interrupt affinity determines which CPU services that queue. The
interrupt schedules NAPI polling, which drains completions in a bounded
batch rather than raising one interrupt per packet. XDP can inspect,
drop, redirect or pass a packet before the normal `sk_buff` path. GRO
coalesces compatible receives; IP and transport layers validate headers,
reassemble where required and find the target socket. TCP puts ordered
bytes on the socket receive queue; `epoll` readiness wakes the waiting
application when it can make progress.

The transmit path runs in the other direction:

```text
application write → socket send queue → TCP/UDP output → route/netfilter
                   → qdisc → driver TX ring → DMA/NIC → wire
```

TCP may combine data into larger GSO/TSO units; the kernel or NIC segments
them near the wire. Checksum offload can also mean a host-side packet
capture sees an apparently invalid checksum before the NIC fills it in.
GRO/GSO change what a capture point calls a “packet,” not the bytes the
peer receives. Queueing disciplines schedule and shape egress traffic;
the driver queue and NIC ring add further buffering. Hardware TX
completion frees descriptors and socket memory, but it does not mean the
remote application has consumed the bytes.

Parallelism is layered too. RSS selects a hardware queue/CPU early; RPS
can steer protocol processing to another CPU later (at the cost of an
inter-processor interrupt); RFS tries to align processing with the CPU
running the consuming application; XPS selects transmit queues. More
queues are not automatically faster: a single hot flow may remain bound
to one queue to preserve ordering, while poor interrupt affinity or
cross-NUMA memory access can waste CPU cycles.

**Where it breaks.** RX-ring exhaustion is a driver/NIC drop; softnet
backlog overflow is later and appears in `/proc/net/softnet_stat`; a
full socket receive queue becomes TCP window pressure or UDP loss. A
high interrupt rate with low application CPU may point to poor batching;
one saturated softirq CPU while other cores idle may point to RSS
imbalance or a single hot flow. A deep qdisc delays packets after the
application has already returned from `write`. Host `tcpdump` can show
large GSO packets or pre-offload checksums that never appeared that way
on the wire.

**In Go.** `net.Conn` and `net.PacketConn` expose ordered bytes or
datagrams, not NIC queues. A successful `Write` means bytes were accepted
by the local socket path, not acknowledged or consumed by the peer.
Deadlines bound application waits; they do not tune NAPI, the qdisc or
the NIC ring.

Inspect each queue boundary rather than raising all buffers at once:

```bash
ip -s link show dev eth0
ethtool -l eth0                 # channel/queue counts
ethtool -x eth0                 # RSS hash and indirection table
ethtool -k eth0                 # checksum/GRO/GSO/TSO offloads
ethtool -S eth0                 # driver-specific ring/drop counters
cat /proc/interrupts
cat /proc/net/softnet_stat
tc -s qdisc show dev eth0
ss -tin                         # TCP state, RTT, cwnd, queues
```

Use `tcpdump` at both endpoints or a switch mirror when wire-level
segmentation/checksum behavior matters. For kernel internals and
interrupt contexts, see [`linux`](../linux/README.md), entries 14, 18 and
19; for proxy/API boundaries see [web services](../webservices/README.md).

---

## 16. TCP loss recovery, timers and congestion control

**Why.** TCP can be reliable and still be slow. A missing segment,
reordered ACK, full receive buffer, overly small congestion window or
large queue each produces a different throughput/latency signature. The
transport uses several interacting feedback loops, not one “retry on
loss” switch.

**How.** The sender tracks bytes in flight, a receiver-advertised window
(`rwnd`) and a congestion window (`cwnd`). New data is limited by the
smaller window; the path bandwidth-delay product (BDP) estimates the
bytes needed in flight to fill a path:

```text
bytes_in_flight ≤ min(cwnd, rwnd)
BDP = bottleneck_bandwidth × RTT
10 Gbit/s × 50 ms ÷ 8 = 62.5 MB in flight to fill the path
```

ACKs clock data transmission and update an RTT estimator. A simplified
smoothed estimator is:

```text
SRTT   ← (1 − α) × SRTT + α × RTT_sample
RTTVAR ← (1 − β) × RTTVAR + β × |SRTT_old − RTT_sample|
RTO     = SRTT + max(clock_granularity, 4 × RTTVAR)
```

The retransmission timeout (RTO) is a safety net: after a timeout the
sender backs off exponentially. Fast loss recovery uses duplicate ACKs,
selective acknowledgments (SACK), and implementation-specific recovery
algorithms to retransmit gaps without waiting for the RTO. Modern Linux
also has RACK/TLP-based loss detection paths; the classic “three duplicate
ACKs means retransmit” is a useful model, not the complete current
implementation.

Congestion control estimates the path's available capacity. CUBIC grows
the congestion window as a function of time since a congestion event;
BBR estimates bottleneck bandwidth and minimum RTT, then paces packets
around that model. Which algorithm is available/default depends on kernel
configuration and system policy. Pacing and queue management matter
because a large qdisc buffer can hide congestion as latency (bufferbloat)
instead of dropping early enough for the sender to react.

**Where it breaks.** A receive window near zero points to the peer's
application or socket buffer, not a congested router. A small `cwnd` plus
retransmissions suggests path loss or reordering. A full BDP-sized queue
can yield good throughput and terrible RTT. A high RTT with no loss can
be propagation delay or queueing; one ping sample cannot distinguish
them. A packet capture on a host with TSO/GRO may not show wire-sized
segments, and ACK compression can distort sender pacing.

**In Go.** Inspect a live Linux TCP socket with `ss -tin`; it reports
fields such as RTT, congestion-control name, cwnd, retransmissions and
bytes in flight (fields vary by kernel/iproute2). `nstat -az` provides
host counters; `tcpdump -tt -nn` shows sequence/ACK progression. Keep
application deadlines separate from TCP's retransmission timers, and do
not turn off congestion control to “fix” a slow path. The kernel packet
queues and offloads are described in
[`linux`](../linux/README.md#14-kernel-networking-internals-skb-softirq-and-socket-queues).

---

## 17. Network security: filtering, tunnels, DNS and route origins

**Why.** An IP address is a routing label, not an identity, and the
network control plane accepts updates from many independent operators.
Security therefore has to protect several different things: who may
reach a service, which path traffic takes, whether name data is authentic,
and whether application bytes remain confidential end to end.

**How.** Separate controls by layer and property:

| Mechanism | Layer / property | What it protects | Important limit |
|---|---|---|---|
| Stateful firewall (`nftables`) | Host/network admission | Which flows may enter, leave or forward | Conntrack state consumes memory; rules do not authenticate users |
| Network segmentation | Routing and policy | Limits lateral reach between workloads | A compromised allowed service can still be abused |
| IPsec / WireGuard VPN | Network-layer tunnel | Encrypts/authenticates packets between tunnel endpoints | Traffic is plaintext after tunnel termination unless separately protected |
| TLS | Application transport | Authenticates peer and encrypts application connection | Protects only the TLS hop; proxies may terminate it |
| DNSSEC | DNS data integrity/authenticity | Validates signed DNS records and delegation chain | Does not encrypt query names or hide the resolver |
| DoT / DoH | DNS transport privacy | Encrypts a client connection to a chosen resolver | Does not itself prove the returned DNS data is DNSSEC-valid |
| RPKI/ROV | BGP origin validation | Rejects routes whose origin AS violates a ROA | Does not authenticate the entire AS path or stop all leaks |

Firewalls can be stateless packet filters or stateful flow filters.
`nftables` evaluates chains at hooks such as input, forward and output;
connection tracking remembers enough flow state for rules such as
“allow replies to outbound connections.” NAT is address translation
implemented alongside this state, not an access-control policy by
itself. Conntrack table exhaustion can drop new flows even when firewall
allow rules are correct.

VPNs create an encrypted tunnel between endpoints, often using a virtual
interface and routing table. A route determines whether a packet enters
the tunnel; policy routing can choose paths by source, mark or table.
TLS is still needed for service identity and application security when
the tunnel endpoint is not the application endpoint. For BGP, RPKI
Route Origin Validation checks whether an AS is authorized to originate
a prefix; it reduces route hijacks but is not a complete path-validation
system. DNSSEC validates the authenticity of DNS data, while DoH/DoT
protect the query transport to a selected resolver—these solve distinct
problems.

**Where it breaks.** A firewall that permits all traffic from a trusted
subnet treats a compromised host as every user. Broad NAT rules can
accidentally expose a service; NAT alone does not make it safe. DNSSEC
validation failures caused by broken clocks or stale signatures can make
valid names disappear, and encrypted DNS to an untrusted resolver merely
moves who can observe the queries. VPN split-tunnel routes can send
sensitive traffic outside the intended tunnel. RPKI-invalid routes may
be dropped by some networks and accepted by others, so a route hijack
can be partially reachable rather than cleanly rejected.

**In Go.** Do not treat a remote address as user identity; authenticate
the application principal and authorize each request. A listening
`net.Listener` still needs host firewall policy and service-level
timeouts. When debugging an inaccessible service, inspect routes and
filters before changing application code:

```bash
ip route get 203.0.113.10
sudo nft list ruleset
sudo conntrack -S                 # if conntrack-tools is installed
wg show                           # WireGuard peer/handshake state
dig +dnssec example.com
```

**Further reading.** The service-level threat model is in
[`webservices`](../webservices/README.md); packet filtering and conntrack
in the kernel are in [Linux entry 14](../linux/README.md).
