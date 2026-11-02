# Networking designs (end-to-end flows)

Five composed walkthroughs tracing real requests through the mechanisms
documented in the sibling [`README.md`](README.md) — one URL, one DNS
query, one BGP announcement, one TCP connection, one TLS handshake. Each
names the stage that fails first when things go wrong, and each failure
story walks the same path with that stage's characteristic error.

---

## 1. Typing a URL and pressing Enter

**Problem.** Trace one request from the address bar to painted pixels:
the caches in front of it, the name and route lookups behind it, and the
three handshakes — TCP, TLS, HTTP — that must all succeed before a
single response header is readable.

**Path.**

```
caches: HSTS pin → DNS cache → warm TCP+TLS session → HTTP cache
resolve: stub → recursive (UDP 53) → root → TLD → auth (cold only)
route: FIB longest-prefix match → next hop → ARP for the gateway MAC
connect: SYN → SYN-ACK → ACK → ClientHello → flight → GET / → 200
edge: anycast PoP → cache key → hit → else origin fetch → store
```

**Walkthrough.**

1. Four caches are consulted before anything leaves the machine: HSTS
   rewrites http:// to https:// in the address bar, the host cache may
   still hold the DNS answer inside its TTL, a pooled socket may skip
   the next four steps, and the HTTP cache can finish with a 304. A hit
   at the last one ends the flow before routing is even consulted.
2. On a miss the stub asks the OS, which forwards to the configured
   recursive resolver over UDP 53 with a randomised TXID and source
   port. A warm recursive answers from cache; a cold one walks root →
   TLD → authoritative, hop by hop in entry 2 below. The answer carries
   a TTL, so the next visit this minute never touches the wire.
3. With an address in hand the kernel does a route lookup: the FIB is
   searched for the longest matching prefix, the winner names a next
   hop, and ARP resolves that next hop to a MAC — the gateway's, never
   the server's, because frames only travel on-link. That is the
   [IP addressing and CIDR](README.md) entry doing its one job.
4. TCP opens: a SYN carries MSS, window scale, SACK and timestamp
   options; the SYN-ACK returns the server's initial sequence number;
   the ACK closes the loop in one RTT. Both sides size the initial
   congestion window — about ten segments — and the first RTT sample
   starts the retransmission clock at one second.
5. TLS 1.3 then spends one more RTT: ClientHello with SNI, versions,
   cipher suites and an X25519 key share; ServerHello selecting the
   suite and returning its share; EncryptedExtensions, Certificate,
   CertificateVerify, Finished. The client checks the chain and SAN,
   answers with its own Finished, and both sides hold traffic keys.
6. The request goes out as HTTP/1.1 GET with a Host header on that
   connection — or, on keep-alive reuse, it skips the previous two steps
   entirely and rides a socket that already has keys. HTTP/2 would put
   it on one of many streams over the same connection, which is why one
   lost segment then stalls every request on it.
7. The address terminates at a CDN edge, reached by anycast or by DNS
   steering to the nearest PoP ([anycast, CDNs and load
   balancing](README.md)). The edge hashes a cache key of host, path,
   query and vary headers; a hit is served from local storage without
   the origin ever hearing about the request.
8. A miss opens a second, independent connection from edge to origin,
   forwards X-Forwarded-For, and waits — and this hop has its own dial,
   TLS and first-byte budgets. It is the stage that usually fails first,
   because the origin answers the edge no faster than anyone else.
9. The response returns as headers plus Content-Length or chunked
   chunks; the browser parses incrementally, kicks off subresource
   lookups, and paints. Time to first byte is everything above summed
   up; time to last byte additionally contains the origin's thinking
   time, which no amount of network tuning removes.

**Sketch.**

```go
dialer := &net.Dialer{
    Timeout:   5 * time.Second,  // connect budget: SYN, SYN-ACK, ACK
    KeepAlive: 30 * time.Second, // probe a pooled conn after it goes idle
}
tr := &http.Transport{
    DialContext:           dialer.DialContext,
    TLSHandshakeTimeout:   10 * time.Second, // ClientHello → Finished
    ResponseHeaderTimeout: 15 * time.Second, // GET sent → first header byte
    IdleConnTimeout:       90 * time.Second, // leave the pool before the LB
    MaxIdleConnsPerHost:   8,                // default is 2: bursts churn
}
client := &http.Client{
    Transport: tr,
    Timeout:   30 * time.Second, // the whole call: dial, TLS, headers, body
}
```

**Failure story.** The name does not exist: the recursive returns
NXDOMAIN and the browser stops — a definitive answer, not an outage,
and the distinction is what you page a human on. The recursive itself
never answers: the stub retries, then times out, and every host is
healthy while the report says "everything is down". DNS resolves but
the SYN gets no SYN-ACK: the port is filtered or the host is gone, and
from here they are identical — only a packet capture separates them.
TLS then refuses on a name mismatch: valid certificate, wrong SAN, so
the browser says exactly that while a script sees a generic error. The
edge answers 502: its origin connection's dial, TLS or first byte
overran — a gateway fault, distinct from 504 where the origin stayed
silent. Everything works but the first byte is slow: RTT and TLS are
fine, and the wait is origin thinking time, seen as a gap before TTFB.

**Watch-outs.**

- `http.Client.Timeout` is one blunt budget over four phases; the
  Transport's dial, TLS, header and idle timeouts name the phase.
- Caching trades freshness for load: a low TTL limits how long a bad
  answer survives but multiplies queries; `no-store` is the escape.
- `MaxIdleConnsPerHost` defaults to 2, so a burst to one host rebuilds
  connections exactly when the path is least stable.
- You cannot see routing from an application: a withdrawn prefix, a
  congested transit link and a dead origin all look like a dial timeout.

---

## 2. One DNS lookup, hop by hop

**Problem.** Follow a single cold lookup from the stub to the
authoritative and back: who asks whom, what each hop caches, how an
oversize answer changes transport, and why a negative answer is as
cacheable as a positive one.

**Path.**

```
stub: UDP 53 query, randomised TXID → OS resolver → app result
recursive cold: cache miss → root referral → TLD referral → auth NS
recursive warm: fresh RRset in cache → answer → TTL ticks down locally
auth: SOA + NS + glue → A 203.0.113.10 → TTL set by the zone file
oversize: EDNS0 UDP payload 1232 → TC bit set → retry over TCP 53
negative: NXDOMAIN → SOA minimum → cached, no new query until expiry
```

**Walkthrough.**

1. The application never speaks DNS itself: a call to `net.Resolver`
   hands the name to the stub, which asks the OS, which forwards to the
   recursive resolver in `/etc/resolv.conf` — UDP 53 by default, with a
   random TXID and a randomised source port, because a forgery must now
   guess both (RFC 5452).
2. The recursive checks its cache first. A fresh RRset for the name and
   type answers immediately with the TTL decremented locally — that is
   the answer for almost every query a busy resolver ever sees.
3. Cold, it starts at the root: a referral listing the TLD's NS records
   plus glue A records so the next hop does not have to resolve itself.
   The referral is cached separately from any answer, at its own TTL.
4. The TLD server replies the same way for the domain: NS records for
   example.com plus glue, which matters when a nameserver name resolves
   inside the zone it serves — in-bailiwick — because without glue the
   resolver would need the very answer it is chasing.
5. The authoritative server answers from the zone file: the A record
   with the operator's TTL, or an NXDOMAIN whose authority section
   carries the SOA. It never recurses; if it does not serve the name it
   says REFUSED rather than guessing.
6. The recursive caches answer, delegation and negative result alike,
   then replies to the stub on the same UDP socket — the source port and
   TXID must match what was sent, or the answer is dropped as an
   off-path forgery.
7. EDNS0 (RFC 6891) puts an OPT record in the query advertising a UDP
   payload size — 1232 bytes is the conservative modern choice. If the
   answer still will not fit, the server sets the TC bit and the stub
   retries over TCP port 53, where zone transfers always run.
8. Negative answers are cached too: RFC 2308 gives an NXDOMAIN the
   SOA's minimum field as its TTL, so a typo costs one query — and so
   does a record you just created, until that TTL expires.
9. Two defences run underneath. TXID plus source-port randomisation
   forces an off-path forger to guess both per attempt, and DNSSEC's
   RRSIG chain removes trust from the transport entirely — though the
   Go stdlib performs no validation of its own.

**Sketch.**

```go
r := &net.Resolver{
    PreferGo:     true, // pure-Go path: identical behaviour on every host
    StrictErrors: true, // partial failure is an error, not a stale answer
    Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
        d := net.Dialer{
            Timeout:   2 * time.Second, // per query, not per lookup
            KeepAlive: -1,              // UDP socket lives for one query
        }
        // network is "udp" first; Go retries the same call over "tcp"
        // itself when the reply came back with the TC bit set.
        return d.DialContext(ctx, network, addr)
    },
}
addrs, err := r.LookupIPAddr(ctx, "www.example.com")
```

**Failure story.** The authoritative server is down while its
delegation is still published: the recursive retransmits, gets silence,
and returns SERVFAIL — the resolver's view of the zone is broken, and
every lookup for it fails together. Worse, the delegation points at a
server that no longer serves the zone — a lame delegation — so answers
come back REFUSED or NXDOMAIN from the wrong place and the zone looks
absent rather than broken. A name that truly does not exist is
NXDOMAIN: definitive, and cached negatively for the SOA minimum. A
recursive that can reach nothing is a timeout, which asserts nothing at
all — and SERVFAIL also covers DNSSEC validation failing on a bad
signature, so one code hides three stories. An off-path forgery race is
lost silently (TXID or port mismatch, answer discarded), while a won one
poisons the cache for the full TTL: every caller inherits the lie until
it expires or you flush the resolver.

**Watch-outs.**

- TTL is the only freshness control you have: short means immediate
  fixes and more queries, long means stale answers you cannot reach.
- Negative caching cuts both ways — a newly published record is
  invisible until the previous NXDOMAIN expires.
- `PreferGo` and `StrictErrors` make resolver behaviour identical across
  machines; neither changes what the network returns.
- The stdlib does no DNSSEC validation and no DoH; encrypting the lookup
  means a custom `Dial` to a DoT server on 853, or a third-party resolver.
- Count SERVFAIL rate, TC rate (oversize answers forcing TCP) and p95
  lookup latency — the numbers a bare timeout hides.

---

## 3. Two ASes and one prefix: BGP in motion

**Problem.** An operator at AS64500 wants 203.0.113.0/24 reachable from
the internet: get the prefix into other networks' forwarding tables,
keep it there while links flap and neighbours misbehave, and have the
application notice nothing at all.

**Path.**

```
session: TCP 179 → OPEN → OPEN → KEEPALIVE → hold timer 180 s armed
announce: UPDATE NLRI 203.0.113.0/24 → AS_PATH [64500] → LOCAL_PREF 200
best path: LOCAL_PREF → AS_PATH len → MED → eBGP over iBGP → IGP cost
propagate: AS64501 prepends → [64501 64500] → iBGP and route reflectors
converge: peer BGP → IGP → FIB install → minutes across the internet
failure: link down → hold expiry → WITHDRAW all → reconverge on backup
```

**Walkthrough.**

1. AS64500 and AS64501 open TCP port 179 — either side may dial — and
   exchange OPEN messages carrying ASNs, a proposed hold timer (usually
   90 or 180 s), a router ID and capability TLVs such as 4-byte ASNs and
   MP-BGP. The negotiated hold governs KEEPALIVEs, sent at a third of
   it; silence for the full hold drops the session.
2. After establishment each side sends its table — about a million
   prefixes today — as UPDATE messages plus an END-OF-RIB marker meaning
   "that was everything". The announcement for 203.0.113.0/24 carries
   the NLRI plus path attributes: AS_PATH, LOCAL_PREF, MED, origin code
   and next hop.
3. The AS_PATH is the loop check as much as the metric: a router that
   receives an UPDATE already containing its own ASN rejects it outright.
   Every export prepends, so the path grows as it travels and a second
   copy of your own announcement can never come back to you.
4. Best-path selection runs a fixed sequence: highest LOCAL_PREF first
   (your own policy), then shortest AS_PATH, then origin type, then
   MED, then eBGP over iBGP, then IGP cost to the exit. RFC 4271 leaves
   the tail implementation-defined, so two vendors may legitimately
   choose different exits for one prefix.
5. The winner is installed in the FIB beside every other route, and
   forwarding does longest-prefix match on it — one stray longer prefix
   beats all of that policy work. Policy chooses the route; the mask
   wins the packet ([IP addressing and CIDR](README.md)).
6. Export follows contract, not topology: AS64501 prepends its ASN —
   [64501 64500] — and re-advertises to its own neighbours. Transit
   customers' prefixes go everywhere, peer prefixes only to each side's
   own customers: the valley-free pattern from
   [routing and BGP](README.md).
7. Propagation takes minutes, not milliseconds: each hop is one TCP
   exchange, but MRAI-style pacing, path exploration after a change and
   queueing at every speaker mean a withdrawal and its replacement can
   take minutes to cross the internet.
8. Inside each AS, iBGP — usually through route reflectors rather than a
   full mesh — carries the chosen route to every router so the whole AS
   agrees on the exit. The control plane rides the data plane it
   configures, so a forwarding fault can take down the protocol that
   would have repaired it.
9. On link failure the session dies and every prefix learned over it is
   withdrawn in one instant, so a healthy site goes dark with every
   local link still up. The slow case waits out the 180 s hold timer;
   BFD shaves detection to tens of milliseconds, and the backup path
   then installs in the usual best-path order.
10. RPKI closes the origin question: the prefix holder publishes a ROA
    naming the ASN that may originate 203.0.113.0/24 and up to what
    length; routers fetch the validated set over RTR and mark each
    announcement valid, invalid or not-found. Only the first two are
    actionable, and only where the operator chooses to act on them.

**Failure story.** A third AS originates 203.0.113.0/24 with an
AS_PATH of one — its own ASN — while the real announcement reads
[64501 64500]. Equal LOCAL_PREF sends the tie to AS_PATH length, so the
shorter lie wins and traffic follows policy into the attacker's hands.
Nothing above routing changes: DNS still resolves, and `net.Dial`
returns `i/o timeout` or lands on the wrong server — from an
application a hijack is indistinguishable from any other black hole. A
route leak is the sibling failure: a network re-exports what it learned
from a provider as though it were a customer's, violating valley-free,
and funnels traffic through a link with a fraction of the capacity.
RPKI makes the first case invalid and, where networks filter,
unpropagated — but many prefixes have no ROA and policies differ, so
RPKI narrows the window rather than closing it. Session failure
looks different again: hold expiry withdraws every prefix at once.

**Watch-outs.**

- You do not choose your AS path. From inside a process you see RTT,
  packet loss and reachability — multi-homing is procurement, not code.
- Convergence takes seconds to minutes, longer than most application
  timeouts: retry with backoff instead of hammering a withdrawn prefix.
- Anycast means the path under a live connection can change without the
  connection noticing, which shows up as a step change in RTT.
- The observability lives outside the process — route collectors, a
  looking glass, ROA coverage for your prefix; none is reachable from `net`.
- No Go sketch here on purpose: no BGP speaker is in the stdlib, and
  FRR, bird and gobgp are operators' tools, not app dependencies.

---

## 4. A TCP connection's life

**Problem.** Open a byte stream over a network that drops, duplicates
and reorders packets; keep it fair to competing flows; recover from loss
without waiting for a timer; and shut it down without leaving the port
unusable for the next minute.

**Path.**

```
establish: SYN seq x → SYN-ACK seq y ack x+1 → ACK ack y+1 (one RTT)
backlog: accept queue full → SYN cookies → state encoded in the seq no
flow: send min(cwnd, rwnd) → cumulative ACK → SACK blocks for the gaps
loss: dupACK ×3 → fast retransmit → fast recovery → cwnd halved
stall: RTO 1 s → 2 s → 4 s → ... (RFC 6298), Karn forbids sampling
shutdown: FIN → FIN-ACK → ACK → TIME_WAIT 2 MSL ≈ 60 s
```

**Walkthrough.**

1. The client picks a pseudorandom ISN and sends a SYN carrying MSS,
   window scale, SACK and timestamp options; the SYN-ACK returns the
   server's ISN, and the ACK finishes the loop in one RTT. The SYN sits
   in the accept backlog first — and when that queue fills, SYN cookies
   encode the state in a sequence number so no memory is held.
2. Established, sequence numbers count *bytes* and an ACK names the next
   byte wanted, making every acknowledgement simultaneously a receipt
   and a retransmission instruction. How much may be sent is
   min(congestion window, advertised window): one is the sender's guess
   at spare capacity in the path, the other is the receiver's real
   buffer space.
3. The receiver delays its ACK — up to about 40 ms, or until a second
   full segment arrives — while Nagle coalesces small writes; together
   they can stall a request for tens of milliseconds per round. Go sets
   TCP_NODELAY for you, which is why request headers leave in one write.
4. Lose one segment and the receiver keeps acknowledging the last
   in-order byte, so duplicate ACKs accumulate; three of them trigger a
   fast retransmit without waiting for any timer, and fast recovery
   halves the congestion window rather than resetting it. SACK blocks
   name the ranges that arrived, so only the hole is retransmitted.
5. A total stall falls back to the retransmission timer: RFC 6298 starts
   RTO at one second and doubles on every attempt — 1, 2, 4, 8 — while
   Karn's rule forbids feeding a retransmitted segment's round trip into
   the estimator, because you cannot tell which copy was acknowledged.
6. Congestion control doubles the window per RTT in slow start until
   the threshold, then adds about one segment per RTT in congestion
   avoidance; loss sets the new threshold. CUBIC grows as a cubic
   function of time since the last loss, BBR models bandwidth and
   minimum RTT and mostly ignores loss — both live in the kernel.
7. Shutdown is half-close: close() sends FIN, the peer ACKs it and
   usually sends its own FIN, and the socket that closed first lingers
   in TIME_WAIT for 2 MSL — about 60 s — so a late duplicate cannot be
   mistaken for a new session and the peer's final ACK can be
   retransmitted.
8. That wait costs addresses: an ephemeral range of roughly 28,000
   ports, divided by 60 s in TIME_WAIT, caps one (host, port) pair at a
   few hundred new connections per second — exactly why a client that
   dials without pooling eventually reports "cannot assign requested
   address".
9. A peer that crashes sends nothing more: writes may succeed into
   kernel buffers for a while, reads hang, and only keep-alive probes or
   an application deadline end it. If the peer's host rebooted, the
   first segment earns an RST instead — an immediate, unambiguous
   answer.
10. In Go every one of those phases needs a deadline: `SetDeadline`
    covers reads and writes together, `SetReadDeadline` and
    `SetWriteDeadline` split them, and `net.Dialer.Timeout` covers the
    connect. A context is not translated automatically — code that
    never sets a deadline has no cancellation at all.

**Sketch.**

```go
d := net.Dialer{
    Timeout:   10 * time.Second, // connect phase only: the SYN must not hang
    KeepAlive: 30 * time.Second, // idle before the first probe, not a TTL
}
conn, err := d.DialContext(ctx, "tcp", "203.0.113.10:443")
if err != nil {
    return err // refused: host up, no listener. timeout: filtered or gone.
}
defer conn.Close()

// One deadline covers every later blocking call — read, write and the
// drain before close. Nothing else cancels a goroutine parked on a
// socket, and a context alone never reaches the kernel.
deadline := time.Now().Add(15 * time.Second)
conn.SetDeadline(deadline)
```

**Failure story.** SYN out and nothing back: the port is filtered
somewhere on the path or the host is gone, and both are the same
`i/o timeout` when the dial budget expires — only a packet capture tells
them apart. A refusal is different and useful: an RST means a host
answered, so the service simply is not listening. Mid-transfer, one lost
segment produces nothing visible for a full second, then a retransmit;
persistent loss doubles the timer — 1, 2, 4, 8 — and the flow feels
frozen while the socket reports no error at all. A peer that crashes
without FIN leaves a half-open connection: writes are acknowledged into
the void until the buffers fill, reads return nothing, and only
keep-alive probes or your deadline notice. A client that opens and
closes thousands of connections a minute exhausts its ephemeral ports
into TIME_WAIT and starts failing connects with "cannot assign
requested address" on a machine where every service is healthy.

**Watch-outs.**

- Deadline discipline: one deadline per operation covering dial, read
  and write; a context alone never reaches the socket.
- Pool instead of churning — every close is 60 s of TIME_WAIT, and
  `SO_REUSEADDR` does not shorten it.
- Goroutine-per-connection works because the runtime parks it on epoll
  underneath ([linux](../linux/README.md)); no deadline parks forever.
- Watch TIME_WAIT count, listen-queue drops, retransmissions and
  zero-window events — each maps to one stage above.
- Congestion control, RTO and windows are kernel policy: user space
  picks the algorithm (`TCP_CONGESTION`) and the MTU, almost no more.

---

## 5. A TLS 1.3 handshake

**Problem.** Prove to a stranger on a hostile path that you are the
named host, agree keys nobody on the path can derive, and do it in one
round trip — then decide later whether the certificate chain behind that
claim was actually worth trusting.

**Path.**

```
first flight: ClientHello → versions, suites, X25519 key_share, SNI, PSK
server flight: ServerHello (suite, key_share) → EncryptedExtensions
→ Certificate → CertificateVerify → Finished, encrypted from here on
key schedule: ECDHE shared secret → HKDF → handshake keys → app keys
verify: leaf → intermediate(s) → root store; SAN match; expiry; OCSP
resumption: ticket → PSK binder → 1-RTT again, or 0-RTT early data
```

**Walkthrough.**

1. ClientHello carries what the client will accept and one X25519 key
   share: supported_versions (1.3 and 1.2), cipher suites, signature
   algorithms, the SNI hostname, and — if it holds a ticket — a PSK
   identity with a binder proving it really saw that ticket.
2. ServerHello picks the version and exactly one suite and returns its
   own key share. Everything after this message is encrypted, which is
   the whole difference from TLS 1.2: EncryptedExtensions, Certificate,
   CertificateVerify and Finished never cross the wire in the clear.
3. Certificate presents the chain, leaf first. The intermediates must be
   in the message: browsers will fetch a missing link over AIA, most
   clients will not, and "chain incomplete" is the result.
4. CertificateVerify is a signature over the transcript hash made with
   the leaf's private key. It proves possession of the key, not merely
   possession of a certificate somebody published.
5. Finished is an HKDF-derived MAC over the whole transcript; the client
   checks the server's, then sends its own. Any earlier tampering — a
   downgraded suite, a swapped certificate — changes the transcript and
   fails here.
6. The key schedule runs X25519 to get a shared secret that is never
   transmitted, then HKDF extract and expand over the transcript:
   early secret, derived secret, handshake secret, master secret, and
   separate client and server traffic keys per direction. The ephemeral
   private key is discarded, which is where forward secrecy comes from.
7. Path building goes leaf → intermediates → a root already in the
   system or browser store. Checks in order: validity window on both
   sides' clocks, basicConstraints and keyUsage on each CA, SAN matching
   the hostname being dialled (the common name is legacy), then
   revocation — an OCSP staple the server included, or nothing at all.
8. Resumption: after a full handshake the server issues a
   NewSessionTicket. The next connection offers the PSK with a binder,
   gets a full handshake in one RTT instead of the certificate steps,
   or sends 0-RTT early data in its very first flight.
9. 0-RTT is the sharp edge: the early data key is derived from the
   ticket alone, so the server accepts it before it has any proof the
   client is the ticket's owner — an on-path attacker can replay that
   flight verbatim.

**Sketch.**

```go
cfg := &tls.Config{
    MinVersion: tls.VersionTLS12, // state the floor; 1.3 is preferred
    ServerName: host,             // SNI now, SAN check against the chain
    // RootCAs defaults to the system store — override only for a
    // genuinely private CA, and know that doing so trusts whoever
    // holds that key.
    ClientSessionCache: tls.NewLRUClientSessionCache(64), // resumption
}
conn, err := tls.DialWithDialer(
    &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
    "tcp", net.JoinHostPort(host, "443"), cfg,
)
if err != nil {
    return err // expired, unknown authority, hostname mismatch, downgrade
}
state := conn.ConnectionState() // state.PeerCertificates[0] is the leaf
```

**Failure story.** An expired leaf fails the validity window — commonly
clock skew on either side — and the error names the not-before or
not-after date. A server that sends only its leaf produces an incomplete
chain: the client cannot reach a trusted root, reported as "unable to
get local issuer", which is a server fault rather than a client one. A
private CA fails as "unknown authority" on any machine outside that
estate — and, silently, on a minimal container image shipped without a
CA bundle. A hostname mismatch means the certificate is genuine but not
for this name: valid chain, wrong SAN, and browsers say so while API
clients collapse it into a generic error. A peer that offers only TLS
1.1 or an RSA key-exchange suite fails version negotiation with an alert
rather than downgrading, so the caller sees a handshake failure with no
version named — which is why `MinVersion` is worth writing down.

**Watch-outs.**

- 0-RTT is not replay-safe for side effects: idempotent GETs only, and
  a server accepting it needs its own anti-replay window.
- `InsecureSkipVerify: true` is test-only: it kills the chain, expiry
  and SAN checks while the connection still looks encrypted.
- Custom roots turn an enterprise MITM into a normal chain: the trust
  store is your policy, and that root's holder is invisible to your code.
- Revocation is approximate — staples go stale, CRLs are huge — which is
  why the answer is certificates short-lived enough for expiry to act.
- Watch handshake duration, resumption rate and x509 error classes; a
  falling resumption rate means tickets are being dropped.
