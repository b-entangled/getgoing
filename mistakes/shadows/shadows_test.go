package shadows

import (
	"errors"
	"testing"
)

// The bug: the inner failure vanishes and the caller sees success.
func TestShadowingHidesInnerError(t *testing.T) {
	got := BadShadowed(nil, ErrSecond)
	if got != nil {
		t.Fatalf("BadShadowed = %v; the buggy version loses the error, expected nil", got)
	}
}

// The fix: the same inputs produce an error the caller can match.
func TestExplicitAssignmentPreservesInnerError(t *testing.T) {
	got := GoodExplicit(nil, ErrSecond)
	if !errors.Is(got, ErrSecond) {
		t.Fatalf("GoodExplicit = %v, want it to wrap ErrSecond", got)
	}
}

// When the outer error is real it still wins — the fix does not change
// the outer-error path.
func TestOuterErrorStillReported(t *testing.T) {
	outer := errors.New("outer")
	if got := GoodExplicit(outer, ErrSecond); !errors.Is(got, outer) {
		t.Fatalf("GoodExplicit = %v, want it to wrap the outer error", got)
	}
}

// The short-statement variable dies with its block: the outer variable is
// never updated.
func TestShortStatementVariableIsScopedToTheIf(t *testing.T) {
	if got := ShortStatementScope(ErrSecond); got != nil {
		t.Fatalf("ShortStatementScope = %v, want nil (inner err out of scope)", got)
	}
}

// Scoping in one assertion: two `:=` in sibling blocks create two
// different variables, neither of which is the caller's.
func TestSiblingBlocksDeclareSeparateVariables(t *testing.T) {
	outer := error(nil)
	{
		if e := ErrSecond; e != nil {
			_ = e
		}
	}
	{
		if e := errors.New("other"); e != nil {
			_ = e
		}
	}
	if outer != nil {
		t.Fatalf("outer changed to %v", outer)
	}
}
