// Package ratelimit implements the two classic admission controls:
//
//   - A token bucket: allows a burst of `burst` requests and then a
//     steady `rate` per second, refilling continuously. This is what
//     API gateways and per-tenant quotas use, because it degrades
//     gracefully — one slow second does not permanently penalise the
//     caller.
//   - A sliding window: allows at most `limit` events whose timestamps
//     fall inside the last `window` of time. This is what dashboards
//     and hard quotas use, because the bound is exact over any window
//     — but it must remember every recent event.
//
// Both take their time from clock.Clock, so refill and window
// behaviour are tested by advancing a manual clock: no sleeping, no
// flakes, no tolerance windows.
package ratelimit

import (
	"sync"
	"time"

	"github.com/b-entangled/projects/learnings/sdesign/clock"
)

// Bucket is a token-bucket limiter: capacity tokens, refilled at
// ratePerSecond, starting full (the burst is available immediately).
type Bucket struct {
	mu       sync.Mutex
	clk      clock.Clock
	capacity float64
	rate     float64 // tokens per second
	tokens   float64
	last     time.Time
}

// NewBucket returns a bucket allowing bursts of up to burst and
// ratePerSecond steady-state. Both must be positive; clk may be nil
// for the real clock.
func NewBucket(clk clock.Clock, ratePerSecond, burst float64) *Bucket {
	if ratePerSecond <= 0 || burst <= 0 {
		panic("ratelimit: rate and burst must be positive")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Bucket{
		clk:      clk,
		capacity: burst,
		rate:     ratePerSecond,
		tokens:   burst,
		last:     clk.Now(),
	}
}

// Allow reports whether one event may proceed, consuming a token if
// so. It never blocks: refusal is the caller's signal to shed load.
func (b *Bucket) Allow() bool { return b.AllowN(1) }

// AllowN reports whether n events may proceed (n must be >= 1). The
// whole batch is granted or denied — no partial admission.
func (b *Bucket) AllowN(n int) bool {
	if n <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refill(b.clk.Now())
	need := float64(n)
	if b.tokens < need {
		return false
	}
	b.tokens -= need
	return true
}

// Tokens reports the currently available tokens (after refill) —
// useful for telemetry and for tests that assert refill amounts.
func (b *Bucket) Tokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(b.clk.Now())
	return b.tokens
}

func (b *Bucket) refill(now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.last = now
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

// Window is a sliding-window limiter: at most limit events inside any
// window of the last `window` duration. Events are remembered as
// timestamps and pruned as they age out.
type Window struct {
	mu     sync.Mutex
	clk    clock.Clock
	limit  int
	window time.Duration
	hits   []time.Time
}

// NewWindow returns a sliding-window limiter. limit and window must
// be positive; clk may be nil for the real clock.
func NewWindow(clk clock.Clock, limit int, window time.Duration) *Window {
	if limit <= 0 || window <= 0 {
		panic("ratelimit: limit and window must be positive")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Window{clk: clk, limit: limit, window: window}
}

// Allow reports whether one event falls within the limit for the
// current window, recording it when so.
func (w *Window) Allow() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.clk.Now()
	cutoff := now.Add(-w.window)
	kept := w.hits[:0]
	for _, t := range w.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	w.hits = kept

	if len(w.hits) >= w.limit {
		return false
	}
	w.hits = append(w.hits, now)
	return true
}

// Count reports how many events are inside the current window.
func (w *Window) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.clk.Now()
	cutoff := now.Add(-w.window)
	n := 0
	for _, t := range w.hits {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}
