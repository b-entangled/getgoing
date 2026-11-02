package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type res struct{ id int }

// The point of a pool: the second Get reuses what the first Put
// returned — the factory runs once.
func TestGetReusesReturnedResources(t *testing.T) {
	var created atomic.Int64
	p := New(Config[*res]{
		MaxOpen: 2,
		Factory: func(context.Context) (*res, error) {
			return &res{id: int(created.Add(1))}, nil
		},
	})
	defer p.Close()
	ctx := context.Background()

	a, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Put(a)
	b, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Put(b)

	if a != b {
		t.Fatal("Get returned a different instance: resource was not reused")
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("factory ran %d times, want 1", got)
	}
}

// Past MaxOpen, Get waits instead of creating — and gives up with the
// caller's context error rather than waiting forever.
func TestGetBlocksWhenExhaustedThenHonoursContext(t *testing.T) {
	p := New(Config[*res]{
		MaxOpen: 1,
		Factory: func(context.Context) (*res, error) { return &res{}, nil },
	})
	defer p.Close()

	held, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.Get(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get on an exhausted pool = %v, want DeadlineExceeded", err)
	}

	// Returning the resource unblocks a fresh Get — same instance.
	p.Put(held)
	got, err := p.Get(context.Background())
	if err != nil || got != held {
		t.Fatalf("Get after Put = (%v, %v), want the held instance", got, err)
	}
}

// A factory failure releases the reservation: the pool does not
// permanently shrink by one slot after one bad call.
func TestFactoryFailureDoesNotWedgeThePool(t *testing.T) {
	var calls atomic.Int64
	p := New(Config[*res]{
		MaxOpen: 1,
		Factory: func(context.Context) (*res, error) {
			if calls.Add(1) == 1 {
				return nil, errors.New("dial failed")
			}
			return &res{}, nil
		},
	})
	defer p.Close()

	if _, err := p.Get(context.Background()); err == nil {
		t.Fatal("first Get should have failed")
	}
	if got := p.Created(); got != 0 {
		t.Fatalf("Created = %d after factory failure, want 0 (slot released)", got)
	}
	if _, err := p.Get(context.Background()); err != nil {
		t.Fatalf("second Get = %v: the pool wedged", err)
	}
}

// Resources past MaxIdle are destroyed on return, not hoarded —
// idle memory is a bounded decision too.
func TestExcessReturnsAreDestroyed(t *testing.T) {
	var alive, destroyed atomic.Int64
	p := New(Config[*res]{
		MaxOpen: 3,
		MaxIdle: 1,
		Factory: func(context.Context) (*res, error) {
			alive.Add(1)
			return &res{}, nil
		},
		Destroy: func(*res) { destroyed.Add(1) },
	})
	defer p.Close()
	ctx := context.Background()

	var got []*res
	for i := 0; i < 3; i++ {
		v, err := p.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	for _, v := range got {
		p.Put(v)
	}

	if d := destroyed.Load(); d != 2 {
		t.Fatalf("destroyed %d, want 2 (only MaxIdle=1 kept)", d)
	}
	if c := p.Created(); c != 1 {
		t.Fatalf("Created = %d, want 1 idle survivor", c)
	}
}

// Close is terminal: idle resources are destroyed, new Gets fail, and
// a resource returned afterwards is destroyed rather than leaked.
func TestCloseDestroysIdleAndFailsNewGets(t *testing.T) {
	var destroyed atomic.Int64
	p := New(Config[*res]{
		MaxOpen: 2,
		Factory: func(context.Context) (*res, error) { return &res{}, nil },
		Destroy: func(*res) { destroyed.Add(1) },
	})
	ctx := context.Background()
	idle, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkedOut, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Put(idle) // exactly one idle at Close time

	p.Close()

	if d := destroyed.Load(); d != 1 {
		t.Fatalf("destroyed %d idle resources on Close, want 1", d)
	}
	if _, err := p.Get(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after Close = %v, want ErrClosed", err)
	}

	p.Put(checkedOut) // still checked out during Close: destroy on arrival
	if d := destroyed.Load(); d != 2 {
		t.Fatalf("late return destroyed %d total, want 2 (no leak)", d)
	}
}

// Under contention the ceiling is absolute: no more than MaxOpen
// resources exist at any instant (and the race detector watches the
// bookkeeping).
func TestNeverExceedsMaxOpenUnderContention(t *testing.T) {
	const maxOpen = 3
	var live, peak atomic.Int64
	p := New(Config[*res]{
		MaxOpen: maxOpen,
		Factory: func(context.Context) (*res, error) { return &res{}, nil },
	})
	defer p.Close()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for i := 0; i < 50; i++ {
				v, err := p.Get(ctx)
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				if n := live.Add(1); n > peak.Load() {
					peak.Store(n)
				}
				live.Add(-1)
				p.Put(v)
			}
		}()
	}
	wg.Wait()

	if peak.Load() > maxOpen {
		t.Fatalf("peak concurrent resources = %d, MaxOpen = %d", peak.Load(), maxOpen)
	}
}
