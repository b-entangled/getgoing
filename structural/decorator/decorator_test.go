package decorator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBaseStoreWorks(t *testing.T) {
	s := NewMapStore()
	ctx := context.Background()
	if err := s.Put(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get(ctx, "a"); err != nil || v != "1" {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := s.Get(ctx, "zz"); !errors.Is(err, ErrMiss) {
		t.Fatalf("want ErrMiss, got %v", err)
	}
}

// The wrapper implements the same interface, so it can replace the original.
func TestLoggingDecoratorSatisfiesInterface(t *testing.T) {
	var sink []string
	var s KeyValue = WithLogging(NewMapStore(), &sink)
	ctx := context.Background()
	_ = s.Put(ctx, "k", "v")
	_, _ = s.Get(ctx, "k")
	_, _ = s.Get(ctx, "absent")

	if len(sink) != 3 {
		t.Fatalf("want 3 log lines, got %v", sink)
	}
	if !strings.Contains(sink[0], "PUT k") || !strings.Contains(sink[2], "ok=false") {
		t.Fatalf("unexpected logs: %v", sink)
	}
}

func TestTTLDecoratorExpires(t *testing.T) {
	inner := NewMapStore()
	d := WithTTL(inner, time.Second).(*ttlKV)

	now := time.Unix(1000, 0)
	d.setClock(func() time.Time { return now })

	ctx := context.Background()
	_ = d.Put(ctx, "k", "v")
	if _, err := d.Get(ctx, "k"); err != nil {
		t.Fatalf("should be fresh: %v", err)
	}

	now = now.Add(2 * time.Second) // past TTL
	if _, err := d.Get(ctx, "k"); !errors.Is(err, ErrMiss) {
		t.Fatalf("expired key should miss, got %v", err)
	}
	// The inner store still has it — the decorator only masks it.
	if _, err := inner.Get(ctx, "k"); err != nil {
		t.Fatal("decorator must not mutate the wrapped value")
	}
}

func TestRetryDoesNotRetryMisses(t *testing.T) {
	s := WithRetry(NewMapStore(), 5, 0)
	if _, err := s.Get(context.Background(), "absent"); !errors.Is(err, ErrMiss) {
		t.Fatalf("want ErrMiss, got %v", err)
	}
}

func TestRetryRetriesTransientErrors(t *testing.T) {
	flaky := &flakyStore{failures: 2}
	s := WithRetry(flaky, 3, 0)
	v, err := s.Get(context.Background(), "k")
	if err != nil || v != "ok" {
		t.Fatalf("want ok after retries, got %q %v", v, err)
	}
	if flaky.calls != 3 {
		t.Fatalf("want 3 calls, got %d", flaky.calls)
	}
}

type flakyStore struct {
	failures int
	calls    int
}

func (f *flakyStore) Get(context.Context, string) (string, error) {
	f.calls++
	if f.calls <= f.failures {
		return "", errors.New("transient")
	}
	return "ok", nil
}
func (f *flakyStore) Put(context.Context, string, string) error { return nil }

func TestRetryExhaustsAttempts(t *testing.T) {
	f := &flakyStore{failures: 99}
	s := WithRetry(f, 3, 0)
	if _, err := s.Get(context.Background(), "k"); err == nil {
		t.Fatal("expected error")
	}
	if f.calls != 3 {
		t.Fatalf("want 3 calls, got %d", f.calls)
	}
}

func TestWrapAppliesInnermostFirst(t *testing.T) {
	var sink []string
	// Wrap reads left-to-right as outermost-to-innermost:
	// logging is outermost, so it observes the TTL decorator's miss.
	var s KeyValue = Wrap(NewMapStore(),
		func(k KeyValue) KeyValue { return WithLogging(k, &sink) },
		func(k KeyValue) KeyValue { return WithTTL(k, time.Nanosecond) },
	)
	_, _ = s.Get(context.Background(), "absent")
	if len(sink) != 1 {
		t.Fatalf("outer decorator did not run: %v", sink)
	}
}

// --- function idiom ---

func echo(_ context.Context, req string) (string, error) { return req, nil }

func TestChainRunsOutsideIn(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, req string) (string, error) {
				order = append(order, "enter:"+name)
				out, err := next(ctx, req)
				order = append(order, "exit:"+name)
				return out, err
			}
		}
	}
	h := Chain(echo, mw("a"), mw("b"), mw("c"))
	if _, err := h(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{"enter:a", "enter:b", "enter:c", "exit:c", "exit:b", "exit:a"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v\ngot  %v", want, order)
	}
}

func TestRecoverConvertsPanicToError(t *testing.T) {
	boom := func(context.Context, string) (string, error) { panic("kaboom") }
	h := Chain(boom, Recover)
	out, err := h(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("want panic as error, got %q %v", out, err)
	}
}

func TestUpperDecoratesOutput(t *testing.T) {
	h := Chain(echo, Upper)
	out, err := h(context.Background(), "hello")
	if err != nil || out != "HELLO" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPrefixDecoratesInput(t *testing.T) {
	h := Chain(echo, Prefix("pre/"))
	out, _ := h(context.Background(), "k")
	if out != "pre/k" {
		t.Fatalf("got %q", out)
	}
}

func TestTimeoutEnforced(t *testing.T) {
	slow := func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	h := Chain(slow, Timeout(10*time.Millisecond))
	_, err := h(context.Background(), "x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}

func TestDecoratorsComposeFreely(t *testing.T) {
	h := Chain(echo, Recover, Prefix("a/"), Upper)
	out, err := h(context.Background(), "b")
	if err != nil || out != "A/B" {
		t.Fatalf("%q %v", out, err)
	}
}
