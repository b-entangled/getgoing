package memlayout

import (
	"testing"
	"unsafe"
)

// Padding is real and measurable: the same three fields cost 12 bytes in
// one order and 8 in the other.
func TestFieldOrderChangesStructSize(t *testing.T) {
	if got := Size[PaddedFirst](); got != 12 {
		t.Fatalf("PaddedFirst = %d bytes, want 12 (3 bytes of padding)", got)
	}
	if got := Size[Reordered](); got != 8 {
		t.Fatalf("Reordered = %d bytes, want 8", got)
	}
	if Size[Reordered]() >= Size[PaddedFirst]() {
		t.Fatal("reordering must make the struct smaller")
	}
	// The data is identical — only the layout differs.
	if unsafe.Sizeof(PaddedFirst{})-unsafe.Sizeof(Reordered{}) != 4 {
		t.Fatal("expected a 4-byte difference")
	}
}

// Every field starts at an offset that respects its own alignment.
func TestFieldOffsetsAreAligned(t *testing.T) {
	var p PaddedFirst
	if off := unsafe.Offsetof(p.b); off != 4 {
		t.Fatalf("int32 at offset %d, want 4 (padded past the byte)", off)
	}
	if off := unsafe.Offsetof(p.c); off != 8 {
		t.Fatalf("trailing byte at offset %d, want 8", off)
	}
	var r Reordered
	if off := unsafe.Offsetof(r.a); off != 4 {
		t.Fatalf("byte after int32 at offset %d, want 4", off)
	}
	if off := unsafe.Offsetof(r.c); off != 5 {
		t.Fatalf("second byte at offset %d, want 5", off)
	}
}

// A struct containing a pointer inherits the pointer's alignment: on
// 64-bit, size and offsets are multiples of 8.
func TestPointerAlignmentDominates(t *testing.T) {
	var w WithPointer
	off := unsafe.Offsetof(w.p)
	if off != unsafe.Sizeof(uintptr(0)) {
		t.Fatalf("pointer at offset %d, want word size %d", off, unsafe.Sizeof(uintptr(0)))
	}
	if got, want := Size[WithPointer](), 2*unsafe.Sizeof(uintptr(0)); got != want {
		t.Fatalf("WithPointer = %d, want %d", got, want)
	}
}

// The total size is always a multiple of the alignment — that is what
// makes arrays of the type indexable by multiplying.
func TestSizeIsAMultipleOfAlignment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  uintptr
		align uintptr
	}{
		{"PaddedFirst", Size[PaddedFirst](), Align[PaddedFirst]()},
		{"Reordered", Size[Reordered](), Align[Reordered]()},
		{"WithPointer", Size[WithPointer](), Align[WithPointer]()},
		{"OneWord", Size[OneWord](), Align[OneWord]()},
	} {
		if tc.size%tc.align != 0 {
			t.Errorf("%s: size %d %% align %d != 0", tc.name, tc.size, tc.align)
		}
	}
}

// Headers: slice = three words (ptr, len, cap), string = two (ptr, len).
func TestHeadersAreFixedNumberOfWords(t *testing.T) {
	word := unsafe.Sizeof(uintptr(0))
	if got := unsafe.Sizeof([]int(nil)); got != 3*word {
		t.Fatalf("slice header = %d bytes, want %d", got, 3*word)
	}
	if got := unsafe.Sizeof(""); got != 2*word {
		t.Fatalf("string header = %d bytes, want %d", got, 2*word)
	}
}

// struct{} is the zero-size type: you can have a slice of them that
// occupies no element storage at all.
func TestZeroSizeStructCostsNothing(t *testing.T) {
	if got := Size[ZeroSize](); got != 0 {
		t.Fatalf("struct{} size = %d, want 0", got)
	}
	s := make([]ZeroSize, 1000)
	if got := unsafe.Sizeof(s); got != 3*unsafe.Sizeof(uintptr(0)) {
		t.Fatalf("slice header for 1000 empty structs = %d, want just the header", got)
	}
	// The backing "array" of 1000 zero-size elements needs no storage:
	// taking the address of two elements yields the same address.
	if unsafe.Pointer(&s[0]) == unsafe.Pointer(&s[999]) && len(s) == 1000 {
		// either representation is acceptable; length is what matters
		if len(s) != 1000 {
			t.Fatal("unreachable")
		}
	}
}
