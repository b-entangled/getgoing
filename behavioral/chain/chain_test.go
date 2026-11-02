package chain

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func validRequest() *Request {
	return &Request{User: "ada", Email: "ada@example.com", Age: 30, Plan: "pro"}
}

func registrationChain() Handler {
	return Stop(
		RequireEmail,
		ValidateEmail,
		BanDisposable,
		RequireAge(13),
		ValidatePlan,
	)
}

func TestValidRequestPassesAllHandlers(t *testing.T) {
	r := validRequest()
	if err := registrationChain()(context.Background(), r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Notes) != 5 {
		t.Fatalf("each handler should annotate, got %v", r.Notes)
	}
}

// Stop short-circuits: later handlers never run.
func TestStopShortCircuitsOnFirstError(t *testing.T) {
	r := &Request{Email: "", Age: 5, Plan: "nope"}
	err := registrationChain()(context.Background(), r)
	if !errors.Is(err, ErrEmailRequired) {
		t.Fatalf("want ErrEmailRequired, got %v", err)
	}
	// RequireEmail failed first, so nothing else annotated.
	if len(r.Notes) != 0 {
		t.Fatalf("later handlers ran: %v", r.Notes)
	}
}

func TestStopReportsHandlerIndex(t *testing.T) {
	r := validRequest()
	r.Plan = "gold"
	err := registrationChain()(context.Background(), r)
	if !errors.Is(err, ErrUnknownPlan) {
		t.Fatalf("want ErrUnknownPlan, got %v", err)
	}
	if !strings.Contains(err.Error(), "handler 4") {
		t.Fatalf("want index in error, got %v", err)
	}
}

// Collect runs everything, so the caller sees every problem at once.
func TestCollectReportsAllErrors(t *testing.T) {
	r := &Request{Email: "not-an-email", Age: 5, Plan: "gold"}
	err := Collect(
		RequireEmail,
		ValidateEmail,
		BanDisposable,
		RequireAge(13),
		ValidatePlan,
	)(context.Background(), r)

	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []error{ErrInvalidEmail, ErrUnderage, ErrUnknownPlan} {
		if !errors.Is(err, want) {
			t.Errorf("missing %v in %v", want, err)
		}
	}
}

// The only difference between Stop and Collect is the loop body.
func TestStopAndCollectShareTheSameHandlerList(t *testing.T) {
	handlers := []Handler{RequireEmail, ValidateEmail, ValidatePlan}
	bad := &Request{Email: "x", Plan: "gold"} // fails only ValidatePlan

	if err := Stop(handlers...)(context.Background(), bad); err == nil {
		t.Fatal("stop should fail")
	}
	if err := Collect(handlers...)(context.Background(), bad); err == nil {
		t.Fatal("collect should fail")
	}
	// With a valid email, RequireEmail+ValidateEmail pass under both drivers.
	good := &Request{Email: "a@b.c", Plan: "gold"}
	if err := Stop(handlers...)(context.Background(), good); !errors.Is(err, ErrUnknownPlan) {
		t.Fatalf("got %v", err)
	}
	if err := Collect(handlers...)(context.Background(), good); !errors.Is(err, ErrUnknownPlan) {
		t.Fatalf("got %v", err)
	}
}

func TestChainsAreBuiltDynamically(t *testing.T) {
	// Rule set depends on runtime config — impossible with fixed next links.
	strict := []Handler{RequireEmail, ValidateEmail, BanDisposable, RequireAge(18)}
	loose := []Handler{RequireEmail, ValidateEmail}

	r := &Request{Email: "ada@example.com", Age: 16}
	if err := Stop(loose...)(context.Background(), r); err != nil {
		t.Fatalf("loose chain should pass a 16-year-old: %v", err)
	}
	if err := Stop(strict...)(context.Background(), r); !errors.Is(err, ErrUnderage) {
		t.Fatalf("strict chain should reject, got %v", err)
	}
}

func TestTerminalMarksHandled(t *testing.T) {
	r := validRequest()
	h := Terminal(func(ctx context.Context, r *Request) error {
		r.Note("accepted")
		return nil
	})
	if err := Stop(RequireEmail, h)(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !r.Handled {
		t.Fatal("terminal handler did not mark the request handled")
	}
}

func TestTerminalErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	h := Terminal(func(context.Context, *Request) error { return boom })
	if err := h(context.Background(), validRequest()); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestCancellationStopsTheChain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := validRequest()
	err := Stop(RequireEmail, ValidateEmail)(ctx, r)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(r.Notes) != 0 {
		t.Fatal("handlers ran on a cancelled context")
	}
}

func TestSignature(t *testing.T) {
	if got := Signature(RequireEmail, ValidateEmail); got != "chain(2 handlers)" {
		t.Fatalf("got %q", got)
	}
}
