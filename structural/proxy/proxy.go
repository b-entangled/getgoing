// Package proxy demonstrates the Proxy pattern.
//
// A proxy has the same shape as a decorator — a wrapper implementing the
// wrapped interface — but a different intent: it *controls access* to the
// subject and may refuse, delay, short-circuit, or fall back, rather than
// simply adding behaviour to every call.
//
// Three proxies are shown over one subject:
//   - caching   (short-circuits reads)
//   - authorise (refuses some calls)
//   - circuit   (fails fast while the subject is unhealthy)
//
// They compose, because each only knows the interface.
package proxy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// User is the domain record.
type User struct {
	ID    string
	Name  string
	Admin bool
}

// Directory is the real subject's interface.
type Directory interface {
	Lookup(ctx context.Context, id string) (User, error)
	Delete(ctx context.Context, id string) error
}

// ErrNotFound is the subject's missing-key sentinel.
var ErrNotFound = errors.New("proxy: user not found")

// ErrForbidden is returned by the authorisation proxy.
var ErrForbidden = errors.New("proxy: forbidden")

// ErrCircuitOpen is returned while the circuit breaker is refusing calls.
var ErrCircuitOpen = errors.New("proxy: circuit open")

// --- real subject ---

// UserDirectory is the expensive/remote thing we are protecting.
// It counts calls so tests can prove proxies actually short-circuit.
type UserDirectory struct {
	mu    sync.RWMutex
	users map[string]User
	calls atomic.Int64 // total Lookup calls reaching the subject
	fail  atomic.Bool  // when true, every call errors (simulated outage)
}

// NewUserDirectory builds a directory seeded with the given users.
func NewUserDirectory(seed ...User) *UserDirectory {
	d := &UserDirectory{users: make(map[string]User, len(seed))}
	for _, u := range seed {
		d.users[u.ID] = u
	}
	return d
}

// Lookup reads a user and increments the call counter.
func (d *UserDirectory) Lookup(ctx context.Context, id string) (User, error) {
	d.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	if d.fail.Load() {
		return User{}, errors.New("proxy: subject unavailable")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	u, ok := d.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

// Delete removes a user.
func (d *UserDirectory) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.fail.Load() {
		return errors.New("proxy: subject unavailable")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.users, id)
	return nil
}

// Calls reports how many Lookup calls reached the subject.
func (d *UserDirectory) Calls() int64 { return d.calls.Load() }

// setFail simulates an outage for the circuit-breaker tests.
func (d *UserDirectory) setFail(v bool) { d.fail.Store(v) }

var _ Directory = (*UserDirectory)(nil)

// =====================================================================
// Proxy 1: caching (short-circuits)
// =====================================================================

// Cache is a write-through caching proxy over a Directory.
type Cache struct {
	inner  Directory
	mu     sync.RWMutex
	data   map[string]User
	hits   atomic.Int64
	misses atomic.Int64
}

// WithCache wraps a Directory in a cache. Writes invalidate the entry so
// the proxy never serves a stale read after a delete.
func WithCache(inner Directory) *Cache {
	return &Cache{inner: inner, data: make(map[string]User)}
}

// Lookup returns from memory when possible without touching the subject.
func (c *Cache) Lookup(ctx context.Context, id string) (User, error) {
	c.mu.RLock()
	u, ok := c.data[id]
	c.mu.RUnlock()
	if ok {
		c.hits.Add(1)
		return u, nil
	}
	c.misses.Add(1)

	u, err := c.inner.Lookup(ctx, id)
	if err != nil {
		return User{}, err
	}
	c.mu.Lock()
	c.data[id] = u
	c.mu.Unlock()
	return u, nil
}

// Delete forwards, then evicts.
func (c *Cache) Delete(ctx context.Context, id string) error {
	if err := c.inner.Delete(ctx, id); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.data, id)
	c.mu.Unlock()
	return nil
}

// Stats reports cache hits and misses.
func (c *Cache) Stats() (hits, misses int64) { return c.hits.Load(), c.misses.Load() }

var _ Directory = (*Cache)(nil)

// =====================================================================
// Proxy 2: authorisation (refuses)
// =====================================================================

// Authoriser refuses operations based on the caller's role. It never
// forwards a forbidden request, which is the distinction from a decorator.
type Authoriser struct {
	inner Directory
}

// WithAuth wraps a Directory with role checks.
func WithAuth(inner Directory) *Authoriser { return &Authoriser{inner: inner} }

// Lookup permits any caller (reads are public within the service).
func (a *Authoriser) Lookup(ctx context.Context, id string) (User, error) {
	return a.inner.Lookup(ctx, id)
}

// Delete requires the context to carry an admin principal.
func (a *Authoriser) Delete(ctx context.Context, id string) error {
	if !IsAdmin(ctx) {
		return ErrForbidden // the subject is never called
	}
	return a.inner.Delete(ctx, id)
}

var _ Directory = (*Authoriser)(nil)

// principalKey is the context key for the admin flag.
type ctxKey int

const adminKey ctxKey = 1

// WithAdmin returns a context marking the caller as an admin.
func WithAdmin(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminKey, true)
}

// IsAdmin reports whether the context carries an admin principal.
func IsAdmin(ctx context.Context) bool {
	v, _ := ctx.Value(adminKey).(bool)
	return v
}

// =====================================================================
// Proxy 3: circuit breaker (fails fast)
// =====================================================================

// State is the breaker's mode.
type State int

const (
	// Closed passes traffic normally.
	Closed State = iota
	// Open fails fast without calling the subject.
	Open
	// HalfOpen admits a limited number of trial calls.
	HalfOpen
)

// String renders the state.
func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Breaker fails fast after the subject has failed repeatedly, then probes
// for recovery. It is a proxy because it decides whether the call happens
// at all.
type Breaker struct {
	inner      Directory
	threshold  int
	resetAfter time.Duration

	mu           sync.Mutex
	state        State
	failures     int
	openedAt     time.Time
	successProbe bool
}

// WithBreaker wraps a Directory. After `threshold` consecutive failures the
// breaker opens for `resetAfter`, then half-opens for a single trial call.
func WithBreaker(inner Directory, threshold int, resetAfter time.Duration) *Breaker {
	if threshold < 1 {
		threshold = 1
	}
	return &Breaker{
		inner:      inner,
		threshold:  threshold,
		resetAfter: resetAfter,
		state:      Closed,
	}
}

// State returns the current mode, applying any pending time-based transition.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()
	return b.state
}

// maybeHalfOpenLocked performs the open→half-open transition. Caller holds b.mu.
func (b *Breaker) maybeHalfOpenLocked() {
	if b.state == Open && time.Since(b.openedAt) >= b.resetAfter {
		b.state = HalfOpen
		b.successProbe = false
	}
}

// guard decides whether to call through. Returns an error to fail fast.
func (b *Breaker) guard() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()

	switch b.state {
	case Open:
		return ErrCircuitOpen
	case HalfOpen:
		if b.successProbe {
			return ErrCircuitOpen // only one trial in flight at a time
		}
		b.successProbe = true
		return nil
	default:
		return nil
	}
}

// record notes the outcome of a call that was allowed through.
func (b *Breaker) record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		if b.state == HalfOpen {
			b.state = Closed // recovered
		}
		b.failures = 0
		return
	}
	b.failures++
	if b.state == HalfOpen || b.failures >= b.threshold {
		b.state = Open
		b.openedAt = time.Now()
		b.successProbe = false
	}
}

// Lookup consults the breaker before forwarding.
func (b *Breaker) Lookup(ctx context.Context, id string) (User, error) {
	if err := b.guard(); err != nil {
		return User{}, err
	}
	u, err := b.inner.Lookup(ctx, id)
	b.record(err == nil)
	return u, err
}

// Delete consults the breaker before forwarding.
func (b *Breaker) Delete(ctx context.Context, id string) error {
	if err := b.guard(); err != nil {
		return err
	}
	err := b.inner.Delete(ctx, id)
	b.record(err == nil)
	return err
}

var _ Directory = (*Breaker)(nil)

// =====================================================================
// Composition: all three at once
// =====================================================================

// Protect is the usual production wiring: auth first (cheapest rejection),
// then breaker (fail fast), then cache (short-circuit), then the subject.
// Order matters: checking auth before the breaker means an anonymous caller
// cannot trip the breaker.
func Protect(inner Directory) Directory {
	return WithAuth(WithBreaker(WithCache(inner), 3, 50*time.Millisecond))
}
