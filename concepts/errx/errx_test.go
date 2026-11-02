package errx

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// Sentinels survive wrapping: errors.Is walks the %w chain.
func TestSentinelIsReachableThroughWrapping(t *testing.T) {
	_, err := ParseRatio("6/0")
	if !errors.Is(err, ErrDivideByZero) {
		t.Fatalf("errors.Is(%v, ErrDivideByZero) = false", err)
	}
	if _, err := Divide(1, 0); !errors.Is(err, ErrDivideByZero) {
		t.Fatal("unwrapped sentinel must match too")
	}
}

// errors.As walks the chain for a typed error and yields its fields.
func TestTypedErrorIsExtractableWithFields(t *testing.T) {
	_, err := ParseRatio("x/1")
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed for %v", err)
	}
	if se.Input != "x/1" {
		t.Fatalf("Input = %q, want %q", se.Input, "x/1")
	}
}

// Wrapping adds context without breaking a deeper sentinel.
func TestInnerSentinelSurvivesOuterContext(t *testing.T) {
	_, err := ParseRatio("1/0")
	// The outer wrap says which ratio failed...
	if !strings.Contains(err.Error(), `"1/0"`) {
		t.Errorf("context missing from %v", err)
	}
	// ...and the sentinel from two layers down still matches.
	if !errors.Is(err, ErrDivideByZero) {
		t.Errorf("sentinel lost: %v", err)
	}
}

// A wrapped library error (strconv) is still matchable.
func TestWrappedStdlibErrorMatches(t *testing.T) {
	_, err := ParseRatio("a/b")
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Fatalf("strconv.ErrSyntax not reachable: %v", err)
	}
}

// errors.Join reports every problem, and each remains matchable.
func TestJoinSurfacesEveryError(t *testing.T) {
	err := Validate("x", "0")
	if err == nil {
		t.Fatal("expected errors")
	}
	// Both the parse failure and the divide-by-zero are present.
	var se *SyntaxError
	if !errors.As(err, &se) && !strings.Contains(err.Error(), "numerator") {
		t.Fatalf("parse failure missing: %v", err)
	}
	if !errors.Is(err, ErrDivideByZero) {
		t.Fatalf("sentinel missing from join: %v", err)
	}
}

func TestValidInputProducesNoError(t *testing.T) {
	v, err := ParseRatio("6/3")
	if err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Fatalf("value = %d, want 2", v)
	}
	if err := Validate("6", "3"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
