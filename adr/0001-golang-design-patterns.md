# ADR-0001: Go Design Patterns and Their Idiomatic Realizations

## Status

Accepted

Date: 2026-10-04

## Context

Go is a deliberately small language. It has no classes, no inheritance, no
generics-heavy type hierarchies (generics arrived in 1.18 but remain
conservative), and no exceptions. The canonical Go proverbs — *"the bigger the
interface, the weaker the abstraction"*, *"errors are values"*, *"don't
communicate by sharing memory; share memory by communicating"* — actively
push against several of the patterns popularized by *Design Patterns: Elements
of Reusable Object-Oriented Software* (Gamma et al., 1994).

Yet the problems those patterns solve do not disappear: we still need to
construct objects flexibly, decouple producers from consumers, vary algorithms
at runtime, and walk heterogeneous structures. Teams therefore keep
re-inventing the same solutions badly — reaching for Java-style abstract
base-class hierarchies, `reflect`-driven factories, or global mutable state —
because the language gives no obvious template.

This record decides:

1. Which GoF patterns we adopt, in what Go-idiomatic form.
2. Which we reject, and what we do instead.
3. The house conventions (naming, file layout, error handling) that make the
   adopted patterns read like Go rather than like translated Java.

## Decision

We adopt a pattern set organized around three Go-specific mechanisms:

- **Interfaces satisfied structurally**, declared at the *consumer* side
  (`interface{ ... }` where the value is used, not where it is produced).
- **Functions as values**, which replace a large fraction of the
  parameterized-behavior patterns.
- **Goroutines + channels + `context.Context`**, which replace much of the
  event-bus / mediator / observer plumbing.

Every pattern below ships with a *distinct* runnable example. No example is
reused across patterns; each targets a different domain so the trade-offs are
visible in context.

---

## Part I — Creational Patterns

### 1. Singleton

**Verdict: adopted, but scoped and explicit.**

Global mutable singletons are a testing liability. We allow a singleton only
for genuinely process-wide, immutable-after-init resources (config, connection
pool registry), and we expose it through an `init`-time constructor that is
idempotent and thread-safe via `sync.Once`.

```go
// package telemetry

import (
	"sync"
	"time"
)

type Meter struct {
	started time.Time
	// ... counters, exporters
}

var (
	once   sync.Once
	meter  *Meter
)

// GetMeter returns the process-wide meter, creating it on first call.
// It is safe for concurrent use.
func GetMeter() *Meter {
	once.Do(func() {
		meter = &Meter{started: time.Now()}
	})
	return meter
}
```

**Consequences.** `sync.Once` gives lock-free reads after the first call and
avoids the classic check-then-act race. Testing remains possible because the
package-level `meter` variable is swappable from an in-package test via a
`resetForTest()` helper (white-box test only). We explicitly *reject* the
`getInstance()` Java idiom with lazy nil-checks and we reject
`func init()` side effects beyond declaration.

---

### 2. Factory Method

**Verdict: adopted — usually as a plain function returning an interface.**

Go has no `new`/virtual-dispatch machinery, so the factory is just a function
that chooses a concrete type based on input and returns the narrow interface
the caller actually needs.

```go
// package storage

import (
	"context"
	"errors"
	"fmt"
)

// Store is the consumer-side interface: only what callers need.
type Store interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

type diskStore struct{ root string }
type memStore struct{ data map[string][]byte }

func (d *diskStore) Put(ctx context.Context, k string, v []byte) error { /* ... */ return nil }
func (d *diskStore) Get(ctx context.Context, k string) ([]byte, error) { /* ... */ return nil, nil }
func (m *memStore) Put(ctx context.Context, k string, v []byte) error  { /* ... */ return nil }
func (m *memStore) Get(ctx context.Context, k string) ([]byte, error)  { /* ... */ return nil, nil }

// ErrUnknownDriver is returned for unrecognised driver names.
var ErrUnknownDriver = errors.New("storage: unknown driver")

// NewStore is the factory method.
func NewStore(driver, root string) (Store, error) {
	switch driver {
	case "disk":
		return &diskStore{root: root}, nil
	case "memory":
		return &memStore{data: make(map[string][]byte)}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, driver)
	}
}
```

**Consequences.** Callers depend on `Store`, never on `diskStore`. The error
is wrapped with `%w` so callers can `errors.Is(err, ErrUnknownDriver)`. For
plugin-style extension we *additionally* support driver registration
(`storage.Register("s3", openS3)`) so adding a backend does not modify this
file — that variant is the Abstract Factory / plugin pattern (§3).

---

### 3. Abstract Factory / Plugin Registry

**Verdict: adopted as a registry of constructors keyed by string.**

When a family of related objects must be created together (a transport +
codec + retry policy that are only compatible in certain combinations), a
map of constructor functions is more idiomatic than an `AbstractFactory`
interface hierarchy.

```go
// package messaging

type Bundle struct {
	Sender  Sender
	Codec   Codec
	Retry   RetryPolicy
}

type Factory func(cfg Config) (Bundle, error)

var factories = map[string]Factory{}

// Register must be called from an init() in the providing package.
func Register(name string, f Factory) {
	if _, dup := factories[name]; dup {
		panic("messaging: duplicate factory " + name)
	}
	factories[name] = f
}

// Open builds a whole bundle in one consistent family.
func Open(kind string, cfg Config) (Bundle, error) {
	f, ok := factories[kind]
	if !ok {
		return Bundle{}, fmt.Errorf("messaging: no factory for %q", kind)
	}
	return f(cfg)
}

// In a separate file/package:
//
//	func init() { messaging.Register("kafka", newKafkaBundle) }
```

**Consequences.** Registration panics on duplicates — a programming error
surfaced at startup, not at request time. The `init()` side effect is
deliberate and confined to registration. Import must be made explicit with a
blank import (`_ "ourco/messaging/kafka"`) so linkage stays visible.

---

### 4. Builder

**Verdict: adopted — two idioms, both in use.**

**Idiom A — functional options** (preferred for configuration structs with
many optional fields):

```go
// package httpserver

type Option func(*Server)

func WithTimeout(d time.Duration) Option {
	return func(s *Server) { s.timeout = d }
}

func WithMiddleware(mw ...Middleware) Option {
	return func(s *Server) { s.mw = append(s.mw, mw...) }
}

func WithAddr(addr string) Option {
	return func(s *Server) { s.addr = addr }
}

func New(opts ...Option) *Server {
	s := &Server{
		addr:    ":8080",
		timeout: 30 * time.Second, // defaults applied before options
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Usage:
//   srv := httpserver.New(
//       httpserver.WithAddr(":9090"),
//       httpserver.WithTimeout(5*time.Second),
//   )
```

**Idiom B — stepwise builder** when construction must be validated or when
fields must be filled in a forced order (SQL, complex value objects):

```go
// package query

type SQL struct {
	table  string
	wheres []string
	limit  int
}

type SQLBuilder struct{ sql SQL }

func From(table string) *SQLBuilder {
	return &SQLBuilder{sql: SQL{table: table}}
}

func (b *SQLBuilder) Where(clause string) *SQLBuilder {
	b.sql.wheres = append(b.sql.wheres, clause)
	return b
}

func (b *SQLBuilder) Limit(n int) *SQLBuilder { b.sql.limit = n; return b }

// Build validates and freezes the value; the SQL struct is unexported
// so no partially-built value can escape.
func (b *SQLBuilder) Build() (SQL, error) {
	if b.sql.table == "" {
		return SQL{}, errors.New("query: table is required")
	}
	return b.sql, nil
}
```

**Consequences.** Functional options cannot be mis-ordered and cost zero
allocation on the happy path; they cannot express "required field missing"
well. The stepwise builder can, at the price of a fluent chain that *can*
escape unvalidated if you expose `SQL` fields — hence the unexported fields
plus `Build()`.

---

### 5. Prototype (Clone)

**Verdict: rejected as a pattern, adopted as an interface where copying is
genuinely required.**

Go has shallow-copy-by-value. We do not build prototype registries. Where
copying is needed we require an explicit `Clone()` that performs a *deep*
copy, because implicit shallow copies of structs containing slices/maps are a
classic bug source.

```go
// package doc

// Document is a value type with reference-typed fields; it must be cloned
// explicitly before mutation.
type Document struct {
	Title string
	Tags  []string
	Attrs map[string]string
}

// Clone returns a fully independent copy. It is safe to call on a zero
// Document.
func (d Document) Clone() Document {
	out := Document{Title: d.Title}
	if d.Tags != nil {
		out.Tags = make([]string, len(d.Tags))
		copy(out.Tags, d.Tags)
	}
	if d.Attrs != nil {
		out.Attrs = make(map[string]string, len(d.Attrs))
		for k, v := range d.Attrs {
			out.Attrs[k] = v
		}
	}
	return out
}
```

**Consequences.** Value receiver makes the shallow copy explicit and cheap;
`Clone` makes the deep copy explicit and auditable. `golang.org/x/exp/maps`
or codegen can replace the manual map loop for large structs. We reject
`reflect`-based deep-copy helpers: they are slow, silently wrong across
unexported fields, and unreviewable.

---

## Part II — Structural Patterns

### 6. Adapter

**Verdict: adopted — this is one of Go's sharpest idioms.**

An adapter is a small type that implements the interface the *consumer*
expects while delegating to a type that implements something else. Note the
interface is declared next to the consumer.

```go
// package cache  (consumer side)

// Reader is what this package needs. Declared here, not by the provider.
type Reader interface {
	Read(key string) ([]byte, bool)
}

type entry struct{ value []byte }

// entryStore is an unrelated legacy type from package legacy.
type entryStore struct{ m map[string]entry }

func (e entryStore) Load(k string) (entry, bool) { /* ... */ return entry{}, false }

// entryStoreAdapter lets a legacy entryStore be used as a cache Reader.
type entryStoreAdapter struct{ src entryStore }

func (a entryStoreAdapter) Read(key string) ([]byte, bool) {
	e, ok := a.src.Load(key)
	if !ok {
		return nil, false
	}
	return e.value, true
}

func NewFromLegacy(src entryStore) Reader {
	return entryStoreAdapter{src: src}
}
```

**Consequences.** Adding the adapter does not modify `legacy`. Compile-time
assertions make the wiring fail fast:

```go
var _ Reader = entryStoreAdapter{}
```

This is also the canonical answer to "how do I use a third-party type with my
interface?" — and it is why Go interfaces are declared at the point of use.

---

### 7. Bridge

**Verdict: adopted as "interfaces on both axes", usually implicit.**

The Bridge's intent — decouple an abstraction from its implementation so both
vary independently — is achieved in Go simply by holding an interface field
and composing rather than embedding.

```go
// package notification

// Notifier is the implementation axis.
type Notifier interface {
	Deliver(ctx context.Context, msg Message) error
}

// Alert is the abstraction axis: severity policy.
type Alert struct {
	notifier Notifier
	severity Severity
}

func NewAlert(n Notifier, s Severity) *Alert {
	return &Alert{notifier: n, severity: s}
}

// Trigger picks channel, retry, and escalation purely from severity,
// while delivery mechanics live entirely behind Notifier.
func (a *Alert) Trigger(ctx context.Context, m Message) error {
	m.Severity = a.severity
	if a.severity >= Critical {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	return a.notifier.Deliver(ctx, m)
}
```

**Consequences.** Two orthogonal axes (severity policy × delivery mechanism)
are tested independently: a `fakeNotifier` exercises all severities without
network I/O. Rejects the GoF version's `Abstraction`/`Implementor` class
naming — in Go this is just a struct with an interface field.

---

### 8. Composite

**Verdict: adopted — interface + recursive struct.**

```go
// package fsplan

// Entry is the leaf/branch interface (the "Component").
type Entry interface {
	Name() string
	Size() int64
	// Render writes an indented tree line.
	Render(w io.Writer, depth int) error
}

// File is a Leaf.
type File struct {
	name string
	size int64
}

func (f File) Name() string  { return f.name }
func (f File) Size() int64   { return f.size }
func (f File) Render(w io.Writer, d int) error {
	_, err := fmt.Fprintf(w, "%s%s (%d)\n", strings.Repeat("  ", d), f.name, f.size)
	return err
}

// Dir is a Composite holding children.
type Dir struct {
	name     string
	children []Entry
}

func (d *Dir) Name() string { return d.name }

// Size aggregates over children — the essence of Composite.
func (d *Dir) Size() int64 {
	var total int64
	for _, c := range d.children {
		total += c.Size()
	}
	return total
}

func (d *Dir) Render(w io.Writer, depth int) error {
	if _, err := fmt.Fprintf(w, "%s%s/\n", strings.Repeat("  ", depth), d.name); err != nil {
		return err
	}
	for _, c := range d.children {
		if err := c.Render(w, depth+1); err != nil {
			return err
		}
	}
	return nil
}
```

**Consequences.** Clients treat `File` and `Dir` identically; recursion lives
in the type, not in a visitor. Mixed value/pointer receivers are a real
hazard here — `Dir` must be behind `*Dir` because `Size`/`Render` are
logically non-destructive but the struct is large; we therefore recommend
pointer receivers across the whole composite for consistency, and add
`var _ Entry = (*Dir)(nil)` to catch drift.

---

### 9. Decorator

**Verdict: adopted — a wrapper type, or a higher-order function.**

**Struct form** (when the decorated thing is a named type):

```go
// package store

type Store interface {
	Get(ctx context.Context, k string) ([]byte, error)
}

// loggingStore decorates a Store with structured logging.
type loggingStore struct {
	inner Store
	log   *slog.Logger
}

func WithLogging(s Store, log *slog.Logger) Store {
	return &loggingStore{inner: s, log: log}
}

func (l loggingStore) Get(ctx context.Context, k string) ([]byte, error) {
	start := time.Now()
	b, err := l.inner.Get(ctx, k)
	l.log.InfoContext(ctx, "store.get",
		"key", k,
		"ok", err == nil,
		"duration", time.Since(start),
	)
	return b, err
}
```

**Function form** (idiomatic when the unit of work is a `func`):

```go
// package middleware

// Handler is a single request-handling function.
type Handler func(ctx context.Context, req Request) (Response, error)

// Recover converts panics into 500 responses — a decorator over Handler.
func Recover(next Handler) Handler {
	return func(ctx context.Context, req Request) (resp Response, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return next(ctx, req)
	}
}

// Usage: h := Recover(Timeout(3*time.Second)(Auth(h)))
```

**Consequences.** Wrappers are composed left-to-right, are independently
testable, and are the structural basis of Go's middleware convention. Reject
Java's `java.io`-style `Wrapper` base classes — nothing is inherited, only
delegated.

---

### 10. Facade

**Verdict: adopted — an exported constructor returning a small API over
several internal packages.**

```go
// package pipeline

// Run is the facade: it wires parsing, validation, and persistence so
// callers never touch those packages directly.
func Run(ctx context.Context, src Source, opts Options) (Summary, error) {
	raw, err := parse.All(src)
	if err != nil {
		return Summary{}, fmt.Errorf("pipeline: parse: %w", err)
	}
	items, err := validate.Each(raw, opts.Rules)
	if err != nil {
		return Summary{}, fmt.Errorf("pipeline: validate: %w", err)
	}
	n, err := persist.Bulk(ctx, items, opts.Dest)
	if err != nil {
		return Summary{}, fmt.Errorf("pipeline: persist: %w", err)
	}
	return Summary{Written: n, Skipped: len(raw) - n}, nil
}
```

**Consequences.** The internal packages stay unexported (lowercase) so the
facade *is* the public API. This is the cheapest way to enforce layering in
Go: no architecture tests needed, the compiler enforces it. Errors are
wrapped at each boundary with operation context.

---

### 11. Flyweight

**Verdict: adopted only when measured; normally replaced by interning
strings or shared immutable values.**

Go's garbage collector makes naive flyweights rarely worthwhile. We adopt it
in exactly one shape: a mutex-guarded intern pool for large immutable
structures.

```go
// package glyph

type Glyph struct {
	Rune   rune
	Font   string
	Metrics FontMetrics // large, ~2KB, identical across uses
}

var (
	poolMu sync.RWMutex
	pool   = map[glyphKey]*Glyph{}
)

type glyphKey struct {
	r    rune
	font string
}

// Intern returns a shared *Glyph for the key, creating it once.
// Callers must treat the result as immutable.
func Intern(r rune, font string, m FontMetrics) *Glyph {
	k := glyphKey{r, font}

	poolMu.RLock()
	g, ok := pool[k]
	poolMu.RUnlock()
	if ok {
		return g
	}

	poolMu.Lock()
	defer poolMu.Unlock()
	if g, ok := pool[k]; ok { // double-checked
		return g
	}
	g = &Glyph{Rune: r, Font: font, Metrics: m}
	pool[k] = g
	return g
}
```

**Consequences.** RWMutex lets concurrent readers share; the double-check
avoids a redundant store. The pool is a *leak* by construction — we bound it
with `sync.Pool` semantics or an LRU cap in production, and we document that
`*Glyph` is immutable. Benchmarks are required before introducing a flyweight
anywhere else; `string` interning usually suffices.

---

### 12. Proxy

**Verdict: adopted — a wrapper that controls access (caching, auth, retry),
as opposed to Decorator, which adds behaviour.**

The distinction from §9 is intent, not shape: a proxy *stands in for* the
subject and may refuse or short-circuit calls.

```go
// package bucket

type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// cachingProxy serves reads from memory and forwards misses.
type cachingProxy struct {
	inner BlobStore
	cache *lru.Cache[string, []byte]
	ttl   time.Duration
}

func WithCache(s BlobStore, c *lru.Cache[string, []byte], ttl time.Duration) BlobStore {
	return &cachingProxy{inner: s, cache: c, ttl: ttl}
}

func (p *cachingProxy) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if v, ok := p.cache.Get(key); ok {
		return io.NopCloser(bytes.NewReader(v)), nil // short-circuit
	}
	rc, err := p.inner.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		rc.Close()
		return nil, err
	}
	rc.Close()
	p.cache.Add(key, buf.Bytes())
	return io.NopCloser(bytes.NewReader(buf)), nil
}

func (p *cachingProxy) Put(ctx context.Context, k string, r io.Reader) error {
	if err := p.inner.Put(ctx, k, r); err != nil {
		return err
	}
	p.cache.Remove(k) // invalidate
	return nil
}
```

**Consequences.** Cache invalidation on write keeps the proxy honest. We use
the same wrapper shape for authz proxies, rate limiters, and circuit breakers
— all are "controls access" rather than "adds behaviour", and all are
swapped in at wiring time via the same functional-option style as §4.

---

## Part III — Behavioral Patterns

### 13. Chain of Responsibility

**Verdict: adopted — a slice of handlers applied in order.**

Rather than each handler holding a `next` pointer, we use a slice, which
makes ordering inspectable and lets the chain be built dynamically.

```go
// package validation

type Rule func(ctx context.Context, u *User) error

// Chain applies rules in order and stops at the first error.
func Chain(rules ...Rule) Rule {
	return func(ctx context.Context, u *User) error {
		for i, r := range rules {
			if err := r(ctx, u); err != nil {
				return fmt.Errorf("rule %d: %w", i, err)
			}
		}
		return nil
	}
}

var (
	requireEmail = func(ctx context.Context, u *User) error {
		if u.Email == "" {
			return ErrEmailRequired
		}
		return nil
	}
	requireAge = func(ctx context.Context, u *User) error {
		if u.Age < 13 {
			return ErrUnderage
		}
		return nil
	}
	banDisposable = func(ctx context.Context, u *User) error {
		if disposable(u.Email) {
			return ErrDisposableEmail
		}
		return nil
	}
)

// Usage: validate := validation.Chain(requireEmail, requireAge, banDisposable)
```

**Consequences.** Short-circuiting at the first error is explicit in the
loop. Indexed error reporting (`rule 1:`) keeps debugging tractable. A
*non*-short-circuiting variant (`Collect`) that appends all errors is one
loop-body change — something the pointer-linked GoF version makes awkward.

---

### 14. Command

**Verdict: adopted for undo/redo and job queues; otherwise use closures.**

```go
// package editor

// Command is the interface: do and undo.
type Command interface {
	Do(ctx context.Context) error
	Undo(ctx context.Context) error
	Name() string
}

// InsertText is a concrete command carrying its own state for reversal.
type InsertText struct {
	doc  *Doc
	at   int
	text string
	saved int // bytes removed on undo
}

func NewInsertText(d *Doc, at int, s string) *InsertText {
	return &InsertText{doc: d, at: at, text: s}
}

func (c *InsertText) Name() string { return "insert" }

func (c *InsertText) Do(ctx context.Context) error {
	c.saved = 0
	return c.doc.Insert(c.at, c.text)
}

func (c *InsertText) Undo(ctx context.Context) error {
	removed := len(c.text)
	_, err := c.doc.Delete(c.at, removed)
	return err
}

// History is the invoker.
type History struct {
	undo, redo []Command
}

func (h *History) Run(ctx context.Context, c Command) error {
	if err := c.Do(ctx); err != nil {
		return err
	}
	h.undo = append(h.undo, c)
	h.redo = h.redo[:0] // new action invalidates the redo stack
	return nil
}

func (h *History) Undo(ctx context.Context) error {
	if len(h.undo) == 0 {
		return nil
	}
	c := h.undo[len(h.undo)-1]
	if err := c.Undo(ctx); err != nil {
		return err
	}
	h.undo, h.redo = h.undo[:len(h.undo)-1], append(h.redo, c)
	return nil
}
```

**Consequences.** Every command stores exactly what it needs to reverse
itself; `History` knows nothing about the document. `ctx` propagates
cancellation into long-running commands (export, batch import). For simple
"queue this work" needs we skip the interface entirely and enqueue
`func(context.Context) error`.

---

### 15. Iterator

**Verdict: rejected in the GoF form; adopted as `iter.Seq` (Go 1.23+) or the
closure-pull idiom.**

```go
// package tree

import "iter"

// All yields every node in depth-first order. The caller controls
// termination with `break` — no Done flag, no error channel.
//
// Note: iter.Seq is a func that returns nothing, so a recursive walk
// cannot forward its result. The recursion therefore lives in a helper
// that returns "continue walking", and All is a one-line adapter.
func All(n *Node) iter.Seq[*Node] {
	return func(yield func(*Node) bool) { all(n, yield) }
}

func all(n *Node, yield func(*Node) bool) bool {
	if n == nil {
		return true
	}
	if !yield(n) {
		return false // consumer broke out
	}
	for _, c := range n.Children {
		if !all(c, yield) {
			return false
		}
	}
	return true
}

// Usage:
//   for n := range tree.All(root) {
//       if n.Skip { continue }
//       ...
//   }
```

Pre-generics closure idiom, still valid for older toolchains:

```go
func EachToken(r io.Reader, fn func(tok string) bool) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if !fn(sc.Text()) {
			return nil // caller signalled stop
		}
	}
	return sc.Err()
}
```

**Consequences.** Range-over-function gives lazy evaluation, natural
short-circuiting, and no state machine object. We reject the
`hasNext()/next()` pair: it forces the iterator to own cursor state and to
signal exhaustion by a sentinel (`io.EOF`), which is error-prone with
generics. Go has no `Iterable` interface, and we will not define one —
"accept interfaces, return functions" applies.

---

### 16. Mediator

**Verdict: replaced by channels; a `Mediator` struct only when coordination
is genuinely complex.**

```go
// package fleet

// Coordinator is a mediator: components never reference each other,
// they all talk to it.
type Coordinator struct {
	commands chan Command
	done     chan struct{}
}

func NewCoordinator(buf int) *Coordinator {
	return &Coordinator{
		commands: make(chan Command, buf),
		done:     make(chan struct{}),
	}
}

// Publish is safe from any goroutine.
func (c *Coordinator) Publish(cmd Command) {
	select {
	case c.commands <- cmd:
	case <-c.done: // shut down; drop
	}
}

// Run is the single event loop. Ownership of c.commands lives here.
func (c *Coordinator) Run(ctx context.Context) {
	defer close(c.done)
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-c.commands:
			c.dispatch(ctx, cmd)
		}
	}
}

func (c *Coordinator) dispatch(ctx context.Context, cmd Command) {
	switch t := cmd.(type) {
	case WaypointReached:
		c.publishTo(t.DroneID, RecalculateRoute{DroneID: t.DroneID})
	case BatteryLow:
		c.publishTo(t.DroneID, ReturnToDock{DroneID: t.DroneID})
	}
}
```

**Consequences.** All coordination state is owned by one goroutine — no
locks, and races become logically impossible. `select` on `ctx.Done()` makes
shutdown clean. We reject the GoF `Mediator` with `Colleague` back-pointers:
those become a lock-ordering nightmare the moment two colleagues call each
other. Buffer the channel or the publisher blocks on a slow consumer.

---

### 17. Memento

**Verdict: adopted as value-copy snapshots with an unexported originator
state.**

```go
// package drawing

// Snapshot is opaque to everyone but Drawing. Fields are unexported on
// purpose: you may hold a Snapshot, but you may not mutate one.
type Snapshot struct {
	shapes []shapeState
	rev    int
}

type Drawing struct {
	shapes []shapeState
	rev    int
}

func (d *Drawing) Snapshot() Snapshot {
	cp := make([]shapeState, len(d.shapes))
	copy(cp, d.shapes) // deep enough: shapeState is all value fields
	return Snapshot{shapes: cp, rev: d.rev}
}

// Restore replaces the drawing's state with a snapshot.
func (d *Drawing) Restore(s Snapshot) {
	cp := make([]shapeState, len(s.shapes))
	copy(cp, s.shapes)
	d.shapes, d.rev = cp, s.rev
}

// Caretaker: keeps history without knowing what a shape is.
type Stack struct{ snaps []Snapshot }

func (st *Stack) Push(s Snapshot) { st.snaps = append(st.snaps, s) }
func (st *Stack) Pop() (Snapshot, bool) {
	if len(st.snaps) == 0 {
		return Snapshot{}, false
	}
	s := st.snaps[len(st.snaps)-1]
	st.snaps = st.snaps[:len(st.snaps)-1]
	return s, true
}
```

**Consequences.** Encapsulation is enforced by the type system (unexported
fields), not by convention — the caretaker genuinely cannot peek. Copies are
explicit and bounded; for large documents we'd switch to copy-on-write page
buffers rather than deep-copying everything, but the interface stays the same.

---

### 18. Observer / Pub-Sub

**Verdict: adopted as a typed event bus; for in-process fan-out, `go func`
per subscriber with an explicit unsubscribe.**

```go
// package bus

type Event struct {
	Kind string
	Data any
}

type Subscription struct {
	ch     chan Event
	cancel func()
}

// Chan returns the subscriber's channel. It is closed on Unsubscribe.
func (s Subscription) Chan() <-chan Event { return s.ch }

type Bus struct {
	mu     sync.RWMutex
	subs   map[string][]chan Event
	closed bool
}

func New() *Bus { return &Bus{subs: map[string][]chan Event{}} }

// Subscribe registers a buffered listener for topic.
func (b *Bus) Subscribe(topic string, buf int) Subscription {
	ch := make(chan Event, buf)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return Subscription{ch: ch, cancel: func() {}}
	}
	b.subs[topic] = append(b.subs[topic], ch)
	b.mu.Unlock()

	return Subscription{ch: ch, cancel: func() { b.unsubscribe(topic, ch) }}
}

// Publish never blocks on a slow subscriber: it drops and counts.
func (b *Bus) Publish(topic string, ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs[topic] {
		select {
		case ch <- ev:
		default: // subscriber too slow — drop
		}
	}
}

func (b *Bus) unsubscribe(topic string, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.subs[topic]
	for i, c := range list {
		if c == ch {
			b.subs[topic] = append(list[:i], list[i+1:]...)
			close(ch)
			return
		}
	}
}
```

**Consequences.** Buffered, non-blocking sends prevent one slow subscriber
from stalling the publisher — the drop is deliberate and observable (we
expose a drop counter). Unsubscribe closes the channel so `range ch`
terminates for the consumer. For 1:N fan-out where subscribers must not miss
messages, use `context`-aware blocking sends instead. Rejects the GoF
`update()` push interface: pull-via-channel gives backpressure for free.
**Note the classic pitfall:** never publish while holding `b.mu` for write,
or a subscriber calling `Unsubscribe` inside its loop deadlocks.

---

### 19. State

**Verdict: adopted — an interface field on the context type, with transitions
performed by the states themselves.**

```go
// package vending

// State is one phase of the machine.
type State interface {
	InsertCoin(m *Machine, c Coin) error
	Select(m *Machine, sku string) error
	Refund(m *Machine) error
}

type Machine struct {
	state State
	cred  int
	stock map[string]int
}

func NewMachine(stock map[string]int) *Machine {
	m := &Machine{stock: stock}
	m.state = &Idle{} // initial state
	return m
}

func (m *Machine) InsertCoin(c Coin) error { return m.state.InsertCoin(m, c) }
func (m *Machine) Select(sku string) error  { return m.state.Select(m, sku) }
func (m *Machine) Refund() error            { return m.state.Refund(m) }

// --- concrete states ---

type Idle struct{}

func (Idle) InsertCoin(m *Machine, c Coin) error {
	m.cred += int(c)
	m.state = &Credited{}
	return nil
}
func (Idle) Select(m *Machine, sku string) error { return ErrNoCredit }
func (Idle) Refund(m *Machine) error             { return ErrNothingToRefund }

type Credited struct{}

func (Credited) InsertCoin(m *Machine, c Coin) error { m.cred += int(c); return nil }

func (Credited) Select(m *Machine, sku string) error {
	if m.cred < m.price(sku) {
		return ErrInsufficientFunds
	}
	m.dispense(sku)
	m.cred = 0
	m.state = &Idle{} // transition
	return nil
}

func (Credited) Refund(m *Machine) error {
	m.cred = 0
	m.state = &Idle{}
	return nil
}
```

**Consequences.** Illegal transitions become compile-time-impossible for the
caller (`Idle.Select` exists but returns a typed error) and the `switch` over
state disappears entirely — each state owns its rules. This beats a single
giant `switch m.state` when the state set grows past ~4. Transitions must go
through `m` so invariants hold; we never assign `m.state` from outside the
package (the field is unexported). Table-driven transitions
(`map[State]map[Event]State`) are the alternative for very large FSMs and are
easier to visualize but harder to attach guards to.

---

### 20. Strategy

**Verdict: adopted — and this is the single highest-value pattern in Go,
because functions are first-class.**

```go
// package pricing

// Discount is a strategy: swap it without touching the calculator.
type Discount func(ctx context.Context, cart Cart) (money.Cents, error)

func NoDiscount(ctx context.Context, cart Cart) (money.Cents, error) {
	return 0, nil
}

func PercentOff(pct int) Discount {
	return func(ctx context.Context, cart Cart) (money.Cents, error) {
		return cart.Subtotal().Percent(pct), nil
	}
}

// Tiered gives more off as the cart grows — a strategy with data.
func Tiered(thresholds []Tier) Discount {
	return func(ctx context.Context, cart Cart) (money.Cents, error) {
		for _, t := range thresholds {
			if cart.Subtotal() >= t.Min {
				return t.Apply(cart), nil
			}
		}
		return 0, nil
	}
}

type Calculator struct {
	discount Discount
}

func NewCalculator(d Discount) *Calculator { return &Calculator{discount: d} }

func (c *Calculator) Total(ctx context.Context, cart Cart) (money.Cents, error) {
	d, err := c.discount(ctx, cart)
	if err != nil {
		return 0, fmt.Errorf("pricing: discount: %w", err)
	}
	return cart.Subtotal().Minus(d), nil
}
```

**Consequences.** No `Strategy` interface hierarchy, no subclassing — a
`func` value plus a closure for captured configuration. Swapping strategies
in tests is one line. This is the correct default answer to "how do I make
this algorithm pluggable?" in Go, and it subsumes what Java would express
with an interface + 3 implementations + a factory.

---

### 21. Template Method

**Verdict: rejected — replaced by "accept a function" (the
functional-decomposition inversion of control).**

The GoF version fixes the skeleton in a base class and lets subclasses fill
the steps. Go does it the other way round: the skeleton is a plain function
that *takes* the steps as parameters. The algorithm is the caller.

```go
// package report

// Steps are supplied by the caller; Export owns the skeleton.
type Steps struct {
	Open  func(ctx context.Context, dst io.Writer) error
	Write func(ctx context.Context, w io.Writer, rows []Row) error
	Close func(ctx context.Context) error
}

// Export is the template: fetch → open → write → close, with cleanup
// guaranteed. Callers implement only the steps.
func Export(ctx context.Context, dst io.Writer, rows []Row, s Steps) (err error) {
	if err := s.Open(ctx, dst); err != nil {
		return fmt.Errorf("report: open: %w", err)
	}
	defer func() {
		if cerr := s.Close(ctx); cerr != nil && err == nil {
			err = cerr // surface a close failure only if nothing else failed
		}
	}()

	if err := s.Write(ctx, dst, rows); err != nil {
		return fmt.Errorf("report: write: %w", err)
	}
	return nil
}
```

**Consequences.** The skeleton's error wrapping and `defer`-based cleanup are
written once and cannot be forgotten by a subclass — there are no subclasses.
Callers supply only the varying parts, so testing a step does not require
constructing an inheritance tree. We reject any base-struct-with-overridable-
methods approach: Go has no overriding, only shadowing, and shadowing a
method while embedding is a well-known source of surprising dispatch.

---

### 22. Visitor

**Verdict: rejected for open sets; adopted (rarely) for closed sets where
`switch` has grown unwieldy.**

Visitors need double dispatch, which Go lacks. Two idiomatic replacements:

**Preferred — a method on the element (closed type set):**

```go
// package ast

type Expr interface {
	Eval(env Env) (int, error)
	String() string
}

type Lit struct{ V int }
type Add struct{ L, R Expr }
type Var struct{ Name string }

func (l Lit) Eval(Env) (int, error) { return l.V, nil }
func (a Add) Eval(e Env) (int, error) {
	l, err := a.L.Eval(e)
	if err != nil {
		return 0, err
	}
	r, err := a.R.Eval(e)
	if err != nil {
		return 0, err
	}
	return l + r, nil
}
func (v Var) Eval(e Env) (int, error) { return e.Lookup(v.Name) }
```

**Alternative — type-switch visitor when you have many operations but few
element types:**

```go
// package ast

type Visitor interface {
	VisitLit(*Lit) error
	VisitAdd(*Add) error
	VisitVar(*Var) error
}

func Walk(v Visitor, n Expr) error {
	switch t := n.(type) {
	case *Lit:
		return v.VisitLit(t)
	case *Add:
		if err := Walk(v, t.L); err != nil {
			return err
		}
		return Walk(v, t.R)
	case *Var:
		return v.VisitVar(t)
	default:
		return fmt.Errorf("ast: unknown node %T", n)
	}
}
```

**Consequences.** The method form adds an operation by adding a method (easy)
but adding a *type* breaks every implementation (hard) — correct when the
grammar is closed, as ASTs usually are. The visitor form inverts that: adding
a type is one `case` in `Walk`, adding an operation is one new `Visitor`
implementation. We default to the method form; the `default:` arm in `Walk`
means an unhandled type fails loudly rather than silently — a strict
improvement over GoF visitors, where forgetting a case is a compile error but
the error message is unreadable.

---

## Part IV — Go-Native Patterns (no GoF equivalent)

These are adopted alongside the GoF set and are usually the *right* first
choice.

| # | Pattern | Shape | Example |
|---|---------|-------|---------|
| 23 | **Functional options** | `...Option` | see §4, Idiom A |
| 24 | **Pipeline / fan-out** | channels + `context` | `gen → sq → collect` |
| 25 | **Worker pool** | N goroutines over a jobs channel | bounded concurrency |
| 26 | **Context cancellation** | `context.WithCancel/Timeout` | first-error wins |
| 27 | **Error group** | `golang.org/x/sync/errgroup` | parallel tasks, `ctx` auto-cancel |
| 28 | **Option-struct config** | one exported struct, zero opts | simple constructors |

### 24. Pipeline

```go
// package nums

func Gen(ctx context.Context, in ...int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out)
		for _, v := range in {
			select {
			case <-ctx.Done():
				return
			case out <- v:
			}
		}
	}()
	return out
}

func Sq(ctx context.Context, in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out)
		for v := range in {
			select {
			case <-ctx.Done():
				return
			case out <- v*v:
			}
		}
	}()
	return out
}

// Usage:
//   ctx, cancel := context.WithTimeout(context.Background(), time.Second)
//   defer cancel()
//   for v := range nums.Sq(ctx, nums.Gen(ctx, 1, 2, 3)) { fmt.Println(v) }
```

### 25. Worker pool

```go
func Work(ctx context.Context, jobs <-chan Job, n int) *errgroup.Group {
	g, ctx := errgroup.WithContext(ctx)
	for i := 0; i < n; i++ {
		g.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case j, ok := <-jobs:
					if !ok {
						return nil
					}
					if err := j.Run(ctx); err != nil {
						return err // cancels siblings
					}
				}
			}
		})
	}
	return g
}
```

---

## House Conventions

1. **Interfaces are declared at the consumer**, named with a verb-phrase or
   capability (`io.Reader`, `storage.Store`), 1–3 methods. Add the compile-time
   check `var _ Iface = (*Impl)(nil)` next to the implementation.
2. **Return `error`, never panic**, except for unrecoverable programmer
   errors at init (duplicate registration, bad literal config).
3. **Wrap errors with `fmt.Errorf("...: %w", err)`** at every pattern
   boundary so `errors.Is`/`errors.As` keep working.
4. **Constructor is `NewX(...)` returning `(X, error)`**; functional options
   are `WithY(...) Option`. A `New` returning a bare pointer with no error is
   only acceptable when construction cannot fail.
5. **Zero value should be useful** wherever practical (`sync.Mutex`,
   `bytes.Buffer`); otherwise document that `New` is required.
6. **`go vet`, `staticcheck`, and `gofmt -l` must be clean**; run
   `go test -race ./...` for anything using goroutines or shared state —
   every pattern in §16, §18, §24, §25 is race-prone by construction.
7. **Prefer a `func` over an interface** whenever the behaviour is a single
   operation with no associated state (Strategy, Chain, Template Method).
8. **No `reflect`-based dispatch or deep-copy** in application code.
9. **Benchmarks before flyweights, caches, or object pools** (`go test -bench`
   plus `-benchmem`); most Go programs should let the GC do its job.

## Alternatives Considered

- **Adopt all 23 GoF patterns verbatim.** Rejected: requires class hierarchies
  Go does not have; produces the "translated Java" style that `staticcheck`
  flags and reviewers hate.
- **Adopt only the idioms and drop the GoF vocabulary entirely.** Rejected:
  the shared vocabulary is genuinely useful in design discussion and code
  review, and knowing *why* Visitor is a poor fit prevents it being
  re-invented badly.
- **Use a DI framework (Wire/Fx) for all object graphs.** Rejected for most
  services: constructor functions plus a small `main` are sufficient, and the
  plugin registry (§3) covers extensibility. Revisit if the graph exceeds
  ~40 constructors.

## Consequences

**Positive**

- One documented vocabulary; reviewers can name a pattern instead of
  describing it.
- Every adopted pattern is expressible in ~30 lines with no framework.
- Explicit rejection of Template Method, Iterator, and Visitor prevents the
  three most common Java-transplant mistakes.
- Functional options, pipelines, and errgroup cover the majority of real
  needs with less code than the GoF equivalents.

**Negative**

- Some teams will look for a `Mediator` class and find a channel instead;
  §16 must be linked from onboarding docs.
- The registry-based Abstract Factory relies on `init()` ordering and blank
  imports, which is a debugging hazard — mitigated by the duplicate-registration
  panic and by keeping registrations in one file per package.
- Channel-based observers (§18) drop messages under backpressure by default;
  every subscriber must decide explicitly whether drop or block is correct.

**Neutral / follow-ups**

- Add a `docs/examples/` directory with a runnable test per pattern.
- Revisit generics use in §1 (`Store`, `Intern`) if call sites multiply.
- Decide a house rule for when to reach for `errgroup` vs. hand-rolled
  `WaitGroup` + error channel.

## Implementation

Every pattern in this record has a working implementation in this module,
with a distinct domain per pattern and a race-checked test suite:

| Group | Package | Patterns |
|-------|---------|----------|
| Creational | `creational/` | singleton, factory, abstractfactory, builder, prototype |
| Structural | `structural/` | adapter, bridge, composite, decorator, facade, flyweight, proxy |
| Behavioral | `behavioral/` | chain, command, iterator, mediator, memento, observer, state, strategy, template, visitor |
| Concurrency | `concurrency/` | pipeline, workerpool, taskgroup, contextdemo |
| Idioms | `idioms/` | options, config |

Verify with:

```
make check          # gofmt + vet + go test -race
# or: gofmt -l . && go vet ./... && go test -race ./...
```

Each package's doc comment explains the pattern, the Go-specific
decision, and the failure mode it avoids; the test file names the property
being proven rather than restating the code.

## References

- Gamma, Helm, Johnson, Vlissides. *Design Patterns: Elements of Reusable
  Object-Oriented Software*. Addison-Wesley, 1994.
- Donovan & Kernighan. *The Go Programming Language*. Addison-Wesley, 2015.
- George Petrov, *Design Patterns in Go* (design-patterns.dev).
- *Go Code Review Comments* — https://go.dev/wiki/CodeReviewComments
- *Go Proverbs* — Rob Pike, https://go-proverbs.github.io/
- `iter.Seq` — Go 1.23 release notes, range-over-func.
