package map_

import (
	"fmt"
	"strings"
	"testing"
)

// Reading a nil map is defined behaviour: zero value, ok=false, len 0.
func TestNilMapIsReadable(t *testing.T) {
	var m map[string]int

	v, ok := m["missing"]
	if v != 0 || ok {
		t.Fatalf("nil map read = (%d, %v), want (0, false)", v, ok)
	}
	if len(m) != 0 {
		t.Fatalf("len(nil) = %d", len(m))
	}
	for range m {
		t.Fatal("range over nil map must not iterate")
	}
}

// Writing a nil map panics with a runtime error — caught here so the
// behaviour is documented rather than discovered in production.
func TestNilMapWritePanics(t *testing.T) {
	var m map[string]int

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("write to nil map must panic")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "nil map") {
			t.Fatalf("panic = %v, want a nil map error", r)
		}
	}()

	m["x"] = 1
}

// Iteration covers every key exactly once — even after growing the map
// through several rehashes.
func TestRangeVisitsEveryKeyExactlyOnce(t *testing.T) {
	const n = 1000
	m := New(n / 4) // force growth beyond the initial hint
	Fill(m, n)

	seen := make(map[string]int, n)
	for k := range m {
		seen[k]++
	}
	if len(seen) != n {
		t.Fatalf("range produced %d distinct keys, want %d", len(seen), n)
	}
	for k, c := range seen {
		if c != 1 {
			t.Fatalf("key %q visited %d times, want 1", k, c)
		}
	}
}

// The implementation randomises where iteration starts: the same map
// ranges in different orders across runs. (The spec promises no order at
// all; this test pins the observable consequence so nobody "fixes" code
// that happens to work today.)
func TestRangeOrderIsRandomised(t *testing.T) {
	const (
		runs = 50
		keys = 8
	)
	orders := map[string]int{}
	for r := 0; r < runs; r++ {
		m := make(map[int]string, keys)
		for i := 1; i <= keys; i++ {
			m[i] = "v"
		}
		var b strings.Builder
		for k := range m {
			b.WriteByte(byte('0' + k))
		}
		orders[b.String()]++
	}
	if len(orders) < 2 {
		t.Fatalf("50 range runs produced %d distinct orders, want at least 2", len(orders))
	}
}

// Spec guarantee: entries removed before they are reached are never
// produced. Deleting everything on the first visit must leave exactly
// that one key visited.
func TestEntriesDeletedDuringIterationAreNotProduced(t *testing.T) {
	const n = 100
	m := New(n)
	Fill(m, n)

	visited := 0
	for range m {
		visited++
		for k := range m {
			delete(m, k)
		}
	}
	if visited != 1 {
		t.Fatalf("visited %d keys after deleting all on first visit, want 1", visited)
	}
	if len(m) != 0 {
		t.Fatalf("map still holds %d keys", len(m))
	}
}

// Sorted iteration is the pattern that makes maps deterministic.
func TestKeysSortedIsDeterministic(t *testing.T) {
	m := New(3)
	m["b"] = 2
	m["a"] = 1
	m["c"] = 3

	got := KeysSorted(m)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted keys = %v, want %v", got, want)
		}
	}
}
