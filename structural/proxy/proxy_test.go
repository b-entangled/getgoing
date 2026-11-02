package proxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seed() *UserDirectory {
	return NewUserDirectory(
		User{ID: "u1", Name: "Ada"},
		User{ID: "u2", Name: "Grace", Admin: true},
	)
}

func TestSubjectWorks(t *testing.T) {
	d := seed()
	ctx := context.Background()
	u, err := d.Lookup(ctx, "u1")
	if err != nil || u.Name != "Ada" {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := d.Lookup(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// --- caching proxy ---

func TestCacheShortCircuitsSecondRead(t *testing.T) {
	d := seed()
	c := WithCache(d)
	ctx := context.Background()

	if _, err := c.Lookup(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lookup(ctx, "u1"); err != nil {
		t.Fatal(err)
	}

	if d.Calls() != 1 {
		t.Fatalf("subject should have been called once, got %d", d.Calls())
	}
	hits, misses := c.Stats()
	if hits != 1 || misses != 1 {
		t.Fatalf("hits=%d misses=%d", hits, misses)
	}
}

func TestCacheDoesNotCacheErrors(t *testing.T) {
	d := seed()
	c := WithCache(d)
	ctx := context.Background()

	if _, err := c.Lookup(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Lookup(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if d.Calls() != 2 {
		t.Fatalf("misses must reach the subject, got %d calls", d.Calls())
	}
}

func TestCacheInvalidatesOnDelete(t *testing.T) {
	d := seed()
	c := WithCache(d)
	ctx := context.Background()

	_, _ = c.Lookup(ctx, "u1") // populate
	if err := c.Delete(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lookup(ctx, "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale cache entry served after delete: %v", err)
	}
}

func TestCacheConcurrentLookups(t *testing.T) {
	d := seed()
	c := WithCache(d)
	done := make(chan struct{})
	for range 16 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 50 {
				if _, err := c.Lookup(context.Background(), "u1"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for range 16 {
		<-done
	}
}

// --- authorisation proxy ---

func TestAuthAllowsAdminDelete(t *testing.T) {
	d := seed()
	a := WithAuth(d)
	if err := a.Delete(WithAdmin(context.Background()), "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lookup(context.Background(), "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete did not reach the subject")
	}
}

func TestAuthRefusesNonAdminWithoutCallingSubject(t *testing.T) {
	d := seed()
	a := WithAuth(d)
	before := d.Calls()

	err := a.Delete(context.Background(), "u1")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	if d.Calls() != before {
		t.Fatal("forbidden request reached the subject — proxy failed to block")
	}
}

// --- circuit breaker ---

func TestBreakerOpensAfterThreshold(t *testing.T) {
	d := seed()
	d.setFail(true)
	b := WithBreaker(d, 3, time.Hour)
	ctx := context.Background()

	// Three failures trip the breaker.
	for range 3 {
		if _, err := b.Lookup(ctx, "u1"); err == nil {
			t.Fatal("expected subject failure")
		}
	}
	if b.State() != Open {
		t.Fatalf("want open, got %s", b.State())
	}

	// Fail-fast: no further subject calls.
	before := d.Calls()
	if _, err := b.Lookup(ctx, "u1"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	if d.Calls() != before {
		t.Fatal("open breaker still called the subject")
	}
}

func TestBreakerHalfOpensAfterTimeout(t *testing.T) {
	d := seed()
	d.setFail(true)
	b := WithBreaker(d, 1, 20*time.Millisecond)
	ctx := context.Background()

	if _, err := b.Lookup(ctx, "u1"); err == nil {
		t.Fatal("expected failure")
	}
	if b.State() != Open {
		t.Fatalf("want open, got %s", b.State())
	}

	time.Sleep(30 * time.Millisecond)
	if b.State() != HalfOpen {
		t.Fatalf("want half-open, got %s", b.State())
	}
}

func TestBreakerClosesOnSuccessfulProbe(t *testing.T) {
	d := seed()
	d.setFail(true)
	b := WithBreaker(d, 1, 10*time.Millisecond)
	ctx := context.Background()

	_, _ = b.Lookup(ctx, "u1") // trips
	time.Sleep(15 * time.Millisecond)

	d.setFail(false) // subject recovers
	if _, err := b.Lookup(ctx, "u1"); err != nil {
		t.Fatalf("probe should succeed: %v", err)
	}
	if b.State() != Closed {
		t.Fatalf("want closed after successful probe, got %s", b.State())
	}
}

func TestBreakerReopensOnFailedProbe(t *testing.T) {
	d := seed()
	d.setFail(true)
	b := WithBreaker(d, 1, 10*time.Millisecond)
	ctx := context.Background()

	_, _ = b.Lookup(ctx, "u1")
	time.Sleep(15 * time.Millisecond) // half-open
	_, _ = b.Lookup(ctx, "u1")        // failing probe → open again
	if b.State() != Open {
		t.Fatalf("want open, got %s", b.State())
	}
}

func TestBreakerDoesNotCountBlockedCallsAsFailures(t *testing.T) {
	d := seed()
	d.setFail(true)
	b := WithBreaker(d, 2, time.Hour)
	ctx := context.Background()

	_, _ = b.Lookup(ctx, "u1")
	_, _ = b.Lookup(ctx, "u1") // trips at threshold=2
	if b.State() != Open {
		t.Fatal("setup")
	}
	for range 10 {
		_, _ = b.Lookup(ctx, "u1") // all blocked
	}
	if b.State() != Open {
		t.Fatal("blocked calls must not change state")
	}
}

// --- composition ---

func TestProtectBlocksAnonymousDelete(t *testing.T) {
	d := seed()
	svc := Protect(d)
	if err := svc.Delete(context.Background(), "u1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
}

func TestProtectCachesAdminReads(t *testing.T) {
	d := seed()
	svc := WithCache(WithAuth(d)) // read path: cache outside auth
	ctx := context.Background()

	_, _ = svc.Lookup(ctx, "u1")
	_, _ = svc.Lookup(ctx, "u1")
	if d.Calls() != 1 {
		t.Fatalf("want 1 subject call, got %d", d.Calls())
	}
}

func TestProtectWholeStackServesReads(t *testing.T) {
	d := seed()
	svc := Protect(d)
	ctx := context.Background()
	for range 5 {
		u, err := svc.Lookup(ctx, "u2")
		if err != nil || !u.Admin {
			t.Fatalf("%+v %v", u, err)
		}
	}
}
