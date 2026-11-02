// Package typednil documents the typed-nil-in-an-interface mistake.
//
// An interface value is a pair: (dynamic type, value). It is **nil** only
// when *both* are nil. Storing a nil pointer of some concrete type gives
// a non-nil interface holding a nil pointer:
//
//	var e error = (*NotFound)(nil)
//	e != nil        // true! the bug
//
// The usual shape is a function whose "not found" branch returns a typed
// nil by accident:
//
//	func Find(id string) error {
//		var nf *NotFound // zero value of the concrete type
//		if id == "missing" {
//			return nf // interface is non-nil
//		}
//		return nil
//	}
//
// Fixes:
//   - return the untyped `nil` explicitly on the success/not-found path;
//   - never declare the concrete type "just to have something to return";
//   - if you must test for it, assert the concrete type *and* check the
//     pointer: `if v, ok := err.(*NotFound); ok && v == nil`.
//
// Domain: a lookup where "absent" is a normal outcome.
package typednil

// NotFound is the typed error (or result) used to demonstrate the trap.
type NotFound struct {
	ID string
}

// Error implements error.
func (e *NotFound) Error() string {
	if e == nil {
		return "not found (nil receiver)"
	}
	return "not found: " + e.ID
}

// BadFind returns a *typed* nil on the not-found path. The caller sees a
// non-nil error and treats a normal outcome as a failure.
func BadFind(id string) error {
	if id == "missing" {
		var nf *NotFound // nil pointer of a concrete type
		return nf        // BUG: error interface is not nil
	}
	return nil
}

// GoodFind returns the untyped nil: the interface itself is (nil, nil).
func GoodFind(id string) error {
	if id == "missing" {
		return nil // untyped nil — the interface is nil
	}
	return nil
}

// BadFindWithValue is the same trap with a real value on the other path:
// a found id returns a *NotFound used as a result marker, and the caller
// still cannot tell "absent" from "present" by comparing to nil.
func BadFindWithValue(id string) error {
	if id == "missing" {
		var nf *NotFound
		return nf
	}
	return &NotFound{ID: ""} // "found" — also a *NotFound, also non-nil
}

// DetectTypedNil shows the only reliable way to spot the situation after
// the fact: assert the concrete type and check the pointer itself.
func DetectTypedNil(err error) bool {
	v, ok := err.(*NotFound)
	return ok && v == nil
}
