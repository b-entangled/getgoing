// Package shadows documents variable shadowing — a new variable declared
// with `:=` in an inner scope that *looks* like an assignment to the
// outer one.
//
// The classic form is error handling:
//
//	err := first()
//	if err == nil {
//		if err := second(); err != nil { // ← declares a NEW err
//			...
//		}
//	}
//	return err // the OUTER err: still nil, even if second() failed
//
// The bug is invisible at a glance because the names match. Defences:
//   - assign to the existing variable inside the block (`err = second()`),
//     or give the inner one a different name and handle it immediately;
//   - `go vet` does not catch this by default — `shadow` and
//     `staticcheck` analyzers do; the tests below pin the semantics.
//
// Domain: a two-step validation where the second step's failure must
// reach the caller.
package shadows

import "fmt"

// ErrSecond is the sentinel the second step returns.
var ErrSecond = fmt.Errorf("shadows: second step failed")

// BadShadowed loses the inner error: the `:=` inside the block declares a
// fresh err, and the outer err (nil) is what gets reported afterwards.
func BadShadowed(outer, inner error) error {
	err := outer
	if err == nil {
		if err := inner; err != nil { // shadows the outer err
			_ = err // "handled" — but only for this block
		}
	}
	// The author expects this to see inner's failure. It sees outer's.
	if err != nil {
		return fmt.Errorf("failed: %w", err)
	}
	return nil
}

// GoodExplicit assigns to the existing variable (or uses a distinct
// name), so the failure survives the block.
func GoodExplicit(outer, inner error) error {
	err := outer
	if err == nil {
		if e := inner; e != nil {
			err = e // same variable the caller will see
		}
	}
	if err != nil {
		return fmt.Errorf("failed: %w", err)
	}
	return nil
}

// ShortStatementScope shows where the shadowed variable's life ends: the
// `e := ...` in an if-statement exists only inside the if. After the
// closing brace it is gone and the outer err is untouched.
func ShortStatementScope(inner error) error {
	err := error(nil)
	if e := inner; e != nil {
		_ = e // e lives here
	}
	// e is not in scope here; nothing updated err.
	return err
}
