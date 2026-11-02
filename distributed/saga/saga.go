// Package saga implements a saga: a long-running transaction built
// from local steps, each with its own compensation, so that a
// distributed workflow can roll back without a distributed lock
// (which consensus would require and most systems cannot afford per
// business operation).
//
// The shape: step 1…n run in order; the first failure stops the
// forward path and runs the compensations of every step that already
// succeeded, in reverse order — the best-effort undo of exactly the
// work that happened.
//
// Design decisions made explicit here:
//
//   - Reverse order, always. Undoing A before B when the forward
//     order was A→B would compensate in a state the system never
//     passed through.
//   - Compensations run on a context detached from the failed
//     caller's cancellation (context.WithoutCancel): if the request
//     that triggered the saga gives up mid-flight, the undo must
//     still happen — values (trace IDs) survive, cancellation does
//     not. The alternative is leaving half-applied state behind
//     because someone hit stop.
//   - A failing compensation does not stop the rest: report every
//     error with errors.Join — the forward cause first, then each
//     compensation failure — because a partial undo needs operator
//     attention, not a swallowed log line.
//   - Sagas are at-least-once by nature: compensations themselves
//     should be idempotent (see the idempotency package) because a
//     crash between a step and its compensation retries the undo.
package saga

import (
	"context"
	"errors"
	"fmt"
)

// Step is one forward action and its undo.
type Step struct {
	Name       string
	Forward    func(ctx context.Context) error
	Compensate func(ctx context.Context) error
}

// Action labels one event in the saga's trace.
type Action int

const (
	// ForwardOK: the step's forward action succeeded.
	ForwardOK Action = iota
	// ForwardFailed: the step's forward action returned an error —
	// the saga stops here.
	ForwardFailed
	// Compensated: a prior step's compensation succeeded.
	Compensated
	// CompensationFailed: a compensation returned an error; the rest
	// still run, and the error is reported.
	CompensationFailed
)

func (a Action) String() string {
	switch a {
	case ForwardOK:
		return "forward-ok"
	case ForwardFailed:
		return "forward-failed"
	case Compensated:
		return "compensated"
	default:
		return "compensation-failed"
	}
}

// Event is one entry of the saga's trace.
type Event struct {
	Step   string
	Action Action
	Err    error // nil except for *Failed actions
}

// Saga runs steps in order with reverse-order compensation.
type Saga struct {
	steps []Step
	trace []Event
}

// New builds a saga from the given steps.
func New(steps ...Step) *Saga {
	return &Saga{steps: steps}
}

// Run executes the forward path. On the first failure it compensates
// every already-executed step in reverse order and returns a joined
// error whose first element wraps the forward cause (so errors.Is on
// the business error still works).
func (s *Saga) Run(ctx context.Context) error {
	executed := make([]int, 0, len(s.steps))

	for i, st := range s.steps {
		if err := st.Forward(ctx); err != nil {
			s.trace = append(s.trace, Event{Step: st.Name, Action: ForwardFailed, Err: err})
			errs := append([]error{fmt.Errorf("saga: step %q failed: %w", st.Name, err)},
				s.compensate(executed, ctx)...)
			return errors.Join(errs...)
		}
		s.trace = append(s.trace, Event{Step: st.Name, Action: ForwardOK})
		executed = append(executed, i)
	}
	return nil
}

// Trace returns the recorded events in order.
func (s *Saga) Trace() []Event {
	return append([]Event(nil), s.trace...)
}

// compensate undoes the executed steps in reverse order, on a context
// that survives the caller's cancellation. It returns one error per
// failed compensation, for the caller to join.
func (s *Saga) compensate(executed []int, ctx context.Context) []error {
	// Values survive; cancellation deliberately does not: undo must
	// run even when the triggering request is gone.
	undoCtx := context.WithoutCancel(ctx)

	var errs []error
	for i := len(executed) - 1; i >= 0; i-- {
		st := s.steps[executed[i]]
		if err := st.Compensate(undoCtx); err != nil {
			s.trace = append(s.trace, Event{Step: st.Name, Action: CompensationFailed, Err: err})
			errs = append(errs, fmt.Errorf("saga: compensate %q: %w", st.Name, err))
			continue
		}
		s.trace = append(s.trace, Event{Step: st.Name, Action: Compensated})
	}
	return errs
}
