package singleton

import (
	"sync"
	"testing"
	"time"
)

func TestGetMeterReturnsSameInstance(t *testing.T) {
	resetForTest()
	defer resetForTest()

	a := GetMeter()
	b := GetMeter()
	if a != b {
		t.Fatalf("expected identical pointers, got %p and %p", a, b)
	}
}

func TestConcurrentCallersShareOneInstance(t *testing.T) {
	resetForTest()
	defer resetForTest()

	const goroutines = 64
	results := make([]*Meter, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = GetMeter()
		}()
	}
	wg.Wait()

	for i, m := range results {
		if m != results[0] {
			t.Fatalf("goroutine %d got a different instance: %p vs %p", i, m, results[0])
		}
	}
}

func TestStateIsSharedAcrossCallers(t *testing.T) {
	resetForTest()
	defer resetForTest()

	const goroutines = 32
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			GetMeter().Inc("requests", 1)
		}()
	}
	wg.Wait()

	if got := GetMeter().Count("requests"); got != goroutines {
		t.Fatalf("want %d, got %d", goroutines, got)
	}
}

func TestInitRunsExactlyOnce(t *testing.T) {
	resetForTest()
	defer resetForTest()

	// started is set inside once.Do; if it ran more than once the timestamp
	// would move between calls.
	first := GetMeter().started
	time.Sleep(5 * time.Millisecond)
	second := GetMeter().started
	if !first.Equal(second) {
		t.Fatalf("initialization ran twice: %v != %v", first, second)
	}
}

func TestUptimeIsNonZero(t *testing.T) {
	resetForTest()
	defer resetForTest()

	if u := GetMeter().Uptime(); u < 0 {
		t.Fatalf("uptime must not be negative, got %v", u)
	}
}
