package lru

import (
	"sync"
	"testing"
	"time"

	"github.com/b-entangled/projects/learnings/sdesign/clock"
)

// The defining property: inserting past capacity drops the least
// recently used entry, not the oldest one.
func TestEvictsLeastRecentlyUsedNotOldest(t *testing.T) {
	c := New[string, int](2, 0, nil)
	c.Put("a", 1)
	c.Put("b", 2)

	if _, ok := c.Get("a"); !ok { // a becomes most recently used
		t.Fatal("a missing before eviction")
	}
	c.Put("c", 3) // must evict b

	if _, ok := c.Get("b"); ok {
		t.Fatal("b survived: LRU (not MRU) must be evicted")
	}
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("a = (%v, %v), want (1, true)", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Fatalf("c = (%v, %v), want (3, true)", v, ok)
	}
}

// A read moves an entry to the head — reads are what keep it alive.
func TestGetRefreshesRecency(t *testing.T) {
	c := New[string, int](2, 0, nil)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a")
	c.Put("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted after a was refreshed")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should have survived")
	}
}

// Updating an existing key occupies no new capacity.
func TestUpdateInPlaceDoesNotEvict(t *testing.T) {
	c := New[string, int](2, 0, nil)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 10)

	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	if v, _ := c.Get("a"); v != 10 {
		t.Fatalf("a = %d, want 10", v)
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b evicted by an update — must not happen")
	}
}

// Expiry is driven by the injected clock: no sleeping, no flakes.
func TestTTLExpiresEntriesWhenTimeAdvances(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	c := New[string, int](10, time.Minute, clk)

	c.Put("a", 1)

	clk.Advance(30 * time.Second)
	c.Put("b", 2) // b stamped at t=30s, expires at t=90s

	clk.Advance(31 * time.Second) // t=61s: a (t=0) dead, b (t=30) alive
	if _, ok := c.Get("a"); ok {
		t.Fatal("a survived past its TTL")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b expired before its TTL")
	}

	clk.Advance(30 * time.Second) // t=91s: b dead
	if _, ok := c.Get("b"); ok {
		t.Fatal("b survived past its TTL")
	}
}

// Purge reclaims expired entries in bulk without touching live ones.
func TestPurgeRemovesOnlyExpiredEntries(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	c := New[string, int](10, time.Minute, clk)

	c.Put("old1", 1)
	c.Put("old2", 2)
	clk.Advance(30 * time.Second)
	c.Put("young", 3)
	clk.Advance(40 * time.Second) // old* at 70s (dead), young at 40s (alive)

	if n := c.Purge(); n != 2 {
		t.Fatalf("Purge removed %d, want 2", n)
	}
	if _, ok := c.Get("young"); !ok {
		t.Fatal("Purge removed a live entry")
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d after purge, want 1", c.Len())
	}
}

// Concurrent readers and writers must not race (verified by -race)
// and must never exceed capacity.
func TestConcurrentAccessKeepsCapacityBound(t *testing.T) {
	c := New[int, int](8, 0, nil)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				k := (g*200 + i) % 32
				c.Put(k, i)
				c.Get(k)
			}
		}(g)
	}
	wg.Wait()

	if c.Len() > 8 {
		t.Fatalf("Len = %d, capacity 8: bound violated", c.Len())
	}
}

// Invalidation by explicit key: Delete drops exactly that entry and
// nothing else, and reports presence honestly (false the second time).
func TestDeleteRemovesSpecificKey(t *testing.T) {
	clk := clock.NewManual(time.Now())
	c := New[string, int](3, 0, clk)
	c.Put("a", 1)
	c.Put("b", 2)

	if !c.Delete("a") {
		t.Fatal("first Delete(a) = false, want true")
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("a is still present after Delete")
	}
	if c.Delete("a") {
		t.Fatal("second Delete(a) = true, want false (already gone)")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b must be untouched by Delete(a)")
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
}
