// Package singleton demonstrates the Singleton pattern in Go using sync.Once.
//
// A singleton is a type with exactly one instance per process. The Go
// implementation avoids the Java-style lazy nil-check (which races) by
// delegating first-call initialization to sync.Once: the fast path after the
// first call is a single atomic load, and the slow path runs exactly once
// even under concurrent callers.
//
// Use this only for genuinely process-wide, immutable-after-init resources.
// Global mutable singletons make tests order-dependent and parallel-unsafe.
package singleton

import (
	"sync"
	"sync/atomic"
	"time"
)

// Meter is a process-wide metrics registry. It is safe for concurrent use.
// Fields are unexported so the single instance cannot be mutated from
// outside the package after construction.
type Meter struct {
	started time.Time

	mu      sync.Mutex
	counts  map[string]uint64
	emitted atomic.Uint64 // number of Read calls, proves the instance is shared
}

var (
	once  sync.Once
	meter *Meter
)

// GetMeter returns the process-wide meter, creating it on the first call.
// Every subsequent call returns the identical pointer.
func GetMeter() *Meter {
	once.Do(func() {
		meter = &Meter{
			started: time.Now(),
			counts:  make(map[string]uint64),
		}
	})
	return meter
}

// Inc increments a named counter.
func (m *Meter) Inc(name string, delta uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[name] += delta
}

// Count returns the current value of a named counter (0 if never set).
func (m *Meter) Count(name string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[name]
}

// Uptime reports how long ago the singleton was created.
func (m *Meter) Uptime() time.Duration {
	return time.Since(m.started)
}

// Marks that a value came from the shared instance; used by tests to prove
// all callers observe the same object.
func (m *Meter) mark() { m.emitted.Add(1) }

func (m *Meter) marks() uint64 { return m.emitted.Load() }

// resetForTest tears the singleton down so each test starts fresh.
// White-box testing helper — lives in the same package as the singleton,
// which is precisely why singletons need in-package test hooks.
func resetForTest() {
	once = sync.Once{}
	meter = nil
}
