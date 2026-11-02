# Web services: HTTP, TLS, gRPC and service security

A web service is a protocol endpoint plus the trust and failure boundaries
around it. A request may cross a browser, DNS resolver, CDN, load balancer,
TLS terminator, API gateway, service mesh and application before reaching
data. This guide explains the interfaces and the security properties at
each hop. It builds on [networking](../networking/README.md) and
[program execution](../execution/README.md), and points into
[`linux`](../linux/README.md) where sockets and packets enter the kernel.

Read in order: service boundaries, HTTP, TLS, identity/authorization,
gRPC, then the production security and reliability envelope.

---

## 1. A web service is a chain of trust boundaries

**Why.** “The API is behind HTTPS” says only that one network hop is
encrypted. It does not identify the caller, authorize access to a record,
protect an internal hop, bound the request, or make retries safe. The
service architecture determines where each of those guarantees must be
checked.

**What.** A web service accepts a protocol message, authenticates a
principal, authorizes an operation on a resource, validates input,
executes bounded work and returns a protocol response. Proxies and
gateways may terminate TLS, rewrite headers, retry requests, buffer bodies
or translate protocols; they are therefore part of the security and
correctness model, not transparent plumbing.

**How.** A typical request passes through several independently
configurable boundaries:

```text
browser/client
  → DNS → TCP/QUIC → TLS
  → CDN/WAF/load balancer (possibly TLS termination)
  → API gateway (authentication, quotas, routing)
  → service (authorization, validation, business logic)
  → database / downstream RPC
  → response travels back through proxies and TLS hops
```

For each hop, record who authenticates the peer, which identity is
forwarded, who is allowed to assert that identity, how deadlines and
request IDs propagate, and whether the hop is encrypted. `Forwarded`,
`X-Forwarded-For`, `X-Forwarded-Proto` and similar headers are trustworthy
only when inserted by a known proxy and stripped/replaced at the public
edge. A direct client must not be able to choose the identity or source
address your service accepts from those headers.

**Advantages.** Explicit trust boundaries make deployment reviews and
incident debugging concrete: each layer has a responsibility and an
observable failure mode. The same application authorization rules can
be tested whether traffic arrived from a browser, gateway or gRPC client.

**Disadvantages.** Every proxy can add latency, buffering, header
rewrites and another certificate/key lifecycle. Security checks split
across layers drift unless their ownership and forwarded identity
contracts are documented and tested.

---

## 2. HTTP semantics, framing and safe API behavior

**Why.** HTTP is more than a request line and JSON body. Persistent
connections, stream multiplexing, message framing, caches and retry
semantics determine whether two intermediaries interpret the same
request and whether a retry duplicates a side effect.

**What.** HTTP defines methods, target, headers, message body and status.
HTTP/1.1 frames messages with `Content-Length` or chunked transfer
encoding; HTTP/2 carries concurrent streams in binary frames over one
TCP connection; HTTP/3 carries streams over QUIC. HTTP/2 removes
application-level response ordering between streams, but TCP packet loss
still stalls the connection; QUIC isolates loss to streams.

**How.** Design the API contract before writing handlers:

| Concern | Contract to choose | Security/reliability reason |
|---|---|---|
| Method | GET/HEAD safe; PUT/DELETE idempotent; POST usually not | Controls which retries are safe |
| Framing | One authoritative body length/encoding interpretation | Parser disagreement can cause request smuggling |
| Content type | Allowlisted media types; validate actual body shape | Prevents parser confusion and oversized work |
| Limits | Header/body size, upload duration, concurrency and response size | Bounds memory and CPU denial of service |
| Status | Stable success/error codes and error schema | Clients need to distinguish retryable from permanent errors |
| Caching | `Cache-Control`, validators, `Vary`, private/public scope | Prevents stale or cross-user data leakage |
| Deadlines | Separate connect, TLS, header, body and total budgets | One slow hop must not consume every downstream resource |

Request smuggling happens when a front proxy and back-end disagree about
message boundaries — historically around conflicting content-length and
transfer-encoding interpretations or protocol downgrades. Keep HTTP
implementations patched, reject ambiguous framing, normalize at one
trusted edge and test the exact proxy-to-service protocol pair. Never
build a second HTTP parser from ad hoc header scanning.

HTTP/2's connection multiplexing does not create independent TCP
congestion windows. HTTP/3 changes the transport to QUIC but introduces
UDP reachability, certificate/ALPN negotiation and different proxy
behavior. Test the protocol negotiated in production, not only the one
used by a local client.

Useful inspection commands:

```bash
curl -sv --http1.1 https://api.example.test/health
curl -sv --http2 https://api.example.test/health
curl -sv --http3-only https://api.example.test/health  # requires curl built with HTTP/3
```

**Advantages.** Correct method and framing semantics make client behavior
predictable across retries, caches and proxies. Explicit request bounds
turn unbounded memory/CPU exposure into a capacity decision.

**Disadvantages.** Intermediary implementations differ, especially when
translating between HTTP versions. Overly aggressive retries, buffering
or caching can turn a brief outage into duplicate writes, memory
pressure or private-data disclosure.

---

## 3. TLS: authenticate the hop you actually have

**Why.** Encryption without peer verification can encrypt a connection
to an attacker. TLS protects one connection between two endpoints; a
proxy that terminates TLS creates a new trust boundary to the upstream.

**What.** TLS negotiates protocol/cipher parameters, proves server
identity using a certificate chain and hostname check, establishes
session keys and protects application records for integrity and
confidentiality. TLS 1.3 typically takes one round trip for a new
handshake; resumed 0-RTT data can be replayed and should only carry
replay-safe operations.

**How.** Verify certificate chain and service name, set minimum supported
protocol versions according to policy, rotate keys/certificates, and
choose where TLS terminates. If a load balancer terminates public TLS,
protect the upstream hop (often with TLS/mTLS) and make sure the
application accepts forwarded scheme/client identity headers only from
that balancer. Service-to-service mTLS can authenticate workload
identities, but the service still needs authorization rules.

```bash
openssl s_client -connect api.example.test:443 \
  -servername api.example.test -alpn h2 -showcerts
```

For Go clients and servers, use `crypto/tls` through `http.Transport` or
`http.Server`; leave certificate verification enabled. A private CA
should be installed into a dedicated root pool rather than bypassing
verification. Rotate trust roots with an overlap period. Certificate
pinning can reduce CA trust but introduces a high-risk rotation and
recovery problem; use it only with an operational pin update plan.

**Advantages.** TLS provides confidentiality and integrity on the
connection and certificate verification binds the connection to a
service name. mTLS can provide a cryptographic workload identity for
east-west traffic.

**Disadvantages.** TLS does not protect data after termination, authorize
an action, hide traffic metadata or prevent an authenticated client from
abusing the API. Certificate expiration, wrong SAN/SNI, incomplete
chains, clock skew and inconsistent trust stores are frequent outage
causes.

---

## 4. Authentication, authorization and browser security

**Why.** Authentication answers “who is making this request?”;
authorization answers “may that identity perform this action on this
object now?” Confusing them causes broken object-level authorization,
the most common API security failure class.

**What.** Authentication establishes a principal from a credential:
session cookie, OAuth access token, API key, client certificate or
workload identity. Authorization evaluates that principal against an
operation, resource, tenant and current policy. The check belongs at the
resource boundary, not only at the gateway route.

**How.** For a JWT, validate the signature with an allowlisted algorithm,
issuer (`iss`), audience (`aud`), expiry (`exp`), not-before (`nbf`) and
key ID/key rotation policy. A decoded JWT is not verified until signature
and claims are validated. For opaque tokens, use trusted introspection or
session state. Keep API keys out of URLs, logs and source control; scope
and rotate them. Check object ownership/tenant on every read and write,
including nested objects and batch endpoints.

Browser-specific controls are different from API authorization:

| Control | Protects | Does not do |
|---|---|---|
| CORS | Which browser origins may read a cross-origin response | Authenticate callers or stop non-browser clients |
| CSRF token / SameSite cookie policy | Cookie-authenticated state changes initiated cross-site | Protect a stolen bearer token |
| CSP | Restricts script/resource execution in a browser page | Validate server-side input |
| Secure/HttpOnly cookie flags | Reduce transport theft and script access | Prevent CSRF by themselves |
| Authorization check | Access to a specific operation/object | Validate the request's syntax or safe size |

Cookie-authenticated state changes need CSRF defenses and safe
SameSite/Origin policy. CORS is not an authorization mechanism. Store
passwords with a modern password hashing function and per-password salt,
never reversible encryption or a fast general-purpose hash. Apply
rate-limits and abuse detection to login, password reset and token
issuance flows without leaking whether an account exists.

**Advantages.** A clear separation of identity, object-level policy and
input validation makes least privilege testable and supports multiple
client types.

**Disadvantages.** Tokens and sessions introduce expiry, revocation and
rotation complexity. Gateway-only authorization misses object-level
checks; CORS-only “security” leaves every non-browser caller unaffected.

---

## 5. gRPC: typed RPC over HTTP/2

**Why.** Internal services often need typed contracts, streaming,
compact serialization, generated clients and deadlines that propagate
through a call graph. Reimplementing those over ad hoc JSON endpoints
creates inconsistent compatibility and error behavior.

**What.** gRPC defines services and messages in Protocol Buffers, then
uses generated stubs over HTTP/2. A normal RPC maps to an HTTP/2 stream;
the gRPC status and trailing metadata carry the final result. Unary,
server-streaming, client-streaming and bidirectional-streaming methods
share the same schema and transport conventions.

**How.** Define a versioned service contract and preserve field numbers:

```protobuf
syntax = "proto3";
package inventory.v1;

service Inventory {
  rpc GetItem(GetItemRequest) returns (GetItemResponse);
  rpc WatchChanges(WatchRequest) returns (stream ItemChange);
}

message GetItemRequest { string item_id = 1; }
message GetItemResponse { string item_id = 1; string version = 2; }
message WatchRequest { string after_version = 1; }
message ItemChange { string item_id = 1; string version = 2; }
```

Propagate caller deadlines and cancellation to every downstream RPC;
bound message size, stream lifetime and concurrent streams. Use TLS
transport credentials, optionally mTLS for service identity, then
authenticate and authorize each method/resource. Treat metadata like
HTTP headers: never put bearer tokens or personal data in logs. Retries
must be limited to idempotent operations or protected by idempotency
keys. Protobuf compatibility requires never reusing a removed field
number; reserve removed numbers/names and make additive changes first.

```go
ctx, cancel := context.WithTimeout(parent, 300*time.Millisecond)
defer cancel()

reply, err := client.GetItem(ctx, &pb.GetItemRequest{ItemId: itemID})
if err != nil {
    // Inspect the gRPC status code; do not retry every error.
    return err
}
```

Production servers should expose health checks and bounded reflection
(or disable reflection), configure message limits and TLS, and distinguish
transport failure from application status. A gRPC-Web gateway may be
needed for browsers that cannot directly use the native HTTP/2 gRPC
transport.

**Advantages.** Generated stubs enforce a shared schema, protobuf is
compact, streaming is built in and deadlines/status codes compose across
service calls.

**Disadvantages.** Browser support and manual debugging are less direct
than JSON/HTTP. Schema evolution is strict, HTTP/2 proxies must preserve
gRPC trailers, and a missing deadline can leave expensive work running
after its caller has abandoned it.

---

## 6. Web-service threat model and secure request handling

**Why.** A public endpoint is an input parser connected to privileged
data and outbound network access. Attackers target every parser,
identity translation, and unbounded resource, not just the TLS handshake.

**What.** Secure service handling combines protocol normalization,
authentication, object-level authorization, validation, resource
limits, safe outbound access, auditability and patching. OWASP API
categories such as broken object authorization, broken authentication,
unrestricted resource consumption, SSRF and security misconfiguration
map directly onto these boundaries.

**How.** Apply layered controls:

1. Terminate malformed/ambiguous HTTP at a patched, consistent edge;
   cap header count/size, body size, decompression ratio and upload time.
2. Authenticate before expensive work and authorize the specific object
   and tenant on every access; default-deny unknown methods and routes.
3. Validate schema, range, encoding and content type; parameterize SQL
   and use context-safe output encoding for HTML.
4. Prevent SSRF in URL-fetching features: allowlist schemes/hosts, resolve
   and validate addresses, block loopback/private/link-local/metadata
   destinations, re-check redirects and pin egress policy at the network
   boundary. DNS rebinding means validating only the initial hostname is
   insufficient.
5. Set connection, header, body, downstream and total deadlines; bound
   concurrency, queue depth, retries and response size.
6. Redact credentials and sensitive payloads from logs; retain request
   IDs, principal, authorization outcome and safe audit events.

Security testing should include wrong-tenant object IDs, expired/wrong-
audience tokens, malformed framing, oversized compressed bodies,
redirect-based SSRF, CSRF on cookie flows, rate-limit exhaustion and
proxy/backend protocol mismatches. Exercise both positive and negative
authorization cases. A 200 response for a valid user is not enough to
prove the endpoint is safe.

**Advantages.** Layered controls limit both the probability and impact
of compromise; explicit resource budgets also improve reliability under
accidental bursts.

**Disadvantages.** Controls across gateway, service mesh and application
can conflict or duplicate work. Security policy must be tested during
certificate rotation, proxy upgrades and schema changes, not merely at
initial deployment.

---

## 7. Deadlines, retries, streaming and observability

**Why.** A service that is locally fast can still be globally slow when
it waits on a downstream, retries a non-idempotent write or buffers a
stream behind a proxy. Reliability is a property of the whole request
graph.

**What.** A request budget includes connection setup, TLS, queue time,
handler work and every downstream call. Retries consume that same
budget; streaming changes buffering and cancellation behavior; telemetry
must join spans and structured logs across boundaries.

**How.** Propagate a deadline rather than starting a fresh timeout at
each hop. Retry only classified transient failures, use capped
exponential backoff with jitter, and ensure the remaining deadline can
cover another attempt. Use idempotency keys for retried writes. Apply
circuit breaking/load shedding at well-defined boundaries, and test
client cancellation while a server is streaming or waiting downstream.

Instrument request count, status, latency histograms, in-flight work,
queue time, retry count, saturation and downstream time. Trace IDs are
correlation identifiers, not credentials; avoid putting tokens or raw
personal data in spans. For HTTP streaming, ensure proxies do not buffer
responses unintentionally; for gRPC streams, enforce message and stream
limits and observe per-stream lifetime.

| Symptom | Inspect first |
|---|---|
| p99 rises, service CPU low | Downstream wait, queueing, lock/futex, connection pool |
| Duplicate writes after timeout | Retry policy, idempotency key, timeout after commit |
| TLS succeeds at edge, upstream fails | Trust roots, SNI, hostname, client certificate, forwarded scheme |
| Browser works, service client denied | CORS confusion, cookie vs bearer auth, audience/scope |
| HTTP/2 works, gRPC fails | Trailer preservation, ALPN, proxy timeout/body limits |
| Outbound URL endpoint reaches metadata service | SSRF allowlist, redirects, DNS re-resolution and egress filtering |

**Advantages.** A shared deadline and retry contract prevents one slow
dependency from multiplying work across a service graph. Cross-hop
telemetry turns “the API is slow” into a specific queue, downstream or
transport interval.

**Disadvantages.** Retries can amplify overload and idempotency keys need
retention/state. Tracing has cardinality and privacy costs, and proxy
buffering can silently defeat streaming semantics.

---

## Further reading

- [HTTP Semantics, RFC 9110](https://www.rfc-editor.org/rfc/rfc9110)
- [HTTP/2, RFC 9113](https://www.rfc-editor.org/rfc/rfc9113)
- [HTTP/3, RFC 9114](https://www.rfc-editor.org/rfc/rfc9114)
- [TLS 1.3, RFC 8446](https://www.rfc-editor.org/rfc/rfc8446)
- [gRPC documentation](https://grpc.io/docs/)
- [OWASP API Security Top 10](https://owasp.org/www-project-api-security/)
- [HTTP/3 and QUIC in networking](../networking/README.md)
