package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The bridge's payoff: axes are varied independently.
func TestPolicyWithDifferentNotifiers(t *testing.T) {
	ctx := context.Background()
	msg := Message{Subject: "disk", Body: "90% full", Severity: Warning}

	email := &EmailNotifier{}
	sms := &SMSNotifier{}

	for _, n := range []Notifier{email, sms} {
		p := NewEscalationPolicy(n, 2)
		if err := p.Notify(ctx, msg); err != nil {
			t.Fatalf("%s: %v", n.Channel(), err)
		}
	}
	if len(email.Sent()) != 1 {
		t.Errorf("email got %d", len(email.Sent()))
	}
	if len(sms.Sent()) != 1 {
		t.Errorf("sms got %d", len(sms.Sent()))
	}
}

func TestSeverityPolicyTagsSubject(t *testing.T) {
	n := &EmailNotifier{}
	p := NewEscalationPolicy(n, 0)
	if err := p.Notify(context.Background(), Message{Subject: "x", Severity: Critical}); err != nil {
		t.Fatal(err)
	}
	if got := n.Sent()[0].Subject; !strings.HasPrefix(got, "[critical] ") {
		t.Fatalf("want critical prefix, got %q", got)
	}
}

func TestRetryBudgetVariesBySeverity(t *testing.T) {
	flaky := &failNTimes{failures: 2}

	// Critical retries up to 1+2=3 times, so the 3rd attempt succeeds.
	p := NewEscalationPolicy(flaky, 2)
	if err := p.Notify(context.Background(), Message{Subject: "s", Severity: Critical}); err != nil {
		t.Fatalf("critical should have retried to success: %v", err)
	}
	if flaky.calls != 3 {
		t.Fatalf("want 3 calls, got %d", flaky.calls)
	}

	// Info never retries, so the first failure is returned.
	flaky.calls = 0
	flaky.failures = 1
	p2 := NewEscalationPolicy(flaky, 5)
	err := p2.Notify(context.Background(), Message{Subject: "s", Severity: Info})
	if !errors.Is(err, errBoom) {
		t.Fatalf("want errBoom, got %v", err)
	}
	if flaky.calls != 1 {
		t.Fatalf("info must not retry, got %d calls", flaky.calls)
	}
}

var errBoom = errors.New("boom")

type failNTimes struct {
	failures int
	calls    int
}

func (f *failNTimes) Deliver(context.Context, Message) error {
	f.calls++
	if f.calls <= f.failures {
		return errBoom
	}
	return nil
}
func (f *failNTimes) Channel() string { return "flaky" }

func TestAckPolicy(t *testing.T) {
	p := NewEscalationPolicy(&EmailNotifier{}, 0)
	if p.AckRequired(Critical) != true {
		t.Error("critical must require ack")
	}
	if p.AckRequired(Warning) || p.AckRequired(Info) {
		t.Error("lower severities must not require ack")
	}
}

func TestRenderCombinesBothAxes(t *testing.T) {
	p := NewEscalationPolicy(&SMSNotifier{}, 0)
	got := p.Render(Message{Subject: "db", Severity: Warning})
	if got != "sms | warning | db" {
		t.Fatalf("got %q", got)
	}
}

// A permanent failure is retried up to the budget and then returned to the
// caller — the policy never silently swallows an undeliverable message.
func TestPermanentFailureRetriedThenReturned(t *testing.T) {
	f := &failNTimes{failures: 999}
	p := NewEscalationPolicy(f, 3)
	err := p.Notify(context.Background(), Message{Subject: "", Severity: Critical})
	if !errors.Is(err, errBoom) {
		t.Fatalf("want errBoom, got %v", err)
	}
	if f.calls != 4 { // 1 initial + 3 retries
		t.Fatalf("want 4 attempts, got %d", f.calls)
	}
}

// The subject tag is applied by the policy, so an empty original subject
// still produces a well-formed message.
func TestPolicyAlwaysProducesTaggedSubject(t *testing.T) {
	n := &EmailNotifier{}
	p := NewEscalationPolicy(n, 0)
	if err := p.Notify(context.Background(), Message{Subject: "", Severity: Warning}); err != nil {
		t.Fatal(err)
	}
	got := n.Sent()
	if len(got) != 1 || got[0].Subject != "[warning] " {
		t.Fatalf("got %+v", got)
	}
}

func TestImplementationIsSwappableAtRuntime(t *testing.T) {
	p := NewEscalationPolicy(&EmailNotifier{}, 0)
	if p.Channel() != "email" {
		t.Fatal("setup")
	}
	// Swap the implementation side without touching the policy.
	p = NewEscalationPolicy(&SMSNotifier{}, 0)
	if p.Channel() != "sms" {
		t.Fatal("implementation not swappable")
	}
}

func TestNegativeRetriesClamped(t *testing.T) {
	p := NewEscalationPolicy(&EmailNotifier{}, -5)
	f := &failNTimes{failures: 1}
	p2 := NewEscalationPolicy(f, -5)
	err := p2.Notify(context.Background(), Message{Subject: "s", Severity: Critical})
	if err == nil {
		t.Fatal("expected failure")
	}
	if f.calls != 1 {
		t.Fatalf("clamped retries should mean 1 attempt, got %d", f.calls)
	}
	_ = p
}

// The implementation itself still rejects an empty subject when called
// directly — the policy normally guarantees this cannot happen.
func TestNotifierRejectsEmptySubject(t *testing.T) {
	n := &EmailNotifier{}
	if err := n.Deliver(context.Background(), Message{Subject: ""}); err == nil {
		t.Fatal("expected error")
	}
	if len(n.Sent()) != 0 {
		t.Fatal("failed delivery must not be recorded")
	}
}
