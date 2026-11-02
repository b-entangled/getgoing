package swallowerr

import (
	"errors"
	"testing"
)

// The discarded error makes a failing operation look successful.
func TestSwallowedWriteErrorReportsSuccess(t *testing.T) {
	w := NewFailWriter()
	if err := BadWrite(w, []byte("report")); err != nil {
		t.Fatalf("BadWrite = %v; the buggy version hides the failure", err)
	}
	// The fix surfaces the same failure.
	if err := GoodWrite(w, []byte("report")); !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("GoodWrite = %v, want ErrWriteFailed", err)
	}
}

// A deferred Close that ignores its error cannot be caught by the caller.
func TestSwallowedCloseErrorIsLost(t *testing.T) {
	if err := BadClose(NewFailWriter()); err != nil {
		t.Fatalf("BadClose = %v; the buggy version loses the close error", err)
	}
	if err := GoodClose(NewFailWriter()); !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("GoodClose = %v, want ErrCloseFailed", err)
	}
}

// The named-return fix does not disturb the success path.
func TestGoodCloseReportsNilWhenEverythingSucceeds(t *testing.T) {
	if err := GoodClose(NewOKWriter()); err != nil {
		t.Fatalf("GoodClose = %v, want nil", err)
	}
	if err := GoodWrite(NewOKWriter(), []byte("x")); err != nil {
		t.Fatalf("GoodWrite = %v, want nil", err)
	}
}

// Partial failure: ignoring the second write hides a half-written result.
func TestIgnoredSecondWriteHidesPartialFailure(t *testing.T) {
	w := NewFailSecondWriter()
	if err := BadPartial(w, []byte("ok"), []byte("lost")); err != nil {
		t.Fatalf("BadPartial = %v; the buggy version reports success", err)
	}
}
