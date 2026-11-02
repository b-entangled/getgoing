package stringmem

import (
	"testing"
	"unsafe"
)

// The unsafe view points at the *same* memory as the []byte: no copy, no
// allocation — and therefore an immutability contract.
func TestUnsafeStringSharesTheUnderlyingBytes(t *testing.T) {
	b := []byte("shared")
	s := SharedString(b)

	if s != "shared" {
		t.Fatalf("s = %q", s)
	}
	addrString := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	addrBytes := uintptr(unsafe.Pointer(&b[0]))
	if addrString != addrBytes {
		t.Fatalf("string at %#x, bytes at %#x: expected sharing", addrString, addrBytes)
	}
}

// The read-only slice over a string points at the same bytes too.
func TestUnsafeBytesShareTheStringsBytes(t *testing.T) {
	s := "shared"
	bs := StringBytes(s)

	if string(bs) != s {
		t.Fatalf("got %q", bs)
	}
	addrString := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	addrBytes := uintptr(unsafe.Pointer(&bs[0]))
	if addrString != addrBytes {
		t.Fatalf("string at %#x, slice at %#x: expected sharing", addrString, addrBytes)
	}
}

// The safe conversion copies, which is why mutating the source cannot
// corrupt a string already in use — the contrast with unsafe sharing.
func TestSafeConversionCopies(t *testing.T) {
	b := []byte("original")
	s := FromBytes(b)
	b[0] = 'X'
	if s != "original" {
		t.Fatalf("s = %q: conversion must have copied", s)
	}

	bs := ToBytes("original")
	bs[0] = 'X'
	if got := ToBytes("original"); got[0] != 'o' {
		t.Fatalf("mutating one []byte affected the source: %q", got)
	}
}

// `+=` in a loop allocates once per append; a pre-grown builder does the
// whole line in one. Exact totals over 100 lines make the comparison
// independent of rounding, and keeping the results live stops the
// compiler from deleting the work.
func TestNaiveJoinAllocatesMoreThanBuilder(t *testing.T) {
	fields := []string{"error", ": ", "something broke", " #", "200"}

	var naiveTotal, builderTotal int
	naive := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			naiveTotal += len(JoinNaive(fields))
		}
	}))
	builder := int(testing.AllocsPerRun(1, func() {
		for i := 0; i < 100; i++ {
			builderTotal += len(JoinBuilder(fields))
		}
	}))

	if naiveTotal == 0 || builderTotal == 0 {
		t.Fatal("results must stay live")
	}
	if naive < 5*len(fields)*100/10 {
		t.Fatalf("naive join = %d allocs for 100 lines of 5 fields, want at least %d (one per append)",
			naive, 5*len(fields)*100/10)
	}
	if builder > 150 {
		t.Fatalf("pre-grown builder allocated %d times for 100 lines, want about 100 (one Grow each)", builder)
	}
	if naive < builder*2 {
		t.Fatalf("naive = %d, builder = %d: expected at least 2x", naive, builder)
	}
}

// Both forms produce the same bytes — the choice is memory, not output.
func TestBothJoinsProduceIdenticalOutput(t *testing.T) {
	fields := []string{"info", ": ", "hello", " #", "7"}
	got, want := JoinNaive(fields), JoinBuilder(fields)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if want != "info: hello #7" {
		t.Fatalf("unexpected line %q", want)
	}
}
