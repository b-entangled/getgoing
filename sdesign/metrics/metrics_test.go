package metrics

import (
	"sync"
	"testing"
)

// Counters must be exact under contention — this is the instrument
// dashboards alert on, so an off-by-N is an incident.
func TestCounterConcurrentIncrementsAreExact(t *testing.T) {
	var c Counter

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()

	if got := c.Value(); got != 8000 {
		t.Fatalf("counter = %d, want 8000", got)
	}
}

// Counters are monotone: a negative Add is a programming error and
// must not silently corrupt the total.
func TestCounterRefusesToDecrease(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("negative Add did not panic")
		}
	}()
	var c Counter
	c.Add(5)
	c.Add(-1)
}

// Gauges go both ways — that is what distinguishes them from counters.
func TestGaugeTracksRisingAndFallingValues(t *testing.T) {
	var g Gauge
	g.Set(10)
	g.Add(5)
	g.Add(-8)
	if got := g.Value(); got != 7 {
		t.Fatalf("gauge = %d, want 7", got)
	}
}

// Observations land in the bucket whose upper bound contains them —
// inclusive at the bound, overflow past the last one.
func TestHistogramBucketAssignmentIsExact(t *testing.T) {
	h := NewHistogram([]float64{1, 5, 10})
	for _, v := range []float64{0.5, 1, 2, 5, 7, 10, 50} {
		h.Observe(v)
	}

	s := h.Snapshot()
	want := []uint64{
		2, // (-∞,1]: 0.5, 1 — bounds are inclusive upper edges
		2, // (1,5]: 2, 5
		2, // (5,10]: 7, 10
		1, // (10,∞): 50
	}
	for i, w := range want {
		if s.Counts[i] != w {
			t.Fatalf("bucket %d = %d, want %d (counts=%v)", i, s.Counts[i], w, s.Counts)
		}
	}
	if s.Count != 7 || s.Sum != 0.5+1+2+5+7+10+50 {
		t.Fatalf("count=%d sum=%v", s.Count, s.Sum)
	}
}

// The quantile estimate must land inside the bucket that contains it:
// the accepted error is the bucket width, never more.
func TestQuantileStaysWithinItsBucket(t *testing.T) {
	h := NewHistogram([]float64{10, 20, 30, 40})
	for v := 0.0; v < 40; v++ { // 40 observations, uniform in [0,40)
		h.Observe(v)
	}

	q50 := h.Quantile(0.5)
	if q50 < 10 || q50 > 30 {
		t.Fatalf("p50 = %v, want it inside [10,30]", q50)
	}
	q90 := h.Quantile(0.9)
	if q90 < 30 || q90 > 40 {
		t.Fatalf("p90 = %v, want it inside [30,40]", q90)
	}
	if q90 < q50 {
		t.Fatalf("p90 (%v) < p50 (%v): quantiles must be monotone", q90, q50)
	}
}

// Mean is exact — sums and counts are kept, only quantiles are
// bucketed.
func TestMeanIsExactEvenThoughQuantilesAreEstimates(t *testing.T) {
	h := NewHistogram([]float64{100})
	for i := 1; i <= 100; i++ {
		h.Observe(float64(i))
	}
	if got := h.Mean(); got != 50.5 {
		t.Fatalf("mean = %v, want 50.5", got)
	}
}

// The registry's contract: the same name is the same instrument for
// every caller — that is what makes totals aggregate.
func TestRegistrySharesOneInstrumentPerName(t *testing.T) {
	r := NewRegistry()

	a := r.Counter("requests_total")
	b := r.Counter("requests_total")
	a.Inc()
	b.Inc()

	if a != b {
		t.Fatal("two instances for one name: totals would not aggregate")
	}
	if got := a.Value(); got != 2 {
		t.Fatalf("counter = %d, want 2", got)
	}
}

// Re-asking for a histogram with different bounds would silently mix
// incompatible buckets — refuse instead.
func TestRegistryRejectsConflictingHistogramBounds(t *testing.T) {
	r := NewRegistry()
	r.Histogram("latency", []float64{1, 2, 3})

	defer func() {
		if recover() == nil {
			t.Fatal("conflicting bounds did not panic")
		}
	}()
	r.Histogram("latency", []float64{1, 2})
}

// Registry use is concurrency-safe: every request path asks for its
// instruments by name (race detector verifies).
func TestRegistryIsRaceFree(t *testing.T) {
	r := NewRegistry()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r.Counter("hits_total").Inc()
				r.Histogram("sizes", []float64{16, 1024}).Observe(float64(i % 100))
				r.Gauge("depth").Set(int64(i))
			}
		}()
	}
	wg.Wait()

	if got := r.Counter("hits_total").Value(); got != 1600 {
		t.Fatalf("counter = %d, want 1600", got)
	}
}
