// Package decorator demonstrates the Decorator pattern.
//
// A decorator wraps a value of the same interface and adds behaviour while
// delegating. Two idioms are shown because Go has two:
//
//   - struct wrapper: when the decorated thing is a named type with several
//     methods (the wrapper implements the whole interface);
//   - higher-order function: when the unit of work is a single func, which
//     covers the entire middleware convention.
//
// Contrast with proxy (package proxy): a decorator *adds* behaviour to every
// call; a proxy *controls access* and may short-circuit a call entirely.
package decorator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// =====================================================================
// Idiom A: struct wrapper
// =====================================================================

// KeyValue is the interface being decorated.
type KeyValue interface {
	Get(ctx context.Context, key string) (string, error)
	Put(ctx context.Context, key, value string) error
}

// ErrMiss is returned by the base store for a missing key.
var ErrMiss = errors.New("decorator: key not found")

// MapStore is the base implementation (the Component being wrapped).
type MapStore struct{ data map[string]string }

// NewMapStore builds an empty base store.
func NewMapStore() *MapStore { return &MapStore{data: make(map[string]string)} }

// Get reads a key.
func (m *MapStore) Get(_ context.Context, key string) (string, error) {
	v, ok := m.data[key]
	if !ok {
		return "", ErrMiss
	}
	return v, nil
}

// Put writes a key.
func (m *MapStore) Put(_ context.Context, key, value string) error {
	m.data[key] = value
	return nil
}

var _ KeyValue = (*MapStore)(nil)

// --- decorator 1: metrics/logging ---

// loggingKV wraps a KeyValue and records every operation.
type loggingKV struct {
	inner KeyValue
	logs  *[]string // shared sink so tests can observe
}

// WithLogging adds operation logging. The sink pointer lets a caller choose
// where logs go; pass nil to discard.
func WithLogging(inner KeyValue, sink *[]string) KeyValue {
	return &loggingKV{inner: inner, logs: sink}
}

func (l *loggingKV) Get(ctx context.Context, key string) (string, error) {
	v, err := l.inner.Get(ctx, key)
	l.record(fmt.Sprintf("GET %s ok=%t", key, err == nil))
	return v, err
}

func (l *loggingKV) Put(ctx context.Context, key, value string) error {
	err := l.inner.Put(ctx, key, value)
	l.record(fmt.Sprintf("PUT %s len=%d ok=%t", key, len(value), err == nil))
	return err
}

func (l *loggingKV) record(line string) {
	if l.logs != nil {
		*l.logs = append(*l.logs, line)
	}
}

var _ KeyValue = (*loggingKV)(nil)

// --- decorator 2: TTL ---

// ttlKV wraps a KeyValue and expires entries. It embeds *no* interface, so
// every method is written out — this keeps delegation explicit.
type ttlKV struct {
	inner KeyValue
	now   func() time.Time
	ttl   time.Duration
	born  map[string]time.Time
}

// WithTTL gives every subsequent Put a time-to-live.
func WithTTL(inner KeyValue, ttl time.Duration) KeyValue {
	return &ttlKV{
		inner: inner,
		now:   time.Now,
		ttl:   ttl,
		born:  make(map[string]time.Time),
	}
}

// Get honours the TTL: expired keys behave as missing.
func (t *ttlKV) Get(ctx context.Context, key string) (string, error) {
	if b, ok := t.born[key]; ok && t.now().Sub(b) > t.ttl {
		return "", ErrMiss
	}
	return t.inner.Get(ctx, key)
}

// Put stamps the birth time.
func (t *ttlKV) Put(ctx context.Context, key, value string) error {
	if err := t.inner.Put(ctx, key, value); err != nil {
		return err
	}
	t.born[key] = t.now()
	return nil
}

// setClock is a test hook for injecting a fake clock.
func (t *ttlKV) setClock(now func() time.Time) { t.now = now }

var _ KeyValue = (*ttlKV)(nil)

// --- decorator 3: retry ---

type retryKV struct {
	inner    KeyValue
	attempts int
	wait     time.Duration
}

// WithRetry re-attempts failing Gets up to n times.
func WithRetry(inner KeyValue, attempts int, wait time.Duration) KeyValue {
	if attempts < 1 {
		attempts = 1
	}
	return &retryKV{inner: inner, attempts: attempts, wait: wait}
}

func (r *retryKV) Get(ctx context.Context, key string) (string, error) {
	var err error
	for i := 0; i < r.attempts; i++ {
		var v string
		if v, err = r.inner.Get(ctx, key); err == nil {
			return v, nil
		}
		if errors.Is(err, ErrMiss) {
			return "", err // a miss is not transient; do not retry
		}
		if r.wait > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(r.wait):
			}
		}
	}
	return "", err
}

func (r *retryKV) Put(ctx context.Context, key, value string) error {
	return r.inner.Put(ctx, key, value)
}

var _ KeyValue = (*retryKV)(nil)

// Wrap is a convenience that applies several decorators innermost-first.
// Usage: Wrap(base, WithLogging, WithTTL) — read it like a Russian doll.
func Wrap(inner KeyValue, decorators ...func(KeyValue) KeyValue) KeyValue {
	for i := len(decorators) - 1; i >= 0; i-- {
		inner = decorators[i](inner)
	}
	return inner
}

// =====================================================================
// Idiom B: function decorator (middleware)
// =====================================================================

// Handler is one unit of request work.
type Handler func(ctx context.Context, req string) (string, error)

// Middleware is a function decorator over Handler.
type Middleware func(Handler) Handler

// Chain applies middlewares so that the first argument runs first and each
// one wraps everything after it.
func Chain(h Handler, mw ...Middleware) Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// Recover turns a panic into an error. Nothing is lost to the runtime.
func Recover(next Handler) Handler {
	return func(ctx context.Context, req string) (resp string, err error) {
		defer func() {
			if r := recover(); r != nil {
				resp = ""
				err = fmt.Errorf("decorator: panic recovered: %v", r)
			}
		}()
		return next(ctx, req)
	}
}

// Timeout enforces a deadline, returning ctx.Err() on expiry.
func Timeout(d time.Duration) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return next(ctx, req)
		}
	}
}

// Upper transforms the response — a pure output decorator.
func Upper(next Handler) Handler {
	return func(ctx context.Context, req string) (string, error) {
		out, err := next(ctx, req)
		return strings.ToUpper(out), err
	}
}

// Prefix tags the request before delegating — a pure input decorator.
func Prefix(p string) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req string) (string, error) {
			return next(ctx, p+req)
		}
	}
}
