// Package errx demonstrates Go's error-as-value conventions:
//
//   - **Sentinel errors** compared with errors.Is, never with == or by
//     string, so wrapping cannot break the match.
//   - **Wrapping with %w** at each layer: fmt.Errorf("op: %w", err)
//     preserves the chain and adds context the caller can act on.
//   - **Typed errors** recovered with errors.As when the caller needs
//     structured fields (the failing input, a position).
//   - **errors.Join** to report several independent failures at once,
//     with every sentinel still reachable through errors.Is.
//   - Callers match on *behaviour* (Is/As), never on error text —
//     messages are for humans and may change.
//
// Domain: parsing and dividing ratios like "6/3". Two layers (parse,
// arithmetic) wrap one another so the chain has somewhere to go.
package errx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrDivideByZero is a sentinel: comparable, wrappable, matchable.
var ErrDivideByZero = errors.New("errx: division by zero")

// SyntaxError is a typed error carrying the input and the position of the
// offending character.
type SyntaxError struct {
	Input string
	Pos   int
}

// Error implements error.
func (e *SyntaxError) Error() string {
	return fmt.Sprintf("errx: syntax error in %q at position %d", e.Input, e.Pos)
}

// Divide returns a/b. The sentinel is returned unwrapped at this layer —
// the layer above decides whether to add context.
func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, ErrDivideByZero
	}
	return a / b, nil
}

// ParseRatio parses "a/b" and divides, wrapping whatever fails with the
// input so the caller learns *which* ratio was bad.
func ParseRatio(s string) (int, error) {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return 0, fmt.Errorf("%w: parse %q", &SyntaxError{Input: s, Pos: 0}, s)
	}
	a, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, fmt.Errorf("%w: parse numerator: %w", &SyntaxError{Input: s, Pos: 0}, err)
	}
	b, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return 0, fmt.Errorf("%w: parse denominator: %w", &SyntaxError{Input: s, Pos: i + 1}, err)
	}
	v, err := Divide(a, b)
	if err != nil {
		return 0, fmt.Errorf("ratio %q: %w", s, err)
	}
	return v, nil
}

// Validate reports every invalid field at once. Each entry keeps its own
// sentinel or typed error, and Join preserves reachability of all of them.
func Validate(numerator, denominator string) error {
	var errs []error
	if _, err := strconv.Atoi(numerator); err != nil {
		errs = append(errs, fmt.Errorf("numerator %q: %w", numerator, err))
	}
	if _, err := strconv.Atoi(denominator); err != nil {
		errs = append(errs, fmt.Errorf("denominator %q: %w", denominator, err))
	}
	if denominator == "0" {
		errs = append(errs, ErrDivideByZero)
	}
	return errors.Join(errs...)
}
