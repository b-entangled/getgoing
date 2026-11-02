// Package flyweight demonstrates the Flyweight pattern.
//
// A flyweight stores the *intrinsic* (shared, immutable) part of a large
// object once and lets many contexts supply only the *extrinsic* (per-use)
// state. The saving is memory: 10,000 sprites that share one texture pay for
// the pixels once.
//
// Go-specific notes:
//   - the pooled value must be documented immutable, because callers share
//     the pointer;
//   - a RWMutex keeps readers parallel and writers exclusive;
//   - a double-checked read under the write lock avoids a redundant store;
//   - the pool is unbounded by default, so a bounded variant is provided —
//     an unbounded intern pool is a memory leak dressed as an optimisation.
package flyweight

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// TextureData is the expensive intrinsic state: a decoded pixel buffer and
// its metadata. In a real engine this would be kilobytes to megabytes.
type TextureData struct {
	Name   string
	Width  int
	Height int
	Pixels []byte
}

// Bytes reports the intrinsic footprint.
func (t *TextureData) Bytes() int64 { return int64(len(t.Pixels)) }

// Sprite is the *extrinsic* per-instance state: where this particular
// occurrence sits. It holds a shared pointer to the intrinsic texture.
type Sprite struct {
	X, Y   float64
	Rotate float64
	Tex    *TextureData // SHARED — must never be mutated through this pointer
}

// Atlas interns textures so sprites share them.
type Atlas struct {
	mu   sync.RWMutex
	pool map[string]*TextureData
	hits atomic.Int64 // cache hits (shared, no lock)
	miss atomic.Int64 // cache misses (shared, no lock)
}

// NewAtlas returns an empty atlas.
func NewAtlas() *Atlas { return &Atlas{pool: make(map[string]*TextureData)} }

// Intern returns a shared texture for the key, building it on first use.
// The returned pointer is shared by every caller and MUST be treated as
// read-only. Concurrent Intern calls for distinct keys may build duplicate
// values; the map assignment is last-write-wins and both results are valid
// and equal in content (see docs on benign duplicate construction).
func (a *Atlas) Intern(key string, build func() *TextureData) (*TextureData, error) {
	// Fast path: read lock only, fully parallel.
	a.mu.RLock()
	t, ok := a.pool[key]
	a.mu.RUnlock()
	if ok {
		a.hits.Add(1)
		return t, nil
	}

	if build == nil {
		return nil, fmt.Errorf("flyweight: no builder for missing key %q", key)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Double-checked: another goroutine may have populated it while we
	// waited for the write lock.
	if t, ok := a.pool[key]; ok {
		a.hits.Add(1)
		return t, nil
	}

	t = build()
	if t == nil {
		return nil, fmt.Errorf("flyweight: builder returned nil for %q", key)
	}
	a.pool[key] = t
	a.miss.Add(1)
	return t, nil
}

// Stats reports hits, misses, and the number of distinct pooled textures.
func (a *Atlas) Stats() (hits, misses int64, pooled int) {
	a.mu.RLock()
	pooled = len(a.pool)
	a.mu.RUnlock()
	return a.hits.Load(), a.miss.Load(), pooled
}

// Pooled reports how many distinct textures are resident.
func (a *Atlas) Pooled() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.pool)
}

// Spawner creates sprites against an atlas — the client of the pattern.
type Spawner struct {
	atlas *Atlas
}

// NewSpawner wires a spawner to an atlas.
func NewSpawner(a *Atlas) *Spawner { return &Spawner{atlas: a} }

// Spawn places a sprite with the given texture key at (x, y).
// Only extrinsic state differs between two spawns of the same texture.
func (s *Spawner) Spawn(key string, build func() *TextureData, x, y float64) (*Sprite, error) {
	tex, err := s.atlas.Intern(key, build)
	if err != nil {
		return nil, err
	}
	return &Sprite{X: x, Y: y, Tex: tex}, nil
}

// =====================================================================
// Bounded variant: an LRU-capped intern pool
// =====================================================================

// BoundedAtlas is an atlas that evicts the least-recently-used texture once
// it exceeds capacity. Eviction means a later Intern rebuilds — a deliberate
// trade of CPU for a hard memory ceiling.
type BoundedAtlas struct {
	mu     sync.Mutex
	cap    int
	order  []string // oldest at index 0
	pool   map[string]*TextureData
	builds atomic.Int64
}

// NewBoundedAtlas creates an atlas holding at most `capacity` textures.
func NewBoundedAtlas(capacity int) *BoundedAtlas {
	if capacity < 1 {
		capacity = 1
	}
	return &BoundedAtlas{
		cap:  capacity,
		pool: make(map[string]*TextureData, capacity),
	}
}

// Intern returns a shared texture, evicting the LRU entry when full.
func (b *BoundedAtlas) Intern(key string, build func() *TextureData) (*TextureData, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if t, ok := b.pool[key]; ok {
		b.touch(key)
		return t, nil
	}
	if build == nil {
		return nil, fmt.Errorf("flyweight: no builder for missing key %q", key)
	}
	t := build()
	if t == nil {
		return nil, fmt.Errorf("flyweight: builder returned nil for %q", key)
	}

	// Evict until there is room (order may already be at capacity).
	for len(b.pool) >= b.cap {
		oldest := b.order[0]
		b.order = b.order[1:]
		delete(b.pool, oldest)
	}

	b.pool[key] = t
	b.order = append(b.order, key)
	b.builds.Add(1)
	return t, nil
}

// touch moves a key to the most-recently-used position. Caller holds b.mu.
func (b *BoundedAtlas) touch(key string) {
	for i, k := range b.order {
		if k == key {
			b.order = append(b.order[:i], b.order[i+1:]...)
			b.order = append(b.order, key)
			return
		}
	}
}

// Pooled reports the number of resident textures (never exceeds capacity).
func (b *BoundedAtlas) Pooled() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pool)
}

// Builds reports how many times a texture had to be constructed — rises
// after eviction, which is the cost of the memory ceiling.
func (b *BoundedAtlas) Builds() int64 { return b.builds.Load() }
