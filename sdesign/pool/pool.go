// Package pool manages a bounded set of reusable resources —
// connections, workers, file handles — so that expensive setup is paid
// once instead of per request, and so that a traffic spike cannot open
// an unbounded number of them.
//
// Design decisions made explicit here:
//
//   - maxOpen is a hard ceiling: beyond it, Get blocks on the caller's
//     context instead of creating one more resource. The ceiling is
//     what protects the far side (the database, the API) from being
//     opened-to-death.
//   - maxIdle bounds what is kept around: idle resources are memory
//     and file descriptors too, so resources returned past the idle
//     cap are destroyed, not hoarded.
//   - Failure is not sticky: if the factory fails, the slot is given
//     back immediately — one bad call must not permanently shrink the
//     pool.
//   - Close destroys everything idle and fails everything new;
//     resources still checked out are the caller's to return (they are
//     then destroyed on arrival rather than leaked).
package pool

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is reported by Get after Close.
var ErrClosed = errors.New("pool: closed")

// Pool is a bounded, blocking resource pool.
type Pool[T any] struct {
	mu      sync.Mutex
	idle    []T
	created int // idle + checked out
	maxOpen int
	maxIdle int
	closed  bool
	waiters chan struct{} // closed & replaced when a slot frees

	factory func(ctx context.Context) (T, error)
	destroy func(T)
}

// Config sizes the pool. maxIdle defaults to maxOpen when zero;
// destroy may be nil when resources need no teardown.
type Config[T any] struct {
	MaxOpen int
	MaxIdle int
	Factory func(ctx context.Context) (T, error)
	Destroy func(T)
}

// New builds a pool. MaxOpen and Factory are required.
func New[T any](cfg Config[T]) *Pool[T] {
	if cfg.MaxOpen < 1 {
		panic("pool: MaxOpen must be >= 1")
	}
	if cfg.Factory == nil {
		panic("pool: Factory is required")
	}
	if cfg.MaxIdle == 0 {
		cfg.MaxIdle = cfg.MaxOpen
	}
	if cfg.MaxIdle < 0 || cfg.MaxIdle > cfg.MaxOpen {
		panic("pool: MaxIdle must be between 0 and MaxOpen")
	}
	if cfg.Destroy == nil {
		cfg.Destroy = func(T) {}
	}
	return &Pool[T]{
		maxOpen: cfg.MaxOpen,
		maxIdle: cfg.MaxIdle,
		factory: cfg.Factory,
		destroy: cfg.Destroy,
		waiters: make(chan struct{}),
	}
}

// Get checks out a resource: an idle one if available, otherwise a
// newly created one if under MaxOpen, otherwise it blocks until a
// resource returns or ctx expires.
func (p *Pool[T]) Get(ctx context.Context) (T, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			var zero T
			return zero, ErrClosed
		}
		if n := len(p.idle); n > 0 {
			v := p.idle[n-1]
			p.idle = p.idle[:n-1]
			p.mu.Unlock()
			return v, nil
		}
		if p.created < p.maxOpen {
			p.created++
			p.mu.Unlock()
			v, err := p.factory(ctx)
			if err != nil {
				p.releaseSlot()
				var zero T
				return zero, err
			}
			return v, nil
		}
		wait := p.waiters
		p.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}

// Put returns a resource to the pool. Resources returned after Close,
// or past the idle cap, are destroyed instead of kept.
func (p *Pool[T]) Put(v T) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed || len(p.idle) >= p.maxIdle {
		p.destroy(v)
		p.created--
		return
	}
	p.idle = append(p.idle, v)
	p.signalLocked()
}

// Close destroys every idle resource and fails all future Gets.
// Resources still checked out are destroyed when they come back.
func (p *Pool[T]) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for _, v := range p.idle {
		p.destroy(v)
	}
	p.idle = nil
	close(p.waiters)
}

// Created reports how many resources exist (idle + checked out) —
// the quantity MaxOpen bounds.
func (p *Pool[T]) Created() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.created
}

// releaseSlot undoes the reservation a failed factory took: the slot
// becomes usable again and any waiter is woken.
func (p *Pool[T]) releaseSlot() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.created--
	p.signalLocked()
}

// signalLocked wakes one waiter (broadcast — the woken goroutines
// re-contend under the lock). Callers must hold p.mu.
func (p *Pool[T]) signalLocked() {
	if p.closed {
		return // Close already woke everyone
	}
	close(p.waiters)
	p.waiters = make(chan struct{})
}
