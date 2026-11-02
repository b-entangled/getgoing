// Package bridge demonstrates the Bridge pattern.
//
// Bridge decouples an abstraction from its implementation so both can vary
// independently. In Go this needs no Abstraction/Implementor class pair:
// a struct holds an interface field on one axis, and its own methods carry
// the other axis. Composition replaces inheritance.
//
// Example: alert *policy* (severity → escalation) varies independently of
// the *delivery mechanism* (email, SMS, pager). 3 severities × 3 channels
// would be 9 classes in the GoF shape; here it is 3 + 3.
package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Severity ranks an alert. Higher is worse.
type Severity int

const (
	Info Severity = iota
	Warning
	Critical
)

// String implements fmt.Stringer.
func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Critical:
		return "critical"
	default:
		return "unknown"
	}
}

// Message is the payload crossing the bridge.
type Message struct {
	Subject  string
	Body     string
	Severity Severity
}

// Notifier is the *implementation axis*: how a message physically leaves
// the process. It knows nothing about severity policy.
type Notifier interface {
	Deliver(ctx context.Context, msg Message) error
	Channel() string
}

// --- implementation A: email ---

// EmailNotifier records deliveries instead of sending them, so the example
// is testable and side-effect free.
type EmailNotifier struct {
	mu   sync.Mutex
	sent []Message
}

// Deliver stores the message.
func (e *EmailNotifier) Deliver(_ context.Context, msg Message) error {
	if msg.Subject == "" {
		return errors.New("bridge: empty subject")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sent = append(e.sent, msg)
	return nil
}

// Channel reports the delivery channel name.
func (e *EmailNotifier) Channel() string { return "email" }

// Sent returns a copy of what was delivered.
func (e *EmailNotifier) Sent() []Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Message(nil), e.sent...)
}

// --- implementation B: SMS ---

// SMSNotifier prefixes the body, mimicking a 160-character channel.
type SMSNotifier struct {
	mu   sync.Mutex
	sent []Message
}

// Deliver truncates and stores.
func (s *SMSNotifier) Deliver(_ context.Context, msg Message) error {
	if len(msg.Body) > 160 {
		msg.Body = msg.Body[:160]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return nil
}

// Channel reports the delivery channel name.
func (s *SMSNotifier) Channel() string { return "sms" }

// Sent returns a copy of what was delivered.
func (s *SMSNotifier) Sent() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.sent...)
}

// --- abstraction axis: escalation policy ---

// EscalationPolicy decides *what* to send given a severity, then delegates
// *how* to send to the notifier. The two axes never know about each other.
type EscalationPolicy struct {
	notifier Notifier
	retries  int
}

// NewEscalationPolicy wires a policy to a delivery mechanism.
func NewEscalationPolicy(n Notifier, retries int) *EscalationPolicy {
	if retries < 0 {
		retries = 0
	}
	return &EscalationPolicy{notifier: n, retries: retries}
}

// Notify applies the severity policy: subject tagging, retry budget, and
// an optional acknowledgement requirement for critical messages.
func (p *EscalationPolicy) Notify(ctx context.Context, m Message) error {
	m.Subject = "[" + m.Severity.String() + "] " + m.Subject

	attempts := 1 + p.retries
	if m.Severity == Info {
		attempts = 1 // informational messages are never retried
	}

	var err error
	for i := 0; i < attempts; i++ {
		if err = p.notifier.Deliver(ctx, m); err == nil {
			return nil
		}
	}
	return err
}

// AckRequired reports whether a human must acknowledge this severity.
func (p *EscalationPolicy) AckRequired(s Severity) bool {
	return s >= Critical
}

// Channel exposes the implementation side for diagnostics.
func (p *EscalationPolicy) Channel() string { return p.notifier.Channel() }

// Render builds the wire format for a channel, demonstrating that both axes
// contribute to the final behaviour.
func (p *EscalationPolicy) Render(m Message) string {
	var b strings.Builder
	b.WriteString(p.Channel())
	b.WriteString(" | ")
	b.WriteString(m.Severity.String())
	b.WriteString(" | ")
	b.WriteString(m.Subject)
	return b.String()
}
