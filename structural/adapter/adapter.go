// Package adapter demonstrates the Adapter pattern.
//
// The interface is declared on the *consumer* side (here), next to the code
// that uses it — not by the provider. That is the whole reason Go's
// structural typing makes adapters trivial: a wrapper type with the right
// method set satisfies the interface with no registration and no codegen.
//
// Two adapters are shown over two unrelated adaptees, which is the point of
// the pattern: unrelated types converge on one interface.
package adapter

import (
	"context"
	"errors"

	"github.com/b-entangled/projects/learnings/structural/adapter/legacy"
)

// Store is what this package needs. Declared here, not by the provider.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
}

// ErrNotFound is returned by every adapter for a missing key, so callers
// have a single sentinel to match regardless of the underlying type.
var ErrNotFound = errors.New("adapter: key not found")

// --- adaptee A: the awkward legacy store ---

// legacyStoreAdapter wraps legacy.EntryStore so it satisfies Store.
type legacyStoreAdapter struct {
	src *legacy.EntryStore
}

// FromLegacy adapts a legacy.EntryStore to Store.
func FromLegacy(src *legacy.EntryStore) Store {
	return legacyStoreAdapter{src: src}
}

func (a legacyStoreAdapter) Get(_ context.Context, key string) ([]byte, error) {
	e, ok := a.src.Load(key)
	if !ok {
		return nil, ErrNotFound
	}
	return e.Payload, nil
}

func (a legacyStoreAdapter) Put(_ context.Context, key string, value []byte) error {
	a.src.Save(key, legacy.Entry{Payload: value})
	return nil
}

// Compile-time proof: if the adapter drifts, this line fails to build.
var _ Store = legacyStoreAdapter{}

// --- adaptee B: a plain map owned by someone else ---

type mapStoreAdapter struct {
	m map[string][]byte
}

// FromMap adapts a plain map to Store. The map is borrowed, not copied:
// reads and writes are visible on both sides, which is usually what a
// caller passing a map intends.
func FromMap(m map[string][]byte) Store {
	if m == nil {
		m = make(map[string][]byte)
	}
	return mapStoreAdapter{m: m}
}

func (a mapStoreAdapter) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := a.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (a mapStoreAdapter) Put(_ context.Context, key string, value []byte) error {
	cp := make([]byte, len(value))
	copy(cp, value)
	a.m[key] = cp
	return nil
}

var _ Store = mapStoreAdapter{}

// --- the consumer, written once against Store ---

// Cache exercises any Store. It knows nothing about legacy or maps.
type Cache struct {
	store Store
}

// NewCache wraps a Store in a cache façade with a small in-front index.
func NewCache(s Store) *Cache { return &Cache{store: s} }

// Get returns the value, translating the sentinel into a wrapped error that
// still matches errors.Is.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := c.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Put stores a value.
func (c *Cache) Put(ctx context.Context, key string, value []byte) error {
	return c.store.Put(ctx, key, value)
}
