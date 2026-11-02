package typednil

import "testing"

// The trap: a nil pointer in an interface is not a nil interface.
func TestInterfaceHoldingNilPointerIsNotNil(t *testing.T) {
	var nf *NotFound
	var err error = nf
	if err == nil {
		t.Fatal("premise: (*NotFound)(nil) stored in an error must compare non-nil")
	}
	if !DetectTypedNil(err) {
		t.Fatal("DetectTypedNil should identify the situation")
	}
}

// The bug in situ: a normal "not found" outcome reported as an error.
func TestBadFindReportsNotAsAnError(t *testing.T) {
	err := BadFind("missing")
	if err == nil {
		t.Fatal("expected the buggy version to return a typed nil (non-nil interface)")
	}
	if !DetectTypedNil(err) {
		t.Fatalf("got %T, want a typed nil *NotFound", err)
	}
}

// The fix: the same path returns an interface that is actually nil.
func TestGoodFindReturnsNilOnNotFounD(t *testing.T) {
	if err := GoodFind("missing"); err != nil {
		t.Fatalf("GoodFind = %v (type %T), want nil", err, err)
	}
	if err := GoodFind("anything"); err != nil {
		t.Fatalf("GoodFind = %v, want nil", err)
	}
}

// The caller-side consequence: `if err != nil` cannot distinguish the two
// worlds — only the concrete check can.
func TestCallerCannotDistinguishByNilComparisonAlone(t *testing.T) {
	bad := BadFind("missing")
	good := GoodFind("missing")

	// Both paths *look* like errors to a naive caller...
	if bad == nil {
		t.Fatal("bad path must be non-nil (that is the bug)")
	}
	// ...and only the deeper check tells them apart.
	if !DetectTypedNil(bad) {
		t.Fatal("bad path is a typed nil")
	}
	if DetectTypedNil(good) {
		t.Fatal("good path is a true nil, not a typed nil")
	}
}

func TestErrorMethodOnNilReceiverDoesNotPanic(t *testing.T) {
	var nf *NotFound
	if got := nf.Error(); got == "" {
		t.Fatal("nil receiver must still produce a message, not panic")
	}
}
