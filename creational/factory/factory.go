// Package factory demonstrates the Factory Method pattern.
//
// Go has no virtual constructor, so the factory is a plain function that
// selects a concrete type from its input and returns the *narrow* interface
// the caller actually needs. The concrete types stay unexported, so callers
// cannot depend on them even by accident.
//
// The second half of the file shows the plugin variant: drivers register
// themselves, so adding a backend does not modify this file (Open/Closed).
package factory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Store is the consumer-side interface: only what callers need.
type Store interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Name() string
}

// ErrUnknownDriver is returned for unrecognised driver names. Callers match
// it with errors.Is.
var ErrUnknownDriver = errors.New("factory: unknown driver")

// --- concrete type A: in-memory ---

type memoryStore struct {
	data map[string][]byte
	mu   sync.RWMutex
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: make(map[string][]byte)}
}

func (m *memoryStore) Name() string { return "memory" }

func (m *memoryStore) Put(_ context.Context, k string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(v))
	copy(cp, v)
	m.data[k] = cp
	return nil
}

func (m *memoryStore) Get(_ context.Context, k string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[k]
	if !ok {
		return nil, fmt.Errorf("factory: key %q not found", k)
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

// --- concrete type B: null (discards writes, returns empty reads) ---

type nullStore struct{}

func (nullStore) Name() string { return "null" }

func (nullStore) Put(context.Context, string, []byte) error { return nil }

func (nullStore) Get(context.Context, string) ([]byte, error) { return nil, nil }

// --- the factory method ---

// NewStore selects a concrete Store implementation from the driver name.
// This is the factory method: callers receive the interface, never the type.
func NewStore(driver string) (Store, error) {
	switch strings.ToLower(driver) {
	case "memory", "":
		return newMemoryStore(), nil
	case "null":
		return nullStore{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, driver)
	}
}

// --- plugin variant: drivers register constructors at init time ---

var (
	regMu    sync.RWMutex
	registry = map[string]func() (Store, error){}
)

// Register installs a driver constructor. It panics on a duplicate name:
// a duplicate registration is a programming error that must fail at start-up,
// not silently win a race at request time.
func Register(name string, ctor func() (Store, error)) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("factory: duplicate driver registration: " + name)
	}
	registry[name] = ctor
}

// Open resolves a driver from the registry. Drivers are contributed by
// separate packages via a blank import, so adding a backend never edits
// this file.
func Open(name string) (Store, error) {
	regMu.RLock()
	ctor, ok := registry[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, name)
	}
	return ctor()
}

// Drivers lists the registered driver names, sorted by the caller if needed.
func Drivers() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

func init() {
	Register("builtin-memory", func() (Store, error) { return newMemoryStore(), nil })
	Register("builtin-null", func() (Store, error) { return nullStore{}, nil })
}
