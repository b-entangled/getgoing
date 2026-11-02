// Package lru implements a fixed-capacity, optionally time-expiring
// cache with least-recently-used eviction — the memory bound every
// caching system design starts from (application caches, CDN edges,
// database buffer pools).
//
// Design decisions made explicit here:
//
//   - Capacity is a hard bound: the cache never holds more than
//     capacity entries, because every held entry is memory someone
//     else cannot use.
//   - Reads refresh recency: an entry accessed after a newer one is
//     no longer the eviction candidate. Recency, not age.
//   - Expiry is lazy: an entry past its TTL is invisible on Get and is
//     reclaimed on Get or Purge. Lazy expiry trades a little staleness
//     risk against the need for a background sweeper goroutine.
//   - Time comes from a clock.Clock, so TTL behaviour is tested by
//     advancing time, never by sleeping.
package lru

import (
	"container/list"
	"sync"
	"time"

	"github.com/b-entangled/projects/learnings/sdesign/clock"
)

type entry[K comparable, V any] struct {
	key     K
	value   V
	expires time.Time // zero means no expiry
}

// Cache is a concurrency-safe LRU cache keyed by K.
type Cache[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	clk      clock.Clock
	items    map[K]*list.Element
	order    *list.List // front = most recently used
}

// New returns a cache holding at most capacity entries. ttl > 0 stamps
// entries on write so they expire that long after insertion (updates
// restamp). clk may be nil, meaning the real clock.
func New[K comparable, V any](capacity int, ttl time.Duration, clk clock.Clock) *Cache[K, V] {
	if capacity < 1 {
		panic("lru: capacity must be >= 1")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Cache[K, V]{
		capacity: capacity,
		ttl:      ttl,
		clk:      clk,
		items:    make(map[K]*list.Element, capacity),
		order:    list.New(),
	}
}

// Get returns the value for key and marks it most recently used.
// Expired or missing keys report ok=false.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	if c.expired(e) {
		c.removeElement(el)
		var zero V
		return zero, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

// Put inserts or updates key. Updates refresh both the value and the
// recency order; inserting past capacity evicts the least recently
// used entry first.
func (c *Cache[K, V]) Put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry[K, V])
		e.value = value
		e.expires = c.deadline()
		c.order.MoveToFront(el)
		return
	}

	el := c.order.PushFront(&entry[K, V]{key: key, value: value, expires: c.deadline()})
	c.items[key] = el

	for c.order.Len() > c.capacity {
		c.removeElement(c.order.Back())
	}
}

// Delete removes key, reporting whether it was present. This is where
// a distributed invalidation message lands: another node says "this key
// is stale now", and the local copy goes — see DESIGNS.md.
func (c *Cache[K, V]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return false
	}
	c.removeElement(el)
	return true
}

// Purge drops every expired entry and returns how many were removed.
func (c *Cache[K, V]) Purge() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0
	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if c.expired(el.Value.(*entry[K, V])) {
			c.removeElement(el)
			n++
		}
		el = next
	}
	return n
}

// Len reports the number of live entries. Expired entries not yet
// visited still count — call Purge for an exact live count.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *Cache[K, V]) expired(e *entry[K, V]) bool {
	return !e.expires.IsZero() && !c.clk.Now().Before(e.expires)
}

func (c *Cache[K, V]) deadline() time.Time {
	if c.ttl <= 0 {
		return time.Time{}
	}
	return c.clk.Now().Add(c.ttl)
}

func (c *Cache[K, V]) removeElement(el *list.Element) {
	if el == nil {
		return
	}
	e := el.Value.(*entry[K, V])
	c.order.Remove(el)
	delete(c.items, e.key)
}
