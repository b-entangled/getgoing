// Package metrics implements the three instruments every system
// design's observability layer is built from — counters, gauges and
// bucketed histograms — plus a registry to hold them by name.
//
// Design decisions made explicit here:
//
//   - Counters and gauges are atomic: incrementing a request counter
//     is on the hot path and must not take a mutex per request. The
//     cost is that a counter is a single number — deltas, not
//     distributions.
//   - Histograms are bucketed, not exact: storing every observation
//     would be unbounded memory; fixed buckets cost O(buckets) memory
//     and give quantile *estimates* bounded by the bucket width. The
//     error you accept is a design choice you make up front (bucket
//     boundaries), not a surprise discovered at p99 time.
//   - Sum and count are kept exactly, so averages are precise even
//     when quantiles are approximate.
//   - The registry is just name → instrument with create-on-first-use:
//     components ask for "http_requests_total" and get the same
//     instrument everyone else got, which is what makes totals
//     aggregate across call sites.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing total.
type Counter struct{ v atomic.Int64 }

// Inc adds one.
func (c *Counter) Inc() { c.v.Add(1) }

// Add adds n (n must not be negative — counters never decrease).
func (c *Counter) Add(n int64) {
	if n < 0 {
		panic("metrics: counter cannot decrease")
	}
	c.v.Add(n)
}

// Value returns the current total.
func (c *Counter) Value() int64 { return c.v.Load() }

// Gauge is a value that goes up and down — queue depth, temperature,
// in-flight requests.
type Gauge struct{ v atomic.Int64 }

// Set replaces the value.
func (g *Gauge) Set(v int64) { g.v.Store(v) }

// Add moves the value by delta (sign allowed).
func (g *Gauge) Add(delta int64) { g.v.Add(delta) }

// Value returns the current value.
func (g *Gauge) Value() int64 { return g.v.Load() }

// Histogram records observations into fixed buckets with upper bounds
// (sorted, strictly increasing). Observations larger than every bound
// land in the overflow bucket.
type Histogram struct {
	mu     sync.Mutex
	bounds []float64
	counts []uint64 // len(bounds)+1: the last is overflow
	sum    float64
	count  int64
}

// NewHistogram returns a histogram with the given upper bounds.
func NewHistogram(bounds []float64) *Histogram {
	if len(bounds) == 0 {
		panic("metrics: histogram needs at least one bound")
	}
	sorted := append([]float64(nil), bounds...)
	sort.Float64s(sorted)
	for i := 1; i < len(sorted); i++ {
		if sorted[i] <= sorted[i-1] {
			panic("metrics: bounds must be strictly increasing")
		}
	}
	return &Histogram{bounds: sorted, counts: make([]uint64, len(sorted)+1)}
}

// Observe records one value.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	i := sort.SearchFloat64s(h.bounds, v)
	// SearchFloat64s finds the first bound >= v, which puts v == bound
	// in the bucket that the bound caps (inclusive upper bound).
	h.counts[i]++
	h.sum += v
	h.count++
}

// Count returns how many observations were recorded.
func (h *Histogram) Count() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// Snapshot is a point-in-time copy of a histogram's distribution.
type Snapshot struct {
	Bounds []float64
	Counts []uint64
	Count  int64
	Sum    float64
}

// Snapshot copies the current state (safe to read without locking).
func (h *Histogram) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Snapshot{
		Bounds: append([]float64(nil), h.bounds...),
		Counts: append([]uint64(nil), h.counts...),
		Count:  h.count,
		Sum:    h.sum,
	}
}

// Quantile estimates the q-quantile (0..1) by linear interpolation
// inside the bucket it falls in — an estimate whose error is bounded
// by the bucket width, which is the trade the design accepts.
func (h *Histogram) Quantile(q float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.count == 0 {
		return 0
	}
	if q <= 0 {
		return 0
	}
	if q >= 1 {
		return h.bounds[len(h.bounds)-1] // best upper estimate available
	}

	target := q * float64(h.count)
	var cumulative float64
	for i, c := range h.counts {
		prev := cumulative
		cumulative += float64(c)
		if cumulative < target || c == 0 {
			continue
		}
		lo := 0.0
		if i > 0 {
			lo = h.bounds[i-1]
		}
		hi := math.Inf(1)
		if i < len(h.bounds) {
			hi = h.bounds[i]
		}
		if math.IsInf(hi, 1) {
			// Overflow bucket: no better information than "at least
			// the last bound".
			return h.bounds[len(h.bounds)-1]
		}
		frac := (target - prev) / float64(c)
		return lo + frac*(hi-lo)
	}
	return h.bounds[len(h.bounds)-1]
}

// Mean returns the exact average of all observations (0 if none).
func (h *Histogram) Mean() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.sum / float64(h.count)
}

// Registry maps names to instruments, creating on first use so that
// every call site asking for the same name shares one instrument.
type Registry struct {
	mu         sync.Mutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// Counter returns the named counter, creating it on first use. Asking
// twice for a name registered as a different type panics — a
// programmer error, not a runtime condition.
func (r *Registry) Counter(name string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	if _, ok := r.histograms[name]; ok {
		panic(fmt.Sprintf("metrics: %q already registered as a histogram", name))
	}
	c := &Counter{}
	r.counters[name] = c
	return c
}

// Gauge returns the named gauge, creating it on first use.
func (r *Registry) Gauge(name string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &Gauge{}
	r.gauges[name] = g
	return g
}

// Histogram returns the named histogram with the given bounds,
// creating it on first use. Later calls must pass identical bounds —
// merging differently-bucketed data would be silently wrong.
func (r *Registry) Histogram(name string, bounds []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[name]; ok {
		if len(h.bounds) != len(bounds) {
			panic(fmt.Sprintf("metrics: %q already registered with different bounds", name))
		}
		for i := range bounds {
			if h.bounds[i] != bounds[i] {
				panic(fmt.Sprintf("metrics: %q already registered with different bounds", name))
			}
		}
		return h
	}
	h := NewHistogram(bounds)
	r.histograms[name] = h
	return h
}
