package deferred

import (
	"errors"
	"reflect"
	"testing"
)

func TestDefersRunLIFO(t *testing.T) {
	r := &Recorder{}
	RunDeferredLIFO(r)
	want := []string{"body", "first", "second", "third"}
	if got := r.Events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Rule 2: arguments are evaluated when defer runs, so the deferred call
// sees the value from *before* the later mutation.
func TestArgumentsAreEvaluatedWhenDeferRuns(t *testing.T) {
	r := &Recorder{}
	RunDeferredWithArgs(r, 1)
	want := []string{"1", "value"} // "1" recorded at defer time, not "2"
	if got := r.Events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The fix for rule 2: wrap the call in a closure so the body — not just
// the argument list — is postponed.
func TestClosureDefersEvaluationUntilRun(t *testing.T) {
	r := &Recorder{}
	RunDeferredWithClosure(r, 1)
	if got := r.Events(); !reflect.DeepEqual(got, []string{"2"}) {
		t.Fatalf("got %v, want [2]", got)
	}
}

// Rules 3 + 4: defers run during panic unwinding; recover works only
// from a deferred call.
func TestDefersRunOnPanicAndRecoverConvertsIt(t *testing.T) {
	r := &Recorder{}
	panicked := RunDefersOnPanic(r)
	if !panicked {
		t.Fatal("recover() did not catch the panic")
	}
	want := []string{"body", "defer-before-recover", "after-recover"}
	if got := r.Events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Rule 5: a deferred call can rewrite a named return value.
func TestDeferredCallCanReplaceNamedReturn(t *testing.T) {
	r := &Recorder{}
	err := FailWithNamedReturn(r)
	if !errors.Is(err, BoomError()) {
		t.Fatalf("err = %v, want the substituted error", err)
	}
	if got := r.Events(); !reflect.DeepEqual(got, []string{"converted"}) {
		t.Fatalf("events = %v", got)
	}
}
