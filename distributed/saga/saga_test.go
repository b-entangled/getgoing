package saga

import (
	"context"
	"errors"
	"testing"
)

// recorder builds a saga over a shared call log so tests can assert
// exact ordering — the property that makes a saga a saga.
func recorder(log *[]string, failAt string, failWith error) []Step {
	step := func(name string) Step {
		return Step{
			Name: name,
			Forward: func(context.Context) error {
				*log = append(*log, "do "+name)
				if name == failAt {
					return failWith
				}
				return nil
			},
			Compensate: func(context.Context) error {
				*log = append(*log, "undo "+name)
				return nil
			},
		}
	}
	return []Step{step("reserve stock"), step("charge card"), step("email receipt"), step("write ledger")}
}

// Success: every forward step runs, no compensation does.
func TestAllStepsSucceedAndNothingIsCompensated(t *testing.T) {
	var log []string
	s := New(recorder(&log, "", nil)...)

	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v", err)
	}
	want := []string{"do reserve stock", "do charge card", "do email receipt", "do write ledger"}
	if !equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// The heart of the design: failure at step k compensates steps
// 0..k-1 in exactly reverse order, and never starts step k+1.
func TestFailureCompensatesExecutedStepsInReverseOrder(t *testing.T) {
	var log []string
	boom := errors.New("card declined")
	s := New(recorder(&log, "charge card", boom)...)

	err := s.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want the forward cause to stay reachable via errors.Is", err)
	}
	want := []string{
		"do reserve stock",
		"do charge card", // fails here
		"undo reserve stock",
	}
	if !equal(log, want) {
		t.Fatalf("log = %v, want %v (reverse prefix only)", log, want)
	}
}

// A compensation failure is reported, but the remaining undo still
// runs — a partial rollback needs all of the errors, not the first.
func TestCompensationFailureStillRunsTheRestAndIsReported(t *testing.T) {
	var log []string
	cardDown := errors.New("card declined")
	undoFail := errors.New("inventory lock stuck")

	steps := []Step{
		{
			Name:       "reserve stock",
			Forward:    func(context.Context) error { log = append(log, "do reserve"); return nil },
			Compensate: func(context.Context) error { log = append(log, "undo reserve"); return nil },
		},
		{
			Name:       "charge card",
			Forward:    func(context.Context) error { return cardDown },
			Compensate: func(context.Context) error { log = append(log, "undo charge"); return nil },
		},
		{
			Name:       "publish event",
			Forward:    func(context.Context) error { log = append(log, "do publish"); return nil },
			Compensate: func(context.Context) error { log = append(log, "undo publish"); return undoFail },
		},
	}
	// Forward: reserve OK, charge fails → compensate reserve only.
	// Make reserve's compensation fail instead, plus a later one.
	steps[0].Compensate = func(context.Context) error { log = append(log, "undo reserve"); return undoFail }

	s := New(steps...)
	err := s.Run(context.Background())

	if !errors.Is(err, cardDown) {
		t.Fatalf("forward cause lost: %v", err)
	}
	if !errors.Is(err, undoFail) {
		t.Fatalf("compensation failure lost: %v", err)
	}
	want := []string{"do reserve", "undo reserve"}
	if !equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// The design-critical behaviour: when the triggering request's context
// dies mid-saga, compensations still run — on a detached context —
// because half-applied state is worse than a cancelled HTTP call.
func TestCompensationsSurviveCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ranCompensation := false
	var undoSawLiveCtx bool

	s := New(
		Step{
			Name:    "step one",
			Forward: func(context.Context) error { return nil },
			Compensate: func(undoCtx context.Context) error {
				ranCompensation = true
				undoSawLiveCtx = undoCtx.Err() == nil
				return nil
			},
		},
		Step{
			Name: "step two",
			Forward: func(ctx context.Context) error {
				cancel() // the caller gives up while we are mid-saga
				return errors.New("caller gone")
			},
			Compensate: func(context.Context) error { return nil },
		},
		Step{
			Name:       "step three",
			Forward:    func(context.Context) error { return nil },
			Compensate: func(context.Context) error { return nil },
		},
	)

	if err := s.Run(ctx); err == nil {
		t.Fatal("Run succeeded despite the failing step")
	}
	if !ranCompensation {
		t.Fatal("compensation never ran after cancellation")
	}
	if !undoSawLiveCtx {
		t.Fatal("compensation ran on a cancelled context: undo would be skipped")
	}
}

// The trace is the audit log: it records the forward run and every
// undo in execution order.
func TestTraceRecordsEveryTransition(t *testing.T) {
	var log []string
	s := New(recorder(&log, "email receipt", errors.New("smtp down"))...)
	s.Run(context.Background())

	got := s.Trace()
	wantActions := []Action{ForwardOK, ForwardOK, ForwardFailed, Compensated, Compensated}
	if len(got) != len(wantActions) {
		t.Fatalf("trace has %d events: %+v", len(got), got)
	}
	for i, a := range wantActions {
		if got[i].Action != a {
			t.Fatalf("event %d = %s (%s), want %s", i, got[i].Action, got[i].Step, a)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
