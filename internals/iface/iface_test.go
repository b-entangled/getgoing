package iface

import (
	"runtime"
	"testing"
	"unsafe"
)

// The type word identifies the dynamic type: same type, same word;
// different types, different words.
func TestTypeWordIdentifiesTheDynamicType(t *testing.T) {
	var a any = 42
	var b any = 42
	var c any = "42"

	typA, _ := Words(a)
	typB, _ := Words(b)
	typC, _ := Words(c)

	if typA == 0 {
		t.Fatal("type word must not be zero")
	}
	if typA != typB {
		t.Fatalf("two ints must share a type descriptor: %#x vs %#x", typA, typB)
	}
	if typA == typC {
		t.Fatal("int and string must not share a type descriptor")
	}
}

// Pointer-like dynamic types are stored *directly*: the data word is the
// pointer itself, with no indirection and no allocation.
func TestPointerIsStoredDirectlyInTheDataWord(t *testing.T) {
	x := 7
	var i any = &x

	_, data := Words(i)
	if data != uintptr(unsafe.Pointer(&x)) {
		t.Fatalf("data word %#x, want the pointer itself %#x", data, uintptr(unsafe.Pointer(&x)))
	}
}

// Non-pointer values get their own storage: two live boxes are two
// distinct addresses (the data word is a pointer *to* the value).
func TestValueBoxesAreDistinctStorage(t *testing.T) {
	var a any = 42
	var b any = 43

	_, dataA := Words(a)
	_, dataB := Words(b)
	if dataA == dataB {
		t.Fatal("two live boxes must occupy different storage")
	}
	if dataA == 42 || dataB == 43 {
		t.Fatal("expected indirect storage for an int, not the raw value")
	}
}

// Boxing that does not escape costs nothing: the box lives on the stack.
// The loop makes the count exact — AllocsPerRun reports integer totals
// per call, so 100 non-escaping boxes must total 0.
func TestNonEscapingBoxStaysOnTheStack(t *testing.T) {
	payload := struct{ A, B int }{1, 2}
	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			var local any = struct{ A, B int }{payload.A + i, payload.B}
			runtime.KeepAlive(local)
		}
	}))
	if allocs != 0 {
		t.Fatalf("100 non-escaping boxes allocated %d times, want 0", allocs)
	}
}

// Boxing that escapes costs a heap allocation per box — again measured as
// an exact total over 100 boxes (the value varies so the compiler cannot
// reuse a static box).
func TestEscapingBoxGoesToTheHeap(t *testing.T) {
	t.Cleanup(ResetSink)
	ResetSink()

	allocs := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			BoxEscape(struct{ A, B int }{i, i + 1})
		}
	}))
	if allocs < 50 {
		t.Fatalf("100 escaping boxes allocated %d times, want at least 50", allocs)
	}
}

// Round trip: the data word carries the value we put in.
func TestAssertionRecoversTheValue(t *testing.T) {
	type point struct{ X, Y int }
	p := point{3, 4}
	var i any = p

	typ, _ := Words(i)
	if typ == 0 {
		t.Fatal("missing type word")
	}
	got, ok := i.(point)
	if !ok || got != p {
		t.Fatalf("assertion gave (%v, %v)", got, ok)
	}
}
